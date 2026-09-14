# Routines — implementační inventura a ověření dev1

> **Archiv k 2026-09-10.** Historická evidence, nikoli aktuální stav ani potvrzení UX.
> Rozsah, otevřené body a přejímku udržuje [živý PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md).

Aktuální souhrn a stav R1–R10: [závěrečné předání pro revizi](ENDING-2026-09-09-ROUTINES-REVIEW.md).
Níže je chronologie; původní inventura nepopisuje stav po všech navazujících změnách.

Datum: 2026-09-08. Výchozí HEAD `cd2074d0b`, `/srv/crewship/crewship_1`,
větev `dev1/issues-preview`. Podklad: [PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md)
a [předání](HANDOFF-2026-09-08-ROUTINES-NEXT-AGENT.md).
Toto je průběžná evidence implementace, nikoli potvrzení dokončeného Release 1.0.
Uživatel požaduje práci na dev1 a ověřování přes CLI. UI text zůstává anglicky.

## Inventura všech hodnocených principů

Procenta jsou převzaté produktové priority ze zadání, nikoli měření nebo stav dokončení.
„Existuje“ zde znamená konkrétní implementaci; samostatně uvádíme nové ověření níže.

| Princip | Hodnota pro 1.0 | Evidence a skutečná mezera |
| --- | ---: | --- |
| Srozumitelný průběh, výsledek, chyba | 100 % | `routine-run-detail.tsx`, `routine-step-spine.tsx`, `routine-run-presentation.ts`. Nalezen a opraven rozpor priority runtime/outcome. Dílčí výstupy zůstávají. |
| Formulář ze vstupního schématu | 98 % | `lib/routine-inputs.ts`, `InputsForm`, použití ve startu a plánování. Bool/number/array/object, options/defaulty existují. Nepodporované typy jsou nově explicitně odmítnuté; oprávněné file/credential pickery ještě vyžadují práci. |
| Obsluha oddělená od Edit | 97 % | `routine-card-detail.tsx` otevírá `RoutineCreateDialog`; společné kroky List/Map existují. Plošný redesign není potřeba. Recipe/Test/Publish, Save draft a nyní také Chat → konkrétní draft ID → editor. Legacy přímé Save zůstává explicitně označené. |
| Draft a explicitní publikace | 96 % | Nově durable revizní draft + atomická publikace přes původní Store, CLI i editor. AI authoring nově používá sdílený draft přes MCP; legacy přímé Save zachovává svůj význam. |
| Validace versus reálné spuštění | 96 % | `TestRun` používá `ModeDryRun`, editor říká Check definition. Nově otestována hranice HTTP/script/agent pro tři režimy plánování; backtest CLI nyní jasně označuje live execution. Fixture sandbox tím nevznikl. |
| Historická verze a vstupy | 95 % | Archiv a `pinned_version`, `RoutineSavedInputs`, `routine-versions-tab.tsx`; run detail nevykresluje HEAD jako historii. Znovu spustit s vybranou verzí je existující funkce. |
| Lidské rozhodnutí a Inbox | 94 % | `RoutineApprovalBanner`, `usePendingApproval`, durable waitpoints a `inbox_item_id`. Ověřit živé souběžné verdikty, timeout a Issue takeover; nevytvářet druhý systém. |
| Oprávnění spustit/upravit/publikovat | 93 % | Server má role a runtime gates, UI používá `routine-governance.ts`. Nová samostatná publikace je serverově MANAGER+ stejně jako editace; spuštění má vlastní existující runtime gate. |
| Ochrana duplicitního startu | 92 % | `IdempotencyStore` existuje. Oba starty v detailu posílaly POST bez klíče. Nově mají synchronní ochranu dvojkliku a zachování klíče při nejasné odpovědi. |
| Chyba → krok → náprava | 92 % | Detail nese error/failed step, uchované výsledky a Run again; neprezentuje replay jako univerzální Resume. Doplnit návaznost na durable Edit/Publish. |
| Nový běh z historických vstupů | 88 % | Version selector a formulář už existují. Nový komponentový test kontroluje retry s historickými vstupy a připnutou verzí. |
| Výběr zdroje dat mezi kroky | 82 % | Transform krok nově vybírá pole receptu nebo výstup jiného kroku, s vyloučením cyklických vazeb. Obecný typovaný picker pro všechny adaptéry není dokončen. |
| Test vybraného kroku | 78 % | CLI `routine step-run` pro agent/http/script/transform už existuje; jde o reálné provedení bez celé historie běhu. Rozšíření zachycených podkladů patří do R9. |
| Bohaté lidské formuláře | 74 % | Základ approve/reject existuje. Typované doplnění dat, pojmenované akce a větve jsou R8/P1; nelze vykázat jako hotové. |
| Mockování a opakované podklady | 72 % | `step-run --input/--outputs` dodává vstupní fixture, ale nemockuje externí akce. Oddělený fixture execution režim není doložen. |
| Pokračování od libovolné chyby | 65 % | `resume.go` obnovuje po restartu, rozpracovaný krok at-least-once. Není univerzální bezpečná obnova po editaci; R9 musí deklarovat konkrétně podporované účinky. |
| Datasety a porovnání modelů | 60 % | `routine backtest`, bench, EvalConfig/quartermaster existují. Backtest je živý replay a doslovné porovnání výstupů, nikoli důkaz stejné kvality modelů. Klientský dashboard je R10/P1. |

R7: jednorázové starty a opakování mají existující kalendář/editor. Zachovat
všechny kalendářní pohledy. Dokončení verzování plánů závisí na R3: připnutý
jednorázový plán, opakování sledující publikovanou verzi, blokace nekompatibilních
presetů a testy DST. Úpravy předchozí relace v `store.go`, `runs.go`,
`pipelines_exec.go` a `pipeline_artifacts.go` byly při převzetí rozpracované;
tato relace je nepovažuje za svoje nové opravy.

## Provedené změny této relace

1. `RoutineStartIntent` drží klíč jednoho startu podle workspace/endpointu,
   vstupů a pinned version. React ref blokuje dvojklik před rerenderem.
   Síťová chyba, nečitelná odpověď nebo chybějící run ID klíč neuzavře.
   Potvrzené run ID dovolí další vědomý start s novým klíčem.
   Použito v detailu receptu i v Run again z historie.
2. Úspěšný HTTP status bez čitelného run ID se už v detailu receptu nevydává
   za potvrzený start.
3. Stavové štítky respektují stop/failure před dříve zaznamenaným výsledkem;
   běžící práce se neoznačuje jako skončená pouze podle outcome. Vysvětlení
   a štítek mají stejnou prioritu pro otestované kombinace.
4. Backtest help/progress/report označuje `execution_mode: live`. Odstraněn
   zavádějící komentář read-only evaluation z CLI a replay API.
5. Aktualizován rozpracovaný test `routine-run-detail-claims.test.tsx`:
   zachovány jeho scénáře, mocky odpovídají současným hookům a assertions
   sdílenému seznamu kroků místo odstraněné záložky Outputs. Přidán retry
   historického startu. Soubor pocházel z předchozí relace, nebyl zahozen.

Hranice start identity: uchovává se po dobu života komponenty, nikoli po reloadu
browseru. Změna vstupů či verze je jiný požadavek. Backendový TTL zůstává
24 hodin; tato změna neslibuje exactly-once účinky jednotlivých kroků.

## R3: konkrétní delta schématu a kompatibilita před implementací

Implementace níže je nyní v pracovním stromu. Nová migrace byla ověřena v testovacích DB; nasazení na dev1 je uvedeno samostatně v protokolu níže.

- Zachovat `pipelines` jako živý recept a `pipeline_versions` jako archiv.
  Nevytvářet paralelní historii nebo druhý executor.
- Přidat append-only SQL migrací `pipeline_drafts`: stabilní ID, workspace,
  cílový slug/pipeline ID, dokument autora v JSON, base published version,
  monotónní revision, autor a timestamps. Draft nového receptu musí existovat
  bez aktivního pipeline řádku. ID se při opětovném založení draftu neopakuje.
- Uložení draftu používá compare-and-swap revision v jedné transakci. Konflikt
  vrátí 409 a editor zachová lokální dokument; nepřepíše ho refresh.
- Publish vyžádá konkrétní draft ID/revision/base, serverová oprávnění,
  současnou validaci, governance a kompatibilitu všech dotčených nepřipnutých
  presetů. Přepnutí živého dokumentu/HEAD, archiv a spotřebování draftu musí
  být atomické. Samostatné API načti → Save → smaž draft tuto vlastnost nemá.
- Zohlednit změny crew/acting agent a capability gates. Role publikace musí
  respektovat současný maker-checker, ne ho obcházet jen proto, že existuje draft.
- Přijaté běhy zachovají efektivní snapshot. Jednorázový plán připne verzi;
  recurrence použije publikovanou verzi při přijetí běhu. Vyhodnotit kompatibilitu
  starých pending rows explicitně, bez tichého přepisu.
- Staré CLI/manifest/Chat `save` má dosud význam přímé změny živé definice.
  Nepřejmenovat jeho význam potichu. Přidat explicitní draft/publish kontrakt,
  zdokumentovat legacy Save a připojit UI i AI autorování ke stejnému draft ID.
- Akceptace: dva editoři; dva publish; stale base po rollbacku; smazání a
  opětovné založení; změna vstupů s nekompatibilním plánem; publish během
  queued/waiting běhu; chybějící oprávnění; restart uprostřed publikace.

## Ověření

Výsledky automatických kontrol jsou uvedeny níže. Logy jsou
lokální pod `/tmp/dev1-routines-*.log` a nejsou trvalý release artefakt.
Živé CLI zatím odmítá přístup: profil dev1 pro `http://localhost:8081` není
přihlášený. Požádáno o přihlášení/párování; přístupy dev2/dev3 se nepoužívají.
Interní walkthrough a použitelnost s reprezentativními uživateli nejsou touto
relací potvrzeny. Práce neobsahuje merge ani zásah do jiných instancí.


### CLI a automatické kontroly této relace

- `validation_effects_test.go`: 3 subtesty off/explicit/auto. Skutečný lokální
  HTTP server; script a agent jsou testovací adaptéry, nikoli živé crew kontejnery.
  Stejná definice musí v dry-run zavolat 0 adaptérů a v ModeRun každý jednou.
- `cmd_handler_drift_test.go`: skutečný CLI handler nad stubovaným API,
  pinned replay na existující run ID a JSON `execution_mode=live`. Prošlo.
- Nový binární soubor `/tmp/dev1-routines-check` sestaven s repo build stampem.
  Přes skutečné `routine validate --agents reviewer` a stdin JSON:
  platný recept s defaulty false/0 → exit 0; duplicitní step ID → exit 1;
  neexistující input reference → exit 1. Vše odpovídá očekávání.
- `go vet -p 2 ./...`: exit 0.
- `pnpm lint`: exit 0, 32 varování (žádné chyby). Dodatečný cílený lint
  upravených stavů a testu run detailu: exit 0.
- `pnpm build`: prošel včetně TypeScriptu a statického exportu po poslední
  změně aplikačního kódu. Tracked `web/out/.placeholder.html` zachován.
- `go test ./... -count=1`: všechny balíčky kromě API/database prošly;
  oba tyto balíčky dosáhly defaultního 10m timeoutu bez hlášené selhané aserce.
  První opakování na disku bylo zastaveno a nahrazeno během s
  `TMPDIR=/run/user/1000/crewship-dev1-routines-tests GOMAXPROCS=4 go test -p 1 -timeout 40m ./internal/api ./internal/database -count=1`.
  Důvod: `/dev/shm` plné, uživatelský RAM disk volný; cizí soubory nemažeme.
- Šest nových kombinací runtime/outcome bylo nejprve červených (6 failed / 11 passed),
  po opravě 17/17 zelených. Testy startu pokrývají současné odeslání, ztracenou
  odpověď, nečitelný JSON, chybějící run ID, nové vědomé spuštění a pinned historii.

Živé HTTP starty, browser walkthrough přihlášeného dev1 a restartové scénáře
na jeho běžících kontejnerech čekají na CLI přístup; nepředstírat je na základě
unit testů. Běžící backend nebyl touto relací restartován ani nahrazen novým
binárním souborem. Změny frontend zdrojů leží přímo v dev1 checkoutu.


Poslední společný frontend průchod: **43 souborů, 292 testů, všechny prošly**
(`components/features/routines` + start-intent + run-presentation).
Opakovaná API sada na RAM disku: **prošla, 130,638 s**.
Read-only kontrola běžícího dev1: `GET :8081/health` → 200;
`GET :3011/routines` → 200 po prvním pomalém načtení (první pokus timeout 20 s).
Tyto HTTP kontroly dokazují dostupnost, nikoli přihlášený průchod funkcí.


**Konečný výsledek automatických kontrol této etapy:** databázový balíček
prošel za **416,683 s**, API za **130,638 s**, opakovaný příkaz exit 0.
Všechny ostatní Go balíčky prošly v původním úplném běhu; dva původní timeouty
byly tímto doověřeny. Frontend 292/292, vet, lint a finální build prošly.
`git diff --check` i lokální odkazy v novém předání jsou v pořádku.
Změny jsou lokální, bez commitu/PR/merge. Kompletní R3/P0 a živá akceptace
nejsou dokončené; za zelené je nelze považovat jen díky těmto testům.

## Navazující implementace draftů (8. září, večer UTC)

- `pipeline_drafts` uchovává dokument, autora, revizi a základ živého receptu.
  `pipelines.publication_revision` s triggerem detekuje i legacy edit + rollback
  na původní definici. Inkrementy runtime počítadel draft nezneplatní.
- Atomická publikace spotřebuje přesný draft, ověří jeho základ a presety
  nepřipnutých opakovaných plánů, zapíše původním Store recept/verzi/trigger.
  Chyba nebo souběžný editor vrací 409 a zachovává draft i živou definici.
  Připnuté plány dál používají archiv; historické verze se nepřepisují.
- Nové user API get/list/save/discard/publish má serverovou roli MANAGER+.
  Publish používá existující parser, cycle check, HMAC potvrzení validace,
  workspace/crew kontroly a capability review. Tokeny a legacy bypass flagy
  se v draftu neukládají. Riziková publikace vyžaduje explicitní approve_risk.
- Editor ukládá a znovu otevírá drafty, ukazuje změny definice a dopad na
  spouštění. Konflikt zachová lokální buffer; discard používá přesnou revizi.
  Oddělený Save draft nic nepublikuje. Dvojklik na publikaci blokuje ref.
- CLI `routine draft list|get|save|publish|discard` používá stejný revizní
  dokument. Publish získá validační proof pro přesnou definici ze souboru.
  Legacy `routine save`, manifest a interní authoring nemění svůj význam.
- Journal dostává `pipeline.published` s uživatelem, hash, draft ID/revizí;
  verze zůstává původním trvalým archivem. Journal emit je existující
  asynchronní mechanismus, nikoli atomická outbox transakce s publikací.
- Drafty jsou zařazené do workspace backupu; OpenAPI, journal registry a
  role manifest jsou regenerované.
- Transform krok má základní picker Provided information / Step result.
  Odmítá výběr potomka podle needs i template reference, přidává explicitní
  závislost a zachovává neznámé klíče. Vstup se renderuje jako text;
  pokročilé projekce a obecná kompatibilita výstupních schémat nejsou hotové.
- Recept už nepředstírá sekvenční vykonání číslováním kroků. Nepodporované
  typy vstupů jsou výslovně zobrazené a blokují odeslání formuláře.

### Otevřený rozsah – nepovažovat celý PRD za dokončený

- R3: Recipe/Test/Publish navigace je zapojená; AI authoring musí vracet
  konkrétní draft, zatím používá kompatibilní legacy Save; doplnit bohatší
  srovnání a navigaci z konfliktu na konkrétní plán.
- R7: nové jednorázové pending runs nyní ukládají pinned_version podle
  preflightované definice; atomické authoring připíná právě uloženou verzi.
  Dispatcher zachovává pin. Staré/deferred položky s NULL si zachovávají
  původní live-at-dispatch význam. CLI pending list, seznam plánů a calendar
  API ukazují verzi. DST a živý kalendářní walkthrough ještě zbývají.
- R2: oprávněné file/credential reference nejsou nové textové vstupy;
  nepodporované typy nyní explicitně odmítáme, vlastní pickery zbývají.
- R6: existující Inbox/waitpoint implementace zachovaná; živé souběžné
  rozhodnutí a Issue takeover vyžadují přihlášený dev1 scénář.
- R8–R10 jsou stále navazující samostatné inkrementy podle PRD: lidské
  formuláře/větve, izolované fixtures a řízená obnova, klientské evaluace.
- Výkon na 100 krocích/1000 pokusech a test 4/5 skutečných uživatelů nebyl
  naměřen. Nelze je vykazovat jako splněnou akceptaci.

Kontrola `lint-migrations` proti origin/main hlásí sedm starších conversation
migrací přítomných na origin/main a nepřítomných na této pracovní větvi.
Tato relace je neodstranila; rozdíl je nutné vyřešit při integraci větve.
Kontrola proti vlastnímu HEAD změněné starší migrace nehlásí. Nový draft
schema soubor je necommitnutý a jeho provedení ověřují reálně migrované API DB.

### Doplnění R7 a ověření existujících scénářů

Migrace `20260908230206_pending_run_versions.sql` přidává nullable
`pending_runs.pinned_version`. NULL zachovává starší live-at-dispatch chování.
Nové `fire_at` starty vybírají archiv podle preflightovaného definition_hash,
nikoli podle později změněného HEAD. API podporuje i explicitní pinned_version
pro jednorázový start. Delay/debounce explicitní pin nepřijímá a nemění se.

`TestOneTimeStartPinsAcceptedPublishedRecipe` a
`TestPendingDispatcherPreservesPinnedAndLegacyVersionPolicy` ověřují přijetí,
pozdější editaci, atomické authoring a předání executorovi. Existující
`TestNextOccurrences_PragueDSTSpring` / `PragueDSTAutumn` už dokládají obě
změny času; nebylo potřeba přepisovat cron engine. Existující resume/waitpoint
sady pokrývají restart/timeout. Živá uživatelská akceptace tím není nahrazena.


### Finální ověřovací protokol navazující relace

- `TMPDIR=/run/user/1000/crewship-dev1-routines-tests GOMAXPROCS=4 go test -p 2 -timeout 40m ./... -count=1`: exit 0.
  CLI 141 s, API 114 s, database 296 s. Log `/tmp/dev1-routines-go-final.log`.
- `go vet -p 2 ./...`: exit 0 (`/tmp/dev1-routines-vet-final.log`).
- Frontend: 45 sad / 367 testů prošlo; samostatné společné Ask form testy
  s input conversion dříve 11 sad / 172 testů. Log `/tmp/dev1-routines-ui-release.log`.
- `pnpm lint`: 0 errors, 32 existujících warnings. `pnpm build`: exit 0;
  export ověřen `scripts/embed-web-out.sh sync`, placeholder zachován.
- `go run ./scripts/agents-invariants`: všechny čtyři invarianty prošly.
  `git diff --check`: bez nálezu.
- Regresní smyčka opravila OpenAPI/role registry, backup intent i dokumentované
  počty endpointů. První červené běhy se nevydávají za finální výsledek.
- Sestavený `/tmp/dev1-routines-check` má skutečné `routine draft` příkazy.
  Pokus `--server http://localhost:8081 routine draft list` končí pravdivě
  `not logged into profile "dev1"`; přihlášený live walkthrough stále neproběhl.
- Změny jsou lokální, bez commitu/PR. Původní rozpracované soubory zachovány.

- Nasazení: `sudo systemctl reload crewship-ws@1` dokončeno exit 0.
  Služba active/running; lokální `/api/health` i veřejná dev1 health 200,
  Next `/routines` 200. Nasazený `/tmp/crewship-1-dev routine draft --help`
  potvrzuje nové příkazy. Ostatní instance nebyly restartovány.

- Headless Chromium přes CLI: veřejné `/routines` se bez relace přesměruje
  na `/login?reason=expired&redirect=%2Froutines`, bez pageerror. To je ověření
  načtení/ochrany stránky, nikoli přihlášený funkční walkthrough.


## Navazující implementace — 9. září 2026 UTC

Tato sekce aktualizuje otevřený rozsah výše; starší protokoly popisují tehdejší stav.

- **Chat → draft → editor (R3):** interní API a MCP `get_routine_draft` /
  `save_routine_draft` používají stejný Store/CAS jako lidský editor. Backend
  ověřuje workspace, bound crew a acting agent; nepředává cizí nepublikovaný
  dokument a přepisuje podvrženou identitu ověřenou identitou volajícího.
  Guide používá stávající ověřené delegování crew. Výsledek vrací `editor_url`
  se slugem, workspace a stabilním draft ID, `published=false`. Save neaktivuje
  plán ani nevytváří pipeline. Bundled routine-author 1.1.0 a Describe prompt
  vedou tímto průchodem. Legacy `save_routine` je popsaný jako přímá publikace.
- Editor otevře přesný návrh; jiný workspace nebo odkaz na zahozený/znovu
  vytvořený draft blokuje další zápis. Uložený agent a rozšířené parametry
  navrženého triggeru zůstávají, pokud autor nezmění crew. Chyba načtení není
  prázdný nový návrh. Souběžná lidská editace odmítne starší zápis agenta.
- **Oprava presetů (R3/R7):** transakční konflikt publikace nese strukturované
  `schedule_conflict {schedule_id,name,reason}`. Editor otevře konkrétní plán
  v nové kartě a uchová svůj draft. Po načtení seznamu se cílová položka
  zvýrazní a dostane focus pouze jednou, nikoli při každé live aktualizaci.
- **Typované opakované vstupy (R2):** New schedule nyní používá společný
  `InputsForm`; samostatné Edit inputs umí změnit existující preset.
  Připnutý plán čte přesný archiv bez fallbacku. U nepřipnutého plánu lze
  explicitně zvolit Saved draft pro přípravu nového schématu. Ukládají se
  skutečné typy, `false` a `0`; ostatní klíče presetu zůstávají. Uživatel vidí,
  že změna presetu platí ihned a může vyžadovat pozastavení plánu před publikací.
  Změna polí nemění pin ani reliability nastavení.
- **Pravdivý test kroku (R4):** API i CLI vrací `execution_mode: live`.
  CLI už neoznačuje provedení jako simulaci. Kompatibilní `simulated: true`
  znamená pouze chybějící pipeline run record; ne sandbox. Dodané upstream
  outputs jsou data, nikoli izolace skutečně prováděného kroku.
- **První stránka pokusů (R1/výkon):** počáteční detail čte jednu stránku
  místo automatického průchodu pěti. Další stránky čte na žádost, maximálně
  pět v souhrnu; úplná stránkovaná historie zůstává v recorded executions.
  Neúplnost je viditelná. Sequence guard odmítá opožděný výsledek jiného
  runu nebo refresh. Dvojklik na Load more nespustí druhé rozšíření.

### Ověření této etapy

- API + sidecar + bundled skill targeted: prošlo. Ověřeno nevytvoření živého
  receptu, společná revize, podvržení identity a přístup cizí crew/workspace.
- Frontend před poslední úpravou focusu/lint helperu: **47 sad / 375 testů**
  prošlo (`/tmp/dev1-routines-sep09-ui-final.log`).
- Syntetická data: 100 kroků, 1 000 pokusů. Počáteční fetch 1 request/100 rows
  oproti původním 5 requests/500 rows; JSON payload méně než čtvrtina původního
  automatického prefixu. Jde o hook test s mockovanou dopravou, ne měření
  interaktivní odezvy živého dev1 ani zrychlení práce modelu.
- CLI profil dev1 stále vrací `not logged into profile "dev1"`; reálné
  přihlášené schválení, spuštění a obnova v běžících crew zatím neověřeny.

### Zbývající rozsah

R2 autorizované file/credential reference; obecné typované datové vazby všech
adaptérů; bohatší porovnání publikace; živá akceptace R6 a požadovaný UX protokol
nejsou touto etapou dokončené. R8–R10 (lidská data/větve, izolované fixtures a
řízená obnova, klientské evaluace) dál vyžadují samostatné implementační celky.
Zelené automatické kontroly nejsou potvrzením dokončení celého PRD.


Doplnění R6: klient blokuje dva současné verdikty pro stejný token. Odpověď
na rozhodnutí z run A nesmí skrýt čekající rozhodnutí z mezitím otevřeného run B.
Úspěch zneplatní starší načítání seznamu, aby nevrátilo již přijatý požadavek.
Regresní hook testy prošly; backendové CAS a společný Inbox se nepřepisovaly.

Celá Go sada této etapy dokončena **exit 0** (`/tmp/dev1-routines-sep09-go.log`):
CLI 188,758 s, API 170,109 s, database 463,727 s, pipeline 11,870 s.
Po dodatečné kontrole typu draft definition (object, ne null/array/string)
prošla cílená API sada znovu; prošel i test strukturovaného odkazu na preset.
`go vet -p 2 ./...` a dodatečný vet API/sidecar: exit 0. Invarianty: 4/4.


### Finální ověření a nasazení etapy 9. září

- Frontend: **48 sad / 384 testů**, exit 0
  (`/tmp/dev1-routines-sep09-ui-release.log`), včetně obou nových approval races.
- Finální `pnpm lint`: exit 0, 0 chyb / 32 dosavadních warnings;
  `pnpm build`: exit 0. Logy `...sep09-lint-release.log` a
  `...sep09-build-release.log`. `web/out` synchronizován; placeholder zachován.
- Plná Go sada exit 0; po poslední API změně cílené Draft/InternalDraft
  testy exit 0 (`...sep09-drafts-final.log`). Full vet + delta vet exit 0.
- `sudo systemctl reload crewship-ws@1`: exit 0. Main PID 2186736,
  Next PID 2186923, service active/running. Ostatní instance nebyly měněny.
  Lokální i veřejná `/api/health`: 200 application/json.
- Nové interní draft route bez tokenu: obě 403. Jde o kontrolu nasazené
  hranice přístupu, ne přihlášený authoring.
- Nasazená binárka CLI: typed-defaults false/0 valid → exit 0;
  duplicitní step ID a nesoulad widget/type → očekávaný exit 1.
  `routine step-run --help` výslovně popisuje live execution.
- Headless Chromium přes CLI otevřel veřejný hluboký odkaz na draft.
  Nepřihlášený přístup přešel na login, zachoval celý odkaz v `redirect`
  (včetně draft ID/workspace); pageErrors=[].
- Přihlášený CLI/browser walkthrough stále chybí. Dev1 profil není
  přihlášen a E2E_EMAIL/E2E_PASSWORD nejsou nakonfigurované. Nebyly použity
  cizí profily ani přímé zásahy do živé DB. Přihlášení profilu:
  `/tmp/crewship-1-dev --server http://localhost:8081 login --profile dev1`.
- Reload sestavil i nový sidecar; načtení nového MCP katalogu v již běžících
  crew kontejnerech nebylo bez přihlášeného přístupu ověřeno.
- Změny zůstávají bez commitu/PR/merge. `git diff --check`: čisté.


## Fixtures a přímá kontrola expiry — navazující etapa 9. září

- R4/R9: nový `pipeline.TestStepWithFixtures`, user API `pipelines/fixture_test`,
  CLI `routine fixture-test` a editor Test → Test with fixtures používají jeden
  implementační bod. Bez nové DB, executor DAG ani cache produkčních výstupů.
- Transform volá stávající pure-Go runner; před renderem odmítne chybějící
  template reference. Agent/HTTP/script nepouští adaptér, vyžaduje explicitní
  náhradní výstup. Ostatní typy a duplicitní step ID odmítá. Vstupní defaulty
  a form validation používají existující funkce.
- Strukturální output checks jsou sdílené. JSON schema používá privátní compiler
  se zakázaným LoadURL, aby ani validace nenačetla file/remote referenci nebo
  produkční cache dříve načteného externího schématu. Interní fragment refs fungují.
- Report obsahuje definition/fixture hash, source transform/fixture, přesný výstup,
  validaci, informaci o deklarovaných kontrolách a omezení. Není to save_token,
  žádné run ID/publikace ani důkaz stejné kvality modelů. Změněný recept označí
  poslední UI výsledek jako starší. Data se neukládají do druhé historie.
- CLI bere JSON/YAML recept, --step, --input, --outputs a explicitní
  --fixture-output-file. Funguje offline; neplatný output vrací nenulový exit.
- R6: `CompleteApproval` nově zahrnuje timeout v SQL CAS. Prošlý pending token
  odmítne i před 30s sweepem; původní sweep dál uzavírá stav, Inbox a wake-up.
  `julianday` srovnává čas včetně starších formátů/zón, ne lexikografii textu.
- Cílené pipeline/API/CLI testy prošly, frontend **49 sad / 387 testů** prošlo.
  Nové expiry testy prošly spolu s Wait/Approval/Trust sadami. Celkové Go/vet,
  build a nasazení této etapy jsou vedené v navazujícím finálním protokolu.

Hranice R9: zatím neprobíhá celý DAG nad fixtures, automatické převzetí z
historického runu přes UI, řízená obnova ani modelové porovnání. Výsledky testu
kroku se za tyto širší schopnosti nevydávají. R8 rich human forms a R10 eval
UI stále vyžadují další implementaci; autorizované R2 file/credential pickery
rovněž zůstávají samostatným úkolem.


### Implementační plán R8 před změnou schématu

Konkrétní delta: append-only migrace přidá do `pipeline_waitpoints` sloupec
`decision_form_json TEXT NOT NULL DEFAULT ''`. Prázdná hodnota zachová původní
approve/reject a původní string output; žádná další rozhodovací tabulka/Inbox.
Wait DSL bude mít volitelné `decision_form` s typovanými fields a pojmenovanými
actions (stabilní ID, label, approved určuje pokračování nebo odmítnutí).
Schéma se zkopíruje do waitpointu při vytvoření, takže restart i editace receptu
nezmění otázku. Rich form nesmí spotřebovat standing approval bez odpovědí.

CompleteApproval ověří action ID, shodu verdiktu a hodnoty proti uloženému
schématu před původním status/expiry CAS. Přijatý JSON payload se vrátí jako
výstup kroku a umožní existující downstream podmínky podle action_id/data.
Legacy klient nesmí schválit rich form prázdným tělem. UI Routines i Inbox
použijí stejnou komponentu a stejnou route; CLI dostane action/data flags.
Ověření: typy/defaulty false/0, chybějící data, podvržená action, dva verdikty,
restart s totožným payloadem, legacy chování a bez automatického schválení.


### Ověření a nasazení fixtures

Celá Go sada `/tmp/dev1-routines-fixtures-all-go.log` skončila exit 0, stejně
jako vet; následný vet pipeline/API zahrnul expiry opravu. Frontend 49/387,
lint 0 chyb (32 existujících warnings), statický build, API kontrakty a 4
invariants prošly. Skutečný `/tmp/dev1-routines-fixture-check routine fixture-test`
vrátil exit 0/output 42, exit 1/output 21 se selhanou validací a exit 1 pro
chybějící fixture. První ruční pokus měl neplatný název/chybějící upstream krok;
CLI jej správně odmítlo, testovací recept byl opraven.

`embed-web-out.sh sync` a reload pouze crewship-ws@1 skončily exit 0; lokální
health 200. Fixture/expiry etapa je nasazená. Autentizovaný reálný průchod
a načtení MCP v existujících crew kontejnerech stále nejsou ověřené.

### R8 — implementované rozšíření, ověřování probíhá

Migrace `20260909091500_waitpoint_decision_forms.sql` přidává plánovaný sloupec.
DSL i exportované JSON Schema deklarují decision_form. CreateApproval validuje
a uloží schéma, Inbox jej promítne, standing trust je pro rich forms zakázáno.
CompleteApproval sdílí validaci napříč user/external dveřmi a původní CAS,
žádné nové rozhodovací úložiště. ApprovalOutput čte přijatý JSON po restartu,
legacy výstup zůstává stejný. Pojmenované approved=true akce lze větvit
v existujícím DAG; approved=false jde přes původní denial/OnFail.

Sdílený HumanDecisionForm je v detailu běhu a Inboxu. Přehled odkazuje na
formulář; mapa rich gate nenabízí původní binární bypass. CLI show/list vypíše
schéma a approve/reject podporují --action a --input. API/OpenAPI rozšířeno.
Cílené pipeline testy včetně race/restart a test skutečné API+Inbox projekce
prošly. Celková sada/build/nasazení R8 budou doplněny po dokončení.


### Zachycená data a srovnání — navazující práce

R9 editor načítá historický run pouze po kliknutí přes existující workspace API.
Vyžaduje dostupný archiv/hash a úspěšné načtení outputs, zachovává neúplné
výstupy jako neúplné. Zobrazí source run/hash, typed inputs používají historické
false/0; nahrazení effectful kroku vyžaduje explicitní volbu. Stará odpověď
z jiného workspace se zahodí. Nevytváří fixture DB ani replay/resume.

R10 analýza našla, že eval compare nepřipínal verzi navzdory komentáři. Nově
používá jednou načtený is_head (ne MAX), případně explicitní --version;
obě POST mají stejný pinned_version/hash. Chybějící archiv odmítne před prací.
Report přiznává execution_mode=live a agreement_basis=execution_status;
čekající/running výsledek je AMBIGUOUS. Jde o dokončení existujícího CLI základu,
ne o hotové klientské datasety/eval dashboard.

R8/R9 frontend: 75 sad / 638 testů (včetně Inbox/fixture importu), exit 0.
Skutečný R8 CLI validate: platný rich form exit 0, duplicitní action a
unsupported credential field exit 1. API/migrace/expiry cíleně prošly.
Celková Go sada odhalila YAML key drift u InputSpec.AllowCustom; opraveno
explicitními yaml tags a parity test prošel. Celá CLI sada se opakuje.
Migrační lint proti HEAD kontroluje pouze tracked registry (0 added),
proto jej nevydáváme za kontrolu untracked SQL; nový sloupec reálně prověřuje
API test běžící nad kompletně migrovanou testovací databází.


### R10 klientská první verze

Editor Test nově obsahuje RoutineComparison nad existujícími versions/run/GetRun
API. Dataset je explicitní JSON 1–20 případů s id/inputs a optional expected_output;
import/export běží lokálně, žádná nová eval DB/engine. Každá strana vybere
archivovanou verzi/hash a requested tier; queue používá neměnný snapshot datasetu.
Každý přijatý run jde do stejné History/Activity a metadata nesou comparison_id,
case_id a side. Obnova odpovědi používá stejný Idempotency-Key; po přijetí run ID
se už jen čte daný run. Očekávaný hash/verze musí odpovídat reálnému archivu.

Čekající běh pozastaví zbývající queue, Continue jej znovu načte. Stop remaining
necanceluje přijaté běhy. Opuštění Test view zastaví další klientské starty;
queue není durable scheduler. Report/export obsahuje skutečné náklady/čas,
verze/hash/inputs a run odkazy, i neúplné výsledky. Bez expected_output se kvalita
negraduje; s ním jde výhradně o exact text, ne semantickou shodu. Tier je
požadovaný override podle stávající policy, ne tvrzení o konkrétním modelu.

Cílené UI testy: stejný klíč po ztracené odpovědi, pinned verze/vstupy obou
stran, WAITING bez druhého startu, mismatch archivu, typed false/0 a prázdná
expected hodnota prošly. Celá opravená CLI sada: exit 0, 144,055 s. Zbytek
celé Go sady prošel (API 133,798 s, database 421,610 s, pipeline 12,431 s);
původní jediná chyba byla opravená YAML key parity a následná CLI sada.
Závěrečný frontend build/test a nasazení této společné etapy probíhají.

Zbývající skutečné mezery: R2 autorizované file/credential reference (ne raw
secret/path input), širší typed mapping všech adaptérů, R9 řízená obnova od
podporovaného bodu/iterace, sdílené serverové datasety a richer graders,
a autentizovaná akceptace a uživatelský UX protokol. Tyto mezery se neskrývají
za úspěchem unit testů či lokálním porovnáváním.


## Nasazená etapa 9. září, 09:42 UTC

- Dev1 reload `crewship-ws@1`: exit 0; main PID 2480983, Next PID 2481211.
  `web/out` export ze 09:40 obsahuje nový RoutineComparison. Lokální i veřejný
  `/api/health` vrací 200/ok. Nasazeny R8 forms, R9 captured import a R10
  klientská dataset comparison spolu s opravou CLI version pinning.
- Finální frontend: 77 sad / 651 testů, exit 0
  (`/tmp/dev1-r10-all-ui.log`). Lint 0 errors / 32 původních warnings
  (`/tmp/dev1-r10-lint.log`), produkční build exit 0
  (`/tmp/dev1-r10-build.log`). Nové ref-cleanup warnings opraveny.
- Celá Go sada `/tmp/dev1-r8-all-go.log` měla jedinou chybu v CLI YAML
  parity. Opraveno a celá CLI sada znovu prošla exit 0 / 144,055 s
  (`/tmp/dev1-r10-all-cli.log`). Ostatní balíky původní celé sady prošly,
  včetně database 421,610 s/API 133,798 s/pipeline 12,431 s.
  Celý vet a poslední delta vet CLI/pipeline/API/generator prošly.
  OpenAPI/docs contract tests prošly; 4 invariants a diff-check čisté.
- Skutečný nasazený CLI ukazuje --action/--input pro waitpoints a --version
  pro eval compare. Offline validace rich DSL dává očekávané exit 0/1.
- Playwright veřejný draft-link otevřel login bez page errors a zachoval
  edit/draft_id/ws v redirect. Screenshot:
  `/tmp/dev1-routines-sep09-final-browser.png`. Není to přihlášený UX test.
- Dev1 CLI stále není přihlášený. Pro skutečné run/approval/compare scénáře
  je potřeba uživatelské přihlášení:
  `/tmp/crewship-1-dev --server http://localhost:8081 login --profile dev1`.
  Nežádat token/heslo do chatu, nepoužít cizí profil ani seed/reset.
- Žádný commit/PR/merge. WIP dalších úloh zachováno. Celé PRD není označené
  hotovo; zbývající rozsah a neověřená akceptace jsou vypsané výše.

### 2026-09-09 — návazné datové vazby a nepodporovaná pole

- Výběr zdroje doplněn pro instrukce agenta, HTTP URL/body, schvalovací
  text, oznámení a foreach seznamy. Nabídka respektuje deklarované typy,
  dosažitelnost a cykly. Lineární recept nepřepíná přidáním vazby do DAG;
  automatické závislosti zůstávají automatické, explicitní se doplňují.
- Editor zachovává neznámé definice vstupů a ukazuje je pouze ke čtení
  s odkazem na Code. Formulář i slash katalog odmítají neznámé typy/widgety
  místo náhrady textovým polem, včetně varianty s options. Nejde o novou
  implementaci autorizovaných file/credential referencí.
- Fixture test odmítá chybějící JSON vlastnost ve zdroji; explicitní prázdný
  text, null, 0 a false rozlišuje a zachovává. Produkční interpolace si
  zachovává dosavadní chování; přísnější kontrola patří fixture testu.
- První ověření této dávky: 78 frontendových sad / 658 testů, lint bez
  chyb, produkční build a celá pipeline sada (12,161 s). Následná oprava
  neznámého widgetu prošla 71 testy routine-inputs. Kompletní Go sada,
  aktualizovaná API sada a poslední build ještě běží; výsledky a nasazení
  doplnit po dokončení, tuto dávku zatím nepovažovat za nasazenou.

Dokončené ověření a nasazení této dávky:
- Celé `go test -p 2 -timeout 40m ./... -count=1` exit 0:
  CLI 153,135 s, API 110,413 s, database 392,464 s, pipeline 15,529 s;
  `/tmp/dev1-routines-final-all-go.log`.
- Po poslední změně slash katalogu znovu celá API sada exit 0 (128,513 s)
  a vet API/pipeline exit 0 (`/tmp/dev1-routines-final-api.log`,
  `/tmp/dev1-routines-final-vet.log`).
- Poslední lint: 0 errors / 32 existujících warnings; build exit 0;
  4 agents-invariants a git diff --check prošly.
- Export synchronizován přes scripts/embed-web-out.sh (1141 souborů),
  reload výhradně crewship-ws@1 exit 0; server PID 2554511 aktivní.
- Nasazený CLI fixture-test: chybějící JSON vlastnost exit 1, explicitní
  hodnota 0 exit 0/output "0". Recept a fixture bez tajných údajů v
  `/tmp/dev1-routines-projection-cli/`.
- Skutečný `/api/health` lokálně8081 i veřejně dev1 vrací status ok.
  `/health` je UI fallback a nepovažuje se za API health důkaz.
- Přihlášené end-to-end scénáře stále neověřené; nasazení ani automatické
  testy neznamenají dokončení celé PRD akceptace. Žádný commit/PR/merge.

### 2026-09-09 — typované předání do call_pipeline

- Konkrétní mezera: dosavadní Render měnil i samostatný odkaz na číselný,
  bool či strukturovaný vstup podřízeného receptu na string. Nový
  renderNestedInputs použije deklarovaný typ cílového vstupu. Přímé vstupy
  zachovávají i int64 bez mezikroku přes float, false, 0 a volitelný null.
  JSON výstupy/projekce se dekódují stejným pravidlem jako transform.
- Chybějící reference, nesprávný typ a nesplněná omezení formuláře selžou
  před dispatch podřízeného receptu. Text uvnitř předaných objektů se
  znovu nevyhodnocuje. Stávající textové/nespecifikované cílové vstupy,
  kombinované texty a doslovné objekty/listy mají dosavadní chování.
  Promptové untrusted fencing se při typování neodstraňuje.
- UI call_pipeline dovoluje vyměnit zdroj již existujícího input bindingu;
  ostatní typované hodnoty, krok a neznámá konfigurace zůstávají zachované.
  Přidávání cílových vstupů a volba podřízené rutiny zůstává v Code.
- Dokumentace opravuje chybný příklad nested_inputs na skutečný klíč inputs;
  CLI přehled už neoznačuje backtest za read-only.
- Pipeline sada prošla (11,098 s), celý go vet prošel, UI kroků 5/5.
  Kompletní Go sada, lint/build ještě běží. Tato dávka zatím nenasazená.
- Dev1 whoami opět ověřeno: profil nepřihlášený. Bez cizích tokenů nebo
  zásahů do účtů/živé DB. Autentizovaná akceptace stále otevřená.

Ověření a nasazení call_pipeline dávky dokončeno:
- Celé Go testy exit0: CLI154,707 s, API120,995 s, DB356,795 s,
  pipeline13,174 s (`/tmp/dev1-routines-nested-all-go.log`).
- Celý go vet exit0, lint0errors/32existujících warnings, build exit0,
  4 agents-invariants, diff-check čistý. UI kroků5/5.
- `scripts/embed-web-out.sh sync`1141souborů; reload pouze dev1 exit0,
  PID2683601 active/running. Lokální8081 i veřejné /api/health200/statusok.
- Nasazený CLI: parent.json validace exit0, invalid.json s neexistujícím
  vstupem exit1 a cestou /steps/0/inputs. Soubory v
  /tmp/dev1-routines-child-binding-cli/. Jde o skutečnou CLI validaci;
  typovaný runtime dokládá integrační test executoru, nikoli přihlášený
  živý běh (profil dev1 stále nepřihlášený).
- Bez commitu/PR/merge; celý rozsah PRD ani živá akceptace není uzavřený.

### 2026-09-09 — klientská frontendová etapa

Podrobný rozsah a browser evidence:
`docs/ux/routines-client-frontend-2026-09-09.md`.

- Nové skutečné UI: testovací pracovní oblast se třemi metodami a zachováním
  dat; per-step editor sample outputs; vizuální human decision builder se
  sdíleným živým formulářem v lokálním náhledu; čitelné shrnutí publikace.
- Neaktivní testovací panely nejsou přístupné pro čtečku. Běžící porovnání je
  viditelné napříč metodami. Nový draft vysvětluje chybějící archiv. Live a
  fixture režimy mají odlišné, pravdivé označení. Technické detaily zůstávají
  v rozbalení. Nezaveden žádný druhý approval/eval/backend engine.
- 77 frontendových sad / 692 testů prošlo; lint0errors/32warnings, produkční
  build prošel včetně posledních režimových badge. Playwright ověřil skutečné
  komponenty s repo CSS, 390px šířku, keyboard tabs, neaktivní tabpanel,
  zachování fixture dat a decision preview bez API verdiktu. Nejde o
  autentizovaný full-app E2E test ani o výsledek uživatelského UX protokolu.
- První Go příkaz vypsal ok pro všechny balíky, ale skončil143 před vet;
  není vykázán jako čistý průchod. Samostatný celý vet prošel. Celá Go sada
  je opakovaná v samostatné procesové skupině, výstup
  /tmp/dev1-routines-professional-verified-go.log a explicitní výsledek
  /tmp/dev1-routines-professional-go-result.json. Nasazení doplnit až po
  dokončení těchto kontrol. Žádný commit/PR/merge.

Frontendová etapa dokončená a nasazená:
- Go rerun: všechny balíky prošly kromě internal/devcontainer, kde archive
  bomb test vyčerpal RAM TMPDIR (no space left on device). Znovu celá
  internal/devcontainer sada na běžném disku TMPDIR=/tmp prošla25,982s.
  Žádné testy ani ochrany archivu se kvůli prostředí neměnily. Celý vet prošel.
- Poslední frontend:77sad/692testů, lint0errors/32existujících warnings,
  produkční build a invariants prošly. Následné browser ověření pokrývá i
  extrémně dlouhé názvy, reviewer otázky a tlačítka:390px bez overflow.
- Export1140souborů synchronizován; reload pouze crewship-ws@1 exit0,
  PID2792510 active/running. Lokální8081 i veřejné /api/health200/statusok.
- Veřejné /routines v Playwright otevřelo login bez page errors, redirect
  zachován. Dev1 CLI whoami stále nepřihlášený. Přihlášený full-app E2E
  není tímto ověřený. Izolovaný lokální Vite preview server ukončen;
  screenshoty a repro podklady zůstávají v /tmp.
- Žádný commit/PR/merge. Celé PRD, backendové mezery a uživatelský UX protokol
  nejsou touto frontendovou etapou prohlášené za hotové.


## Závěrečná kontrola 9. září, 12:15 UTC

Aktuální stav R1–R10 a předání pro nezávislou revizi: [ENDING](ENDING-2026-09-09-ROUTINES-REVIEW.md).
Opraven replay s poškozenými historickými vstupy a tělem bez Content-Length; neplatné požadavky se odmítají před dispatch. Cílené handler testy prošly.
Celá Go sada na TMPDIR=/tmp: exit0; celý vet: exit0. Offline fixture CLI ověřil úspěch i chybu. Invariants prošly. Migration lint proti HEAD prošel, proti origin/main má9 rozdílů historie větví; není to uzavřená integrace do main.
Reload pouze dev1: exit0, PID2956508; lokální i veřejné /api/health200/statusok, binárka i JS asset ověřeny hashově. Přihlášený E2E stále neověřen.
Uživatel výslovně odmítl srozumitelnost Edit/Test. Nový agent má řešit klientskou orientaci; technické testy nejsou UX akceptace. Celé PRD není dokončené, žádný commit/PR/merge.
