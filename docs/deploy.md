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

Nyckeln i `QUEBEC_SSH_KEY` når kontot `deploy` på quebec, och med *den*
nyckeln kan kontot göra exakt en sak. `authorized_keys` binder nyckeln till
ett *forced command*:

```
command="/usr/local/bin/deploy-dinner",no-agent-forwarding,no-port-forwarding,no-pty,no-user-rc,no-X11-forwarding
```

Vad den andra änden än ber om kör ssh det skriptet. Inget skal, ingen scp,
ingen vidarebefordran. Skriptet ägs av root och går inte att skriva till från
`deploy`, så nyckeln kan inte heller peka om sig själv.

Registret har sin egen nyckel bunden till sitt eget skript i samma
`authorized_keys`. Nycklarna kan alltså inte rulla ut varandras tjänst.

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

## Hemligheter i repot

| Secret | Vad |
|---|---|
| `QUEBEC_SSH_KEY` | privata halvan av nyckeln som kör `deploy-dinner` |
| `QUEBEC_HOST` | `ssh.rudbeckia.nu` |
| `QUEBEC_USER` | `deploy` |
| `QUEBEC_KNOWN_HOSTS` | värdnyckeln, så att utrullningen inte litar på vad som helst |

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

`/usr/local/bin/deploy-dinner`, ägt av root:

```sh
#!/bin/sh
set -eu

DIR=/srv/dinner
cd "$DIR"

ALLOWED="config.yaml docker-compose.yml DEPLOYED_SHA"

if [ ! -t 0 ]; then
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT
    if cat > "$tmp/in.tgz" && [ -s "$tmp/in.tgz" ]; then
        echo "==> unpacking configuration"
        tar -xzf "$tmp/in.tgz" -C "$tmp" $ALLOWED 2>/dev/null || {
            echo "    the archive did not hold what was expected" >&2; exit 1; }
        for f in $ALLOWED; do
            [ -f "$tmp/$f" ] || continue
            if cmp -s "$tmp/$f" "$DIR/$f"; then
                echo "    $f unchanged"
            else
                cp "$tmp/$f" "$DIR/$f"
                echo "    $f updated"
            fi
        done
    fi
fi

[ -f DEPLOYED_SHA ] && echo "==> version $(cat DEPLOYED_SHA)"

echo "==> pulling the image"
docker compose pull --quiet dinners

echo "==> restarting"
was=$(docker inspect dinners-rudbeckia --format '{{.State.StartedAt}}' 2>/dev/null || echo none)
docker compose up -d dinners
now=$(docker inspect dinners-rudbeckia --format '{{.State.StartedAt}}' 2>/dev/null || echo none)

echo "==> waiting for it to answer"
i=0
while [ "$i" -lt 40 ]; do
    if curl -fsS --max-time 3 http://localhost:8099/healthz >/dev/null 2>&1; then
        echo "    healthy after ${i}s"
        if [ "$was" = "$now" ]; then
            echo "    already running this image; nothing was restarted"
            exit 0
        fi
        if docker compose logs --no-color --since 120s dinners 2>&1 |
           grep -q "mattermost bot ready"; then
            echo "    the Mattermost bot is connected"
        else
            echo "    NOTE: running without Mattermost — lists are only written to the log"
        fi
        exit 0
    fi
    i=$((i + 1))
    sleep 1
done

echo "    it never became healthy. Last log lines:" >&2
docker compose logs --no-color --tail 40 dinners >&2
exit 1
```
