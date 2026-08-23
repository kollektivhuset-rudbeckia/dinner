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

Saker att prova: anmäl ditt hushåll till en kväll och lägg den i din kalender,
slå på en **stående anmälan** under *Mina anmälningar* och se hur du dyker upp
på alla tisdagar, tacka nej till en enskild kväll ändå, anmäl dig som gäst utan
lösenord och läs kvittot du får på sidan, byt
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
matlagen och säsongen som står där. Lagledarnas namn slås samtidigt upp i
Mattermost, så att schemat kan skriva ut dem utan att fråga chatten varje gång
någon läser en sida. Det är bara ett utgångsläge: därefter är det
administrationssidan som äger dem, och en senare ändring i `config.yaml` rör
inte det matgruppen har skrivit.

---

## Boten i Mattermost

Boten är ett konto i husets Mattermost och gör tre saker: bekräftar varje
anmälan till den som gjorde den, skickar matlistan till lagledaren när anmälan
stänger, och slår upp vilka som finns i huset så att både hushåll och matgrupp
kan välja person i stället för att minnas användarnamn. Så här kopplar du in
det:

1. I Mattermost: **System Console → Integrations → Bot Accounts**, slå på dem.
2. **Integrations → Bot Accounts → Add Bot Account**. Kalla den `dinner`.
3. Kopiera token som visas *en gång* och lägg den i `.env` som
   `MATTERMOST_TOKEN`. Sätt `MATTERMOST_URL` till husets adress.
4. Boten behöver få slå upp användare och skicka direktmeddelanden. Ett vanligt
   bot-konto räcker; den behöver inte vara systemadministratör.
5. Starta om: `docker compose up -d`. Loggen säger `mattermost bot ready` med
   botens användarnamn om token fungerar, och servern vägrar starta om den inte
   gör det.

```bash
# .env
MATTERMOST_URL=https://chat.rudbeckia.nu
MATTERMOST_TOKEN=...
```

Sätt båda eller ingen — halv konfiguration ser inkopplad ut utan att vara det,
och avvisas därför vid start. Utan dem fungerar sajten ändå: anmälan går som
vanligt, kvällen går att lägga i kalendern från sidan och matlistan ligger där
den ligger, men ingen blir tillsagd — meddelandena skrivs i loggen i stället.
Då är fältet för vem man är en vanlig textruta, och det som skrivs sparas som
det står. Det är också vad demoläget gör: demon når aldrig en
riktig chattserver och kan därför aldrig råka skriva till någon.

**Bara boten pratar utåt.** Kommunikationen går i en riktning: boten skickar,
den lyssnar inte. Det finns ingen inkommande webhook och inget slash command
att konfigurera, och därmed ingen ny väg in i huset.

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

Varje lag har en **lagledare**, angiven med sitt användarnamn i Mattermost. Det
är dit matlistan skickas, och det är allt som ska fyllas i: **namnet hämtas från
kontot**, stavat som personen själv stavar det. Det finns alltså inget eget
namnfält att hålla i takt med chatten.

Fältet tar användarnamnet — det som står efter `@` — men också hela namnet:
matgruppen kan skriva *Anna Andersson* och få rätt konto. Finns det två som
heter samma säger sidan vilka de är i stället för att gissa, och medan man
skriver föreslår den husets konton. Ett lag utan användarnamn får inget
meddelande, och det syns som **ingen lagledare** i listan.

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
kost gör i stället en anmälan per kosthållning — gästanmälningar hör inte till
något hushåll och får vara hur många som helst.

Allergier och annat skrivs som fri text och hamnar på matlistan.

### Bekräftelsen

Den som anmäler sig får ett **direktmeddelande från boten** med det som blev
sparat — kväll, klockslag, var maten står, antal, kosthållning och eventuella
allergier — och **kvällen som kalenderfil**. Tackar man nej blir det ett
meddelande om det i stället, med kvällen som en avbokning så att den försvinner
ur kalendern om den redan låg där.

Meddelandet kommer på det språk personen har i Mattermost, inte det sidan råkar
visa: det är ju chatten som läser det.

Samma kväll ligger också att hämta på anmälningssidan — som `.ics`-fil eller
med en knapp rakt in i Google Calendar eller Outlook på webben.

Utan Mattermost inkopplat sparas anmälan precis som vanligt; bekräftelsen
skrivs bara i loggen, och sidan lovar inget utskick.

Efter att anmälan stängt går det inte att anmäla sig eller ändra sig — varken
som hushåll eller som gäst, och inte heller genom att gå tillbaka till sin egen
gästlänk. Den som ändå behöver ändra får höra av sig till lagledaren.

En **stående anmälan** är husets gamla permanentlista: fyll i hur ni brukar
äta en viss veckodag, så räknas ni med varje gång utan att göra något. Den
slås på och av under **Mina anmälningar**.

En anmälan för en enskild kväll vinner alltid över den stående — även en
anmälan för noll personer, vilket är precis så man hoppar över en kväll utan
att röra sin stående anmälan.

### Gäster

Gäster anmäler sig själva på <https://din-adress/gast> utan lösenord. De väljer
kväll, skriver sitt namn och vem de hälsar på — och lämnar ingen adress alls,
för ingenting skickas till dem. **Sidan är kvittot:** den visar vad som blev
anmält, erbjuder kvällen till gästens egen kalender, och ger en länk tillbaka så
att de kan ändra sig eller avanmäla sig. Länken är hela relationen, och det är
gästen som förvarar den.

Sidan visar aldrig något om huset — bara gästens egen anmälan, och inte ens
vilket lag som lagar. Går att stänga av helt under **Inställningar**.

### Matlistan och utskicket

När anmälan stänger får lagledaren ett direktmeddelande från boten. Det
innehåller **summorna laget handlar efter** — hushåll, vuxna, barn, portioner
av varje kosthållning, antal gäster och hur många som anmält en allergi — och
en länk till matlistan.

Så ser det ut i chatten:

> **Hej Anna!** Anmälan till middagen tisdag 25 augusti är stängd och matlistan
> är klar.
>
> | | |
> |---|---|
> | **Hushåll** | 3 |
> | **Vuxna** | 5 |
> | **Barn** | 1 |
> | **Portioner** | 6 |
> | **Allätare** | 2 |
> | **Flexitarian** | 0 |
> | **Pescetarian** | 0 |
> | **Vegetarian** | 3 |
> | **Vegan** | 1 |
> | **Gäster** | 0 |
> | **Allergier** | 1 |
>
> [Öppna matlistan](#) · Maten serveras 18:00 i stora matsalen.

Siffrorna står i meddelandet därför att det är dem laget vill se direkt, i
chatten de redan läser. **Namn och allergitexter står inte där.** De hör till
hushållen som skrivit dem och ligger kvar på listan, ett klick bort, där de
alltid är aktuella.

Matlistan visar samma summor plus allergierna och vilka hushåll som kommer.
Alla fem kosthållningarna står med även när ingen valt dem, så att en gryta som
inte behövs syns som en nolla i stället för att saknas — både i meddelandet och
på sidan. Sidan är gjord för att skrivas ut.

Länken i meddelandet är signerad och öppnar **den kvällens lista och inget
annat**. Lagledaren behöver alltså inte leta rätt på husets lösenord för att se
vad hen ska laga.

Har meddelandet kommit bort går det att skicka om från schemat i
administrationen. Utan Mattermost fungerar allt annat som vanligt; utskicket
skrivs bara i loggen, och varje sida säger *Utan utskick* nere i foten.

### Till kalkylark

Matlistan går att få ut som CSV, för matlag som håller sin planering i ett
kalkylark. **Ladda ner CSV** på matlistan ger en enda platt tabell: en rad per
hushåll, en rubrikrad, inga summeringsrader och inga tomrader — den går att
importera i Google Kalkylark (*Arkiv → Importera*) eller öppna i Excel. Filen
har byte-order mark, så å och ä kommer fram rätt även i Excel.

Vill man slippa importera om varje vecka finns en levande länk. Matlaget och
administratören får en färdig formel att klistra in i en cell:

```
=IMPORTDATA("https://dinner.rudbeckia.nu/middag/2026-08-25/lista.csv?nyckel=…")
```

Arket hämtar då listan självt, och siffrorna uppdaterar sig ända till anmälan
stänger. Länken bär samma nyckel som meddelandet till lagledaren: den öppnar den
kvällens lista utan lösenord, och ingenting annat. Därför visas den bara för
matlaget och administratören — alla i huset kan läsa listan, men en länk som
funkar utan inloggning är en annan sak att dela ut.

Hela säsongen på en gång finns under **Administration → Schema**.

### Språk

Sidan finns på svenska och engelska, och man byter med flaggan i övre högra
hörnet — precis som man byter mellan ljust och mörkt läge. Valet sparas i en
kaka och gäller allt: sidor, datum, veckodagar och matlistan.

Har man inte valt något gissar servern på webbläsarens `Accept-Language`, och
faller tillbaka på `site.language` i `config.yaml`.

Meddelandet till lagledaren följer `site.language`, inte någons webbläsare — vi
vet ju inte vad den som läser det har för inställningar.

### Vem som ser vad

Alla i huset delar ett lösenord — sajten har inga egna konton. Man säger vem man
är genom att **välja sitt konto i husets Mattermost**, och det är det som knyter
anmälan till hushållet så att man kan ändra sig senare. Det är också dit
bekräftelsen kommer. Ingen e-postadress finns kvar i sajten, varken för hushåll
eller för gäster.

Fältet fungerar som på bokningssidan: skriv namnet eller användarnamnet, och
sidan söker i husets katalog medan du skriver. Man kan lika gärna skriva
`@anna.andersson`, klistra in en profillänk eller bara skriva *Anna* — pekar det
på en enda person blir det den personen, pekar det på flera säger sidan vilka i
stället för att gissa. Fungerar även utan JavaScript: då skriver man
användarnamnet självt.

Användarnamnet visas bara för en själv, för matgruppen i administrationen och i
CSV-exporten. Andra i huset ser namn, antal och specialkost — aldrig konton.

Anmälan kräver alltså både husets lösenord och ett konto i husets chat. Gäster
kräver ingetdera: de anmäler sig utan lösenord och utan konto, och får sin
bekräftelse på sidan.

---

## Administration

`/admin` kräver `ADMIN_PASSWORD` och är uppdelad i flikar:

| Flik | Vad du gör där |
|---|---|
| **Schema** | Kvällarna i en säsong: byta matlag för en enskild kväll, ställa in den, skriva ett meddelande till huset, se hur många som anmält sig och om listan är skickad — och skicka om den |
| **Matlag** | Turordningen, som dras på plats, och lagen med sina lagledares användarnamn i Mattermost |
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
  title: Rudbeckia middagar          # syns i huvudet och i meddelandena
  tagline: Kollektivhuset Rudbeckia
  house_name: Kollektivhuset Rudbeckia
  timezone: Europe/Stockholm         # allt visas i den här tidszonen
  language: sv                       # sv eller en: språket innan man valt,
                                     # och språket i meddelandena till matlagen
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
    mattermost: anna.andersson       # lagledarens användarnamn i husets chat;
                                     # namnet hämtas från kontot

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
| `BASE_URL` | `http://localhost:8080` | Den publika adressen. Används i länken som skickas till matlaget, så den måste stämma |
| `ADMIN_PASSWORD` | tomt | Låser upp `/admin`. Tomt stänger av administrationen helt |
| `SESSION_SECRET` | härleds ur lösenorden | Signerar sessioner och de länkar som skickas ut. Sätt den för att slippa logga ut alla när ett lösenord byts |
| `SESSION_DAYS` | `90` | Hur länge en inloggning håller |
| `CONFIG_PATH` | `config.yaml` | Var husets inställningar ligger |
| `DB_PATH` | `data/dinners.db` | Var databasen ligger |
| `LISTEN_ADDR` | `:8080` | |
| `TRUST_PROXY` | `true` | Läs klientens adress ur `X-Forwarded-For` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` eller `error` |
| `DEMO` | `false` | Demoläge. Aldrig skarpt |
| `MATTERMOST_URL` | tomt | Husets Mattermost, t.ex. `https://chat.rudbeckia.nu` |
| `MATTERMOST_TOKEN` | tomt | Bot-kontots access token. Utan URL och token skickas inget alls, bara loggrader — sätt båda eller ingen |

---

## Utveckling

```bash
make            # visar alla kommandon
make demo       # kör demon lokalt
make test       # go test ./...
make test-js    # testerna för sökningen i webbläsaren (kräver Node)
make race       # med kapplöpningsdetektorn
make check      # fmt + vet + race + kontrollera config.yaml
make image      # bygg containern lokalt
```

Koden är server-renderad HTML utan byggsteg. `internal/web/static/app.js` är
bara små förbättringar — allt fungerar utan JavaScript, inklusive flikarna i
administrationen, som är vanliga länkar, och fälten där man väljer person, som
utan skript är textrutor som tar ett användarnamn. Sökningen bland husets
konton ligger i `members.js` och rör inget DOM, så den kan testas för sig med
`make test-js`.

| Paket | Ansvar |
|---|---|
| `internal/config` | `config.yaml` och miljövariablerna |
| `internal/i18n` | Varje ord sidan säger, på båda språken, plus datum och räkneord |
| `internal/auth` | Lösenordsspärren, sessioner, identiteten och de signerade länkarna |
| `internal/store` | SQLite: lag, säsonger, uppehåll, anmälningar, utskicksloggen |
| `internal/dinner` | Schemat, turordningen och summeringen. Rör aldrig databasen |
| `internal/mattermost` | Boten: uppslag i husets katalog och direktmeddelanden |
| `internal/ical` | Kvällen som kalenderfil och som knapp in i Google och Outlook |
| `internal/web` | Routing, sidor, mallar och utskicket |
| `internal/setup` | Första starten och demodatan |

Databasen uppgraderar sig själv vid start: `internal/store` frågar schemat hur
det ser ut och gör bara det som fattas. Går du från en tidigare version, där
kosthållning var två räknare per anmälan, blir en anmälan som var delvis vegansk
eller vegetarisk den kosthållningen rakt igenom — det är ändå den maten laget
måste laga.

Kom du från versionen som mejlade matlistan får matlagen en kolumn för
användarnamn, och kolumnen med lagledarnas e-postadresser tas bort: det går
inte längre att skicka något dit, och då ska adresserna inte ligga kvar. Lagen
och turordningen är orörda, men **matgruppen måste fylla i ett användarnamn per
lag** — tills dess står det *ingen lagledare* och ingen får någon lista.
Lagledarens namn skrivs över med namnet på kontot när laget sparas.

Kom du från versionen där **hushållen kändes igen på sin e-postadress** är det
en större sak, för en adress går inte att räkna om till ett Mattermost-konto:

* Adressen tas bort ur både anmälningar och stående anmälningar.
* **De stående anmälningarna nollas.** En stående anmälan utan hushåll bakom sig
  hade fortsatt räkna in folk varje vecka utan att någon kunde ändra den.
* **Anmälningar till kvällar som ännu inte ätits tas bort**, så att ett hushåll
  som anmäler sig igen inte räknas dubbelt. Kvällar som redan är avklarade
  ligger kvar precis som de var — det är husets historik.
* Gästanmälningar rörs inte: de har aldrig hört till en adress.

Säg alltså till huset att **anmäla sig igen och slå på sin stående anmälan** när
ni uppgraderar, helst mellan två anmälningsstopp. Ta en kopia av
`data/dinners.db` innan du uppgraderar, som alltid.

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
