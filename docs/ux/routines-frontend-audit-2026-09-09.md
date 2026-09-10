# Routines — audit čitelnosti frontendu, 9. 9. 2026

Evidence k [work orderu](../prd/WORK-ORDER-2026-09-09-ROUTINES-FIX-AND-UX.md) §8.
Auditován pracovní strom `/srv/crewship/crewship_1` na `cd2074d0b` + necommitnuté
změny, tj. přesně to, co běží na dev1. Data ověřena read-only dotazem do
`crewship.db`: 27 rutin, 80 běhů, 0 rozvrhů, 81 kroků.

Podnět: **„Frontend je katastrofální. U té rutiny nemám šanci zjistit, co dělá.
Je to nic neříkající."**

## 1. Kořenové příčiny

1. **Účel se nikde nevede jako první.** `description` existuje v DSL
   (`internal/pipeline/types.go:21`), v DB, v obou API DTO
   (`internal/api/pipelines.go:351,426`) a je vyplněný u **25 z 27 rutin**.
   V seznamu se zobrazí **jen v hover tooltipu** (`routines-explorer.tsx:516`).
   Na detailu je až třetí prvek a jen když ho autor napsal. Komponenta, která
   popis na řádku umí vykreslit, existuje — `routines-tab.tsx:144` — ale je
   namountovaná na `/orchestration`, ne na `/routines`.

2. **Kroky se jmenují technickými ID.** `step.name` je v UI primární popisek
   (`routine-step-spine.tsx:272`), ale **0 z 81 kroků na dev1 ho má** — a mít
   nemůže: `schemas/routine.v1.json` → `$defs/Step` má
   `additionalProperties: false` a `name` mezi 27 povolenými klíči není. Go
   struct `Step` ho nemá také. Fallback je `readableFieldName(step.id)`, tedy
   podtržítko → mezera. V rozbaleném detailu je nadpis **surové ID**
   (`routine-step-definition.tsx:11`). Uživatel čte `Red state`, `Page summary`,
   `Scan sha label`. Přitom `describeStep()` (`lib/routine-step-describe.ts`)
   už umí vyrobit „Ask sam", „Wait for approval", „Call routine X" — jen se
   volá až jako sekundární zdroj. **Precedence je obrácená.**

3. **První obrazovka je katalogová statistika, ne odpověď.** `/routines`
   mountuje jako výchozí `RoutinesOverview` — dashboard KPI dlaždic
   (`routines-overview.tsx:190–511`). Jediný skutečný seznam rutin je 280px
   sidebar. Kdo přijde zjistit „co tahle rutina dělá", dostane nejdřív donut
   a graf za sedm dní.

4. **Jeden pojem má pět názvů, jeden název pět pojmů.** „Overview" znamená tři
   různé věci; validace má osm názvů; vstupy sedm; výsledky osm. Editor má tři
   konkurenční navigační úrovně nad týmž stavem (`routine-create-dialog.tsx:1120`,
   `:1129`, `:1127`). Uživatel se neučí model, učí se mapu synonym. Součástí je
   i mrtvý odkaz na neexistující záložku „Triggers" (`routines-overview.tsx:264`).

5. **Prázdnota má stejnou váhu jako obsah, odpovědi jsou schované.** Prázdná
   karta zabírá tolik místa co plná: **27/27** rutin má prázdné „When it runs"
   (v DB je 0 rozvrhů), 8/27 prázdné „You get", prázdný workspace dostane šest
   prázdných karet a nulový donut. Zároveň je chyba běhu za `<details>`
   (`routine-run-detail.tsx:168`), instrukce kroku za dvěma `<details>`, a
   **selhaný krok se nerozbalí sám** (`routine-step-spine.tsx:142`).

## 2. Detail rutiny — pořadí bloků

| # | Blok | file:line | Odpovídá „co to dělá"? |
|---|---|---|---|
| 1 | Banner schválení (jen `proposed`) | `routines-detail-panel.tsx:476` | ne |
| 2 | Identity karta: ikona, název, `Current recipe · v{N}`, agent, tlačítka | `routine-identity-header.tsx:43` | ne |
| 3 | **Popis** — jen když ho autor vyplnil | `routine-identity-header.tsx:54` | **ano, jediný přímý** |
| 4 | Pilulky: lifecycle, status, `agentless`, `scheduled`, `ephemeral` | `routine-card-detail.tsx:200` | ne |
| 5 | Nav: Overview / Run / History / Versions / Schedule | `routine-navigation.tsx:7` | ne |
| 6 | „What it needs and what it returns" | `routine-work-overview.tsx:94` | částečně |
| 7 | **„What this routine does" — prvních 6 kroků** | `routine-step-spine.tsx:162,135` | **ano, ale až tady** |
| 8–10 | Last run · „When it runs" · „Access" | `routine-card-detail.tsx:246,256,332` | ne |
| 11 | Spodní dok: runs / logs / schedule / yaml | `routines-layout.tsx:313` | ne, duplikuje nav |

Ke skutečné instrukci kroku vedou **2 kliknutí** (`<details>` kroku
`routine-step-spine.tsx:312`, pak vnořené „Step configuration" `:415`).

## 3. Detail běhu

15 bloků. Verdikt je blok 3, `run.error_message` blok 4 **zabalený v `<details>`**,
schvalovací banner až blok 7 — pod StatStripem.

| Typ běhu | Nahoře | Selhaný krok auto-rozbalen? | Další akce |
|---|---|---|---|
| Dokončený | identita → verdikt | n/a | „Run again" |
| **Selhaný** | identita → „This run could not finish" | **ne** (`openIds` prázdné, `initialLimit=8`) | „Run again"; chyba až po 2 kliknutích |
| **Čekající** | identita → „A review is needed" | n/a | Approve/Reject až blok 7 |

Běh bez výstupu vykreslí **dvě po sobě jdoucí karty, které obě říkají, že nic
není** (`routine-run-detail.tsx:187`, `routine-run-artifacts.tsx:50`).

## 4. Editor — 29 navigovatelných ploch

Vstup: „Describe it" | „Fork an existing routine" | „Write it yourself".
V advanced režimu tři konkurenční navigační úrovně na jedné obrazovce:

| Úroveň | Položky | file:line |
|---|---|---|
| L1 wizard | Recipe · Test · Publish | `:1120–1126` |
| L2 nav | Overview · Steps · Schedule | `:1129` |
| Plovoucí | **Code** (absolutně pozicované) | `:1127` |
| Interní model | `["Overview","Steps","Schedule","Validate","Publish"]` | `:828` |

L1 a L2 mapují na tentýž stav, ale mají jiné popisky. Sekce Overview obsahuje
**7 skládaček**; Validate → `RoutineTestWorkspace` má 3 taby a uvnitř „Test a
step" další import, výběr kroku, editor upstream outputů, náhradní výstup,
formulář vstupů a `<details>` „Test evidence and limits".

### 4.1 Kolize pojmenování

**„Validate" — 8 názvů jedné akce:** `Validate` (`:828`), `Test` (`:1122`),
`Check your recipe` (`:1133`), `Build confidence before you run`
(`routine-test-workspace.tsx:18`), `Check definition` (`:22`), `Definition check`
(`:27`), `Validate recipe` (`:27`), `Validate & Publish` (`:845`) — plus čtyři
varianty výsledku.

**„Overview" — 3 významy:** workspace dashboard (`routines-workspace.tsx:23`),
recept rutiny (`routine-navigation.tsx:9`), identita v editoru (`:828`).

**Vstupy — 7 názvů:** `Start form` · `You provide` · `What you provide` ·
`Questions` · `Inputs` · `Input schema` · `Reviewer questions`.

**Výsledky — 8 názvů:** `Results` · `You get` · `Recorded result` ·
`Declared outputs` · `Saved files and outputs` · `Expected results` ·
`Result checks configured`.

**Kroky:** `Steps` · `Workflow` · `Recipe workflow` · `What this routine does` ·
`What happened, step by step`. **Upravit:** `Edit` · `Edit recipe` ·
`Edit definition` · `Edit code in Definition` · `Advanced configuration in Code`.
**Spouštění:** `Schedule` · `When it runs` · `Firing next` · **`Triggers`**
(neexistuje). **Běhy:** `History` · `Runs` · `Recent runs` · `Run`.

## 5. Žargon prosakující k uživateli

Nepochopitelné pro neexperta, s místem výskytu: `fixture` / „Test with fixtures"
(`routine-test-workspace.tsx:23`), `waitpoint payload`
(`routine-approval-banner.tsx:150`), `execution_path` vykreslené syrově
(`routine-run-artifacts.tsx:51`), `definition` místo „recept"
(`routine-publication-review.tsx:7`), `DSL version` (`routine-card-detail.tsx:556`),
`SHA-256` / `definition_hash` (`:558`, `routine-fixture-test.tsx:64`), `slug`,
`pinned_version`, `Idempotency-Key` v chybové hlášce (`routine-comparison.tsx`),
„JSON object keyed by step ID" (`routine-fixture-outputs-editor.tsx:28`),
`upstream outputs`, `ephemeral` (`routine-card-detail.tsx:204`),
`concurrency_key` / `max_concurrent` / „(429)" (`routine-schedules-tab.tsx:337`),
`chain N` (`:675`), `Run ID` jako vstupní pole bez zdroje
(`routine-fixture-import.tsx:33`).

## 6. Wireframy cílového stavu

Vše používá data, která už existují, není-li uvedeno jinak.

### (a) Řádek seznamu

```
┌────────────────────────────────────────────────────────────────────┐
│ 🌐  Audit a public web page                        ● hotovo · 2d    │
│     Načte veřejnou stránku a vrátí audit obsahu a navigace.        │
│     3 kroky · ručně · 0 běhů za 7 dní                     [Spustit] │
└────────────────────────────────────────────────────────────────────┘
```

Druhý řádek = `Pipeline.description`, dnes jen v tooltipu. Když chybí (2/27),
odvodit větu z `describeStep` prvního a posledního kroku. Seznam patří do
**hlavního panelu**; dnešní dashboard se stává druhou záložkou.
Jediná backendová položka: **`step_count`** do list DTO
(`internal/api/pipelines.go:351,426`), spočítat z parsované definice —
bez migrace, bez změny schématu.

### (b) Detail rutiny

```
┌── 🌐  Audit a public web page ─────────────────── [Spustit] [⋯] ───┐
│  Načte veřejnou stránku a vrátí audit obsahu a navigace.           │
│  ručně · v3 · naposledy před 2 dny: hotovo (12 s)                  │
└────────────────────────────────────────────────────────────────────┘

  Co udělá  (3 kroky)                                    [Mapa] [Kód]
  1 🌐 Stáhne stránku            GET {{ adresa }}              12 s ✓
  2 🤖 Sam vytáhne strukturu     „Auditni HTML níže. Vrať…"     8 s ✓
  3 ⚙ Převede na JSON            výstup kroku 2               <1 s ✓

  Co zadáš                          Co dostaneš
  Adresa · text · nepovinné         data · text

  ┌── Poslední běhy ───────────────────────────────────────────┐
  │ 7. 9. 09:12  hotovo  12 s  ručně                          → │
  │ 5. 9. 18:40  selhal   3 s  ručně  krok 1: 404             → │
  └────────────────────────────────────────────────────────────┘

  ▸ Spouštění (žádný plán)   ▸ Přístupy (1)   ▸ Verze (3)
```

Popis **vždy** jako druhý řádek identity. „Co udělá" je první blok pod
identitou. Název kroku = `step.name` → `describeStep().title` → ID až jako
sekundární mono chip. Zobrazit všechny kroky do 12 (`initialLimit` 6 → 12).
Prázdné karty degradovat na sbalený řádek. Spodní dok na detailu vypnout.

### (c) Detail běhu

```
┌── ✗ Běh selhal ────────────────────────────────────────────────────┐
│  Audit a public web page · 5. 9. 18:40 · 3 s · ručně · v3          │
│  Krok 1 „Stáhne stránku" skončil chybou:                          │
│  http 404 for https://example.com/missing                          │
│                                    [Spustit znovu]  [Otevřít krok] │
└────────────────────────────────────────────────────────────────────┘

  Co se stalo  (3 kroky)
  1 🌐 Stáhne stránku       ✗ selhal  3 s  ← rozbaleno, s chybou
  2 🤖 Sam vytáhne strukturu   — neproběhlo
  3 ⚙ Převede na JSON          — neproběhlo

  Výsledek: žádný nebyl zaznamenán.   Soubory: žádné.

  ▸ Zadané vstupy   ▸ Průběh   ▸ Pokusy   ▸ Technické údaje
```

Verdikt, chybová hláška i identifikace selhaného kroku v jednom bloku nahoře.
Selhaný / aktuální / čekající krok auto-rozbalit (inicializovat `openIds`
z `record`) a vždy zahrnout do `shown` bez ohledu na `initialLimit`.
U čekajícího běhu `RoutineApprovalBanner` nad StatStrip. Dvě prázdné karty
sloučit do řádku.

## 7. Co ponechat

**Fixní a zároveň dobré:** grafický systém (`DetailCard`, `Pill`, `KpiCard`,
`StatusDonut`, `StatStrip`, `EntityChip`), sidebar včetně stavových kbelíků
a zkratek `/` `Esc` `c` (mění se **jen obsah řádku**), kalendář rok/měsíc/týden/den
s plánováním klikem, ikonový systém (`resolveRoutineIcon/Color`, `STEP_VISUALS`).

**Dále stojí za zachování:** jeden krokový hřbet pro recept i běh
(`routine-step-spine.tsx`) — správný model, jen špatně pojmenované řádky;
`routineRunPresentation` / `routineRunExplanation`
(`lib/routine-run-presentation.ts`) — jediné místo v kódu, které mluví lidsky
a nelže o stavu, vzor pro zbytek; `describeStep`; `runProvenance`; `describeCron`;
`RoutineSavedInputs`. Celá nová testovací a publikační mašinérie je věcně
správná a testy pokrytá — **nemazat, jen ji schovat pod jedno „Ověřit"
a zbavit žargonu.** Není to plocha pro klienta, je to plocha pro autora.
