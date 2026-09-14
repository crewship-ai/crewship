# Routines — závěrečné předání pro nezávislou revizi, 9. září 2026

> **Archiv k 2026-09-10.** Historická evidence, nikoli aktuální stav ani potvrzení UX.
> Rozsah, otevřené body a přejímku udržuje [živý PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md).

Tento dokument je aktuální vstupní bod k implementaci. Původní PRD zůstává
zadáním; starší implementační protokol je chronologická evidence, nikoli
aktuální seznam toho, co chybí. **Celé PRD ani klientská použitelnost nejsou
uzavřené.** Uživatel při dnešním ověření označil Edit/Test za nesrozumitelný:
neví, co má vyplnit a co jednotlivé funkce znamenají. Požaduje samostatnou
navazující práci na přehlednosti frontendu. Technické testy tuto výhradu neřeší.

## Rozsah a původ změn

- Pracovní kopie: `/srv/crewship/crewship_1`, větev `dev1/issues-preview`,
  výchozí HEAD při závěrečné kontrole `cd2074d0b`.
- Cíl nasazení pouze https://crewship-dev1.unifylab.cz, Go8081 / Next3011.
- Změny jsou lokální WIP; v této etapě nevznikl commit, PR, merge ani nezávislá
  revize. Pracovní adresář obsahuje také starší a cizí práci. Celý `git diff`
  nelze vydávat za dnešní autorský příspěvek. Untracked soubory jsou podstatnou
  částí implementace a samotný `git diff` je nezahrne.
- Již existovaly engine, replay, archiv verzí, start dedup, boot resume,
  schvalování a eval mechanismy. Byly propojeny, rozšířeny a opraveny;
  nebyly znovu napsány všechny tyto subsystémy.

## Co bylo doplněno nebo opraveno

1. **Draft a publikování:** uložený draft s revizí a kontrolou souběhu;
   oddělené publikování, kontrola základní publikované revize i návratu na
   dřívější verzi, validační důkaz a kompatibilita plánovaných vstupů.
   Stejný draft používají API, CLI, editor a Chat/MCP.
2. **Spouštění, historie a plány:** stabilní identita požadavku při dvojkliku
   a nejistém síťovém výsledku; nový úmyslný start má novou identitu.
   Historie pracuje s archivovaným receptem a vstupy. Nové jednorázové plány
   připínají publikovanou verzi, opakování používá publikovanou verzi při
   přijetí běhu. Staré plány s NULL připnutím se tiše nemigrují na jiný význam.
3. **Pravdivý výsledek:** rozlišení stavu vykonávání, potvrzení výsledku a
   dostupnosti dat; dílčí výstupy a chyby načítání nejsou prázdný úspěch.
   Sdílené kroky receptu a běhu využívají existující step spine a journal.
4. **Tři různé významy testování:** kontrola definice; offline fixtures;
   skutečné spuštění se skutečnými účinky a náklady. Fixture režim výslovně
   nahrazuje agentní/HTTP/script výstupy a nepouští jejich externí akce.
   Lokální JSON Schema reference fungují, externí síťové/souborové se odmítají.
   Import zachycených výstupů kontroluje workspace, běh, archiv a hash.
5. **Lidská rozhodnutí:** trvale uložený formulář otázky a stabilní action IDs,
   šest typů polí, serverová validace odpovědi, sdílený formulář Inbox/běh,
   CLI action/input. Souběžné rozhodnutí má jediného vítěze, expirovaný token
   neprojde; restart zachová formulář i rozhodnutí. Staré approve/reject zůstává.
6. **Vazby dat:** výběr podporovaných vstupů a výstupů bez ručního výrazu,
   kontrola cyklů a zachování režimu závislostí. Vnořená rutina dostane
   deklarované číslo/bool/object/array ve správném typu; 0 a false se neztratí.
   Chybná vazba je odmítnuta před spuštěním podřízené rutiny.
7. **Porovnání:** lokální JSON dataset, dvě archivované verze / modelové
   úrovně, reálné běhy s jasnými náklady, pauza fronty, dohledání přijatého
   požadavku a export. CLI obě strany připíná na ověřený archiv.
   Exact-match je označen jako přesná shoda, nikoli automatický důkaz kvality.
8. **Frontend:** pracovní oblasti Recipe/Test/Publish, editor vzorků po
   krocích, náhled lidského formuláře a čitelný přehled změn před publikací.
   Opraveno přetékání dlouhých názvů a otázek na mobilu. Zachován původní
   sidebar, kalendář, ikony a grafický systém. **Nejde o kompletní redesign
   aplikace a uživatel tento způsob Edit/Test nepovažuje za srozumitelný.**
9. **Závěrečná oprava replay:** poškozené historické vstupy nyní vedou na409
   před spuštěním, místo tichého použití prázdných vstupů. Tělo požadavku bez
   Content-Length zachová vybranou verzi; chybný/trailing JSON a nekladný pin
   jsou400. Historické prázdné tělo nadále zachovává původní význam replay na HEAD.

## Aktuální stav proti PRD

„Implementováno“ zde znamená existenci kódu a uvedených testů, nikoli
automatickou akceptaci celého požadavku v přihlášené aplikaci.

| PRD | Stav a hranice |
| --- | --- |
| R1 / P0, společný průchod a stavy | Propojení a opravy implementovány; klientská srozumitelnost není přijata. |
| R2 / P0, vstupní formuláře | Sdílené typované formuláře implementovány. Autorizovaný výběr souborů a credential referencí chybí; nepodporovaný typ se explicitně blokuje. |
| R3 / P0, draft/publish/verze | Durable kontrakt, API/CLI/Chat/editor a konflikty implementovány; nutný nezávislý audit oprávnění a kompatibility všech autorovacích cest. |
| R4 / P0, pravdivé testování | Oddělení režimů a omezené offline fixtures implementovány; obecný sandbox není součástí. |
| R5 / P0, chyba a náprava | Historická evidence, nový běh a integrita replay doplněny. Obecné pokračování od selhaného kroku není implementováno ani slibováno. |
| R6 / P0, lidská práce | Sdílené rozhodnutí a ochrany souběhu doplněny. Celý přihlášený průchod včetně převzetí v Issues zbývá ověřit. |
| R7 / P0, kalendář a verze | Pravidla připnutí a sdílené vstupy doplněny. Kompletní přejímací protokol DST a velkých dat není doložen. |
| R8 / P1, bohaté formuláře | Typované otázky a vlastní akce implementovány; obecný vizuální návrhář rozhodovacích větví chybí. |
| R9 / P1, testovací data a obnova | Zachycené výstupy, fixtures a podporované vazby implementovány. Řízené pokračování z vybraného selhaného kroku/iterace chybí. |
| R10 / P1, experimenty | Lokální dataset, porovnání a export implementovány. Sdílené serverové datasety a sémantické hodnocení chybí. Modelová úroveň není záruka konkrétního modelu. |

Původní procenta 100–60 % vyjadřovala produktovou prioritu inspirace, nikoli
procento implementace. Nezaměňovat je za release readiness. Nejvyšší váhu při
revizi mají chybné externí účinky, oprávnění, konflikty publikace, verze běhu,
dvojklik a souběžná rozhodnutí. P1 recovery a sdílené evaluace nesmí zastínit
nedokončenou obsluhu a P0 vstupní reference.

## Mapa pro technického recenzenta

| Oblast | Hlavní soubory (cesty od kořene repozitáře) |
| --- | --- |
| Draft | `internal/pipeline/drafts.go`, `internal/api/pipeline_drafts*.go`, `internal/sidecar/routine_drafts.go`, `lib/routine-drafts.ts` |
| Start/verze/plány | `lib/routine-start-intent.ts`, `internal/pipeline/pending_dispatcher.go`, `internal/api/pipeline_deferred.go`, `internal/api/pipeline_runs_replay.go` |
| Fixtures | `internal/pipeline/fixture_steps.go`, `internal/api/pipeline_fixture.go`, `cmd/crewship/cmd_routine_fixture.go`, `lib/routine-fixtures.ts`, `components/features/routines/routine-fixture-test.tsx` |
| Rozhodnutí | `internal/pipeline/decision_forms.go`, `internal/api/pipeline_waitpoint_decider.go`, `cmd/crewship/cmd_routine_waitpoints.go`, `lib/decision-form.ts`, `components/features/approvals/human-decision-form.tsx` |
| Datové vazby | `lib/routine-data-sources.ts`, `internal/pipeline/nested_inputs.go`, `internal/pipeline/dsl.go`, `internal/pipeline/executor.go` |
| Porovnání | `cmd/crewship/cmd_eval_compare.go`, `lib/routine-comparison.ts`, `components/features/routines/routine-comparison.tsx` |
| Editor | `components/features/routines/routine-create-dialog.tsx`, `routine-test-workspace.tsx`, `routine-fixture-outputs-editor.tsx`, `routine-decision-form-builder.tsx`, `routine-publication-review.tsx` (poslední čtyři ve stejném adresáři), `lib/routine-publication-changes.ts` |

Nové migrace v `internal/database/migrations/`:
`20260908223757_pipeline_drafts.sql`, `20260908230206_pending_run_versions.sql`,
`20260909091500_waitpoint_decision_forms.sql`. Revize musí zahrnout čerstvou
instalaci i upgrade, a nesmí měnit již vydané migrace.

## Testovací evidence a její omezení

- Frontend:77 sad /692 testů prošlo. Příkaz:
  `pnpm exec vitest run components/features/routines/__tests__ components/features/approvals/__tests__ lib/__tests__/routine*`.
  Log `/tmp/dev1-routines-professional-final-ui.log`.
- Lint0 errors /32 existujících warnings; production build prošel.
  Logy `/tmp/dev1-routines-professional-final-lint.log` a
  `/tmp/dev1-routines-professional-final-build.log`.
- Závěrečné replay testy skutečných HTTP handlerů s migrovanou testovací DB
  prošly: pin bez Content-Length, poškozené vstupy, trailing JSON a neplatný
  pin. Kontrolují, že při odmítnutí nevznikne nový běh.
  `internal/api/pipeline_replay_input_integrity_test.go`,
  log `/tmp/dev1-routines-ending-replay.log`.
- Kompletní závěrečná Go sada **prošla, exit0**. API1043,147s,
  databáze1094,071s, pipeline16,947s. Běh na fyzickém disku byl výrazně
  pomalejší než předchozí běh s RAM TMPDIR, žádný test nebyl kvůli tomu změněn.
  Příkaz `TMPDIR=/tmp GOMAXPROCS=4 go test -p 2 -timeout 40m ./... -count=1`,
  log `/tmp/dev1-routines-ending-all-go.log`. Samostatné celé
  `go vet -p 2 ./...` prošlo (log `/tmp/dev1-routines-ending-vet.log`).
- Offline CLI na běžící dev1 binárce: fixture se vstupem0 úspěšná (exit0),
  vstup7 nevyhověl deklarovanému must_contain (exit1); oba výsledky obsahují
  režim a hash evidence. Logy `/tmp/dev1-routines-ending-cli-{0,7}.log`.
- `agents-invariants`:4 kontroly prošly. Migration lint proti HEAD prošel;
  výchozí kontrola proti origin/main selhala na9 migracích nepřítomných
  v této větvi (workspace conversations, provider pool, private service).
  Jde o rozdíl historie větví, nikoli lokální smazání těchto migrací.
  **Integrace s main je otevřená a tento check není zelený.** Nepřekopírovat
  samostatné migrace bez příslušného aplikačního kontraktu. Logy
  `/tmp/dev1-routines-ending-migrations.log` a `-migrations-head.log`.
- Dřívější kompletní Go běh měl environmentální chybu plného RAM TMPDIR;
  dotčený balík `internal/devcontainer` prošel po opakování s TMPDIR=/tmp.
  Nezaměňovat tento kombinovaný výsledek za jeden zelený plný příkaz.
- Playwright ověřil skutečné komponenty s repo CSS ve vzorovém izolovaném
  preview: uchování vzorku mezi záložkami, lokální náhled otázky, klávesovou
  navigaci, jediný přístupný tabpanel a390px šířku včetně dlouhých názvů.
  Není to přihlášený E2E test aplikace ani akceptace designu.
- Veřejná adresa a JS asset byly ověřeny proti lokálnímu exportu; browser bez
  přihlášení skončí na loginu. Profil dev1 pro CLI nemá přihlášenou relaci.
  Nebyla použita cizí identita, reset demo dat ani přímé změny živé databáze.

## Poslední nasazení na dev1

Ověřeno9. září2026 ve12:15UTC. `scripts/embed-web-out.sh sync` zachoval
placeholder a ověřil1140souborů exportu. `systemctl reload crewship-ws@1`
skončil exit0. Služba active/running, PID2956508; lokální i veřejné
`/api/health` odpověděly200/statusok. Poslední oprava replay je součástí
této binárky. SHA256 spuštěného `/proc/2956508/exe` souhlasí s nově
sestaveným `/tmp/crewship-1-dev`:
`63b35d8dd6ea313936d51568116a68373ea8f1bd9f9ee6e3bc18f526a3e5a837`.

Veřejný JS asset `/_next/static/chunks/157ryajvw5803.js` je byte-for-byte
shodný s lokálním embedded exportem, SHA256:
`a7eb89533bf5f0e8e06f40b36440dacff0704c35969f8a3b9d75d347a738edc0`.
Frontend v této poslední opravě zůstal z ověřeného buildu11:14UTC;
nový Test je uvnitř Edit, nikoli další samostatné tlačítko v Overview.
Strojová evidence `/tmp/dev1-routines-ending-verification.json`.
Health a hash dokládají nasazení, **ne přihlášenou funkční akceptaci**.

## Zadání nezávislé revize a následujícího UX agenta

Přečti AGENTS.md a CODEX.md, potom tento dokument a
[PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md). Audituj skutečný diff
**i uvedené untracked soubory**; neakceptuj tvrzení protokolu bez ověření.
Prověř zejména isolation fixtures, auth/workspace scope, CAS/expiry lidských
rozhodnutí, ABA publikování, nulové hodnoty, idempotenci při nejistém síťovém
výsledku, pin a hash historických dat a zastavení fronty porovnání.
Ochranu UI Start nepřenášej automaticky na všechny endpointy: legacy
`ReplayRun`/bulk replay v `pipeline_runs_replay.go` neposílá executorovi
IdempotencyKey. Opakovaný POST na tuto cestu proto není doložen jako jeden běh;
poslední oprava integrity vstupů tento starší kontrakt nemění.

Po přihlášení projdi pět úloh z PRD na dev1 včetně chyb, čekání, restartu
a souběhu. Živé testy s externími akcemi označ a použij kontrolovaný recept.
Přilož run IDs a pozorované výsledky. Technické testy ani screenshot loginu
nenahrazují tento protokol.

UX agent musí začít z dnešní uživatelské výhrady, nikoli z předpokladu, že
stačí přidat další karty či záložky. Ověř, zda klient bez výkladu pozná:
co kontrola udělá, co má zadat, zda něco skutečně spustí, co výsledek znamená
a co udělat dál. Zachovej původní grafický systém, sidebar a kalendář.
Názvy Fixtures, Definition a Compare nejsou samy o sobě důkazem porozumění.
Větší redesign je další práce, nikoli dokončená část tohoto předání.

Podrobnější chronologie: [implementační protokol](ROUTINES-RELEASE-IMPLEMENTATION-2026-09-08.md).
UI evidence: [frontend 9. září](../ux/routines-client-frontend-2026-09-09.md).
