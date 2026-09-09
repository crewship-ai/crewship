# Routines 1.0 — univerzální pracovní prostor, definice a historie provedení

2026-09-08 · dev1 · baseline dev1/issues-preview @ 492c90ad9.

Tento návrh je určující pro UI navazující práce. Účetnictví a Terraform byly
příklady; nesmějí určovat navigaci, schéma výsledků ani požadavky na klienta.
Předchozí backend audity zůstávají evidencí jednotlivých mezer. Není to změna
existujícího kontraktu Issues, Inbox nebo Chat.

Wireframe: public/design/routines-workspace-v2.html. Samostatné HTML bez
závislostí a API volání, pouze ilustrační data. Detail je rozpracován pro jednu
ukázkovou rutinu a čtyři stavy jejího běhu; ostatní katalogové řádky představují
rozmanitost. Nejde o nasazenou implementaci runtime.

## Hlavní produktové rozhodnutí

Tři identity, které klient musí vždy rozeznat:

- Rutina: k čemu slouží, co potřebuje a kdy se spouští.
- Definice/verze: připravený postup, který lze prohlížet a upravovat.
- Běh: jedno konkrétní provedení, jeho kroky, pokusy, činnost a výsledky.

Graf definice zůstává viditelnou hlavní součástí detailu rutiny. Předchozí návrh
schovat jej standardně pod rozbalení neodpovídá upřesněnému požadavku uživatele.
Graf konkrétního běhu naopak musí používat jeho historickou verzi.

## Obrazovky a navigace

### A. Dashboard Routines

Upřesnění uživatele 2026-09-08: zachovat původní design aplikace. Levý
RoutinesExplorer je hlavní katalog s vyhledáváním, filtry stavů, autorů a
použití, ikonami a barvami. Zůstává dostupný v přehledu, detailu i historii
konkrétního běhu. Na telefonu se otevírá nad obsahem, na desktopu je standardně
rozbalený. Úprava ikony a barvy v hlavičce rutiny zůstává zachovaná.

Hlavní plocha zachovává původní dashboardové karty a grafy sdílené s aplikací;
nová samostatná tabulka katalogu se ruší. Původní HTML wireframe je v těchto
vizuálních rozhodnutích překonaný tímto upřesněním.

Přepínání Přehled / Kalendář / Poslední běhy. Kalendář zobrazuje plánované
termíny, nikoli fiktivní dokončení. Kliknutí na budoucí termín otevře rutinu/plán;
kliknutí na minulý skutečný běh otevře run. Událostní a ruční rutiny nemají
vymyšlený next_run_at. Aktivace rutiny, pozastavený plán a výsledek posledního
běhu jsou odlišné údaje.

Čítače a filtry sdílejí stejný výpočet a časové okno. Úspěšnost uvádí velikost
vzorku. Historická chyba po pozdějším úspěchu patří do historie, nemusí znamenat
aktuální problém. Pozornost musí zahrnovat i živé waitpointy a NEEDS_HUMAN,
ne jen poslední technický status.

### B. Detail rutiny

Název, účel, tým, Spustit a Upravit plán. Sekce:

1. Postup / definice: viditelný graf se skutečnými závislostmi. Výběr kroku
   ukáže vstupy, úkol, vykonavatele, očekávaný výstup a chování při neúspěchu.
   Agentní krok může mít vlastní tool loop; nekreslit každé otevření souboru
   jako krok celé definice.
2. Historie běhů: datum/čas, původ, použitá verze, výsledek a výstupy.
3. Nastavení: zdroje, přístupy, tým, rozpočty, retry, verze, technická definice.

Vedle grafu: plán, očekávané výstupy a poslední/aktuální běh. Slug, hash,
procento agentních kroků a opakované čítače netvoří hlavní obsah.

### C. Detail konkrétního běhu — nejvyšší implementační priorita

Header uvádí datum/čas, původ, verzi, průběh a skutečný výsledek. Ovládání
závisí na stavu a oprávnění: zastavit, rozhodnout, prohlédnout chybu, spustit
znovu. Nové spuštění má nový run ID; rozhodnutí pokračuje v původním běhu.

Sekce Průběh a kroky / Vytvořené výstupy / Vstupy a souvislosti.
Pevná identita běhu zůstává při přepínání všech sekcí.

Průběh obsahuje:

- přehled skutečných kroků a jejich stavů;
- rozbalení větví, foreach položek a vnořených rutin;
- detail kroku: zadání/vstupy, kdo nebo co jej vykonalo, čas a výsledek;
- chronologickou činnost agenta a nástrojů;
- oddělené pokusy, jejich připomínky a výstupy;
- dostupné výstupy už během běhu, se stavem draft/ověřeno/předáno;
- čekání s konkrétním důvodem a stejným rozhodnutím jako v Inboxu.

Wireframe používá pět lidsky pojmenovaných fází; fáze „Vyhodnotit a ověřit“
sdružuje dvě větve grafu. Produkční renderer musí zachovat skutečné jednotlivé
uzly i jejich závislosti; agregovaná fáze je volitelný nadřazený pohled.
Počet hotových kroků není procento zbývajícího času, zejména u větvení/dávek.

### D. Výstupy

Společný prohlížeč musí umět soubor, text, strukturovaná data, externí záznam
a potvrzení o akci. Není nutné mít všechny specializované preview v první verzi;
spolehlivý generic renderer je lepší než prázdná karta.

Každý výstup uvádí původní běh/krok/pokus, stav, název a dostupnost. Soubor,
který agent pouze četl, není „vytvořený výstup“. Z textu odhadnutá cesta není
ověřený soubor. Nedostupný originál nebo expirovaný odkaz musí být přiznaný.
Oprávnění se kontroluje při otevření, nestačí tajný odkaz.

### E. Nová rutina a kalendář

Cíl → tým → zdroje/výstup → spouštění → čitelný návrh → ověření → aktivace.
Zdrojové vazby se upřesní podle návrhu Leada, klient nemusí vyplňovat neznámé
technické požadavky dopředu. Kopie/import/editor jsou alternativní vstupy
do stejného závěrečného souhrnu.

Spouštění: ručně, jednorázové datum/čas, opakování, událost. Náhled několika
konkrétních termínů v určené zóně; cron jen pokročile. Skutečný Run vyžádá
deklarované vstupy a po potvrzení otevře detail vzniklého běhu.

## Co lze použít z existující implementace

- internal/api/pipeline_runs.go:GetRun: vstupy, výstup, per-step outputs,
  sub_spans, warnings, outcome, původ a vazba na Issue.
- pipeline_runs: pipeline_version a definition_hash už existují — nepřidávat
  znovu stejné sloupce. GetRun je v aktuální podobě do odpovědi nevybírá.
- pipeline_versions: archiv definic; ověřit dostupnost verze podle uložené
  identity běhu, nedělat fallback na aktuální HEAD bez označení.
- pipeline_run_step_outputs: poslední uložený výstup podle (run_id,step_id).
  Tento upsert není úplný archiv všech pokusů.
- journal: step lifecycle, retry, validation a agent tool subspans.
- Activity: TraceCanvas, přehledy běhů, rozbalení agentní činnosti a renderery.
- lib/trace/collect-step-files.ts: spojuje přečtené/zapsané soubory a odhady
  z textu. Hodí se pro diagnostiku, ne jako autoritativní registr výsledků.
- Existující waitpointy, approvals a Issue execution: zachovat zdroj pravdy.

## Backend/API: konkrétní implementační návrh

### První řez bez migrace

Rozšířit existující GetRun response o pipeline_version, definition_hash,
odkaz na historickou definici a informaci o úplnosti dostupné historie.
Přidat tyto údaje a outcome do FE typů. Definici načíst přes existující version
store/API; případná backend agregace zabrání dalším klientským roundtripům.
Při chybě načtení step outputs vrací dnešní GetRun prázdnou mapu; UI musí
dostat údaj o chybě sekce, aby netvrdilo „žádné výstupy“.

Společná read projekce pro detail Routines i Activity:

run identity + technical_status + outcome + definition reference
steps tree + attempts + available outputs + current human action
data availability + pagination cursors

Žádný druhý executor ani kopie stavů do Routines-only tabulek. Seznamy
stránkovat; události a velké výstupy načítat až po otevření kroku. WebSocket
událost invaliduje projekci; po reconnectu doplnit chybějící data a deduplikovat
podle stabilní event identity. Selhání realtime není ztráta historie.

### Historické pokusy

Nejprve ověřit rekonstruovatelnost z journalu: identita větve, položky, pokusu,
modelu a výstupu. Pokud chybí, frontend je nesmí dopočítat z finálního stavu.
Pro nové běhy ukládat explicitní identity provedení. Stará data označit
„detail pokusů není dostupný“, nevyrábět zpětně časovou osu.

### Povinné kontroly

Oddělit advisory grading od required gate. Required gate se při chybě
hodnotitele neuzavře jako úspěšná. Napojit výsledný stav na stejnou projekci
výsledku běhu. Toto je samostatný backend řez s regresními testy.

## Databáze: minimální rozšíření, ne přepis

Níže jsou kandidátní kontrakty pro další implementační řez, nikoli již
aplikované migrace. Nejprve prověřit journal a existující attachment storage.

1. pipeline_step_executions, pokud journal nestačí pro stabilní historii:
   id, run_id, parent_execution_id, step_id, execution_path, attempt,
   status, agent/model identity, started_at, ended_at, result/error reference.
   Unikátní (run_id, execution_path, attempt); cesta rozliší opakované uzly
   a foreach položky. parent_execution_id tvoří strom skutečných provedení,
   definice stále drží závislosti DAG. Indexy pro run a parent.
2. pipeline_run_artifacts: id, run_id, step_execution_id (nullable pro starší
   běhy), kind, label, media_type, reference to blob/record, hash/revision,
   state, created_at. Původ z journalu/explicitního výsledku ukládat jako
   provenance. Blob storage, přístupová práva a dedupe znovu nevynalézat.

Autoritativní execution rows vznikají v executor lifecycle, nikoli scrapingem
UI logů. Tam, kde jde o jednu DB, uložit výsledek a přechod stavu atomicky;
publikace události musí být obnovitelná a nesmí být jediným důkazem dokončení.
Audit řádků a CAS terminalizaci navrhnout proti restartům/duplicitním callbackům.
DB nesmí vytvářet iluzi exactly-once externích akcí.

Historický obsah výstupu se nesmí změnit přepsáním stejné souborové cesty.
U exportovaných souborů ukládat neměnný obsah nebo ověřovaný odkaz na revizi.
Retence nesmí smazat výstup, který stále vlastní Issue attachment.
Vše scopeovat přes workspace vlastníka run; unikátní ID samo není oprávnění.

Pro kalendář nepřidávat table calendar_events jako kopii cron occurrences.
Opakované termíny odvozovat z plánů; jednorázové trvalé spuštění napojit na
existující pending queue. Zvolit a otestovat API kontrakt, storno před startem,
restart a časovou zónu. Začátek/konec série či výjimky přidat jen s jasným
produktem, ne jako nový univerzální kalendářový backend pro 1.0.

## Pořadí samostatně ověřitelných změn

| Řez | Změna | Přejímací podmínka |
| --- | --- | --- |
| 1 | Shared RunDetail + outcome + historická definice | Spustím rutinu a vidím skutečný průběh; otevření starého běhu neukáže HEAD; Routines/Activity souhlasí. |
| 2 | Hlavní přehled + detail definice + historie | Katalog, graf a run nejsou zaměněné, fungují deep links/back, chyby API jsou čitelné. |
| 3 | Přesné pokusy a výstupy | Dva pokusy zůstanou samostatné po reloadu, přečtené soubory se neukážou jako vytvořené, výstup je dostupný a nezměněný. |
| 4 | Kalendář + tvorba + vstupy | Klient nepíše cron, vidí konkrétní termíny; potvrzení Run otevře vzniklý běh. |
| 5 | Required review, Stop/restart a živý regresní průchod | Neověřený výsledek není zelený, rušení neobnoví práci, waitpointy a Issues zachovají kontrakt. |

Řez 1 je nejlepší první implementace: přímo plní uživatelovu hlavní potřebu
„vidět, jak to běželo“. Řezy 3 a 5 se nesmějí vydávat za hotové na základě
pouhého nového layoutu. U skutečného backend/frontend kódu dodržet issue claim,
oddělenou větev z main a explicitní závislost na dosud nesloučeném Issues PR;
dev1 preview nesmí přijít o jeho kontrakt ani cizí WIP.

## Stavové a obecné testy

Prázdný katalog, ruční/cron/webhook/Issue původ, běh bez agenta, agentní krok,
souběžné větve, podmínkou přeskočený krok, foreach položky, vnořená rutina,
queued/running/waiting/completed/failed/cancelled/interrupted, retry s dvěma
odlišnými výsledky, technicky completed + outcome FAILED/NEEDS_HUMAN, výpadek
API, odpojení realtime, stará nebo smazaná definice, nedostupný/velký výstup,
odmítnutí oprávnění, vícenásobné kliknutí, reload a browser back.

Zvlášť: restart v půli kroku, zrušení během čekání, zastaralé schválení,
neměnnost historického artefaktu, DST a jednorázové datum bez ročního opakování.
Fixture model musí pokrýt obecné tvary workflow, ne všechna konkrétní odvětví.

## Ověření návrhu

Wireframe se ověřuje Chromium/Playwright proti Next.js dev1 localhost:3011.
Produkční veřejná doména servíruje sestavený aplikační shell a nový soubor
zatím není součástí embedded buildu. Kvůli návrhu se živý server nerestartuje.
Playwright ověřil dashboard, definici a výběr uzlu, spuštění přes vstupní dialog,
přepínání čtyř historických/živých stavů, dva pokusy selhání, tři druhy výstupu,
náhled artefaktu, přechod ke schválení a tvorbu s jednorázovým datem.
Žádné pageerror. První mobilní průchod odhalil přetečení definice; opraveno
min-width:0 na grid položkách a opakovaný průchod při 390 px prošel.
Kalendář a detail běhu také bez přetečení stránky. Samostatně ověřeno filtrování
a prázdný výsledek hledání. Screenshoty jsou v /tmp/routines-v2-*.png.
Nejsou provedeny nové aplikační změny, DB migrace ani nový kompletní release gate.

## Approved unified navigation refinement (2026-09-08)

Preserve the original explorer, icon/color controls, spacing, dark gray cards
and existing graph renderer. The recipe and a selected run share one identity
header and Definition / History / Versions / Plan / Settings navigation.
Overview, calendar and recent runs share the same card palette. A run is a
historical execution of this recipe, not a differently branded application.

History is executions; Versions is immutable recipe changes. Archives show a
graph and comparison with the current version. Use as draft opens an explicitly
unsaved editor buffer; saving follows the existing save/reclassification gate
and creates a version. This is not a persisted draft collaboration feature.
Run again reviews inputs and selects either the current or executed recipe.
Historical starts preserve HEAD and validate credentials/integrations/resources
against the selected definition; runtime step overrides remain governed by the
executor and captured in the effective execution snapshot.

The explorer/detail latest state uses pipeline_runs. A technically completed
agent run with FAILED outcome must not be presented as successful. A boolean
false output is data, not an execution error; only author-declared output
value_labels assign business meaning. Optional input labels retain stable input
names and types and are shared with the Chat form contract.

Activity links carry the same routine slug and run ID. Routines and Activity
render the same run component and journal timeline. A pending approval links to
its existing workspace-scoped Inbox item; no duplicate decision model is added.
Settings links credentials, integrations and routine Activity; bound Issue runs
retain their Issue link and existing acceptance/review ownership.

Release fixtures using reserved test-routines-/test-issue- prefixes are hidden
from the normal explorer/overview/calendar unless Show test recipes is enabled.
They remain addressable, clearly marked, and unmodified. This prefix convention
is presentation filtering, not authorization or a new database lifecycle state.

## Full calendar navigation and date-based scheduling

User-approved addition: Day / 3 days / Week / Month / Year, Today, previous/next,
direct date navigation and routine identities on calendar entries. Monday starts
a week. Day/3-day/week use hour rows with independently clickable slots; month
and year dates open Schedule a routine. Year month headings drill into a month.
All views retain the existing explorer and gray card design. Calendar position
is linkable with tab=calendar, calendar=<view>, date=YYYY-MM-DD.

The scheduling dialog chooses an active existing recipe, date, local time and
typed declared inputs. It creates an actual fire_at pending run through the
existing Run API, refreshes only after a confirmed pending_id, and preserves
errors without claiming success. Repeating schedules remain in the recipe Plan.
The displayed timezone is the browser's local timezone; nonexistent DST hours
and past times are refused. Entries represent starts, not invented durations.

Year queries twelve monthly windows with at most three simultaneous requests,
respecting the existing 32-day API bound. The pending-run calendar query now
filters by workspace AND displayed interval before limiting. The generic list's
implicit fallback to 50 rows previously hid distant dates. Dense responses mark
truncated explicitly. No migration or separate calendar database is introduced.

### English presentation and unified Plan visibility

Routines explicitly formats dates/weekdays/months in en-GB; the browser's
local timezone is retained. This separates application language from execution
timezone. Plan's Schedules section contains both one-time starts (including
Calendar additions) and repeating schedules. An empty recurring list must never
claim that no schedules exist. One-time rows lead, and technical concurrency
settings are collapsed below the scheduling controls.

## Guided recipe authoring — first implementation

The manual creation surface opens on Overview and expands to a wider editor
while retaining the shared gray CreateSurface and a left section navigator:
Overview, Inputs, Steps, Outputs, Code, Schedule and Validate. Templates are an
explicit expandable choice in Overview. Inputs/outputs and ordered step summaries
read the same live definition; editing these structures remains in YAML/JSON for
this slice. Steps also shows the definition graph. Section switches keep the
editor mounted, and returning from its graph uses the current buffer.

Validation is named accurately: test_run performs static validation and mints a
save token, not a real execution. The creation UI no longer offers bypassing it.
Saving retains the token contract and configured trigger; Schedule explicitly
explains activation on save. Real execution remains Run after saving, with
History/Activity. Drafts are explicitly unsaved in-memory buffers, not persisted
server drafts. Forks identify their source and create a separate routine. Existing
metadata and arbitrary DSL fields remain supported; this is not yet a visual
step-construction engine.

## Prepared input forms

Overview now owns What you provide (editable questions and form preview) and
What you receive; Inputs/Outputs are no longer separate navigation destinations.
The author can set a question label, stable variable name, help text, answer
kind, required flag, prepared choices, custom-answer permission, typed default
and numeric bounds. Defaults can be cleared. Supported controls are short/long
text, single choice, multiple choice, whole/decimal number, boolean and JSON
object/list. Preview answers are local, not saved run inputs. Step cards expose
script/code configuration when declared.

InputSpec adds optional widget, options (string choices), allow_custom and
placeholder metadata. Data type remains independent of presentation. Multiple
choice uses an array of strings, JSON-encoded in the shared form's string state,
so commas/newlines in values do not become delimiters. The existing Ask forms'
multiselect encoding is preserved unless value_type explicitly says array.
Single-choice custom answers use an editable suggestion field. These fields
flow through the shared Routines/Calendar form mapping and the server's Chat
slash catalog. No database migration: definitions and historical versions
already store JSON.

New form declarations opt into static/default/choice/type/required/bounds checks.
The run API rejects invalid answers before enqueue; executor entry and nested
execution also enforce the checks, including defaults. Legacy unannotated
inputs retain their existing runtime contract. Strings entered as answers remain
data; {{ inputs.name }} is how authors reference variables in recipe steps, not
an invitation to recursively evaluate user answers. No credential values are
introduced as a new form field type.

## Shared creation and editing builder (2026-09-08 follow-up)

Keep the original explorer and visual language. Recipe and historical run headers
expose Edit; it opens the same builder as New routine, prefilled from the current
recipe. Definition / History / Versions / Plan remain reading/navigation views;
Settings is removed. Old settings links open the editor. Access, budget and
technical metadata are expandable in Overview. Historical restoration seeds an
unsaved builder draft and saving records a new recipe version.

The builder offers the existing icon/color picker and a crew-scoped agent picker
with real avatars for each top-level agent_run step. This edits agent_slug in the
recipe, not a cosmetic owner. Different steps may use different agents; nested
steps and review agents remain configurable in Code. Existing acting-agent
identity is preserved when the crew is unchanged. Icons are saved through the
existing appearance API; a failed appearance write reports the partial save and
retries only appearance, without duplicating recipe save/scheduled creation.

Editing keeps the slug and omits trigger creation from save. Schedule opens the
existing schedule/webhook managers; changes there, and budget changes, apply
immediately and say so. They are independent of the unsaved recipe buffer.
Creation continues to use atomic recipe+trigger save. Static validation is still
required and never executes work. No database migration or Issues contract change.

## Readable authoring hierarchy (2026-09-08 refinement)

Use the shared New Project identity pattern: icon and name on one line, a short
brief and team. Start form and declared results are counted disclosures; technical
identity is secondary. Preserve the explorer and builder navigation.

Steps has mutually exclusive List / Graph views. Compact step rows show the
actual action and input condition; selecting one reveals its editable fields.
Edits retain unknown configuration, dependencies and conditions. On small screens
the selected inspector replaces the list, with an explicit return. Animations
respect reduced motion. This is recipe configuration, never simulated execution.

Recurrence uses day/week/month presets, including weekends and multiple weekdays.
Unsupported cron stays intact in Advanced. One-time scheduling uses an English
calendar picker plus time. Repeating schedules are primary, one-time starts
secondary, event triggers expandable. Scheduling still uses existing APIs; no
new database representation or execution contract is introduced.

## Sjednocení popupu s Credentials

Upřesnění uživatele: editor má používat stejný design jako Credentials / Add
secret. Sdílený CreateSurfaceSteps nahrazuje pouze vnitřní svislou navigaci
editoru; hlavní levý RoutinesExplorer se nemění. Horní kroky Overview, Steps,
Schedule, Validate lze přímo přepínat, protože nejde o závislé odpovědi.
Credentials zachovávají dosavadní zákaz přeskakování budoucích kroků.

Popup má šířku 800 px pro graf a inspektor, společné záhlaví, pole a patičku.
Code je samostatné tlačítko s návratem do poslední sekce. Back / Continue jsou
v patičce; Validate & Save se objeví v posledním kroku. Klávesová zkratka
odpovídá aktuálnímu hlavnímu tlačítku. Přecházení sekcí neukládá ani nespouští
rutinu. Rozepsaný kód i přehled zůstávají při navigaci zachované. Výjimka pro
plánování existující rutiny je nadále viditelná: změny plánu platí ihned.

Start form a Results jsou kompaktní rozbalitelné řádky. Ikona/barva, tým,
avatary agentů, List/Graph, inspektor kroku i kalendář zůstávají zachované.
