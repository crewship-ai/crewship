# Work order — Routines: opravy, integrace, přepracování Edit/Test

Datum: 2026-09-09. Autor zadání: nezávislá revize (Claude Opus 5) na žádost
uživatele. Adresát: Codex s vyčištěným kontextem.

Tento dokument je **jediný aktuální seznam práce**. Nahrazuje seznam „co dál"
v [ENDING-2026-09-09](ENDING-2026-09-09-ROUTINES-REVIEW.md); ten zůstává jako
evidence stavu, nikoli jako zadání. Nezačínej psát nový PRD ani nový handoff.

## 0. Než uděláš první commit

Přečti v tomto pořadí: `AGENTS.md`, `CODEX.md`, `CLAUDE.md`,
[ENDING-2026-09-09](ENDING-2026-09-09-ROUTINES-REVIEW.md) (stav) a
[PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md) (zadání). Pak tento soubor.

Tři evidenční dokumenty, na které se tenhle work order odvolává — čti je, až
budeš dělat příslušnou sekci, ne dopředu:

- [audit frontendu](../ux/routines-frontend-audit-2026-09-09.md) → §7
- [důkazní hodnota testů](ROUTINES-TEST-VALIDITY-2026-09-09.md) → §8
- [rešerše čitelnosti u konkurence](../ux/routines-competitive-legibility-2026-09-09.md) → §9

Výchozí stav, ověřeno 2026-09-09:

- Pracovní kopie `/srv/crewship/crewship_1`, větev `dev1/issues-preview`,
  HEAD `cd2074d0b`. Nasazeno na `crewship-dev1.unifylab.cz` (`:8081`).
- **62 commitů před `origin/main`, 13 za ním.** Na `origin/main` je 9 migrací,
  které tady nejsou (workspace conversations, provider pool, private service).
- **87 změněných + ~30 untracked souborů, žádný commit, žádný PR, žádné CI,
  žádný CodeRabbit.** Untracked soubory jsou podstatná část implementace;
  samotný `git diff` je nezahrne.
- Ověřeno: `pnpm exec vitest run components/features/routines/__tests__
  components/features/approvals/__tests__ lib/__tests__/routine*` →
  77 souborů / 692 testů zelených, exit 0.

**Nepokračuj v přidávání funkcí.** Pořadí práce je: opravit → integrovat →
přepracovat frontend. Nic jiného.

---

## 1. Nálezy code review (opravit v tomto pořadí)

Číslování je závazné, používej ho v commit messages a v PR popisech.
Ke každému nálezu patří **červený test na současném kódu → oprava → zelený**.
Test, který na `cd2074d0b` neselže, opravu nedokládá.

### P0 — kritické, blokují publikaci a spouštění

**N1 · `internal/api/pipeline_drafts.go:91` — draft s `&`, `<` nebo `>` nelze
publikovat.** `d.Document, _ = json.Marshal(doc)` použije výchozí HTML escaping
Go, takže `&` → `&`. `PublishDraft` pak hashuje tyto uložené bajty
(`pipeline.DefinitionHash` = holé sha256, bez kanonizace), zatímco prohlížeč
razil `save_token` nad výstupem `JSON.stringify`, který neescapuje. Jakékoli
`&` v HTTP URL nebo `>` v `when:` podmínce znamená trvale nepublikovatelný
draft: `422 save_token invalid`.
*Oprava:* kanonizovat na jednom místě — `pipeline.ToCanonicalJSON` už v repu
existuje a používá ji `fixture_steps.go`. Hashuj kanonickou formu na obou
stranách, nebo vypni HTML escaping encoderem s `SetEscapeHTML(false)`.
*Test:* recept s `https://x/?a=1&b=2` v HTTP kroku projde save → test_run →
publish. Přidej i regresní test na `>` v podmínce.

**N2 · `components/features/routines/routine-comparison.tsx:53` — porovnání
spouští běhy synchronně a zabíjí je zavřením tabu.** POST na
`/pipelines/{slug}/run` neposílá hlavičku `Prefer: respond-async`, kterou
posílají obě ostatní startovací cesty (`routine-run-detail.tsx:91`,
`routines-detail-panel.tsx:249`). `PipelineHandler.Run` proto spadne do
synchronního `exec.Run(r.Context(), input)`: každý případ blokuje fetch po celou
dobu běhu a při zavření tabu nebo timeoutu proxy se `r.Context()` zruší a běh
zemře uprostřed. To přímo popírá text, který komponenta sama zobrazuje
(„Leaving this Test view stops the remaining queue, not runs already started").
Polling Pending/Continue na řádcích 61–69 je nedosažitelný.
*Oprava:* přidat `Prefer: respond-async`. *Test:* handler test, že se běh
nezruší po zrušení request contextu.

**N3 · `internal/pipeline/drafts.go:171` (dotaz na `:148`) — legacy netypovaný
vstup natrvalo blokuje publikaci.** Předkontrola rozvrhu volá `validateFormValue`
na *každý* `dsl.Inputs`, zatímco zavedený kontrakt (`ValidateFormInputs`,
`hasInputForm`) neanotované vstupy záměrně přeskakuje.
`{"inputs":[{"name":"topic"}]}` projde `Validate` i `ValidateFormInputs`, ale
spadne do `default:` větve `validateFormValue` → `unsupported value type`.
Každá rutina s netypovaným vstupem a rozvrhem, který ho dodává, vrátí při každé
publikaci 409 `ScheduleDraftConflict` a jediná náprava je smazat rozvrh.
Dotaz navíc postrádá `enabled = 1`, takže publikaci blokuje i vypnutý rozvrh.
*Oprava:* respektovat `hasInputForm`; doplnit filtr na povolené rozvrhy.

**N4 · `internal/pipeline/store.go:426` — jednorázový start se dedupuje na
starý běh.** Upsert nově přezbrojí řádek na `status='pending'` kdykoli se liší
`fire_at`, ale id řádku je stabilní `"pnd_once_" + pipelineID` a
`pending_dispatcher.go:231` z něj odvozuje idempotency key executoru s TTL 24 h.
Start, který vystřelil v 10:05 a je přeuložen na 11:30, dostane v 11:30
`DEDUPED` a `SetFiredRunID` zpětně zapíše run id z 10:05. Původní
`WHERE pending_runs.status='pending'` to činilo nedosažitelným.
*Oprava:* zahrnout `fire_at` (nebo revizi přezbrojení) do idempotency key.

**N5 · `components/features/activity/trace-step-node.tsx:435` — Activity
canvas neumí rozhodovací formulář.** Diff přidal `decision_form` do
`PendingWaitpoint` a upravil všechny ostatní rozhodovací plochy, ale trace
canvas dál volá `waitpointDecide(ws, token, approved)` bez odpovědi. Pro bránu
s formulářem `CompleteApproval` spustí `NormalizeDecisionAnswer` nad prázdným
payloadem a vrátí 400 `action_id and data required`. `TraceStepNodeData.waitpoint`
navíc nese jen `{token, workspaceId}`, takže formulář ani nelze vykreslit —
operátor uvízne na červeném toastu.
*Oprava:* buď formulář vykreslit (sdílenou `HumanDecisionForm`), nebo na této
ploše rozhodnutí s formulářem explicitně odkázat do Inboxu. Tiché 400 ne.

### P0 — vysoké, tichá ztráta dat a falešná selhání

**N6 · `internal/api/pipeline_drafts_internal.go:87` — agentní draft neumí
smazat klíč.** `document` je už naplněný na řádku 55 ze *současného* draftu a
`json.Unmarshal(d.Document, &document)` do té nenulové mapy merguje místo
nahrazení, takže klíče, které agent záměrně odstranil, přežijí. Ověřená cesta:
agent uloží s `trigger` + `description`, pak uloží bez obojího — uložená revize
si obojí ponechá a rozvrh, který agent odstranil, se při lidské publikaci
znovu vytvoří. Uživatelské `SaveDraft` je správně (čerstvá `doc`); postižené je
jen agentní dveře, a podmínka `document == nil` na tom řádku je mrtvá.

**N7 · `lib/routine-comparison.ts:25` — porovnání se po restartu nikdy
nedokončí.** Množina terminálních stavů je `["completed","failed","cancelled"]`,
kanonická množina (`internal/pipeline/runs.go:607`,
`internal/cli/pipeline_runs.go:45`) obsahuje navíc `interrupted` a `dry_run`.
`interrupted` je značka boot recovery — `systemctl reload` uprostřed porovnání
znamená, že `comparisonVerdict` vrací navždy `"Pending"`, `advance()` přeruší
smyčku bez inkrementace `cursor` a frontu už nelze dokončit.
*Oprava:* jeden sdílený zdroj pravdy pro terminální stavy, ne třetí kopie.

**N8 · `internal/pipeline/nested_inputs.go:40` — volitelný vstup se stal
povinným.** `if !referenceValueExists(ref, render)` vrací chybu *dříve*, než se
o dvacet řádků níž zkontroluje `spec.Required`. Pro spec
`{Name:"payload", Type:"object", Required:false}` a `payload: "{{ inputs.overrides }}"`
chybějící `overrides` nově zhavaruje na `referenced value is unavailable`,
zatímco explicitní `nil` projde. Nahrazený kód v `executor.go` propouštěl
vyrenderovaný prázdný řetězec — rodičovská rutina, která volitelný vstup
vynechá, teď shodí celý `call_pipeline` krok.

**N9 · `internal/pipeline/step_executions.go:108-113` a `:137-138` — úspěšný
krok označen jako selhaný kvůli artefaktu.** `err = errors.Join(err,
publisher.PublishRunArtifacts(...))` činí zachycení artefaktu fatálním pro krok.
Přechodná chyba zápisu do DB, selhání `storeAttachmentBlob` nebo
`artifact workspace mismatch` promění úspěšný krok v selhaný a odložené
`finish(ctx, id, out, err)` zapíše exekuci jako `failed` s chybou publikace jako
důvodem. Problémy na úrovni souborů se přitom už řeší elegantně
(`state = "unavailable"`) — ta asymetrie vypadá nezamýšleně.

**N10 · `components/features/approvals/human-decision-form.tsx:28` — nelze
zamítnout.** `decide()` uvolní `required` pro nezamítající akci, ale pořád
spustí `routineInputsFromValues`, které vyhodí `RoutineInputError` při
jakémkoli selhání konverze. Volitelné `object`/`array` pole s rozepsaným JSON
nebo volitelné `integer` s `4.5` zablokuje **Reject** hláškou
`"{foo" is not valid JSON` a běh zůstane zaparkovaný — přestože serverové
`NormalizeDecisionAnswer` `Required` u zamítnutí také ruší.

**N11 · `components/features/routines/routine-create-dialog.tsx:625-627` — v
create flow nelze přejmenovat recept.** Podmínka se rozšířila z `routine &&` na
`(routine || draftRef.current?.id) &&`. V create flow první „Save draft"
nastaví `draftRef.current.id`, takže jakákoli pozdější změna `name:` v Code
bufferu skončí hláškou „Fork the routine to create a separate recipe" — u
receptu, který nikdy nebyl publikován a nelze ho forkovat. Jediné úniky práci
zahodí nebo osiří draft řádek pod starým slugem.

### P1 — střední

**N12 · `internal/api/pipeline_artifacts.go:110`** — promote `UPDATE` matchuje
*každou* exekuci striktně pod `execution_path` rodiče, bez filtru na pokus a
stav. Protože `pipeline_step_executions` má `UNIQUE(run_id, execution_path,
attempt)`, retryovaný potomek má řádek na každý pokus a `foreach` fan-out na
každou položku. Dva potomci publikující stejné `(kind,label,source)` — běžný
případ sdíleného `/crew/shared/x.md` — znamenají, že jedna rodičovská publikace
přepíše `state/content_type/sha256/content/error` u všech, čímž zničí vlastní
záznam selhaného pokusu nebo sourozenecké položky.

**N13 · `hooks/use-run-executions.ts:158`** — `loadMore` je `undefined` po dobu
`loading`, ale `load()` volá `setLoading(true)` při *každém* volání včetně
třísekundového pollu během `active`. Přesně u běhů, kvůli kterým stránkování
existuje (běžící/čekající s >100 řádky exekucí), tlačítko „Load more executions"
při každém cyklu zmizí. Odděleně: `loadMore`/`refresh` předávají
`new AbortController().signal`, který se nikdy neabortuje, takže requesty
přežijí unmount.

**N14 · `components/features/routines/routine-schedules-tab.tsx:314`** —
tlačítka Cancel/Create inline karty „New schedule" se přesunula do
`RoutinePresetForm` → `InputsForm`, které se vykreslí až po načtení schématu
vstupů. Když `GET /pipelines/{slug}` selže, karta ukáže jen chybu a Retry —
žádný submit, žádný cancel — a protože se přepínač „Add schedule" během
`formOpen` skrývá, formulář je slepá ulička bez odchodu ze stránky.

**N15 · `internal/pipeline/runner_wait.go:193`** — schvalovací konec nově vždy
deleguje na `ApprovalOutput`, které se doptává `pipeline_waitpoints` na
`status='approved'` a vrací svou chybu přímo z `runWaitStep`. Tento konec byl
dřív neselhatelný (`return "waited:approval:approved"`). Přechodná chyba čtení
z DB — nebo `ErrAlreadyDecided`, pokud řádek není čitelný pod `in.WorkspaceID` —
teď shodí wait krok a tím celý běh *poté*, co už člověk schválil.

### P2 — pod čarou, opravit při dotyku souboru

`internal/pipeline/waitpoints.go:663` (expirovaný, ale neuklizený waitpoint
hlásí až 30 s `ErrAlreadyDecided`, zatímco jeho inbox karta je pořád aktivní);
`internal/api/pipeline_deferred.go:86` (`err != nil || version < 1` slučuje
skutečné selhání DB do zavádějícího 409); `internal/pipeline/fixture_steps.go:83`
(fixture testy nikdy nerozřeší `env.*`/`run.*`/`secrets.*` reference);
`cmd/crewship/cmd_eval_compare.go:165` (scan `is_head` omezen na prvních 100
verzí); `lib/routine-drafts.ts:13` a `routine-create-dialog.tsx:692`
(`await res.json()` před kontrolou `ok` → syrový `SyntaxError` při 502 z proxy);
`routine-create-dialog.tsx:869` (nehlídané souběžné načtení draftu, vyhrává
pomalejší odpověď); `internal/api/pipeline_calendar.go:64`
(`occurrences[len-1].Before(end)` nehlídá nulový čas, takže nesplnitelný cron
napevno nastaví `truncated: true`); `inbox_take_over.go:40` a
`issues_internal_work.go:39` (konstruují `IssueHandler` s nil `MissionStarter`,
takže `StopMission` se na těchto dvou cestách tiše přeskočí).

---

## 2. Kontrakt API: sedm netypovaných operací

Ověřeno proti `internal/api/openapi.gen.json`: **všech sedm nových
draft/publish operací nemá pojmenované schéma** — pět odpovědí spadne na
neomezený `object`, dvě request body na generický JSON:

```
RESP  GET    /api/v1/workspaces/{workspaceId}/pipelines/drafts
RESP  POST   /api/v1/workspaces/{workspaceId}/pipelines/drafts
REQ   POST   /api/v1/workspaces/{workspaceId}/pipelines/drafts
RESP  GET    /api/v1/workspaces/{workspaceId}/pipelines/{slug}/draft
RESP  DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/draft
RESP  POST   /api/v1/workspaces/{workspaceId}/pipelines/{slug}/publish
REQ   POST   /api/v1/workspaces/{workspaceId}/pipelines/{slug}/publish
```

Zbylých 588 z 611 operací pojmenované schéma má. `fixture_test` ho má taky
(`cmd/gen-openapi/schemas_workflow_request_audit.go:109`) — takže jde
o nekonzistenci, ne o rozhodnutí. Dopiš schémata, přegeneruj spec a oprav větu
s celkovými počty v `docs/api-reference/openapi.mdx`.

---

## 3. Integrace — udělej to dřív, než napíšeš další řádek funkcionality

Nezamergovaný kód má nulovou hodnotu a každý den je dražší.

1. Rebase na `origin/main` (jsi 13 commitů pozadu, 9 migrací chybí). Migrace
   nekopíruj bez příslušného aplikačního kontraktu.
2. Rozděl na **4 PR** v tomto pořadí, každý samostatně zelený:
   - **PR A — draft/publish kontrakt:** `internal/pipeline/drafts.go`,
     `internal/api/pipeline_drafts*.go`, `internal/sidecar/routine_drafts.go`,
     migrace `20260908223757_pipeline_drafts.sql`, CLI `routine draft`.
     Obsahuje N1, N3, N6 a schémata z §2.
   - **PR B — fixtures:** `internal/pipeline/fixture_steps.go`,
     `internal/api/pipeline_fixture.go`, `cmd_routine_fixture.go`,
     `routine-fixture-*.tsx`.
   - **PR C — lidská rozhodnutí:** `decision_forms.go`,
     `pipeline_waitpoint_decider.go`, migrace
     `20260909091500_waitpoint_decision_forms.sql`, `human-decision-form.tsx`,
     Inbox. Obsahuje N5, N10, N15.
   - **PR D — běhy, plány, porovnání:** N2, N4, N7, N8, N9, N12–N14.
3. Na každý PR čekej 2–5 min na CodeRabbit. Když je rate-limited, zrevidovat
   sám na stejnou úroveň, napsat v PR co bylo a nebylo strojově zkontrolováno,
   a `scripts/review-status.sh --retrigger`.
4. **Nikdy nemerguj na červeném CI.** Pozor na známé pasti: CHANGELOG
   serializuje paralelní PR; `pull_request` workflow na konfliktním PR vůbec
   nevystřelí a tři zelené řádky znamenají „oprav konflikt v CHANGELOGu".

---

## 4. Hodnocení: co stačí a co ne

**Go vrstva — stačí, nepřepisuj ji.** CAS a ABA ochrana v `drafts.go`, izolace
fixtures s vlastním offline schema compilerem, zamrznutý rozhodovací formulář
a odvození identity rozhodujícího z auth kontextu (nikdy z těla requestu) jsou
nadprůměrná práce. Po opravě N1–N15 je to solidní základ.

**Frontend — nestačí.** Nesplňuje ani vlastní PRD. PRD §5.4 říká doslova „Tři
srozumitelné pracovní oblasti: Recipe / Test / Publish" a „Další fáze nemá
přidávat další karty a vysvětlující odstavce". Dodáno bylo
(`routine-create-dialog.tsx:828`):

```
["Overview", "Steps", "Schedule", "Validate", "Publish"] + přepínač Code
    └─ Validate → RoutineTestWorkspace → další 3 taby
        (Check definition | Test a step | Compare versions)
```

Pět sekcí, přepínač Code, uvnitř tři taby, `<details>` skoro v každém panelu.
Jedna a tatáž věc se jmenuje **„Validate"** v navigaci, **„Check your recipe"**
v hlavičce, **„Check definition"** v tabu a **„Test"** v dokumentaci.
Uživatelova výhrada „nevím, co mám vyplnit a co ty funkce znamenají" není
subjektivní dojem — je to přímý důsledek téhle informační architektury.

**Testy — stačí počtem, nestačí druhem.** 692 zelených unit testů a nula
přihlášených E2E. N1 i N2 jsou přesně ta třída chyby, kterou unit test se
stubem nikdy nechytí: N1 je neshoda serializace mezi prohlížečem a serverem,
N2 chybějící HTTP hlavička. Obojí by spadlo při jednom skutečném průchodu.

**Dokumentace — příliš mnoho.** Přes 300 KB proseckých dokumentů za dva dny,
částečně se překrývajících (`HANDOFF-WORKSPACE` 46 KB, `IMPLEMENTATION` 45 KB,
`UX-DECISION` 31 KB, `WORKSPACE-V2` 24 KB). Nepiš další. Až tahle práce
skončí, slouč je do jednoho živého dokumentu a zbytek označ jako archiv.

**Styl nového frontend kódu — oprav.** Nové komponenty mají průměrnou délku
řádku 230 znaků a maximum 969, zbytek repozitáře 40–54. Diff je nečitelný,
CodeRabbit z něj dostane minimum signálu a každá budoucí změna přepisuje celý
řádek. Při dotyku souboru rozlámej na normální řádky.

---

## 5. Přepracování Edit/Test — cílový stav

Tohle je samostatná práce **až po §1–§3**. Nezačínej ji přidáním dalších karet.

Zásada: **builder se má zmenšovat, ne růst.** Konkurenční výhoda Crewship není
lepší workflow builder — n8n má 400+ integrací a tenhle závod nevyhrajeme.
Výhoda je „agenti pracují, člověk rozhoduje ve správný okamžik". Autorovací
plocha má být co nejmenší, aby zbylo místo pro tohle.

Cíl: **jeden dokument, jedno `Test`, jedno `Publikovat`** — slovesa jsou
závazně určena v §9.2 a odpovídají standardu trhu.

- Zruš počítadlo kroků wizardu. Recept je jeden scrollovatelný dokument
  (identita → vstupy → kroky → rozvrh), ne pět obrazovek s Continue.
- Zruš vnořené taby v Test. `Test` je tab v panelu vybraného kroku, ne sekce
  navigace. Popisek musí pravdivě říct, co se stane — viz §9.2.
  „Compare versions" není součást editace receptu — patří do detailu rutiny
  vedle History, protože pracuje s publikovanými verzemi a stojí peníze.
- Sjednoť názvy. Není jich čtyři, je jich osm — úplný seznam v §7 a v auditu
  frontendu. Jedno slovo v navigaci, hlavičce, na tlačítku i v dokumentaci.
- Zachovej původní grafický systém, sidebar, kalendář a ikony. Vzor hustoty:
  Credentials a New Project.

**Akceptační kritérium je uživatelské, ne technické.** Klient bez výkladu
pozná: co ta kontrola udělá, co má zadat, jestli něco skutečně spustí, co
výsledek znamená a co udělat dál. Zelené testy tohle nedokládají. Názvy
Fixtures, Definition a Compare samy o sobě nedokládají porozumění.

---

## 6. Co v PRD chybí a co do něj doplnit

Nepiš nový PRD. Do
[stávajícího](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md) doplň:

1. **Akceptační kritéria formulovaná jako pozorovatelné chování klienta**, ne
   jako existence kódu. Tabulka R1–R10 v ENDING dokumentu měří „implementováno"
   jako „kód a testy existují" — to je právě ta záměna, kvůli které vznikl
   rozpor mezi zelenými testy a odmítnutým UX.
2. **Závaznou informační architekturu Edit** — jmenovitě, kolik ploch a jak se
   jmenují. Bez toho vznikne pátý wizard znovu.
3. **Chybějící P0 z ENDING tabulky**, které se dosud nikam nezapsaly jako
   práce: autorizovaný výběr souborů a credential referencí (R2), řízené
   pokračování od vybraného selhaného kroku (R5/R9), sdílené serverové
   datasety (R10). Rozhodni, jestli jsou v Release 1.0, nebo je z něj vyřaď.
4. **Definici hotovo pro R6**: přihlášený průchod Inbox ↔ Routines s run ID,
   ne unit test. Dnes je to nejcennější a nejméně doložená část.

---


## 7. Frontend: proč je nečitelný a co s tím

Plná evidence s file:line:
[audit frontendu](../ux/routines-frontend-audit-2026-09-09.md). Tady je předpis.

Výhrada zní „u té rutiny nemám šanci zjistit, co dělá". Není to dojem, má to
čtyři měřitelné příčiny:

- **Popis rutiny je vyplněný u 25 z 27 rutin na dev1** a v seznamu se zobrazí
  **jen v hover tooltipu** (`routines-explorer.tsx:516`). Komponenta, která ho
  na řádku vykreslit umí, existuje — `routines-tab.tsx:144` — ale je
  namountovaná na `/orchestration`, ne na `/routines`.
- **Lidský název kroku má 0 z 81 kroků** a mít ho nemůže:
  `schemas/routine.v1.json` → `$defs/Step` má `additionalProperties: false`
  a `name` mezi 27 klíči **není**. Go struct `Step` ho nemá také. UI ho přitom
  čte jako primární popisek (`routine-step-spine.tsx:272`,
  `routine-create-dialog.tsx:1167`, `lib/routine-data-sources.ts`) a padá na
  `readableFieldName(step.id)`. V rozbaleném detailu je nadpis **surové ID**
  (`routine-step-definition.tsx:11`). Uživatel čte `Red state`, `Scan sha label`.
- **Hlavní panel `/routines` je KPI dashboard**, seznam rutin je zamáčknutý do
  280px sidebaru. Kdo přijde zjistit, co rutina dělá, dostane nejdřív donut.
- **Editor má 29 navigovatelných ploch**, tři konkurenční navigační úrovně nad
  týmž stavem a osm názvů pro jednu akci (`Validate` / `Test` /
  `Check your recipe` / `Check definition` / `Definition check` /
  `Validate recipe` / `Validate & Publish` / „Build confidence before you run").
  „Overview" znamená tři různé věci. Vstupy mají sedm názvů, výsledky osm.
  V `routines-overview.tsx:264` je odkaz na záložku **Triggers, která neexistuje**.

### 7.1 Práce v pořadí podle poměru dopad / úsilí

**F1 · Obrátit precedenci názvu kroku.** `step.name` → `describeStep().title`
→ **až pak** ID, a ID jen jako sekundární mono chip.
`routine-step-spine.tsx:272`, `routine-step-definition.tsx:11`,
`routine-recipe-steps.tsx:80`. `describeStep` (`lib/routine-step-describe.ts`)
už umí „Ask sam", „Wait for approval", „Call routine X" — jen se dnes volá jako
druhá volba. **Tři jednořádkové změny, žádný backend, 81 z 81 kroků na dev1
přestane být `red_state`.** Nejlepší poměr v celém auditu; udělej to první.
Doplň chybějící `describeStep` větve pro `script`, `notify`, `query`, `foreach`,
`crewship` — dnes vracejí `kind: "unknown"` (`lib/routine-step-describe.ts:116,237`).

**F2 · Přidat `name` do kontraktu kroku.** F1 řeší dnešek, tohle řeší zítřek:
autor musí mít možnost krok pojmenovat. Přidat `Name string` do `Step`
(`internal/pipeline/types.go`), do `schemas/routine.v1.json` `$defs/Step`
(jinak ho `additionalProperties: false` odmítne a editor ho označí za neznámý
klíč, protože `lib/routine-dsl-schema.ts` bere klíče odtud), a nabídnout ho
v editoru kroku jako první pole. Nepovinné, s `describeStep` jako placeholder.
Zkontroluj `internal/pipeline/schema_test.go` — hlídá paritu schéma ↔ struct.

**F3 · Popis na řádek seznamu a seznam do hlavního panelu.** Druhý řádek
`SidebarRow` (`routines-explorer.tsx:469–496`); prohodit pořadí záložek tak,
aby hlavní panel byl seznam a dashboard druhá záložka
(`routines-workspace.tsx:20–23`). Když popis chybí (2/27), odvodit větu
z `describeStep` prvního a posledního kroku. Backend jen drobnost: **`step_count`
do list DTO** (`internal/api/pipelines.go:351,426`), spočítat z parsované
definice — bez migrace, bez změny schématu.

**F4 · Sjednotit slovník na jednu sadu.** Vyber jedno slovo pro každý pojem a
použij ho v navigaci, hlavičce, na tlačítku i v dokumentaci. Zruš osm názvů
validace, tři významy „Overview", sedm názvů pro vstupy, osm pro výsledky.
Zredukuj editor ze tří navigačních úrovní na jednu
(`routine-create-dialog.tsx:1120–1133`). Smaž mrtvý odkaz na „Triggers"
(`routines-overview.tsx:264`). Přesuň žargon (`execution_path`, `SHA-256`,
`concurrency_key`, `fixture`, `DSL version`, `chain N`, `Idempotency-Key`
v chybové hlášce) pod „Technické údaje". Slovo `definition` nahraď slovem
`recipe` — aplikace ho jinde už používá.

**F5 · Přeuspořádat detail běhu.** Verdikt, chybová hláška **a** identifikace
selhaného kroku do jednoho bloku nahoře — dnes je `run.error_message` za
`<details>` (`routine-run-detail.tsx:168`) a chyba kroku za dalším. Selhaný,
aktuální nebo čekající krok **auto-rozbalit**: inicializovat `openIds` z
`record` (`routine-step-spine.tsx:142`) a vždy ho zahrnout do `shown` bez
ohledu na `initialLimit` (`:163`). Schvalovací banner nad StatStrip
(`routine-run-detail.tsx:182`). Dvě po sobě jdoucí prázdné karty („žádný
výsledek" + „žádné soubory") sloučit do jednoho řádku.

**F6 · Přestat plýtvat plochou na prázdno.** 27 z 27 rutin dnes vykresluje
prázdnou kartu „When it runs", protože v DB je nula rozvrhů; 8 z 27 prázdné
„You get". Prázdná sekce = sbalený řádek s počtem, ne karta. Zvednout
`initialLimit` kroků z 6 na 12 (`routine-step-spine.tsx:135`). Vypnout spodní
dok na detailu (`routines-layout.tsx:313`) — duplikuje History a Schedule.

### 7.2 Co nechat být

Grafický systém, sidebar včetně stavových kbelíků a zkratek, kalendář a ikony
jsou fixní zadání a zároveň fungují. Nechat i **jeden krokový hřbet pro recept
i běh** (`routine-step-spine.tsx`) — model je správný, jen jsou špatně
pojmenované řádky. A hlavně `routineRunPresentation` / `routineRunExplanation`
(`lib/routine-run-presentation.ts`): je to jediné místo v kódu, které mluví
lidsky a nelže o stavu („Completion was not confirmed", „Stopping does not undo
actions that already happened"). **To je vzor pro zbytek aplikace.**

Celá nová testovací a publikační mašinérie (`routine-test-workspace`,
`routine-fixture-*`, `routine-comparison`, `routine-publication-review`,
`routine-decision-form-builder`) je věcně správná a pokrytá testy. **Nemazat.**
Jen ji celou schovat pod jedno „Ověřit", zbavit žargonu a nepodstrkovat ji
klientovi — je to plocha pro autora.

## 8. Testy: zelené neznamená doložené

Plná evidence: [audit důkazní hodnoty testů](ROUTINES-TEST-VALIDITY-2026-09-09.md).

Napříč osmi auditovanými funkcemi **nekříží hranici klient↔server ani jeden
řádek testu**. Proto 692 zelených testů minulo N1 i N2 — obojí je neshoda
kontraktu, ne chyba logiky. Verdikt po funkcích: skutečně otestované jsou
drafty (CAS/ABA), fixtures, rozhodnutí na serveru a replay integrita; **jen
stub** jsou publikační proof, start intent a porovnání verzí.

Nejfragilnější místo v celé dávce, které code review minul: tělo rozhodnutí se
skládá spreadem na dvou nezávislých místech —
`lib/api/waitpoints.ts:25` (`{ approved, ...answer }`) a
`hooks/use-pending-approval.ts:131`. Server to unmarshaluje do ploché
`DecisionAnswer`. **Dnes to funguje**, ale netestuje to nic a přepsání na
`{ approved, answer }` nechá všech 692 testů zelených, zatímco každé typované
rozhodnutí skončí na 400. Nejcennější funkce dávky visí na netestované spojce.

Napiš těchto pět testů, popis a přesné vstupy jsou v evidenčním dokumentu:

| # | Test | Proč |
|---|---|---|
| T1 | Akceptační draft/publish s **bajty z prohlížeče** (`&`, `<`, `>`) | Chytí N1. Infrastruktura už existuje: `cmd/crewship/acceptance_routine_trigger_test.go` |
| T2 | Tvar drátu pro rozhodnutí: `waitpoints.ts` × Go handler, zrcadlově | Jediná zcela nekrytá cesta, přes kterou jde 100 % typovaných rozhodnutí |
| T3 | Guard na `Prefer: respond-async` napříč **všemi** start cestami + statický grep test | Chytí N2 a zabrání jeho návratu |
| T4 | Upgrade test tří nových migrací ze staršího schématu s daty | Dnes je pokrytý jen fresh install |
| T5 | Zrušený one-time start nesmí ožít editací receptu | Chytí rozšíření N4 objevené auditem |

Pozor na past: `internal/pipeline/drafts_test.go:12` běží nad **ručně
sestaveným** schématem bez tabulky `workspaces`, ne nad produkčním. Šest draft
testů zůstane zelených, i když bude API rozbité. Fake timery jsou naopak čisté
— žádný z 20 nových FE souborů je nepoužívá.

## 9. Jak to řeší konkurence

Plná evidence s URL u každého tvrzení:
[rešerše čitelnosti](../ux/routines-competitive-legibility-2026-09-09.md).
Zkoumáno devět produktů z veřejné dokumentace: n8n, Windmill, Make, Zapier,
Dify, Temporal, LangSmith, Langfuse, Power Automate, UiPath, Retool, Airtable.
Bez přihlášeného testování.

### 9.1 Tři čísla, která rozhodují

**Ploch v editoru:**

| Windmill | Dify | Zapier | n8n | Make | **Crewship dnes** |
|---|---|---|---|---|---|
| 1 | 2 | 2 | 3 | 3 | **29** |

**Vícekrokový průvodce pro autoring nepoužívá ani jeden z devíti produktů.**
Náš pětisekční průvodce trh nekopíruje — odchyluje se od něj. Cíl je **≤ 4**
plochy: lišta, levý rail, hřbet kroků, panel vybraného kroku.

**Slovo `Validate` jako plochu nepoužívá nikdo.** `Dry run` nepoužívá nikdo.
`Preview` znamená u Windmill „nenasazený kód" a u Dify „průchod koncovým UI" —
ani jednou „bez následků".

**Režim bez vedlejších účinků v tomto odvětví neexistuje.** Zapier u `Test`
píše doslova: *„Testing is live and may result in changes made in your app."*
Jediné doložené mocky jsou Power Automate `Enable Static Result` a n8n pinned
data — tedy přesně náš fixture model. **Naše fixtures jsou tím pádem nad
standardem, ne pod ním** — jen se jmenují testerským žargonem.

### 9.2 Volba sloves — závazná

- **Kontrola: `Test`.** V UI *Test rutiny* a *Test tohoto kroku*, jako tab
  v panelu kroku, ne jako sekce navigace. **Ruší se všech osm dnešních názvů**
  včetně `Validate`, `Check your recipe` a `Check definition`, i v dokumentaci.
- **Ostré spuštění: `Spustit`**, jednorázově *Spustit jednou*.
- **Zpřístupnění: `Publikovat`** (shodně n8n, Zapier, Retool, Dify, Power
  Automate). Přepínač *Aktivní / Pozastaveno* je věc druhá.
- **Povinná poctivost:** `Test` musí nést Zapierovu větu — *„Test spustí
  skutečné volání a může změnit data v napojených systémech."* Kde běží
  fixtures, říct opačně a stejně konkrétně. **Předstírat sandbox je horší
  než mít průvodce.**

### 9.3 Co převzít, doložené

1. **Shrnutí jako pole viditelné v seznamu i v hlavičce** — Temporal
   `Static Summary` (200 bajtů, jednořádkové), Windmill `Summary`
   („If omitted, the UI will use the path by default"). U nás už existuje
   a je vyplněné u 25/27 rutin. **Nejlevnější vítězství v celé rešerši.**
2. **`name` na kroku s bezpečným fallbackem** — Windmill. LangSmith ukazuje
   odvrácenou stranu: „run names default to the class name (e.g. `ChatOpenAI`)".
   **To je přesně náš stav 0/81.** Viz F1 a F2.
3. **Detail není editor** — Power Automate vede metadaty a do designeru se
   kliká zvlášť; Windmill operátorovi plátno skryje úplně a **zakáže mu
   spouštět previews**. Oddělit `/routines/:id` od `/routines/:id/edit`.
4. **Selhání se aranžuje** — červený vykřičník na kroku (Power Automate) →
   auto-výběr chybného kroku (Zapier „Go to step") → panel **„jak to opravit"**
   (Power Automate *How to fix*, Zapier *Troubleshoot*) → akce. Viz F5.
5. **Replay s explicitní verzní volbou** — n8n má nejlepší sémantiku:
   *Retry with currently saved workflow* vs *Retry with original workflow*.
   Neuhodnout za uživatele. Detail běhu musí nést **verzi, kterou běžel**.
6. **Okno zdraví 7–30 dní** — n8n Insights (7 dní, klouzavé), Power Automate
   (28 dní). **Manuální a testovací běhy se do metrik nepočítají.**
7. **Poznámky s AI generováním** — Zapier „Generate with AI", 5 000 znaků.
   Past k nezopakování: u Zapieru poznámka žije jen v draftu, dokud se
   nepublikuje. **U nás musí patřit rutině, ne verzi.**

### 9.4 Kde jsme mimo standard a je to naše sázka

Označit v PRD jako **záměrné odlišení**, ne jako převzatý vzor:

- **Jednořádkové shrnutí jednoho běhu.** Nedělá to nikdo. LangSmith používá
  pro náhled **heuristiku, ne AI**, a AI shrnutí dělá až na agregátu, kde se
  u konkrétního běhu nikdy nezobrazí. **Postavit deterministicky** — věta ze
  stavu + `name` selhaného kroku + první řádky chybové hlášky. Model až jako
  druhá iterace. Deterministická verze nemůže lhát a nemá latenci.
- **Detail vedený prózou místo grafu.** Nejblíž Power Automate a Windmill, ale
  ani jeden nerenderuje kroky jako věty.
- **Auto-rozbalení chybného kroku v běžném detailu běhu.** Dva precedenty, ale
  standard je filtruj-a-scanuj. Držet a vědět o tom.

### 9.5 Jedna věc, kterou musíme opravit v našem HITL

**Dify nemá schránku schválení** — Human Input se doručuje e-mailovým odkazem,
který vyřídí kdokoli, kdo ho drží, **bez účtu v Dify**. Naše rozhodnutí musí
vést do **autentizované schránky**: vzor Power Automate *Action center* (taby
Received / Sent / History) a UiPath *Inbox* (Pending / Unassigned / Completed,
**read-only, když ho otevře jiný uživatel**). Náš Inbox tuhle roli už hraje —
udržet ji a nedoplňovat vedle ní sdílené odkazy.

## 10. Definice hotovo pro tuhle dávku

### Blok 1 — opravy a integrace (nic dalšího dřív, než tohle projde)

- [ ] N1–N11 opraveno, každý s testem, který na `cd2074d0b` selže.
- [ ] Sedm operací z §2 má pojmenované schéma; `openapi.mdx` má správné počty.
- [ ] Testy T1–T5 z §8 napsané a zelené. Zejména **T2** — dnes na netestované
      spojce visí 100 % typovaných rozhodnutí.
- [ ] PR A–D zamergovány do `main` na zeleném CI, po CodeRabbit review.

### Blok 2 — čitelnost

- [ ] F1 hotovo: **žádný krok v UI se nejmenuje technickým ID.** Ověřit na
      dev1 na všech 81 krocích.
- [ ] F2 hotovo: `name` je v `Step`, ve `schemas/routine.v1.json` a v editoru
      kroku jako první pole. `internal/pipeline/schema_test.go` zelený.
- [ ] F3 hotovo: seznam rutin ukazuje shrnutí a je v hlavním panelu.
- [ ] F4 hotovo: **jedno slovo pro jeden pojem**, `Test` / `Spustit` /
      `Publikovat`, včetně dokumentace. Mrtvý odkaz na „Triggers" pryč.
- [ ] F5, F6 hotovo.
- [ ] Editor má **≤ 4 navigovatelné plochy**. Dnes 29.
- [ ] `Test` nese pravdivou větu o vedlejších účincích u každého režimu.

### Blok 3 — přejímka

- [ ] Přihlášený průchod na dev1: pět úloh z PRD včetně chyby, čekání,
      restartu a souběhu. Přiložit run IDs a pozorované výsledky. Živé testy
      s externími akcemi označit a použít kontrolovaný recept.
- [ ] **Uživatel bez výkladu odpoví na pěti rutinách:** co ta rutina dělá, co
      má zadat, jestli něco skutečně spustí, co výsledek znamená, co udělat dál.
- [ ] Uživatel potvrdil, že Edit/Test je srozumitelný.
      **Tenhle bod nemůžeš odškrtnout sám.**
