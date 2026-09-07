# Utrullning till quebec

Middagssidan körs som en container på quebec och håller allt i en enda
SQLite-fil. Efter den första handpåläggningen sköter sig servern själv: en
push till `main` bygger en image, och när bygget lyckats hämtar servern den
och startar om.

## Vad som är tillstånd, och vad som inte är det

| | Var det bor | Går det att göra om? |
|---|---|---|
| **Anmälningarna** — hushåll, stående anmälningar, gäster, matlag, säsonger | Docker-volymen `dinner_dinners-data` | Nej. Det finns ingen annan kopia |
| Hemligheter | `/srv/dinner/.env` | Nej — `DINNER_PASSWORD` och bottoken går att byta, inte hämta |
| Husets uppgifter | `config.yaml` | Ja, den ligger i repot |
| Programmet | `ghcr.io/kollektivhuset-rudbeckia/dinner` | Ja, hämtas |

Bara det första är svårt att göra om, så det är det som aldrig rörs av en
utrullning.

## Var den står

```
/srv/dinner/
    .env                  # hemligheter — bara på servern, aldrig i repot
    config.yaml           # husets uppgifter, skickas med varje utrullning
    docker-compose.yml    # hur den körs, skickas med varje utrullning
    DEPLOYED_SHA          # vilken commit som rullades ut
                          # + volymen dinner_dinners-data
```

Katalogen ägs av kontot `deploy`, samma konto som rullar ut registret. Framför
containern står husets nginx och proxar `dinner.rudbeckia.nu` till
`localhost:8099`.

Volymens namn kommer av katalogens namn plus namnet i `docker-compose.yml`.
Katalogen heter fortfarande `dinner`, precis som den gjorde när sidan låg i
`/home/kitain/git/dinner`, så volymen heter `dinner_dinners-data` både före
och efter flytten — databasen behövde varken kopieras eller importeras om.

## Hur den kommer in

Nyckeln ligger i organisationens `QUEBEC_SSH_KEY` och delas av husets tre
tjänster. Den når kontot `deploy` på quebec, och med den nyckeln kan kontot
göra exakt en sak. `authorized_keys` binder nyckeln till ett *forced command*:

```
command="/usr/local/bin/deploy",no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding
```

Vad den andra änden än ber om kör ssh det skriptet. Inget skal, ingen scp,
ingen vidarebefordran. Skriptet ägs av root och går inte att skriva till från
`deploy`, så nyckeln kan inte heller peka om sig själv.

### Varför tjänsten står i ssh-kommandot

Ett forced command hänger på *nyckeln*, inte på repot. Eftersom nyckeln är
gemensam kan den alltså inte i sig säga vilken tjänst som ska startas om, och
därför skickar workflowet namnet som ssh-kommando:

```bash
tar -czf - config.yaml docker-compose.yml DEPLOYED_SHA \
  | ssh "$USER@$HOST" dinner
```

Det körs inte som ett kommando. `sshd` lägger strängen i
`SSH_ORIGINAL_COMMAND` och kör skriptet ändå, och skriptet matchar den mot en
fast lista — `members`, `dinner`, `booking` — där varje gren sätter katalog,
container och port från literaler. Strängen kommer utifrån och används därför
aldrig för att bygga en sökväg. Allt annat avvisas i stället för att gissas
på, och ett okänt namn ekas inte tillbaka i loggen.

Baksidan av en gemensam nyckel är värd att säga rakt ut: varje repo i
organisationen som kommer åt hemligheten kan rulla ut vilken som helst av de
tre tjänsterna. Vill man inte det, är det nyckeln som ska delas upp — en per
tjänst, var och en bunden till sitt eget skript.

## Vad som skickas

Deploy-nycklar är avstängda för repot, så i stället för att ge servern en
GitHub-kredential den annars aldrig behöver tar utrullningen med sig det den
ska ha. Workflowet packar tre filer och skickar dem över samma ssh-kanal:

| Fil | Varför |
|---|---|
| `config.yaml` | husets uppgifter, som containern läser från disk |
| `docker-compose.yml` | hur den körs |
| `DEPLOYED_SHA` | vilken commit som rullades ut |

Skriptet packar upp **bara** de tre — de står uppräknade vid namn, vilket är
det som gör att ett arkiv inte kan skriva var det vill. `.env` finns bara på
servern och rörs aldrig av en utrullning.

Servern har alltså ingen GitHub-token och ingen väg till GitHub alls. Imagen
är publik, så `docker compose pull` behöver ingen inloggning.

## Kedjan

`Deploy to quebec` hänger på `workflow_run` från **Build and publish image**
och inte på pushen, så en utrullning kan aldrig hinna före bygget och starta
om på gårdagens image. Ett bygge som misslyckas rullas inte ut alls.
`concurrency` släpper igenom en utrullning i taget och avbryter aldrig en som
är i gång — en halvkörd utrullning kan lämna containern stoppad.

Utrullningen checkar ut den commit som *byggdes*, inte vad `main` har hunnit
bli under minuterna sedan dess.

## Hemligheter

De ligger på organisationen och gäller alla tre tjänsterna:

| Secret | Vad |
|---|---|
| `QUEBEC_SSH_KEY` | privata halvan av nyckeln som kör `/usr/local/bin/deploy` |
| `QUEBEC_HOST` | `ssh.rudbeckia.nu` |
| `QUEBEC_USER` | `deploy` |
| `QUEBEC_KNOWN_HOSTS` | värdnyckeln, så att utrullningen inte litar på vad som helst |

Ett repo som sätter en egen hemlighet med samma namn tar över den från
organisationen. Gör det bara om tjänsten också har en egen nyckel bunden till
ett eget skript — annars går utrullningen in med en nyckel vars forced command
startar om någon annans container.

## När något går fel

Utrullningen väntar på att `/healthz` svarar och avbryter med de sista
loggraderna om den aldrig gör det. Den rullar **inte** tillbaka av sig själv —
en container som inte startar lämnar den förra imagen kvar i registret, och
att välja version är ett beslut för en människa:

```bash
ssh ssh.rudbeckia.nu
cd /srv/dinner
docker compose down                 # inget -v, det raderar anmälningarna
sed -i 's/:latest/:sha-abc1234/' docker-compose.yml
docker compose up -d
```

Nästa utrullning skriver över `docker-compose.yml` igen, så en pinnad version
håller bara till dess. Ska den hålla, pinna den i repot.

En saknad Mattermost-anslutning är ingen misslyckad utrullning: en bottoken
som inte går att logga in med fäller starten och därmed körningen, men en sida
som körs *utan* bot fungerar. Skriptet skriver `NOTE: running without
Mattermost` i stället för att fälla körningen.

Vill du rulla ut för hand, eller igen efter en misslyckad körning:
**Actions → Deploy to quebec → Run workflow**.

## Säkerhetskopiering

Allt ligger i en fil. SQLite kör i WAL-läge, så en kopia av en igångvarande
databas kan sakna det senaste — och `.db-wal` är lika viktig som `.db`. Att
stoppa i några sekunder är enklare än att komma ihåg det:

```bash
cd /srv/dinner
docker compose stop dinners
docker run --rm -v dinner_dinners-data:/d -v /backup:/b alpine \
    tar czf /b/dinner-$(date +%F).tgz -C /d .
docker compose start dinners
```

## Skriptet på servern

Skriptet är gemensamt för alla tre tjänsterna och står i sin helhet i
[bokningens motsvarande sida](https://github.com/kollektivhuset-rudbeckia/booking/blob/main/docs/deploy.md#skriptet-på-servern).
Grenen för den här tjänsten:

```sh
    dinner)
        NAME=dinner; SVC=dinners; CONTAINER=dinners-rudbeckia; PORT=8099
        READY='mattermost bot ready'
        UNREADY='running without Mattermost — lists are only written to the log'
        ;;
```

Att lägga till en fjärde tjänst är en rot-ändring i `/usr/local/bin/deploy`,
vilket är rätt sorts tröskel.
