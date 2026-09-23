# Klastr D: O2 (přehled běhu + kopírovatelný podklad), A3 (původ běhu), O3 (agentní vysvětlení — jen odhad)

## 0. Ověření prostředí
Clone `/srv/crewship/crewship_3`, větev `main`, HEAD `5a0ad11f` (= origin/main), git status jen untracked `docs/prd/*` cizí WIP; žádný tracked soubor nezměněn, oba dočasné testy (`internal/pipeline/zz_opp_D_test.go`, `internal/api/zz_opp_D_test.go`) smazány, zdroj + výstup v §7. Souběžné PR: #2562 (`feat/routines-operator-console`, head `9b5e1388`, **CONFLICTING** s main, stacked na #2556 `1cefe100`, REVIEW_REQUIRED).

## 1. Verdikt per funkce

**O2 — implementovat, ale jako čistou klientskou transformaci nad již autorizovanými DTO, s explicitním allowlistem a dvěma malými aditivními serverovými poli.** Všechna potřebná fakta už existují na `GET /pipeline-runs/{id}`, `GET /crews/{c}/issues/{i}/runs`, `GET /runs/{id}` a `GET /journal?run_id=`; problém není nedostatek dat, ale nadbytek (detail běhu vrací každému členovi workspace včetně VIEWER raw inputs, metadata, výstup, definici a s `?include_io=1` celá těla tool volání — §7 probe 2). Serverový builder pro O2 nepřidává ochranu, kterou by klient neměl; má smysl až pro O3 (agentní cesta). Odhad O2 3–3,5 dne (ne 2–3), protože issue větev nemá žádný detail agentního běhu (viz nález K2) a #2562 přepisuje `routine-run-detail.tsx`.

**A3 — implementovat zúženě: iniciátor + historická verze + uložené vstupy ano; „doložené použití credentialu“ musí být v 1.0 vždy „nezaznamenáno“.** Snapshoty existují (`pipeline_runs.triggered_via/triggered_by_id/invoking_user_id/invoking_crew_id/invoking_agent_id/pipeline_version/definition_hash/executed_definition_json/inputs_json`; `assignments.issue_brief_revision`, `run.started.payload.created_by_user_id/author_agent_id`), ale `GetRun` tři `invoking_*` sloupce nevrací (K5). Runtime důkaz použití credentialu na úrovni běhu v systému neexistuje ani pro rutiny, ani pro agenty (K3) — bez write-side změny lze poctivě ukázat jen „deklarováno v použité verzi“ (snapshot z `executed_definition_json.credentials_required`).

**O3 — odložit (Later), ale rozhodnout teď dvě věci:** (1) PR #2562 už dnes obsahuje agentní handoff „ask the lead to fix it“ přes `/chat/<lead>?prompt=` s auto-odesláním a instrukcí volat mutující `save_routine_draft` — to je O3 bez O3 podmínek (K1); (2) read-only evidence tool v sidecaru neexistuje a sidecar čte workspace-wide, takže O3 potřebuje nový scoped tool i nový serverový builder.

## 2. Tabulka funkcí

| ID | Současný stav (HEAD) | Souběžné PR | Doporučený rozsah (min. verze) | Závislosti | Odhad | Důkaz v kódu (soubor:řádek) |
|---|---|---|---|---|---|---|
| O2-R (rutiny) | Detail běhu má stav, vysvětlení, failed step + raw error, verze receptu, „Technical details“ s run id/hash; žádná reference `{kind,id}`, žádné Kopírovat odkaz/podklad, žádný „poslední záznam + captured at“ | #2562: serverová `failure{kind,step_id,step_name,summary,kept,not_done}` na GetRun, `routineRunBanner`, raw error do Technical details, `askLead` handoff; přepis 331 řádků `routine-run-detail.tsx` | Čistá `lib/run-evidence.ts` (`RunEvidenceView` v1) + panel „Reference a podklad“ pod bannerem #2562: reference, Kopírovat odkaz, Kopírovat podklad (náhled před kopírováním), poslední zaznamenaná činnost (z `humanizeRun`), čas načtení | merge #2562 (nebo koordinace na tomtéž souboru) | 1,5 d | `components/features/routines/routine-run-detail.tsx:335-714`; `lib/routine-run-presentation.ts` (HEAD); #2562 `internal/api/pipeline_run_definition.go` +`enrichRunFailure`, `internal/pipeline/failure.go` |
| O2-I (issues) | Karta Runs: řádek = assignment, `Open run` → `/activity?run=<trace_id>` → `RoutineRunDetail` → 404 → jen timeline bez stavu/outcome/hard-stop; `hard_stop_*` a `mission_id` DTO má, TS typ ne | #2562 se issue větve nedotýká | Stejný builder nad `IssueRun` DTO + `GET /runs/{id}` (journal agregát) + `journal?run_id=`; stav „Běh se nespustil“ jen když `run_id` chybí a status ∈ {FAILED, CANCELLED} | O2-R builder | 1 d | `internal/api/issue_handler_runs.go:24-74,185-189,215-216`; `components/features/issues/issue-runs-card.tsx:20-39,75-82,158-170`; `components/features/activity-stream/drill-downs.tsx:231-233`; `routine-run-detail.tsx:131-145` |
| A3 | Snapshoty v DB existují; UI ukazuje trigger label + `recipe vN`; `inputs` zobrazuje `RoutineSavedInputs`; iniciátor (user) se v detailu neukazuje, `GetRun` ho nevrací | #2562 čte `run.invoking_crew_id` v detailu, ale GetRun ho nevrací → vždy undefined | Aditivně vrátit `invoking_user_id/invoking_crew_id/invoking_agent_id` z GetRun (gen-openapi + `docs/api-reference/pipelines.mdx`); sekce „Původ“: trigger, iniciátor, verze+hash (snapshot), vstupy (bez dokladu necitlivosti — viz K6), credential = „deklarováno v použité verzi“ + „použití nezaznamenáno“ | O2-R | 1 d (+0,5–1 d volitelný write-side `credential.resolved` pro http kroky) | `internal/api/pipeline_runs.go:168-176` (SELECT bez invoking_*), `:579,643-645` (list je má); `internal/pipeline/executor.go:2540-2560`; `internal/api/pipeline_run_definition.go:13-35` |
| O3 | Neexistuje read tool pro evidence; sidecar má `/results/{assignmentId}` (workspace scope) a `/pipelines/{slug}`; existuje automatický LLM narátor `runverdict` za flagem `run_verdict_summaries` (`summary.generated`) | #2562 `askLead` = de facto O3 handoff | Nezačínat; nejdřív serverový Go builder se stejným `RunEvidenceView` schématem + fixture-kontrakt sdílený s TS | O2 hotové | +3–6 d (beze změny) | `internal/sidecar/routine_mcp.go:185-266,393-453`; `internal/sidecar/assignment.go:142-164`; `internal/runverdict/verdict.go:148`; `app/(dashboard)/chat/chat-client.tsx:285-301` |

## 3. Nálezy podle závažnosti

### Kritické

**K1 — PR #2562 zavádí agentní handoff s auto-odesláním, důkazy v URL a mutující instrukcí.** [POTVRZENO] `origin/feat/routines-operator-console:components/features/routines/routine-run-detail.tsx` (`fixPrompt`, `askLeadHref = /chat/<lead>?prompt=…`): text obsahuje `run.id`, název kroku a **raw `run.error_message`** (nebo `failure.summary`) a pokyn „save the change as a draft with save_routine_draft — do not publish“. `app/(dashboard)/chat/chat-client.tsx:285-301` potvrzuje, že `?prompt=` se odešle samo (`autoSendInitial`). Handoff §5 A2 výslovně: „současné `?prompt=` auto-odesílá… žádné důkazy ani tajemství do URL“; PRD §6: „Otevření a kopírování nic nespouští“, §12: read-only vynutit nástroji. Reprodukce: `git show origin/feat/routines-operator-console:components/features/routines/routine-run-detail.tsx | grep -n "askLeadHref\|fixPrompt"`. Dopad na PRD: buď se O2/O3 hranice v PRD přepíše, nebo review #2562 musí handoff změnit na copy-only (bez `?prompt=`, bez error_message v URL) — jinak O2 „nevolá LLM“ nebude pravda na stránce, kam O2 přijde.

**K2 — Issue běh nemá žádný detail; `Open run` vede na routine detail, který pro agentní běh 404-uje.** [POTVRZENO] `issue-runs-card.tsx:77` → `entityHref({kind:"run"})` → `/activity?run=<trace_id>`; `drill-downs.tsx:231-233` `RunDrillDown` = `RoutineRunDetail`; `routine-run-detail.tsx:131-145` při `error === "run: 404"` vykreslí jen „No routine execution record…“ + `RunActivityTimeline`. Nikdo v UI nekonzumuje `GET /api/v1/runs/{id}` (`grep -rn "api/v1/runs/\${" components hooks lib app` → nic; jen CLI `crewship run get`). Takže stav/outcome/hard-stop/exit code agentního běhu se v detailu nezobrazí nikde; PRD §6 „u issues vč. assignmentu bez běhu“ znamená postavit tento detail, ne jen panel. Iterace 4 je proto větší než „panel u issues“.

### Vysoké

**K3 — „Doložené použití credentialu“ na úrovni běhu neexistuje pro žádný engine.** [POTVRZENO]
- Rutinní `http` krok: `internal/pipeline/credential_resolver.go:95-170` dělá jen `SELECT encrypted_value … LIMIT 1`, nezapíše ani id vybraného credentialu, žádný audit/journal.
- Agentní běh: audit `USE` vzniká v `internal/api/internal_credentials.go:295-322` při fetchi hodnot loopback LLM proxy (`include_values=true`), debounce 60 s (`sidecarUseDebounce`), scope = crew (`maybeRecordSidecarUse(..., crewID)`), bez run id; sidecar sdílí kontejner celého crew.
- `cost_ledger.credential_id` existuje (`internal/paymaster/types.go:162`), ale `Scope{WorkspaceID,CrewID,AgentID,MissionID}` (`:145-150`) run id nemá; `llm.call` journal entry (`ledger.go:200-212`) nenese trace_id (ctx je HTTP handler, ne běh).
- `credentials.last_used_at` = poslední fetch, ne běh.
Dopad: A3 v 1.0 musí u každého běhu psát „použití nezaznamenáno“; cokoli jiného by bylo odvozeno z konfigurace nebo času, což PRD zakazuje. Write-side minimum pro rutiny: emit `credential.resolved` (id, type, step_id; nikdy hodnota) s `ActorID: runID` v resolveru — 0,5–1 d, samostatný PR. Pro agentní běhy per-run důkaz architektonicky nejde bez run-kontextu v sidecar proxy.

**K4 — Journal události agentních kroků rutin nejsou korelované s run id; sidecar/LLM události nejsou korelované vůbec.** [POTVRZENO testem, §7 probe 1] Executor nevolá `journal.WithRunID` (jediná volání: `assignments_run.go:755`, `query_handler.go:296`, `webhook.go:941`, `scheduler.go:578`, `chatbridge/bridge.go:1104`); ctx předaný `AgentRunner.RunStep` nese `RunIDFromContext == ""`. Orchestrátor emituje `exec.command`/`exec.output_chunk`/`run.session_init` bez trace_id (`orchestrator_run.go:536-548`, `exec_stream.go:588-600`, adaptér `server/journal_adapter.go:25-40` TraceID nepřenáší), payload `run_id` nemá (`journal_exec_command.go:274-304`). Jen `run.agent_span` (`agent_span_emit.go:67`) a script kroky (`runner_script.go:319`) stamují `TraceID=runID`. Sidecar `network.egress`/`file.written` (`sidecar/journal_emit.go:237-245`, `internal_journal.go:33-46` — TraceID záměrně nepřijímán) a `llm.call` (K3) mají jen crew/agent scope. Důsledek: dva souběžné běhy téhož agenta (dvě rutiny, nebo rutina + assignment) v jednom kontejneru mají tyto události nerozlišitelné; `journal?run_id=<pipeline run>` (`journal/queries.go:167-172`) je nevrátí (ztráta, ne smíchání), zatímco filtr `agent_id`/`mission_id` je smíchá. Pro O2: evidence[] smí vzniknout jen z `run_id`-filtrovaného dotazu; nikdy z `agent_id`/`mission_id`/`container_id`. Write-side oprava (1 řádek + test v `internal/pipeline`) je mimo O2 rozsah dle handoffu §3, ale doporučuji ji jako samostatný PR před O3.

**K5 — Autorizace detailu běhu = pouze členství ve workspace; VIEWER dostane všechno včetně raw vstupů a těl tool volání.** [POTVRZENO testem, §7 probe 2] `middleware.go:390-445` `RequireWorkspace` jen ověří `workspace_members` a uloží roli; `pipeline_runs.go:135-290` `GetRun` nemá `canRole` (na rozdíl od `RunLogs:433`), `runs.go:149` `Get` a `journal_handler.go:48` `List` také ne; `requireRole(w,r,"read")` v `issue_handler_runs.go:104` projde každé roli (`helpers.go:561`). Žádný crew/issue scope pro lidi neexistuje (CASL `lib/permissions/abilities.ts` nemá subjekt Run/Journal). Cross-tenant maskováno 404 (`TestRunHandler_Get_CrossTenantMasked` PASS). Dopad: pro O2 je klientský builder z hlediska scope ekvivalentní serverovému; export musí allowlistovat pole, protože DTO sám je „všechno“. Pro O3 by agentní tool zdědil workspace-wide čtení — musí být nově scopován.

**K6 — Citlivá pole v DTO (allowlist exportu musí vynechat).** [POTVRZENO]
- `IssueRun.task` = celý prompt (`issue-runs-card.tsx:63-67` to sám přiznává), `result_summary` (agentní próza; list je zkrácen `truncateErrorForList`, `/result` vrací celé), `error_message`.
- `GET /pipeline-runs/{id}`: `inputs` (raw `inputs_json`, žádný typ „secret“ v `InputSpec` `types.go:226-243` → uživatel může do stringu napsat token; `pipeline.run.started.payload.inputs_preview` ho navíc zkopíruje do journalu, `pipeline/journal.go:130-153`), `metadata` (caller-supplied scratchpad), `output`, `step_outputs`, `definition` (celý recept vč. promptů), `sub_spans[*].input/output` při `?io_step`/`include_io`, `error_message` (raw, neomezený).
- `GET /runs/{id}`: `metadata` (z run.started payload, caller-supplied), `session_id`, `error_message`.
- Journal: `exec.output_chunk.payload.output` (scrubovaný raw stdout, `exec_stream.go:576-583`), `exec.command.payload.cmd` (argv s redigovaným promptem), `chat.*`, `llm.call`. `humanizeRun` (`lib/run-activity.ts:44-60,444`) tyto chunky už zahazuje a je dnes jediná „schválená projekce“ — evidence[] z ní vycházet má; ale neznámé typy padají na volný `summary` text → export musí per-type allowlist, ne fallback.

### Střední

**K7 — Stav v `pipeline_runs`/`assignments` a stav agregovaný z journalu se rozcházejí; autoritativní musí být řádek.** [POTVRZENO]
- `interrupted`: `pipeline/runs.go:829-860` `MarkInterrupted` jen UPDATE, žádný journal emit; `resume.go:146,202,253`. Journal agregát (`journal/runs.go:74-89`, `runStatusFromTerminal`) takový běh hlásí `RUNNING` navždy.
- Recovery sweeper: `assignments_running_recovery.go:144` volá `finishAssignment(..., runID="")` → žádný `run.failed` (`assignments_run.go:1405-1435`), assignment FAILED, journal RUNNING.
- Journal `run.started` emit selhal → `runID=""` (`assignments_run.go:745`) → assignment proběhl, ale `run_id` chybí; „Běh se nespustil“ proto smí UI tvrdit jen při `status ∈ {FAILED,CANCELLED}` bez `run_id` a s uloženým důvodem (`:561`, `:693`).
- Pipeline cancel: journal `pipeline.run.failed` + `payload.status="CANCELLED"` (`journal/runs.go:232-241`, `scanRunAggregated:705-713`), enum `TIMEOUT` pro rutiny neexistuje.
Enumy (autoritativní zdroj): `pipeline_runs.status` ∈ queued|running|completed|failed|cancelled|dry_run|interrupted|waiting (`pipeline/runs.go:31-39`); `assignments.status` ∈ PENDING|RUNNING|COMPLETED|FAILED|CANCELLED (`assignments_run.go:1250-1264`; TIMEOUT jen v `internal_runs.go:455` pro chat běhy); `outcome` ∈ NO_CHANGE|SUCCEEDED|WORK_CREATED|PARTIAL|NEEDS_HUMAN|FAILED|CANCELLED (`orchestrator/outcome.go:32-38`; „no outcome reported“ → FAILED + `error_message` `:103`); `hard_stop_result` ∈ TERMINATED_TERM|TERMINATED_KILL|ALREADY_EXITED|UNSUPPORTED|NOT_FOUND|ERROR|PENDING_EXEC (`issue_handler_runs.go:64-73`); journal `RunStatus` ∈ RUNNING|COMPLETED|FAILED|CANCELLED|TIMEOUT. Exit code: agentní terminál nese `exit_code` jen `0` při COMPLETED (`assignments_run.go:1412-1414`); 137 se objeví jen v textu chyby — PRD invariant „exit 137 ≠ OOM“ je tedy o parsování textu, ne o poli.

**K8 — `run.invoking_crew_id` v #2562 detailu je vždy undefined.** [POTVRZENO] `GetRun` SELECT (`pipeline_runs.go:168-176`) `invoking_*` nevrací; #2562 `routine-run-detail.tsx` `const crewId = routine?.author_crew_id || run.invoking_crew_id` → fallback na autorský crew. Neškodné, ale A3 to potřebuje opravit (aditivní pole).

**K9 — Assignment ID vs run ID: pasti v pojmenování.** [POTVRZENO] `mission_comment_mentions.claimed_by_run_id` obsahuje assignment id (`issue_handler_runs.go:181,327-366`); `IssueRun.id` = assignment; `run_id == trace_id` (`:215-216`). Reference `{kind:"assignment", id}` a `{kind:"agent_run", id}` musí být dvě položky.

### Nízké

**K10 — Není sdílený copy/reference helper.** [POTVRZENO] `navigator.clipboard` ad hoc v 18 komponentách (`grep -rln navigator.clipboard components`), např. `activity-stream/activity-detail.tsx:353` „Copy trace id“ bez ošetření chyby, `chat/export/export-dialog.tsx:30-40` s toast success/error. Stable reference formát neexistuje; deep-linky dává `lib/entity-links.ts:52-58` (`/activity?run=`, `/journal?trace_id=`), rutiny `/routines?slug=&run=`.

**K11 — Journal `GetRunByID` přibírá `run.agent_span` řádky (trace_id = pipeline run id, `entry_type LIKE 'run.%'`).** [NÁVRHOVÉ RIZIKO] `journal/runs.go:589-593`; projekce `kind` = MAX('pipeline','agent') vyjde správně jen díky lexikografii; `started_at` filtr je bezpečný. Nesmíchá běhy, ale je to křehké.

**K12 — #2562 CONFLICTING + stacked na #2556.** [POTVRZENO] `gh pr view 2562 --json mergeable` → CONFLICTING; stacked PR nedostane CI (paměť). O2-R nelze začít na main bez konfliktu v `routine-run-detail.tsx`.

## 4. Navržené přesné změny PRD (necituji celé, jen znění k vložení)

1. **§6, odstavec „Důležité invarianty“, doplnit:** „Autoritativní stav je řádek `pipeline_runs`/`assignments`; journal agregát (`GET /runs/{id}`) je sekundární a u přerušených (`interrupted`) a sweeper-ukončených běhů hlásí RUNNING bez terminální události — rozpor se zobrazí jako rozpor.“
2. **§6, A3, nahradit větu „Claim ‚použito‘ vyžaduje runtime důkaz; konfigurační vazba sama nestačí“ tímto:** „Runtime důkaz použití credentialu na úrovni běhu dnes neexistuje pro žádný engine (credential_audit USE je crew-scoped a debounced, cost_ledger nemá run id, http resolver nic nezapisuje). V 1.0 se zobrazuje ‚Deklarováno v použité verzi: <typy z executed_definition_json>‘ a ‚Použití: nezaznamenáno‘. Zápis `credential.resolved` (id, typ, krok; nikdy hodnota) s actor_id = run id pro http kroky je samostatná write-side změna mimo O2/A3.“
3. **§6, „Implementační místa“, doplnit:** „Detail agentního běhu issue neexistuje (`/activity?run=<trace_id>` vede na routine detail, který 404-uje); O2 u issues zahrnuje nový lehký detail nad `GET /runs/{id}` + `GET /journal?run_id=`, nikoli jen panel v kartě Runs.“
4. **§6, Akceptace, doplnit:** „Podklad vzniká výhradně z dotazu filtrovaného `run_id`/`trace_id`; nikdy z `agent_id`, `mission_id` ani `container_id`. Události sidecaru (`network.egress`, `file.written`) a `llm.call` nejsou per-run korelované a v podkladu se neobjeví; panel to přizná větou ‚Síťové a LLM události nejsou k tomuto běhu přiřazeny‘.“
5. **§6 nebo §12, nový invariant vůči #2562:** „Žádná akce na stránce běhu nesmí předávat důkazy přes `?prompt=` (auto-odeslání) ani instruovat agenta k mutaci; ‚ask the lead‘ z #2562 se před O2 mění na copy-only, nebo se přesune pod O3 s jeho podmínkami.“
6. **§6, Datový kontrakt (odkaz na handoff §4):** explicitní allowlist: `reference[]`, `sourceLinks[]`, `status`, `outcome`, `hard_stop_result/at`, `started/ended/captured/lastRecordedAt`, `failed_at_step`, `failure.kind/summary` (#2562), `error_message` oříznutý na 1 KiB, `warnings[].stage/message` oříznuté, `trigger`, `pipeline_version`, `definition_hash`, `evidence[]` z `humanizeRun` rows (title/detail/meta, per-type allowlist). **Vyloučeno:** `task`, `inputs`, `metadata`, `output`, `step_outputs`, `definition`, `sub_spans`, `result_summary` (celé), `session_id`, payloady journalu.
7. **§4 tabulka:** O2 odhad „2–3 dny“ → „3–3,5 dne (issue detail neexistuje)“; A3 „1–3 dny“ → „1 den v O2 + 0,5–1 den volitelný write-side“.
8. **§14:** „Doporučené pořadí“ doplnit „O2 začíná až po merge #2562 (přepis `routine-run-detail.tsx`) nebo se implementuje jako oddělený panel-komponent bez dotyku tohoto souboru.“

## 5. Návrh iterací pro tento klastr

Posouzení výchozího plánu: pořadí 2→3→4→5 sedí, ale (a) iterace 4 je největší, ne nejmenší; (b) iterace 2 **nemá** být server-side builder — důvody: všechna fakta jsou v autorizovaných DTO, server by nepřidal scope (K5), nový endpoint tripne čtyři gate-y (gen-openapi, openapi.mdx totals, CLI příkaz + acceptance, docs-inventory) a #2562 už serverovou klasifikaci (`failure`) dodává. Podmínka: builder má `schemaVersion` a fixture-kontrakt (JSON vstup → JSON výstup) uložený v repu tak, aby ho O3 Go builder mohl přehrát 1:1.

**Iterace 2 — kontrakt + builder + ověřená korelace/autorizace (1–1,5 d)**
Vstupy: `hooks/use-trace.ts` `RunDetailResponse` (+ #2562 `failure`), `IssueRun` (doplnit `hard_stop_result`, `hard_stop_at`, `mission_id` do TS typu), `runResponse` z `/runs/{id}`, `JournalEntry[]` z `journal?run_id=`. Rozsah: `lib/run-evidence.ts` — `buildRunEvidence(input): RunEvidenceView` (čistá), `formatRunEvidenceText(view, {maxBytes:16384, maxEvents:20})` (UTF-8 byty přes `TextEncoder`, terminální chyba vždy zachována, chronologicky), `runReferences()` → `{kind:"pipeline_run"|"agent_run"|"assignment"|"issue", id}` + `sourceLinks` přes `entityHref`. Serverově jen aditivně: `GetRun` vrátí `invoking_user_id/invoking_crew_id/invoking_agent_id` (`pipeline_runs.go:168-176,240-280`) + gen-openapi (`cmd/gen-openapi/domain_schemas.go`) + `docs/api-reference/pipelines.mdx` + CHANGELOG. Nepatří sem: UI, endpoint, změna write-side. Testy: `lib/__tests__/run-evidence.test.ts` — sentinely v zakázaných polích (`task`, `inputs`, `metadata`, `output`, `definition`, `sub_spans.input`), byte-limit s českým textem, řazení/duplicity/neplatný čas, rozpor `assignment.status=FAILED` vs journal RUNNING → `status` z řádku + `unavailableReasons` „journal bez terminální události“, „no run_id + FAILED“ → `summary: Běh se nespustil`, `interrupted`, `NEEDS_HUMAN` ≠ selhání, exit 0 + outcome FAILED. Go: `TestPipelineRuns_GetRun_*` zelené + nový test na `invoking_*`. Podmínka dokončení: fixture soubory `lib/__fixtures__/run-evidence/*.json` (vstup/výstup) commitnuté. Handoff: seznam polí a proč jsou mimo allowlist.

**Iterace 3 — rutiny: panel + kopírování (1–1,5 d)**
Vstupy: iterace 2, merged #2562 (jinak nový samostatný soubor `routine-run-evidence-panel.tsx` a jen jeden `<RoutineRunEvidencePanel/>` řádek do `routine-run-detail.tsx`). Rozsah: panel pod bannerem #2562: reference (zkrácené ID + plné do clipboardu), Kopírovat odkaz (`/routines?slug=&run=`), Kopírovat podklad s náhledem (`<details>` s textem před tlačítkem), „Poslední zaznamenaná činnost · čas · načteno v“, warnings jako varování (ne selhání), `failure.kind` z #2562 jako „známý důvod“. Nepatří sem: druhý banner, LLM, `?prompt=`, změna Stop/Run again. Testy: Vitest na komponentě (clipboard reject → chybová hláška, ne success toast; klávesnice; dlouhé hodnoty), `routine-run-detail-claims.test.tsx` a `routine-run-presentation.test.ts` zelené; Playwright `e2e/routines-*` stávající zelené. Podmínka: fixture rutina se záměrným selháním na izolované instanci (`CREWSHIP_DATA_DIR`), screenshot + zkopírovaný podklad porovnán s `GET /pipeline-runs/{id}`.

**Iterace 4 — issues: detail agentního běhu + assignment bez běhu (1,5 d)**
Vstupy: iterace 2. Rozsah: `RunDrillDown` (`drill-downs.tsx:231`) při `useTrace` 404 zkusí `GET /runs/{id}`; nová lehká komponenta `agent-run-detail.tsx` (stav z assignment řádku předaný z karty nebo z `runResponse`, outcome, hard stop, poslední činnost, panel z iterace 3 sdílený). Karta Runs: u řádku bez `run_id` a terminálním stavem „Běh se nespustil · <error_message>“, u PENDING/RUNNING bez `run_id` „Čeká na spuštění“. Nepatří sem: změna `ListRuns` SQL, retry/restart akce. Testy: `issue-runs-card.test.tsx` rozšířit (bez run_id × 3 stavy, hard stop), Vitest pro `agent-run-detail`; Go `TestIssueRuns_CarriesRunAndAgentLinks`, `TestIssueRuns_ExposesHardStopResult` zelené. Podmínka: issue se dvěma assignmenty (jeden bez běhu) na izolované instanci; souběžný druhý issue téhož agenta — podklad neobsahuje jeho ID.

**Iterace 5 — původ běhu (1 d + volitelný write-side 0,5–1 d)**
Rozsah: sekce „Původ“ v panelu: trigger (`triggered_via` + `triggered_by_id` → odkaz na schedule/webhook/issue), iniciátor (`invoking_user_id` → „vy“/id; `run.started.payload.created_by_user_id` u assignmentů), verze (`pipeline_version` + `definition_hash`, odkaz na versions), vstupy jen odkazem na existující `RoutineSavedInputs` (do exportu nejdou), credentials: „Deklarováno v použité verzi: API_KEY, …“ ze snapshotu `definition.credentials_required` + „Použití: nezaznamenáno“. Volitelný samostatný PR: `credential.resolved` journal v `credential_resolver.go` s `ActorID: runID` (+ registry_generated, journal-groups test, docs). Nepatří sem: A2 (dopad odebrání), live join na dnešní verzi receptu. Testy: fixture s `executed_definition_json` ≠ aktuální definice → panel ukazuje snapshot; Go test na `invoking_*` v GetRun.

**Doporučené write-side PR mimo O2 (před O3):** `journal.WithRunID(ctx, runID)` v `executor.go` před voláním runneru (+ test z §7 probe 1 jako regresní), `MarkInterrupted` emituje `pipeline.run.failed{status:"INTERRUPTED"}` nebo nový typ.

## 6. Rozhodnutí vyžadující produktový vstup

1. **#2562 „ask the lead to fix it“**: ponechat jako mutující auto-odeslaný handoff (a přepsat O2/O3 hranici v PRD), nebo zredukovat na copy-only před mergem? Technicky doporučuji copy-only; je to však změna cizího PR.
2. **Viditelnost pro VIEWER**: má O2 export respektovat dnešní model (každý člen vidí vše) nebo má být VIEWER z kopírování podkladu vyloučen? Bez nového RBAC (A1) nelze scopovat víc než workspace.
3. **Credential evidence**: přijmout „nezaznamenáno“ pro 1.0, nebo zařadit write-side `credential.resolved` (jen rutinní http kroky; agentní běhy zůstanou bez důkazu)?
4. **Vstupy v podkladu**: PRD říká „dostupné necitlivé vstupy“ — systém typ „citlivý vstup“ nemá; navrhuji vstupy do kopírovaného podkladu nedávat vůbec (zobrazit jen v UI, kde už jsou). Souhlas?

## 7. Příloha: dočasné testy a příkazy

### Probe 1 — `internal/pipeline/zz_opp_D_test.go` (smazán)
```go
package pipeline

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestZZOppD_ExecutorCtxCarriesRunID(t *testing.T) {
	store, resolver, cleanup := openExecutorTestDB(t)
	defer cleanup()
	var seenRunID, seenReqRunID string
	runner := runnerFunc(func(ctx context.Context, req AgentStepRequest) (AgentStepResult, error) {
		seenRunID = journal.RunIDFromContext(ctx)
		seenReqRunID = req.PipelineRunID
		return AgentStepResult{Output: "ok"}, nil
	})
	em := &captureEmitter{}
	exec := NewExecutor(store, resolver, runner, em)
	dsl := &DSL{DSLVersion: "1.0", Name: "demo", Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "agent_lead", Prompt: "hi"}}}
	res, err := exec.RunDefinition(context.Background(), dsl, RunInput{WorkspaceID: "ws_test", AuthorCrewID: "crew_a", Mode: ModeRun})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	t.Logf("pipeline run id=%q req.PipelineRunID=%q journal.RunIDFromContext(ctx)=%q", res.RunID, seenReqRunID, seenRunID)
	if seenRunID != res.RunID {
		t.Errorf("ctx handed to the agent runner carries journal run id %q, want pipeline run id %q", seenRunID, res.RunID)
	}
}
```
Příkaz: `go test ./internal/pipeline/ -run TestZZOppD_ExecutorCtxCarriesRunID -count=1 -timeout 300s -v`
Výstup:
```text
=== RUN   TestZZOppD_ExecutorCtxCarriesRunID
    zz_opp_D_test.go:31: pipeline run id="run_cmu2yphef00020680b11a" req.PipelineRunID="run_cmu2yphef00020680b11a" journal.RunIDFromContext(ctx)=""
    zz_opp_D_test.go:33: ctx handed to the agent runner carries journal run id "", want pipeline run id "run_cmu2yphef00020680b11a"
--- FAIL: TestZZOppD_ExecutorCtxCarriesRunID (0.03s)
FAIL	github.com/crewship-ai/crewship/internal/pipeline	0.120s
```
(FAIL = potvrzení nálezu K4; jako regresní test po write-side opravě má být zelený.)

### Probe 2 — `internal/api/zz_opp_D_test.go` (smazán; kopie `scratchpad/zz_opp_D_api_test.go.txt`)
```go
package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestZZOppD_ViewerReadsPipelineRunDetailWithInputsAndIO(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pl_1", "demo")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO pipeline_runs (
		    id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at,
		    step_outputs_json, cost_usd, duration_ms, triggered_via, inputs_json,
		    metadata_json, created_at, updated_at
		) VALUES ('prn_v', ?, 'pl_1', 'demo', 'failed', 'run', ?, '{}', 0, 0, 'manual',
		    '{"api_token":"SENTINEL_INPUT_SECRET"}', '{"note":"SENTINEL_METADATA"}', ?, ?)`,
		wsID, now, now, now); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	payload := `{"step_id":"s1","seq":1,"kind":"tool","name":"Bash","status":"ok","input":"SENTINEL_TOOL_INPUT","output":"SENTINEL_TOOL_OUTPUT"}`
	if _, err := db.Exec(`
		INSERT INTO journal_entries (id, ts, workspace_id, entry_type, severity, actor_type, summary, payload, trace_id)
		VALUES ('je_span', ?, ?, 'run.agent_span', 'info', 'orchestrator', 'span', ?, 'prn_v')`,
		now, wsID, payload); err != nil {
		t.Fatalf("seed span: %v", err)
	}
	for _, role := range []string{"VIEWER", "MEMBER"} {
		req := withWorkspaceUser(
			httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/prn_v?include_io=1", nil),
			userID, wsID, role,
		)
		req.SetPathValue("runId", "prn_v")
		rr := httptest.NewRecorder()
		h.GetRun(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("[%s] status = %d body=%s", role, rr.Code, rr.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		raw := rr.Body.String()
		t.Logf("[%s] status=%d has inputs sentinel=%v metadata sentinel=%v tool input sentinel=%v tool output sentinel=%v",
			role, rr.Code,
			strings.Contains(raw, "SENTINEL_INPUT_SECRET"), strings.Contains(raw, "SENTINEL_METADATA"),
			strings.Contains(raw, "SENTINEL_TOOL_INPUT"), strings.Contains(raw, "SENTINEL_TOOL_OUTPUT"))
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		t.Logf("[%s] top-level keys: %v", role, keys)
	}
	_ = sql.ErrNoRows
}
```
Příkaz: `go test ./internal/api/ -run TestZZOppD_ViewerReadsPipelineRunDetailWithInputsAndIO -count=1 -timeout 300s -v`
Výstup:
```text
    zz_opp_D_test.go:55: [VIEWER] status=200 has inputs sentinel=true metadata sentinel=true tool input sentinel=true tool output sentinel=true
    zz_opp_D_test.go:63: [VIEWER] top-level keys: [triggered_by_id step_outputs_available sub_spans ended_at status warnings workspace_id definition_hash idempotency_key current_step_id inputs metadata outcome error_message output cost_usd definition_status id is_replay pipeline_name step_outputs definition failed_at_step issue_identifier mode replay_of duration_ms pipeline_id pipeline_slug tags triggered_via started_at]
    zz_opp_D_test.go:55: [MEMBER] status=200 has inputs sentinel=true metadata sentinel=true tool input sentinel=true tool output sentinel=true
--- PASS: TestZZOppD_ViewerReadsPipelineRunDetailWithInputsAndIO (2.75s)
ok  	github.com/crewship-ai/crewship/internal/api	2.878s
```
(Poznámka: klíče `invoking_user_id/invoking_crew_id/invoking_agent_id` v odpovědi chybí — K8.)

### Existující testy spuštěné (všechny PASS)
`go test ./internal/api/ -run 'TestIssueRuns_CarriesRunAndAgentLinks|TestRunHandler_Get_HappyPath|TestRunHandler_Get_CrossTenantMasked' -count=1 -timeout 300s -v` → 3× PASS (4.7 s).

### Další příkazy
- `gh pr view 2562 --json title,body,state,headRefName,baseRefName,files,mergeable` (CONFLICTING; 100+ souborů; `gh pr diff 2562` odmítnut: >20 000 řádků → použit `git fetch origin feat/routines-operator-console` + `git diff main...origin/feat/routines-operator-console -- <soubory>`), `gh pr view 2556 --json …` (UNKNOWN mergeability).
- `git show origin/feat/routines-operator-console:internal/pipeline/failure.go`, `:lib/routine-run-presentation.ts`, `:components/features/routines/routine-execution-insights.tsx`; `git diff main...origin/feat/routines-operator-console -- components/features/routines/routine-run-detail.tsx`.
- Čtené soubory HEAD: `internal/api/issue_handler_runs.go`, `runs.go`, `pipeline_runs.go`, `pipeline_run_definition.go`, `pipelines_exec.go`, `assignments_run.go`, `assignments_running_recovery.go`, `internal_credentials.go`, `internal_cost.go`, `internal_journal.go`, `journal_handler.go`, `middleware.go`, `helpers.go`, `credential_audit.go`; `internal/journal/runs.go`, `queries.go`, `emit.go`; `internal/pipeline/runs.go`, `executor.go`, `resume.go`, `journal.go`, `credential_resolver.go`, `runner_orchestrator.go`, `run_verdict.go`, `agent_span_emit.go`; `internal/orchestrator/orchestrator.go`, `orchestrator_run.go`, `exec_stream.go`, `journal_exec_command.go`, `outcome.go`; `internal/server/journal_adapter.go`; `internal/sidecar/journal_emit.go`, `assignment.go`, `routine_mcp.go`; `internal/paymaster/types.go`, `ledger.go`; `internal/runverdict/verdict.go`; `lib/run-activity.ts`, `lib/entity-links.ts`, `lib/permissions/abilities.ts`; `components/features/issues/issue-runs-card.tsx`, `issue-detail-surface.tsx`, `issue-card-detail.tsx`; `components/features/routines/routine-run-detail.tsx`; `components/features/activity-stream/drill-downs.tsx`, `activity-detail.tsx`; `components/features/activity/run-activity-timeline.tsx`, `runs-view.tsx`; `app/(dashboard)/chat/chat-client.tsx`; `components/features/chat/export/export-dialog.tsx`.
