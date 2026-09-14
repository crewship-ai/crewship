# Routines: audit a návrh pro Release 1.0

2026-09-08, dev1, `dev1/issues-preview` @ `492c90ad9`. Rozsah tohoto průchodu:
rozbor screenshotů, aktuálního kódu a cílené automatizované testy; návrh UI.
Nejde o nasazenou změnu aplikačního workflow ani o nový živý end-to-end test.
Výchozí kontrakt Issues: `docs/prd/HANDOFF-2026-09-08-ISSUES-EXECUTION.md`.

## Produktový závěr

Rutina je uložený opakovatelný postup, běh je jedno jeho provedení. Issues drží
zadání a přijetí výsledku; Inbox rozhodnutí; Chat přípravu a diskusi. Routines
mají ukazovat účel, spouštění, výsledek a požadovanou akci. Současné UI klade
důraz na definici a provozní statistiky. Engine má výrazně více schopností,
než klient z těchto obrazovek pochopí.

## Doložené nálezy v aktuálním kódu

| Nález | Evidence | Doporučení |
| --- | --- | --- |
| Detail ukazuje graf vysoký 56vh, metadata a několik kopií posledního stavu; výstup není obsahem LastRunCard | `components/features/routines/routine-card-detail.tsx`: RoutineCardDetail, LastRunCard, RunsList | Nahoru účel, výsledek a případné rozhodnutí. Kroky lidsky; skutečný graf rozbalit. |
| Graf definice vyrábí fiktivní run s `triggered_via: "schedule"`, i pro ruční rutinu | `routine-definition-canvas.tsx`: definitionRun | Kořen „Spuštění“ nebo skutečné způsoby spouštění; nevydávat ilustraci za plán. |
| Badge scheduled závisí na počtu plánů, nikoli na jejich enabled; webhooky se do badge nepromítají | `routine-card-detail.tsx`: mine, identity pills | Rozlišit ručně, aktivní plán, pozastavený plán a události. |
| Nulová délka běhu se mění na pomlčku, průměr vynechává nuly | `routine-card-detail.tsx`: summarise, LastRunCard | Rozlišit 0 ms / méně než 1 ms od neznámé hodnoty. |
| Pass rate je z posledních nejvýše 50 záznamů bez uvedení vzorku, Runs je celkový čítač | `hooks/use-pipeline-run-records.ts`, `routine-card-detail.tsx` | Stejné období a jmenovatel, nebo přiznaný omezený vzorek. |
| Detail ignoruje loading/error hooku historie a bere jen records | `routine-card-detail.tsx`: usePipelineRunRecords | Chybu načtení nesdělovat jako „žádné běhy“. |
| Needs attention počítá poslední selhání a proposed definice, Waiting on you navíc používá waitpointy | `lib/routines-overview.ts`: needsAttention; `routines-overview.tsx`: waiting | Jeden sdílený model pozornosti, deduplikace podle rutiny. Nesoulad je doložen výpočtem, nebyl reprodukován živým čekajícím během v tomto průchodu. |
| New routine / Describe otevírá Chat s Leadovým slugem a promptem | `routine-create-dialog.tsx`: handleDescribe | Zachovat, ale výslovně oznámit přechod a nabídnout návrat k připravenému návrhu. |
| Fork kopíruje definici do pokročilého editoru; nepřenáší plány | `routine-create-dialog.tsx`: handleForkPick | „Zkopírovat“ místo „Fork“; jasně uvést rozsah kopie. |
| Test run v tvorbě je ModeDryRun; agent se nespouští. UI přesto píše Test passed a posílá sample_inputs: {} | `routine-create-dialog.tsx`: handleTestRun; `internal/api/pipelines_exec.go`: TestRun | „Ověřit definici“; samostatný skutečný zkušební běh s formulářem vstupů. Statická kontrola negarantuje výsledek. |
| Editor vytváření neposílá trigger, API a nástroj Leada ho už podporují atomicky | `routine-create-dialog.tsx`: handleSave; `internal/api/pipelines_crud.go`: SaveWithTrigger; `internal/sidecar/routine_mcp.go`: trigger schema | Závěrečný souhrn musí zahrnout způsob spouštění. F17 staršího PRD nelze přebírat jako dnešní obecnou chybu. |
| Opětovné spuštění standardně bere původní vstupy a aktuální definici; lze připnout verzi | `internal/api/pipeline_runs_replay.go`: replayRun | Rozlišit nové spuštění, opakování s původními vstupy a pokračování po schválení. Uvést verzi. |

## Co zachovat

- Oddělení definice od konkrétního běhu; skutečný graf větvení, historie a verze.
- Zadání přirozeným jazykem přes Leada a pokročilý editor pro zkušené uživatele.
- Formulář deklarovaných vstupů před ručním spuštěním a serverové preflight kontroly.
- Trvalé čekání na rozhodnutí, rušení, obnova, retry a ochrany plánovače.
- Přístupy a rozpočet, ale běžně kompaktně; výrazná barva při skutečném problému.
- Vazbu na konkrétní běh Issues a navazující Lead review, včetně lidského přijetí.

## Navržené obrazovky

Klikací HTML: `public/design/routines-release-1-proposal-20260908.html`.
Ilustrační obsah, nikoli produkční data. Návrh používá češtinu pro diskusi;
implementace má respektovat jazykový systém aplikace.

1. Přehled: nahoře akce vyžadující člověka, pak katalog s účelem, stavem a
   spouštěním. Hledání a relevantní filtry. Grafy pod rozbalovací statistikou;
   u většího katalogu lze zachovat explorer, ale ne dvě úplné kopie seznamu.
2. Detail: účel → aktuální/poslední výsledek → další krok → kdy se spouští →
   lidsky popsané kroky → historie. ID, hash, DSL, procento agentích kroků,
   odhady a verze do detailů. Přímé názvy akcí místo opakovaného Manage.
3. Nová rutina: rovnou zadání a tým, další cesty menší. Spouštění je součást
   návrhu i finálního potvrzení. Rozlišit uložení, statické ověření, skutečný
   zkušební běh a aktivaci. Slovo „návrh“ v UI není slib nového lifecycle enumu;
   implementace musí použít existující governance a aktivaci triggeru.

Výsledek běhu musí zobrazovat uložený obsah; nevyrábět úspěšný souhrn z pouhého
zeleného statusu. U testovací transform rutiny ze screenshotu není doložen
smysluplný klientský výstup ani použití AI.

## Životní cyklus a zbývající živé ověření

| Oblast | Implementace / dosavadní evidence | Potřebný scénář na dev1 |
| --- | --- | --- |
| Tvorba | Chat → authoring MCP; editor → test_run/save | Zadání v Chatu → náhled → uložení → návrat do Routines; chybějící Lead |
| Vstupy | RoutineRunInputsDialog, preflight; QUA-12 ověřeno v předání | Povinná/prázdná/neplatná hodnota z UI; správný výstup konkrétního běhu |
| Plán | Cron, zóna, pinning, catch-up, circuit breaker | Skutečné odpálení dočasného plánu, deaktivace, chybějící oprávnění |
| Události | Webhooky a automations | Duplicitní doručení, neplatný podpis, chyba před vznikem běhu |
| Čekání | Trvalé waitpointy, Inbox; QUA-13 přežil restart | Schválit, zamítnout, timeout, opakované a zastaralé rozhodnutí |
| Selhání/retry | Retry politiky a ukládání chyb | Selhání uprostřed, vyčerpání retry, srozumitelný stav a dostupný výstup |
| Zrušení/obnova | Registry, cancel, resume, hash ochrana | Zrušit běžící i čekající; žádný pozdější krok, žádné oživení pozdní odpovědí |
| Historie | Verze, uložené vstupy a výstupy, replay | Opakování po změně definice; jasná verze a riziko opakování zápisů |
| Issues | Přesný run ID, Lead review; QUA-12/13 v předání | Regresní kontrola integrace, nespojovat obecné replay s novým Issue execution bez kontraktu |
| UI chyby | Existující error tests | Výpadek API, prázdný katalog, malé displeje, dlouhý název/výstup |

Nové živé scénáře připravit jako samostatné testovací rutiny bez externích
vedlejších účinků. V tomto analytickém průchodu se nic nespouštělo v týmech,
nerestartoval server ani neměnily existující Issues. Cizí WIP zachováno.

## Ověření tohoto průchodu

- 335 frontendových testů v 29 souborech prošlo: celý adresář testů Routines
  a navíc routines-overview, routine-inputs, routine-flow.
- `go test ./internal/pipeline -count=1 -timeout=10m` prošel.
- `go vet ./internal/pipeline` prošel.
- `go test ./internal/api -run 'Test.*(Pipeline|Routine|Waitpoint)' -count=1 -timeout=10m` prošel.
- Wireframe ověřen Chromium/Playwright přes Next.js dev1 na localhost:3011:
  navigace všech tří obrazovek, žádné JS chyby, šířka 390 px bez horizontálního
  přetečení. Veřejná doména zatím vrací pro tuto novou cestu aplikační shell;
  HTML nebylo zahrnuto do nového produkčního buildu. Dostupný je místní soubor.
- Nebyl proveden nový kompletní Go/frontend release gate ani živý E2E.
  Zelené izolované testy nejsou důkaz všech integrací či produkční připravenosti.
