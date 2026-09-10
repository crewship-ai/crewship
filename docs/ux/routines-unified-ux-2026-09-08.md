# Routines: sjednocení detailu, běhu a historie

Návrh po uživatelském review 8. 9. 2026. Výstupem je samostatná HTML maketa,
nikoli další změna nasazené aplikace.

Maketa: `public/design/routines-unified-ux-20260908.html`. Otevřít v prohlížeči;
nepotřebuje server ani knihovny, nevolá API a nic skutečně nespouští. V horním
pruhu je „Principy a mapa UX“ s navigačním diagramem, rozhodnutími a pořadím
implementace. Údaje a verze jsou ilustrační, nikoli kopie produkční historie.

## Doložený současný stav

Přečtené obrazovky na dev1:
- `/routines?slug=morning-briefing`
- `/routines?slug=cost-spike-probe&run=run_cmtsned4k0001a79253b5`

Rutina používá `routine-card-detail.tsx`, původní kartu s ikonou a šedým
podkladem. Běh používá sdílený `routine-run-detail.tsx`, jinou hlavičku,
podklad a záložky. Sdílení s Activity nepřineslo vizuální sjednocení s detailem
rutiny. Sidebar sám tuto nespojitost nevyřešil.

Morning briefing má v načtené definici jediný agentní krok `compose` a volitelný
vstup `focus` s výchozí hodnotou a popisem. Cost spike probe má jediný krok
`probe` a vstupy `spend_usd` a `threshold_usd`, výchozí 3 a 5. Výstup `false`
z jejich porovnání je platný úspěšný výsledek, ne selhání rutiny.

`key` je povinný string v testovací rutině `test-routines-stop-20260908`.
Předává se jako argument testovacímu skriptu. Nemá popis ani výchozí hodnotu.
Není to obecný požadavek Routines a není důvod jej klientovi prezentovat jako
běžný produktový scénář. Testovací recepty mají být jasně označené/oddělené.

Další zjištění: při čtení Morning briefing vracel katalog
`last_invocation_status=COMPLETED`, ale karta ukazovala poslední běh Failed.
Je nutné ověřit zdroje posledního běhu a sjednotit je; není správné tento
rozpor vyřešit pouze barvou. Výsledek kontroly se musí oddělit od technického
stavu dokončení. Screenshoty jsou v `/tmp/routines-unified-current-*.png`.

## Navržená zkušenost

Jedna rutina zůstává otevřená před spuštěním i po něm. Ikona, popis, sidebar,
pozadí karet a tvar uzlů se nemění. Spuštění otevře konkrétní běh uvnitř sekce
Běhy; vedle něj je datum, použitá verze a stav. Graf vykresluje skutečnou
historickou definici stejnou komponentou jako Postup, doplněnou o stavy a
pokusy. Typ kroku (agent/skript/výpočet/akce) nemění design celé stránky.

- Postup: co má rutina dělat; vybraný krok vysvětluje úkol a očekávaný výstup.
- Běhy: co se při konkrétních spuštěních skutečně stalo.
- Verze: kdo a jak změnil recept; historický postup a porovnání změn.
- Plán: datum, opakování, časové pásmo a uložené vstupy.
- Nastavení: účel, vlastník, přístupy a provozní pravidla.

V běhu: Průběh / Výstupy / Činnost / Vstupy a souvislosti. Zobrazení chyby,
čekání nebo výsledku nemění navigaci ani identitu běhu. Chronologie nástrojů
je pod detailem kroku; ne každý tool call je samostatná fáze receptu.

Kalendář používá tutéž šedou kartu jako Overview, včetně okrajů, hlavičky a
barevných tokenů. Minulý záznam otevře skutečný běh, budoucí plán otevře plán.
Ruční/událostní rutina bez termínu nesmí dostat fiktivní kalendářový záznam.

Formulář spuštění má lidské názvy, popisy, správné typy a výchozí hodnoty.
Technický identifikátor lze generovat jen tehdy, když to dovoluje kontrakt
receptu. Povinný věcný údaj nelze libovolně potichu doplnit. Přístupové údaje
patří do systému credentials. Opakované spuštění jasně uvede vybranou verzi
receptu a původ předvyplněných vstupů.

## Co je návrh, ne potvrzení implementace

Historické definice, provedení, výstupy a kalendář mají implementovaný základ.
Koncept/publikace, srozumitelné labely vstupů, porovnání verzí, volba starší
verze při opakování a pravidla přepnutí plánu na novou verzi potřebují audit
existujících API a případné doplnění kontraktu. U výsledků typu boolean se
lidský význam musí opřít o deklaraci autora, ne o hádání modelu.

Případné datové změny mají doplnit současnou historii: verzovaný prezentační
popis vstupů/výsledků, stav konceptu/publikace a explicitní politiku verze
plánu. Běh zůstává zdrojem původní definice a vstupů; krok/pokus zdrojem
provedení a výstupů. Nerušit kontrakt Issues, Inbox ani Chat.

Maketa demonstruje interakce, ne kompletní engine: větvení, foreach, vnořené
rutiny a externí nevratné akce vyžadují vlastní testy. Návrh neslibuje identické
agentní výstupy napříč modely; recept může sjednotit postup a kritéria výsledku.

## Ověření artefaktu

Playwright: kalendář, výběr rutiny, vstup do běhu, přepínání stavů, schválení,
výstupy, porovnání verzí, historický graf a mobilní sidebar. Bez JS chyb a bez
horizontálního přetékání při šířce 390 px. Důkaz:
`/tmp/routines-unified-verify.log`, screenshoty `/tmp/routines-unified-*.png`.

Produkční komponenty, backend a databáze v tomto návrhovém kroku nejsou měněny.
