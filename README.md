# dinners

Anmälan till de gemensamma middagarna i Kollektivhuset Rudbeckia. Huset lagar
mat åt varandra två kvällar i veckan; den här sidan håller reda på hur många
som kommer, vad de äter, och vilket matlag som ska laga.

Ersätter kombinationen av Google-formulär, kalkylblad och limkod med en enda
statisk Go-binär och en SQLite-fil. Ingen databasserver, ingen byggkedja för
frontend, inget att uppdatera utöver containern.

---

## Prova på

Vill du bara se hur det ser ut? Starta en demo. Den behöver ingen
konfiguration, inga lösenord och ingen databas — och den fyller sig själv med
en säsong som redan är i gång, så att du har något att klicka på.

**Med Docker** — det enda du behöver är Docker:

```bash
docker run --rm -p 8080:8080 -e DEMO=true ghcr.io/o5ten/dinner:latest
```

**Med Go** — om du klonat repot:

```bash
make demo          # eller: go run ./cmd/server -demo
```

**Med repot och Docker** — bygger från källkoden:

```bash
docker compose -f docker-compose.demo.yml up
```

Öppna sedan <http://localhost:8080>.

| | |
|---|---|
| Lösenord | `demo` — redan ifyllt i formuläret |
| Administratör | `admin` — låser upp **Administration** |
| Gästsidan | <http://localhost:8080/gast> — inget lösenord alls |

Demon säger tydligt ifrån med en banner på varje sida, och all data ligger i
en slängbar fil: stoppar du containern är allt borta. Kör aldrig `DEMO=true`
skarpt — lösenorden står ju på sidan.

Saker att prova: anmäl ditt hushåll till en kväll, slå på en **stående
anmälan** under *Mina anmälningar* och se hur du dyker upp på alla tisdagar,
tacka nej till en enskild kväll ändå, anmäl dig som gäst utan lösenord, byt
språk med flaggan uppe till höger, öppna den utskriftsvänliga **matlistan**,
och logga in som `admin` för att flytta anmälningsstoppet, byta matlag för en
kväll eller lägga in ett lov.

---

## Kom igång på riktigt

```bash
cp .env.example .env
$EDITOR .env                 # sätt åtminstone DINNER_PASSWORD
docker compose up -d
```

Sidan ligger på <http://localhost:8080>. Logga in med `DINNER_PASSWORD`, gå
till **Administration** med `ADMIN_PASSWORD` och lägg in matlagen och
säsongens datum. Sen är det i gång.

Vill du köra utan Docker:

```bash
DINNER_PASSWORD=hemligt go run ./cmd/server
```

Alla vardagskommandon finns i `Makefile` — kör `make` för att se dem.

### Första starten

Startar servern mot en tom databas läser den `config.yaml` och lägger in
matlagen och säsongen som står där. Det är bara ett utgångsläge: därefter är
det administrationssidan som äger dem, och en senare ändring i `config.yaml`
rör inte det matgruppen har skrivit.

---

## Så fungerar det

### Säsonger och middagskvällar

En **säsong** är den period huset lagar mat, och vilka kvällar i veckan det
gäller — normalt tisdagar och torsdagar. Utanför säsongerna finns inga
middagar att anmäla sig till. Två säsonger får inte överlappa varandra;
administrationen säger ifrån om du försöker.

Schemat lagras inte, det räknas fram. En säsong plus matlagens ordning räcker
för att säga vad som händer varje tisdag och torsdag fram till jul, och bara
avvikelserna behöver en egen rad.

### Uppehåll

Skollov, röda dagar och andra veckor utan matlagning läggs in som
**uppehåll**. Kvällarna inne i ett uppehåll försvinner ur schemat helt — och
turordningen står stilla över dem. Det lag som stod på tur lagar första
middagen efter lovet i stället för att missa sin gång.

Samma sak gäller en enstaka **inställd** kväll: ingen lagar den, och ingen
förlorar sin tur på den.

### Matlagen och turordningen

Matlagen turas om i den ordning de står i administrationen. Ordningen ändrar
man genom att dra lagen dit de ska — eller med pilarna, om JavaScript är
avstängt. Det finns inget nummer att skriva in, och därför inget sätt att av
misstag ge två lag samma plats. Nya lag hamnar sist.

Ett lag som är avstängt hoppas över. Behöver ni byta för en enskild kväll
väljer ni ett annat lag för just den — resten av säsongen ligger kvar som
förut.

Varje lag har en **lagledare** med en e-postadress. Det är dit matlistan
mejlas.

### Anmälningsstopp

Anmälan stänger **en gång i veckan**, inte per middag: matlaget handlar för
hela veckan på en gång. Stoppet räknas bakåt från måndagen i middagsveckan.

Standard är **fredag 23:59, en hel vecka före** — alltså natten mot lördag,
nio dagar innan tisdagsmiddagen, så att laget har helgen på sig att handla.
Både veckodag, klockslag och antal veckor ställs in under **Administration →
Inställningar**.

### Anmälan

Ett hushåll anmäler **hur många vuxna och hur många barn** som kommer, och
**vad de äter** — ett val för hela anmälan:

| | |
|---|---|
| **Allätare** | äter allt |
| **Flexitarian** | kyckling och fisk, inget rött kött |
| **Pescetarian** | fisk och skaldjur, inget kött |
| **Vegetarian** | inget kött eller fisk |
| **Vegan** | inga animalier alls |

Ett val per anmälan, inte per person: matlaget lagar en gryta av varje sort, och
då är summan av valen precis antalet portioner. Äter någon i hushållet annat
skriver man det i fältet för allergier. En gäst som är flera personer med olika
kost gör i stället en anmälan per kosthållning — gästanmälningar är inte
knutna till en adress och får vara hur många som helst.

Allergier och annat skrivs som fri text och hamnar på matlistan.

En **stående anmälan** är husets gamla permanentlista: fyll i hur ni brukar
äta en viss veckodag, så räknas ni med varje gång utan att göra något. Den
slås på och av under **Mina anmälningar**.

En anmälan för en enskild kväll vinner alltid över den stående — även en
anmälan för noll personer, vilket är precis så man hoppar över en kväll utan
att röra sin stående anmälan.

### Gäster

Gäster anmäler sig själva på <https://din-adress/gast> utan lösenord. De väljer
kväll, skriver sitt namn och vem de hälsar på, och får en egen länk tillbaka
så att de kan ändra sig eller avanmäla sig. Sidan visar aldrig något om huset —
bara gästens egen anmälan. Går att stänga av helt under **Inställningar**.

### Matlistan och utskicket

När anmälan stänger mejlas lagledaren. **Mejlet innehåller ingen lista och inga
specialkoster** — bara en länk till matlistan. Där står allt på ett ställe,
alltid aktuellt, och inget känsligt ligger kvar i en inkorg.

Matlistan visar summorna laget lagar efter — vuxna, barn och antalet portioner
av varje kosthållning — plus allergierna och vilka hushåll som kommer. Alla fem
kosthållningarna står med även när ingen valt dem, så att en gryta som inte
behövs syns som en nolla i stället för att saknas. Sidan är gjord för att
skrivas ut.

Länken i mejlet är signerad och öppnar **den kvällens lista och inget annat**.
Lagledaren behöver alltså inte leta rätt på husets lösenord för att se vad hen
ska laga.

Har mejlet kommit bort går det att skicka om från schemat i administrationen.
Utan SMTP fungerar allt annat som vanligt; utskicket skrivs bara i loggen.

### Språk

Sidan finns på svenska och engelska, och man byter med flaggan i övre högra
hörnet — precis som man byter mellan ljust och mörkt läge. Valet sparas i en
kaka och gäller allt: sidor, datum, veckodagar och matlistan.

Har man inte valt något gissar servern på webbläsarens `Accept-Language`, och
faller tillbaka på `site.language` i `config.yaml`.

Mejlet till lagledaren följer `site.language`, inte någons webbläsare — vi vet
ju inte vad den som öppnar mejlet har för inställningar.

### Vem som ser vad

Alla i huset delar ett lösenord — det finns inga konton. Man säger vem man är
med **sin e-postadress**, och det är den som knyter anmälan till hushållet så
att man kan ändra sig senare.

Adressen visas bara för en själv, för matgruppen i administrationen och i
CSV-exporten. Andra i huset ser namn, antal och specialkost — aldrig adresser.

---

## Administration

`/admin` kräver `ADMIN_PASSWORD` och är uppdelad i flikar:

| Flik | Vad du gör där |
|---|---|
| **Schema** | Kvällarna i en säsong: byta matlag för en enskild kväll, ställa in den, skriva ett meddelande till huset, se hur många som anmält sig och om listan är mejlad — och mejla om den |
| **Matlag** | Turordningen, som dras på plats, och lagen med sina lagledare och e-postadresser |
| **Säsonger** | Start- och slutdatum, vilka veckodagar som är middagskvällar, och vilket lag som tar säsongens första middag |
| **Uppehåll** | Lov och röda dagar |
| **Inställningar** | Anmälningsstoppet och gästsidan |
| **Stående anmälningar** | Vilka hushåll som är med varje gång |

Under **Schema** finns också en CSV-export av säsongens anmälningar.

---

## Konfiguration

### `config.yaml`

Det som är fast för installationen. Filen läses vid start, så starta om
containern när du ändrat något. Kontrollera först att den är giltig:

```bash
go run ./cmd/server -check-config      # eller: make check
```

```yaml
site:
  title: Rudbeckia middagar          # syns i huvudet och i mejlen
  tagline: Kollektivhuset Rudbeckia
  house_name: Kollektivhuset Rudbeckia
  timezone: Europe/Stockholm         # allt visas i den här tidszonen
  language: sv                       # sv eller en: språket innan man valt,
                                     # och språket i mejlen till matlagen
  home_url: https://rudbeckia.nu
  support_url: https://rudbeckia.nu/kontakt/
  footer_note: Kollektivhuset Rudbeckia · Rosendal, Uppsala

dinner:
  weekdays: [tisdag, torsdag]        # förval för nya säsonger
  serving_time: "18:00"
  location: stora matsalen
  # Husets egna ord högst upp på gästsidan, i stället för de inbyggda. Sätter
  # du den ena, sätt den andra också — annars byter sidan språk men behåller
  # ett stycke på det gamla. Lämna båda tomma för den inbyggda texten, som
  # redan finns på båda språken.
  guest_info: ""
  guest_info_en: ""

deadline:                            # utgångsläge; ändras sedan i admin
  weekday: fredag
  time: "23:59"
  weeks_before: 1

teams:                               # bara till första starten
  - name: Lag 1
    leader: Anna Andersson
    email: anna@example.se

# season:                            # bara till första starten
#   name: Hösten 2026
#   start: 2026-08-25
#   end: 2026-12-17
```

### Miljövariabler

Det som är hemligt eller beror på var sidan står.

| Variabel | Standard | Vad den gör |
|---|---|---|
| `DINNER_PASSWORD` | — | **Krävs.** Husets gemensamma lösenord |
| `BASE_URL` | `http://localhost:8080` | Den publika adressen. Används i länken som mejlas till matlaget, så den måste stämma |
| `ADMIN_PASSWORD` | tomt | Låser upp `/admin`. Tomt stänger av administrationen helt |
| `SESSION_SECRET` | härleds ur lösenorden | Signerar sessioner och de mejlade länkarna. Sätt den för att slippa logga ut alla när ett lösenord byts |
| `SESSION_DAYS` | `90` | Hur länge en inloggning håller |
| `CONFIG_PATH` | `config.yaml` | Var husets inställningar ligger |
| `DB_PATH` | `data/dinners.db` | Var databasen ligger |
| `LISTEN_ADDR` | `:8080` | |
| `TRUST_PROXY` | `true` | Läs klientens adress ur `X-Forwarded-For` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` eller `error` |
| `DEMO` | `false` | Demoläge. Aldrig skarpt |
| `SMTP_HOST` | tomt | Tomt = inga utskick, bara loggrader |
| `SMTP_PORT` | `587` | |
| `SMTP_USER`, `SMTP_PASSWORD` | tomt | |
| `SMTP_FROM`, `SMTP_FROM_NAME` | tomt / `Rudbeckia middagar` | Avsändare |
| `SMTP_ENCRYPTION` | `starttls` | `starttls` (587), `tls` (465) eller `none` |
| `SMTP_REPLY_TO` | tomt | Dit svar går |
| `SMTP_BCC` | tomt | Hemlig kopia av varje utskick |

---

## Utveckling

```bash
make            # visar alla kommandon
make demo       # kör demon lokalt
make test       # go test ./...
make race       # med kapplöpningsdetektorn
make check      # fmt + vet + race + kontrollera config.yaml
make image      # bygg containern lokalt
```

Koden är server-renderad HTML utan byggsteg. `internal/web/static/app.js` är
bara små förbättringar — allt fungerar utan JavaScript, inklusive flikarna i
administrationen, som är vanliga länkar.

| Paket | Ansvar |
|---|---|
| `internal/config` | `config.yaml` och miljövariablerna |
| `internal/i18n` | Varje ord sidan säger, på båda språken, plus datum och räkneord |
| `internal/auth` | Lösenordsspärren, sessioner, identiteten och de signerade länkarna |
| `internal/store` | SQLite: lag, säsonger, uppehåll, anmälningar, utskicksloggen |
| `internal/dinner` | Schemat, turordningen och summeringen. Rör aldrig databasen |
| `internal/mail` | SMTP och MIME |
| `internal/web` | Routing, sidor, mallar och utskicket |
| `internal/setup` | Första starten och demodatan |

Databasen uppgraderar sig själv vid start: `internal/store` frågar schemat hur
det ser ut och gör bara det som fattas. Går du från en tidigare version, där
kosthållning var två räknare per anmälan, blir en anmälan som var delvis vegansk
eller vegetarisk den kosthållningen rakt igenom — det är ändå den maten laget
måste laga.

Datum lagras som `2006-01-02` i husets egen tidszon. En middag är en kväll, inte
ett ögonblick, och det tar bort alla sommartidsfällor på en gång. Tidpunkter
som verkligen är ögonblick lagras som UTC.

---

## Drift

`.github/workflows/ci.yml` kör formatering, `go vet`, testerna med
kapplöpningsdetektorn, en validering av `config.yaml` och ett rökprov som
startar demon och hämtar en sida. Den bygger också containern, så en trasig
`Dockerfile` fastnar i pull requesten och inte i releasen.

`.github/workflows/release.yml` bygger och publicerar
`ghcr.io/o5ten/dinner` för `linux/amd64` och `linux/arm64` vid varje push
till `main` och vid varje `v*`-tagg.

Uppdatera med:

```bash
docker compose pull && docker compose up -d
```

Databasen ligger i volymen `dinners-data`. Ta en kopia av `/data/dinners.db`
innan större ändringar — det är en vanlig fil.
