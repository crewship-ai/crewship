# Oponentura PRD „zlepšení Crewshipu inspirovaná AWX a Omarchy“ — 2026-09-15

Oponovaný dokument: [`docs/prd/awx-omarchy-product-improvements-2026-09-14.md`](../awx-omarchy-product-improvements-2026-09-14.md)
(a jeho prováděcí handoff [`release-1-0-run-diagnostics-pages-palette-handoff-2026-09-11.md`](../release-1-0-run-diagnostics-pages-palette-handoff-2026-09-11.md)).
Ověřeno proti `main` @ `5a0ad11f` (= `origin/main`, 2026-09-15 odpoledne). Souběžné PR: #2562 (CONFLICTING, stacked na #2556), #2556 (MERGEABLE), #2558. Git status obsahoval jen untracked dokumenty v `docs/prd`; žádný tracked soubor nebyl změněn, PRD nebylo přepsáno, nic nebylo nasazeno ani mergnuto.

Oponentura proběhla ve čtyřech paralelních, nezávislých klastrech; každý má vlastní důkazní report se zdrojem dočasných testů a jejich výstupem v adresáři [`awx-omarchy-prd-opponent-2026-09-15/`](awx-omarchy-prd-opponent-2026-09-15/):

| Klastr | Funkce | Report |
|---|---|---|
| P | O1 Pages v paletě, O4 Zeptat se na tuto Page | [klastr-P-pages-paleta-chat.md](awx-omarchy-prd-opponent-2026-09-15/klastr-P-pages-paleta-chat.md) |
| D | O2 přehled běhu, A3 původ běhu, O3 agentní vysvětlení | [klastr-D-diagnostika-behu.md](awx-omarchy-prd-opponent-2026-09-15/klastr-D-diagnostika-behu.md) |
| S | A1 oddělená práva, A2 závislosti credentialu | [klastr-S-opravneni-credentials.md](awx-omarchy-prd-opponent-2026-09-15/klastr-S-opravneni-credentials.md) |
| R | A4 verze při opakování, O5/A5 první agendy | [klastr-R-replay-zadani.md](awx-omarchy-prd-opponent-2026-09-15/klastr-R-replay-zadani.md) |

Štítky: **[POTVRZENO]** = viděno v kódu na HEAD nebo reprodukováno testem (soubor:řádek / výstup v příloze); **[NÁVRHOVÉ RIZIKO]** = plyne z designu, neověřeno během; **[NEOVĚŘENO]** = předpoklad. Starší reporty a komentáře v kódu nebyly brány jako důkaz; dva takové komentáře se ukázaly jako zastaralé (§3, N1).

---

## 1. Verdikt

**Implementovat beze změny rozsahu:** O1 (Pages v paletě). Čistá změna rozhraní, žádný endpoint, žádná gate. Jediná oprava mimo skupinu Pages je Recent palety, který dnes není vázán na uživatele ani workspace.

**Implementovat s upraveným zadáním:**
- **O2 + A3** — data existují, ale ne tam, kde PRD čeká: detail agentního běhu issue neexistuje vůbec (iterace „u issues“ je největší, ne nejmenší), builder má být **klientská čistá funkce s allowlistem**, ne serverový endpoint (server by nepřidal scope — autorizace detailu běhu je dnes jen členství ve workspace), a „doložené použití credentialu“ na úrovni běhu **nelze v 1.0 splnit** pro žádný engine — panel musí psát „nezaznamenáno“.
- **A4** — už z větší části existuje (Run again s volbou Current/Executed a prefill vstupů), ale slib verze nedrží: „Current“ neposílá pin a API nemá `expected_*`, po publikaci v3 mezi otevřením dialogu a submitem tiše běží v3. PRD navíc předpokládá `/replay`, UI používá `/run`. Minimální verze = backendové `expected_definition_hash` → 409 + CLI `--version` + UI label s číslem verze.
- **A1** — backend práva už odděluje (5 rolí × 5 tříd akcí + per-člen capabilities včetně `routine.run` = execute bez edit a `credentials:reveal`). Chybí jednotná aplikace této matice (Page action, replay, run_batch, schedule-run capability `routine.run` ignorují → cílový průchod PRD „Page → spustit“ pro operátora dnes vrací 403) a per-resource „effective access“ endpoint; UI práva odhaduje z názvu role, přesně to, co PRD zakazuje. Audit odhalil **jeden kritický bezpečnostní nález** (K1 níže). A1 se rozpadá na (a) opravy autorizace = iterace 1, (b) `access/me` + UI = iterace 8.
- **A2** — vazba rutina→credential není uložená, je odvozená podle **typu** (`credentials_required`, `credential_ref.type`, `{{ secrets.<type> }}`) a resolver bere „nejnovější ACTIVE“; „zaznamenané skutečné použití“ na úrovni rutiny neexistuje. Proveditelné jako `GET /credentials/{id}/dependents` s trojím rozlišením configured / would-resolve / recorded(crew-level) a přiznanou neúplností pro `agent_run` kroky. Blokuje ho produktové rozhodnutí o crew scope (V2).
- **O4** — proveditelné bez nové agentní čtecí cesty, ale PRD staví na dvou věcech, které na HEAD existují jen z půlky: draft režim (`ChatPanel` prefill umí, `chat-client` ho nikdy nezapne; `?prompt=` navíc cílí **nejnovější existující vlákno**, ne novou session) a provenance (server persistuje z metadat jen `ask_submission`). Serverové znovunačtení kontextu pro agenta je nová schopnost (+2–3 dny) a do úzké verze nepatří.

**Odložit / vyčlenit:**
- **O3** (Later — potvrzeno). Read-only evidence tool v sidecaru neexistuje, sidecar čte workspace-wide; O3 potřebuje nový scoped tool i serverový builder. **Pozor:** PR #2562 už obsahuje de facto O3 handoff bez O3 podmínek (K2 níže).
- **O5/A5** — jako **kód** odložit (vše nosné existuje: ask_forms, suggested_prompts, vstupní formulář se serverovou validací, seed packs, 422 `missing_credentials`); jako **obsah** připravit parametrizovaný pack v `cmd/crewship/seeddata/packs/<role>`, až padne rozhodnutí o roli a datovém zdroji. Datový konektor „objednávek“ neexistuje.
- **Desktopy** — mimo 1.0 (PRD §13 beze změny).

**Celkový odhad úzkých verzí po oponentuře:** O1 0,5–1 · A1-opravy 1–1,5 · O2 kontrakt+rutiny+issues 3,5–4,5 · A3 1 (+0,5–1 volitelný write-side) · A4 1,5–2,5 · A1 access/me+UI 3–5 · A2 2,5–3,5 · O4 2–3,5 člověkodne. Součet ≈ 15–22 čd; PRD §4 uvádí pro tytéž položky ≈ 11–22 čd, ale podhodnocuje O2 (issue detail neexistuje) a A1 (nemá tam bezpečnostní opravy) a nadhodnocuje A4 (větší část hotová). Odhady zahrnují cílené testy a repo gate-y (gen-openapi, openapi.mdx totals, CLI příkaz/flag, docs-inventory, CHANGELOG), nezahrnují čekání na review.

---

## 2. Tabulka všech funkcí

| ID | Současný stav na HEAD 5a0ad11f | Souběžné PR | Doporučený rozsah (min. verze) | Závislosti | Odhad | Důkaz v kódu |
|---|---|---|---|---|---|---|
| **O1** | Paleta má 9 fan-out skupin + Recent + navigační řádek `Pages`; skupina konkrétních Pages chybí, `/api/v1/pages` se nevolá [POTVRZENO testem]. List je RBAC-filtrovaný (reach), nestránkovaný, úplný (cap 100/workspace je Go konstanta), nese `has_application`/`has_project`. Recent = jeden `localStorage` klíč bez userId/workspaceId. | žádné (#2562/#2556/#2558 tyto soubory nemění) | Desátý `apiFetch("/api/v1/pages")` ve stávajícím `Promise.allSettled`; skupina „Pages“ s `PageGlyph`, složka/crew jako sekundární popis, `href=/pages/${encodeURIComponent(slug)}`; Recent klíč `[userId, workspaceId]`; reset polí i ve větvi `!workspaceId`. Bez `usePages()` (hook je realtime-invalidovaný, paleta trvale mountovaná). | žádné | 0,5–1 d | `command-palette.tsx:139-155, 252-293, 419-467`; `pages_handler.go:461-604`; `internal/pages/pages.go:45`; `use-pages.ts:554, 640-650` |
| **O2-R** (rutiny) | Detail běhu má stav, failed step, raw error, verzi receptu, Technical details; chybí reference `{kind,id}`, Kopírovat odkaz/podklad, „poslední zaznamenaná činnost · načteno v“. | **#2562**: serverová `failure{kind,step_id,summary,kept,not_done}` na GetRun, `routineRunBanner`, přepis 331 ř. `routine-run-detail.tsx`, **`askLead` handoff přes `?prompt=`** (K2) | Čistá `lib/run-evidence.ts` (`RunEvidenceView` v1 + `formatRunEvidenceText` s byte-limitem) + panel „Reference a podklad“ pod bannerem #2562; `failure.kind` z #2562 jako „známý důvod“. | merge #2562 (nebo izolovaný soubor komponenty) | kontrakt 1–1,5 d + panel 1–1,5 d | `routine-run-detail.tsx:335-714`; `lib/routine-run-presentation.ts`; #2562 `pipeline_run_definition.go`, `internal/pipeline/failure.go` |
| **O2-I** (issues) | Karta Runs: řádek = assignment; `Open run` → `/activity?run=<trace_id>` → `RoutineRunDetail` → 404 → jen timeline. **Nikdo v UI nekonzumuje `GET /runs/{id}`** (jen CLI). `hard_stop_*`, `mission_id` v DTO jsou, v TS typu ne. | #2562 se nedotýká | Lehký `agent-run-detail.tsx` nad `GET /runs/{id}` + `journal?run_id=`; sdílený panel z O2-R; u řádku bez `run_id` a terminálním stavem „Běh se nespustil · důvod“, u PENDING/RUNNING bez `run_id` „Čeká na spuštění“. | O2 kontrakt | 1,5 d | `issue_handler_runs.go:24-74, 185-216`; `issue-runs-card.tsx:20-39, 75-82`; `drill-downs.tsx:231-233`; `routine-run-detail.tsx:131-145` |
| **A3** | Snapshoty v DB existují (`executed_definition_json`, `definition_hash`, `pipeline_version` jen při pinu); UI ukazuje trigger label + `recipe vN`; iniciátor se neukazuje, `GetRun` `invoking_*` nevrací (list ano). | #2562 čte `run.invoking_crew_id` v detailu — vždy `undefined` (K8) | Aditivně `invoking_user_id/crew_id/agent_id` z GetRun; sekce „Původ“: trigger, iniciátor, verze+hash ze snapshotu, vstupy jen odkazem, credentials = „deklarováno v použité verzi“ + „použití: nezaznamenáno“. | O2-R | 1 d (+0,5–1 d volitelný `credential.resolved` journal pro http kroky) | `pipeline_runs.go:168-176, 579, 643-645`; `executor.go:2540-2560`; `credential_resolver.go:95-170` |
| **A1-a** opravy autorizace | Role VIEWER…OWNER × read/create/update/manage/delete; capabilities `routine.run`, `routine.create`, `credential.create/rotate`, `credentials:reveal`, `page.create`…; Run = MANAGER+ ∨ `routine.run`. Page action, replay, run_batch, schedule-run capability ignorují [POTVRZENO testem]. Hlavičky `X-Crewship-Invoking-*` se čtou z JWT požadavku [POTVRZENO testem]. | žádné | Page action + replay + schedule-run honorují `routine.run` (run_batch ne — spend); hlavičky jen od crew-bound interního tokenu; `/replay` zaznamená `invoking_user_id`; docs/security odstavec o sémantice revokace za běhu. | žádné | 1–1,5 d | `helpers.go:516-563`; `capabilities.go:35-51`; `pipelines_exec.go:88-97, 166-167`; `router_pipelines.go:46-47, 136, 212`; `router_pages_actions.go:46`; `pages_actions.go:503`; `waitpoints.go:400-452` |
| **A1-b/c** effective access + UI | Workspace-level `GET /workspaces` → `currentUserRole`, `currentUserCapabilities`; per-page `GET /pages/{slug}/access/me`; member capabilities jen manage-only. UI: `roleAtLeast(role,"MANAGER")`, Run gated jen statusem (VIEWER vidí aktivní Run → 403); credential sheet zná 4 z 9 reveal gate. `lib/permissions/tiers.ts` sám dokumentuje drift CASL vs server. | **#2562** přepisuje `routine-card-detail.tsx`, `routine-identity-header.tsx`, `routines-detail-panel.tsx` | `GET …/pipelines/{slug}/access/me` a `GET /credentials/{id}/access/me` počítané **týmiž funkcemi jako handlery** + `reasons[]`; jedna komponenta „Your access“; Run button disabled s důvodem; 2 CLI příkazy. | A1-a, merge #2562 | 3–5 d (2 endpointy = 4 gate-y) | `workspaces.go:252`; `pages_folder_acl.go:784-820`; `credentials_reveal.go:258-335`; `routine-card-detail.tsx:113`; `routines-detail-panel.tsx:454`; `credential-detail-sheet.tsx:277-311` |
| **A2** | Žádný endpoint ani UI pro rutiny; Used by = agenti přes N+1 fan-out; `pipeline.RequiredCredentialTypes` pokrývá jen `credentials_required`; executor nezapisuje USE ani `last_used_at`; audit USE jen ze sidecar fetche (crew scope, debounce 60 s, bez run_id); sheet chybu auditu/fields ukazuje jako „Nothing has happened“. | #2556 mění `executor.go`, `types.go` (bez kolize s resolverem) | `ReferencedCredentialTypes(dsl)` export; `GET /credentials/{id}/dependents` → `{configured[{slug,version,refs,resolves_to_this}], dynamic{agent_run_routines:n}, recorded{last_used_at, source, run_attributable:false}}`; sekce v sheetu + přehled před Delete/změnou scope; oprava error≠empty; CLI `credential dependents`. | A1-b (sheet), rozhodnutí V2 | 2,5–3,5 d | `dsl_validate_credentials.go:45-66`; `executor_render.go:68-120`; `credential_resolver.go:106-175`; `internal_credentials.go:44-86`; `credential-detail-sheet.tsx:372-390, 1440-1450` |
| **A4** | Run again = `POST /pipelines/{slug}/run` + `inputs` + volitelný `pinned_version`; volba „Current“ pin neposílá, `expected_*` neexistuje [POTVRZENO sondou: HEAD v2→v3, submit → běží v3, `pipeline_version=nil`]; `/replay` je CLI/backtest cesta (MANAGER+, bez `routine.run`, bez `invoking_user_id`, synchronní, bez idempotency). CLI `routine run` nemá `--version`. | **#2556** `initialInputs` vs defaulty; **#2562** dialog dostane `headVersion` — ale ze stavu při otevření stránky, ne z čerstvého fetche | Backend `expected_definition_hash` v `runRequestBody` → 409; UI `prepareAgain` z čerstvého GET, label „Current recipe (v6)“ / „Executed recipe · v4“, 409/404/422 čitelně; CLI `routine run --version` + docs/cli řádek; text „vstupy jsou součástí historie běhu“. | backend+CLI hned; UI po merge #2562 | 1,5–2,5 d | `pipelines_exec.go:32, 139-158`; `routine-run-detail.tsx:182-189, 375-383, 547-571`; `pipeline_runs_replay.go:46-115`; `pipeline_deferred.go:85-107` (precedens pinu); `cmd_pipeline.go:1147-1162` |
| **O4** | Akce Page jsou v `pages-layout.tsx` SubBar (ne `page-view.tsx`); `?prompt=` → `autoSendInitial` → auto-odeslání do **nejnovějšího vlákna** agenta; `ChatPanel.initialInput` bez auto-send existuje, `chat-client` ho nezapíná; server persistuje z metadat jen `ask_submission`; agent nemá read tool na Page; `workspace_overview` vrací názvy všech Pages workspace bez reach. | žádné | 9a „draft handoff“ (`?page=<slug>`/`?draft=1`, `initialInput` bez auto-send, explicitní nová session, nepřepsat rozepsaný draft); 9b tlačítko v SubBar → `/chat/<agent>?page=…&new=1`, chip s odstranitelným kontextem, kontext jako ohraničený nedůvěryhodný text v `content` + whitelistovaný `metadata.page_context` v bridge + provenance chip. | rozhodnutí o cílovém agentovi; 9a je předpoklad i pro O3 | 9a 0,5–1 d; 9b 1,5–2,5 d | `pages-layout.tsx:276-345`; `chat-client.tsx:294-330, 540-575, 776`; `chat-panel.tsx:594-612`; `chat-composer.tsx:121-138`; `chatbridge/bridge.go:533-566`; `internal_status.go:150-158` |
| **O5/A5** | ask_forms per agent, suggested_prompts s role-packy, ChatEmptyState „Try it“ ze skills, vstupní formulář + `ValidateFormInputs`, seed packs (`routines_packs.go`), 422 `missing_credentials`/`missing_integrations` s toastem. Chybí: konektor objednávek (`query` step umí jen `pipeline_runs`), readiness **před** spuštěním, obsah. | **#2562** maže `STARTER_TEMPLATES` + test; **#2556** formát vstupů | Obsahový pack `seeddata/packs/<role>` (2 deterministické rutiny + Page panel, 1 ask form, 4 suggested_prompts), parametrizovaný `orders_source_url` + `credentials_required: [{type: api_key}]`; `seed verify`. Preflight endpoint volitelně (+1 d, 4 gate-y). | rozhodnutí role + zdroje; #2562 | obsah 1–1,5 d; E2E 0,5–1 d | `askforms/forms.go:38-148`; `lib/agent-suggestions.ts:11-99`; `dsl_validate_inputs.go:83-100`; `seeddata/routines_packs.go:22-60`; `pipeline_credentials_gate.go:35-58`; `runner_query.go:34` |
| **O3** | Žádný read-only evidence tool; sidecar má `/results/{assignmentId}` (workspace scope), `/pipelines/{slug}`; existuje LLM narátor `runverdict` za flagem. | **#2562 `askLead`** = O3 handoff bez O3 podmínek | Nezačínat. Předpoklad: Go builder se stejným `RunEvidenceView` schématem přehrávající fixture-kontrakt z O2 1:1; nový scoped tool. | O2, 9a | +3–6 d (beze změny) | `routine_mcp.go:185-266, 393-453`; `sidecar/assignment.go:142-164`; `runverdict/verdict.go:148` |

---

## 3. Nálezy podle závažnosti

Číslování je společné pro tento report; původní čísla z klastrových reportů jsou v závorce.

### Kritické

**K1 [POTVRZENO] Hlavičky `X-Crewship-Invoking-Crew/-Agent` se přijímají z libovolného JWT požadavku a ukládají jako provenance běhu; invoking crew přepisuje autonomy posture pro trust grants.** (S-K1)
`internal/api/pipelines_exec.go:166-167` čte hlavičky bez ohledu na to, zda volá sidecar nebo prohlížeč; middleware je nestripuje. Persistují se do `pipeline_runs.invoking_crew_id/agent_id`; `internal/pipeline/waitpoints.go:400-401` je používá místo author crew při `routineTrust` → `consumeTrust` (`:444-452`). Reprodukce `TestOppS_RunHeaderForgesInvokingCrew`: MEMBER + `routine.run`, hlavička `crew-i-do-not-belong-to` → řádek běhu `invoking_crew_id="crew-i-do-not-belong-to" invoking_agent_id="agent-forged"`. Bypass trustu za běhu nereprodukován (vyžaduje grant) — ta část je [NÁVRHOVÉ RIZIKO]. Stejná třída chyby jako už opravený `triggered_by_id` forge (`pipelines_exec_trigger_forge_test.go`). **Dopad na PRD:** A1 akceptace „modifikované HTTP tělo není eskalace“ pro hlavičky neplatí; A3 by zobrazovalo podvržený původ. **Oprava:** hlavičky přijmout jen když `InternalTokenCrewFromContext(ctx) != ""`. Patří do iterace 1 bez ohledu na PRD.

**K2 [POTVRZENO] PR #2562 zavádí agentní handoff s auto-odesláním, raw chybou v URL a mutující instrukcí — O3 bez O3 podmínek.** (D-K1)
`origin/feat/routines-operator-console:components/features/routines/routine-run-detail.tsx` (`fixPrompt`, `askLeadHref = /chat/<lead>?prompt=…`): text nese `run.id`, název kroku, **raw `run.error_message`** a pokyn „save the change as a draft with save_routine_draft — do not publish“; `chat-client.tsx:285-301` potvrzuje auto-odeslání. Handoff §5 a PRD §6/§12 to zakazují („žádné důkazy ani tajemství do URL“, „otevření a kopírování nic nespouští“, read-only vynutit nástroji). **Dopad:** pokud #2562 mergne beze změny, O2 „nevolá LLM“ na stránce, kam O2 přijde, nebude pravda. Doporučení: v review #2562 změnit na copy-only (bez `?prompt=`, bez `error_message` v URL), nebo přesunout pod O3 s jeho podmínkami. Je to změna cizího PR → produktové rozhodnutí (§7, R1).

**K3 [POTVRZENO] Issue běh nemá žádný detail.** (D-K2)
`issue-runs-card.tsx:77` → `/activity?run=<trace_id>` → `RunDrillDown` = `RoutineRunDetail` → 404 → jen timeline bez stavu/outcome/hard-stop/exit. `GET /api/v1/runs/{id}` v UI nekonzumuje nic. **Dopad:** iterace „O2 u issues“ = postavit lehký detail agentního běhu, ne panel; odhad O2 roste na 3,5–4,5 d.

### Vysoké

**V1 [POTVRZENO] Page action ignoruje capability `routine.run` → cílový průchod PRD „Page → spustit“ pro operátora vrací 403.** (S-V1, R-N4) `router_pages_actions.go:46` `roleCreate` + `pages_actions.go:503` `requireRole "create"`; komentář `pages_actions.go:65` cituje zastaralý stav. Reprodukce: MEMBER s `["chat","routine.run"]` → page action 403, přímý `/run` 200; totéž `runs/{id}/replay` (403), `run_batch`, `pipeline-schedules/{id}/run`. Navíc page action spouští pending run **bez pinu** (`pending_runs.go:16` legacy live-at-dispatch) — u scénáře PRD §1 neplatí ani „published at start“.

**V2 [POTVRZENO] Credential se scope na více crew rezolvuje jen pro první crew.** (S-V2) `credentials_mutate.go:878-882` `crew_ids=[A,B]` → `credentials.crew_id = A`; resolver i probe (`credential_resolver.go:114,169`) filtrují jen `crew_id`. Reprodukce: probe pro crew-A `ok=true`, crew-B `ok=false` → rutina crew B dostane 422, UI ji ukazuje jako dostupnou. **Dopad na A2:** mapa závislostí je pro multi-crew credentialy nesprávná, dokud se nerozhodne, která tabulka je pravda (§7, R3).

**V3 [POTVRZENO] Volba „Current recipe“ při opakování nic neslibuje; po posunu HEAD tiše běží nová verze; neznámé pole v těle se ignoruje.** (R-V1) Sonda: head v2, „kolega“ publikuje v3, `POST /run {"inputs":{},"expected_head_version":2}` → 200, `pipeline_version=nil`, výstupy `v3step`. `pinned_version:2` funguje. Precedens pinu existuje v deferred cestě (`pipeline_deferred.go:85-107`, #2500). #2562 to nezavírá (`headVersion` ze stavu při otevření stránky). PRD §9 „změna HEAD je pokryta testem“ dnes nemá červený test.

**V4 [POTVRZENO] Journal události agentních kroků rutin nejsou korelované s run id; sidecar/LLM události nejsou korelované vůbec.** (D-K4) Executor nevolá `journal.WithRunID`; ctx předaný `AgentRunner.RunStep` má `RunIDFromContext == ""` (reprodukce `TestZZOppD_ExecutorCtxCarriesRunID` FAIL). `exec.command`/`exec.output_chunk`/`run.session_init` bez trace_id; sidecar `network.egress`/`file.written` a `llm.call` jen crew/agent scope. **Důsledek:** dva souběžné běhy téhož agenta v jednom kontejneru mají tyto události nerozlišitelné; `journal?run_id=` je nevrátí (ztráta), filtr `agent_id`/`mission_id` je smíchá. **Pro O2:** evidence[] výhradně z `run_id`-filtru. Write-side oprava = 1 řádek + test v `internal/pipeline`, samostatný PR.

**V5 [POTVRZENO] Autorizace detailu běhu = pouze členství ve workspace; VIEWER dostane raw `inputs`, `metadata`, `definition`, `output` i těla tool volání.** (D-K5) `RequireWorkspace` jen ověří členství; `GetRun`, `runs.Get`, `journal.List` nemají `canRole`; `requireRole(w,r,"read")` projde každé roli. Reprodukce `?include_io=1` jako VIEWER → sentinely v `inputs`, `metadata`, tool input/output. Cross-tenant maskováno 404 (test PASS). **Pro O2:** serverový builder scope nepřidá → klientský builder je ekvivalentní; export musí allowlistovat. **Pro O3:** agentní tool by zdědil workspace-wide čtení.

**V6 [POTVRZENO] „Doložené použití credentialu“ na úrovni běhu neexistuje pro žádný engine.** (D-K3, S-S2) http resolver nic nezapisuje; audit USE vzniká jen při sidecar fetchi (crew scope, debounce 60 s, bez run id); `cost_ledger` nemá run id; `credentials.last_used_at` = poslední fetch. **Dopad:** A3 v 1.0 „použití: nezaznamenáno“ + „deklarováno v použité verzi“; A2 „recorded“ jen na úrovni crew. Write-side `credential.resolved` (id, typ, krok; nikdy hodnota) s `ActorID: runID` pro http kroky = 0,5–1 d samostatně; pro agentní běhy per-run důkaz architektonicky nejde bez run-kontextu v sidecar proxy.

**V7 [POTVRZENO] Recent palety není vázán na uživatele ani workspace a nikdy se nečistí.** (P-V1) `RECENT_KEY = "crewship.palette.recent"`; reprodukce testem: položka zapsaná pod ws-A se nabídne pod ws-B. Týká se všech skupin; pro Pages by název stránky po revokaci zůstal a klik skončil na 404.

**V8 [POTVRZENO] `?prompt=` auto-odesílá do nejnovějšího existujícího vlákna agenta, ne do nové session; server zahazuje všechna metadata zprávy kromě `ask_submission`.** (P-V2, P-V3) `chat-client.tsx:540-561, 776`; `chat-panel.tsx:596-610`; `bridge.go:548-551`. Komentář `routine-create-dialog.tsx:1048` („opens a fresh session“) neodpovídá kódu. **Dopad na O4:** draft režim potřebuje i volbu session; „obnova chatu se zachovaným původem“ vyžaduje whitelistovaný `page_context`.

**V9 [POTVRZENO] Uložené vstupy rutin jsou archivovány doslova a vraceny každému čtenáři běhu; typ „secret“ pro vstup neexistuje.** (R-V3) Sonda: `inputs.password="hunter2"` → GET run detail jako VIEWER vrátí hodnotu. Do URL se nic nepřenáší. PRD §9 „tajemství se nepřenášejí jako archivní vstupy“ je splnitelné jen prohlášením, ne kódem replaye.

**V10 [POTVRZENO] „Execute-only“ existuje, ale je workspace-wide, ne per-rutina.** (S-V3) `routine.run` dovoluje spustit každou aktivní rutinu včetně `workspace_visible=false`. AWX-styl „Martička smí spustit jen Import objednávek“ nemá ekvivalent; per-routine grant = nová tabulka + 4 gate + UI ≈ 4–6 d — to je ta „nová obecná matice RBAC“, kterou PRD správně odkládá.

### Střední

**S1 [POTVRZENO] Stav v řádku `pipeline_runs`/`assignments` a stav agregovaný z journalu se rozcházejí.** (D-K7) `MarkInterrupted` jen UPDATE bez journal emitu → agregát hlásí RUNNING navždy; recovery sweeper volá `finishAssignment(runID="")` → assignment FAILED, journal RUNNING; selhaný `run.started` emit → assignment bez `run_id`. Enumy: `pipeline_runs.status` ∈ queued|running|completed|failed|cancelled|dry_run|interrupted|waiting; `assignments.status` ∈ PENDING|RUNNING|COMPLETED|FAILED|CANCELLED; `outcome` ∈ NO_CHANGE|SUCCEEDED|WORK_CREATED|PARTIAL|NEEDS_HUMAN|FAILED|CANCELLED; `hard_stop_result` ∈ TERMINATED_TERM|…|PENDING_EXEC. Exit code: agentní terminál nese jen `0` při COMPLETED — invariant „137 ≠ OOM“ je o parsování textu, ne o poli.

**S2 [POTVRZENO] Citlivá pole v DTO, která allowlist exportu musí vynechat.** (D-K6) `IssueRun.task` = celý prompt; `result_summary`; `GET /pipeline-runs/{id}`: `inputs`, `metadata`, `output`, `step_outputs`, `definition`, `sub_spans[*].input/output`, neomezený `error_message`; `GET /runs/{id}`: `metadata`, `session_id`; journal `exec.output_chunk.payload.output`, `exec.command.payload.cmd`, `chat.*`, `llm.call`. `humanizeRun` (`lib/run-activity.ts`) je dnes jediná schválená projekce; neznámé typy padají na volný text → export per-type allowlist, ne fallback.

**S3 [POTVRZENO] UI práva odhaduje z názvu role; per-resource `access/me` pro rutinu ani credential neexistuje.** (S-S1) Detaily v tabulce A1-b/c. `GET …/members/{id}/capabilities` je manage-only — MEMBER se na sebe zeptat nemůže.

**S4 [POTVRZENO] Sémantika revokace za běhu (k zdokumentování, ne k opravě).** (S-S5) Lidská práva jen při přijetí požadavku; deferred/page-action běh se odpálí i po ztrátě role (`pending_dispatcher.go:163-235`); executor lidskou identitu nepoužívá; proxy-injektované klíče sidecar dropne do 60 s (`credstore_reap.go:16`), env-delivered do restartu [NÁVRHOVÉ RIZIKO]; smazaný credential zastaví **další** běh preflightem. Capability cache 30 s (`capabilities_check.go:55-58`).

**S5 [POTVRZENO] Dvě autorizační cesty pro „opakovat“: `/run` zná `routine.run`, `/replay` ne; `/replay` nezaznamená iniciátora, je synchronní bez idempotency; metadata/vstupy se kopírují bez validace vůči cílové verzi.** (R-V2, R-S1–S3) A4 má zůstat na `/run`; PRD §9 přepsat.

**S6 [POTVRZENO] Sheet credentialu: chyba načtení auditu/fields se zobrazí jako prázdno; Used by = agenti přes N+1 fan-out.** (S-S3, S-S4) `credential-detail-sheet.tsx:376-390` `.catch(() => setAudit([]))` → „Nothing has happened“. Access sekce chybu propaguje správně.

**S7 [POTVRZENO] PRD ukazuje na špatné soubory/cesty.** O4: akce Page jsou v `pages-layout.tsx` SubBar, ne `page-view.tsx` (P-S1). A4: UI používá `/run`, ne `/replay` (R). A3/#2562: `run.invoking_crew_id` v detailu vždy `undefined`, protože GetRun pole nevrací (D-K8).

**S8 [POTVRZENO] Agent nemá čtecí nástroj Page; `workspace_overview` vrací názvy a slugy všech Pages workspace bez reach; `agentViewer` nemá volajícího; `internal/api` importuje `chatbridge`, takže bridge nemůže volat pages authz přímo.** (P-S3, P-S4) Serverové re-load kontextu pro O4/O3 = nová schopnost.

**S9 [POTVRZENO] Readiness „před spuštěním“ pro rutinu neexistuje; jen 422 při odmítnutí; cesta Run again 422 zobrazí jen text bez odkazu.** (R-S6) Pro O5 „srozumitelné při odmítnutí“ platí, „před spuštěním“ = nový endpoint.

**S10 [POTVRZENO] Pro „objednávky“ není žádný datový konektor.** (R-S7) Jen `http` step s `credential_ref` a `script` step nad crew share; `query` step umí jen `pipeline_runs`.

**S11 [POTVRZENO] `RunRecord.PipelineVersion` komentář tvrdí „mirrors head_version at insert“, ukládá se jen explicitní pin; u HEAD běhů se verze dopočítává z hashe, join není omezen na jeden řádek.** (R-S5, R-N5) Proto `expected_definition_hash`, ne `expected_head_version`.

**S12 [POTVRZENO] Prefill composeru přepíše uložený neodeslaný draft session; při ztrátě `workspaceId` za otevřené palety zůstanou staré řádky.** (P-S5, P-S6)

### Nízké

**N1 [POTVRZENO] Zastaralé komentáře jako „důkaz“:** `pages_actions.go:65`, `router_pages_actions.go:14-19` („running a routine is MANAGER+“), `routine-create-dialog.tsx:1048` („opens a fresh session“), `internal/pages/pages.go:45` („admin-raisable“ — je to konstanta), `runs.go:83`.
**N2 [POTVRZENO] VIEWER s `routine.run` může spouštět** (test PASS na HEAD, `lib/capabilities.ts:89` dokumentuje). PRD „čtenář nemůže spustit“ upřesnit.
**N3 [POTVRZENO] Není sdílený copy/reference helper** — `navigator.clipboard` ad hoc v 18 komponentách, některé bez ošetření chyby.
**N4 [POTVRZENO] Nález z 12. 9. (rollback/re-enable obcházejí preset gate) je na HEAD opraven** — oba testy PASS. V #2562 web rollback mizí („Restore as draft“).
**N5 [POTVRZENO] Replay běžícího runu API nebrání (UI skrývá); schvalování se při opakování vyžádá znovu (nový waitpoint).**
**N6 [POTVRZENO] Index Pages nenese `owner_crew_name`, jen slug** — paleta doplní z už načtených `crews`.
**N7 [NÁVRHOVÉ RIZIKO] Journal `GetRunByID` přibírá `run.agent_span` řádky; projekce `kind` vyjde správně jen díky lexikografii MAX.**
**N8 [POTVRZENO] #2562 je CONFLICTING a stacked na #2556; stacked PR nedostane CI; CodeRabbit rate-limited a PR má ~120 souborů (nad limitem 100) → žádné strojové review.**

---

## 4. Navržené přesné změny PRD (PRD nepřepsáno; znění k vložení)

**§3 Inventura**
- Řádek *Oprávnění*, „Existující základ“ doplnit: „capabilities per člen (`routine.run`, `routine.create`, `credential.*`, `credentials:reveal`, `page.create`), `currentUserCapabilities`, `GET /pages/{slug}/access/me`“.
- Řádek *Běhy* doplnit: „Run again (`/run` + `pinned_version`) a replay (`/replay`, CLI) jsou dvě cesty s odlišnou autorizací; detail agentního běhu issue neexistuje.“
- Řádek *Aplikace* doplnit: „agent nemá čtecí nástroj Page; `workspace_overview` vrací názvy všech stránek workspace bez reach (`internal_status.go:150`).“
- Řádek *Credentials* doplnit: „vazba rutina→credential je odvozená podle typu; runtime důkaz použití na úrovni běhu neexistuje.“

**§4 tabulka odhadů**
- O2 „2–3 dny“ → „3,5–4,5 dne (kontrakt 1–1,5 + rutiny 1–1,5 + issues 1,5; issue detail neexistuje)“.
- A1 „1–3 dny UI; nový RBAC zvlášť“ → „1–1,5 dne opravy autorizace (iterace 1) + 3–5 dní `access/me` + UI; per-routine granty zvlášť (4–6 d, mimo 1.0)“.
- A3 „1–3 dny“ → „1 den v O2 + 0,5–1 den volitelný write-side `credential.resolved`“.
- A4 „1–2 dny“ → „1,5–2,5 dne (backend 409 + CLI flag + UI + testy); UI část po merge #2562“.
- O4 „2–4 dny“ → ponechat s poznámkou „bez serverového re-loadu; +2–3 dny za agentní čtecí cestu“.
- Nový řádek: „#2562 obsahuje serverovou `failure` klasifikaci a `askLead` handoff; O2 je konzumuje, nesmí je duplikovat; handoff musí být copy-only nebo O3.“

**§5 O1**
- „Recent musí respektovat identitu, scope a dostupnost stránky.“ → „Recent palety je dnes jeden `localStorage` klíč bez uživatele a workspace pro všechny skupiny. O1 ho klíčuje na `[userId, workspaceId]` pro všechny skupiny naráz; ověření dostupnosti Recent řádku proti serveru se nedělá — nedosažitelná stránka končí na existujícím 404 stavu Page.“
- „Při neúplném listu přiznat omezení hledání.“ → „`GET /api/v1/pages` je nestránkovaný a úplný (cap 100/workspace, `internal/pages/pages.go:45`); paleta nesmí posílat `limit` a hlášku o neúplnosti nepotřebuje; guard test to zamkne.“
- „Ověřit podporu nových Pages Apps“ → „Ověřeno: list nese `has_application`/`has_project`; viditelnost = overview.“
- Doplnit: „Načítat přes `apiFetch` ve stávajícím `Promise.allSettled`, ne přes `usePages()`.“

**§6 O2 + A3**
- Invarianty doplnit: „Autoritativní stav je řádek `pipeline_runs`/`assignments`; journal agregát (`GET /runs/{id}`) u přerušených a sweeper-ukončených běhů hlásí RUNNING bez terminální události — rozpor se zobrazí jako rozpor.“
- Doplnit: „Podklad vzniká výhradně z dotazu filtrovaného `run_id`/`trace_id`; nikdy z `agent_id`, `mission_id` ani `container_id`. Události sidecaru (`network.egress`, `file.written`) a `llm.call` nejsou per-run korelované a v podkladu se neobjeví; panel to přizná.“
- A3, nahradit „Claim ‚použito‘ vyžaduje runtime důkaz; konfigurační vazba sama nestačí“ → „Runtime důkaz použití credentialu na úrovni běhu dnes neexistuje pro žádný engine. V 1.0 se zobrazuje ‚Deklarováno v použité verzi: <typy z executed_definition_json>‘ a ‚Použití: nezaznamenáno‘. Zápis `credential.resolved` (id, typ, krok; nikdy hodnota) s actor_id = run id pro http kroky je samostatná write-side změna.“
- Implementační místa doplnit: „Detail agentního běhu issue neexistuje; O2 u issues zahrnuje nový lehký detail nad `GET /runs/{id}` + `GET /journal?run_id=`.“ a „Serverový builder se pro O2 nepřidává (nezvýšil by scope, tripl by čtyři gate-y); builder je čistá TS funkce s `schemaVersion` a fixture-kontraktem, který O3 Go builder přehraje 1:1.“
- Datový kontrakt — explicitní allowlist: `reference[]`, `sourceLinks[]`, `status`, `outcome`, `hard_stop_result/at`, `started/ended/captured/lastRecordedAt`, `failed_at_step`, `failure.kind/summary` (#2562), `error_message` oříznutý na 1 KiB, `warnings[]` oříznuté, `trigger`, `pipeline_version`, `definition_hash`, `evidence[]` z `humanizeRun` (per-type allowlist). Vyloučeno: `task`, `inputs`, `metadata`, `output`, `step_outputs`, `definition`, `sub_spans`, `result_summary`, `session_id`, payloady journalu.
- Nový invariant: „Žádná akce na stránce běhu nesmí předávat důkazy přes `?prompt=` ani instruovat agenta k mutaci.“

**§7 A1**
- Za „Použít aktuální názvosloví produktu“ doplnit: „Backend už práva odděluje: workspace role (`canRole`) a per-člen capabilities (`internal/api/capabilities.go`). ‚Operátor‘ = MEMBER/VIEWER s `routine.run`; grant je workspace-wide, per-rutinový grant neexistuje a není součástí tohoto PRD.“
- „Pokud již existuje ekvivalent, pouze jej propojit“ → „Ekvivalent existuje jen na úrovni workspace a Pages. Pro rutinu a credential se doplní `…/access/me`, který vrací rozhodnutí spočtená týmiž funkcemi jako mutační handlery; UI je pouze zobrazí.“
- Doplnit: „Hlavičky `X-Crewship-Invoking-Crew/-Agent` smí server přijmout jen od sidecaru (crew-bound interní token); z JWT požadavku se ignorují.“
- Akceptace: „čtenář **bez `routine.run`** nemůže spustit; operátor spustí povolenou rutinu **z detailu, palety i Page** (dnes Page/replay/schedule-run 403); modifikované tělo **ani hlavička** není eskalace; po revokaci další spuštění selže do 30 s (capability cache).“
- Doplnit odstavec sémantiky: „Lidská oprávnění se ověřují při přijetí požadavku/zařazení do fronty; běžící ani odložený běh se nezastaví. Proxy-injektované klíče sidecar odebere do 60 s, env-delivered až restart kontejneru; smazaný credential zablokuje další běh preflightem.“

**§8 A2**
- „Zdroje vazeb ověřit v routine definicích, credential assignments/bindings a journalu“ → „Vazba je odvozená podle TYPU (`credentials_required`, `credential_ref.type`, `{{ secrets.<type> }}`) a scope author crew; resolver vybírá nejnovější ACTIVE, takže ‚používá‘ znamená ‚byl by vybrán‘ (`resolves_to_this`). `agent_run` kroky vybírají za běhu přes sidecar — uvádět jako neúplnost. Journal ani `credential_audit` nenesou run_id; ‚zaznamenané použití‘ je na úrovni crew.“
- Akceptace doplnit: „Multi-crew credential musí rezolvovat pro každý crew v `credential_crews` (dnes jen první); rotace in place zachová vazby, nový credential téhož typu je nepřebírá, ale `{{ secrets.<type> }}` na něj přejde — obojí zobrazit. Audit/fields sekce sheetu dnes chybu zobrazí jako prázdno — opravit v rámci A2.“

**§9 A4**
- Nahradit odstavec o `/replay`: „UI ‚Run again‘ používá `POST /pipelines/{slug}/run` s `pinned_version`; `/replay` zůstává CLI/backtest cestou. A4 rozšiřuje `/run`; `replay_of` není součástí akceptace. Backend doplní `expected_definition_hash` (409 při neshodě s `pipelines.definition_hash`); UI pošle hash zobrazené verze a při 409 nabídne ‚načíst znovu‘. Pin přes `pinned_version = head_version` z UI nestačí (zastaralý `head_version` u legacy řádků, viz #2500).“
- Akceptace „tajemství se nepřenášejí … jako archivní vstupy“ → „Vstupy rutin nemají typ secret, jsou archivovány doslova a viditelné každému, kdo smí číst běh. Dialog to přizná; redakce se nepřidává.“
- Doplnit: „Opakování pod schvalovací branou vyžádá schválení znovu. `crewship routine run --version N` dorovná CLI k API.“

**§10 O4**
- „Akce v hostitelské hlavičce Page … `page-view.tsx`“ → „Akce patří do `SubBar` v `pages-layout.tsx` (`:276-345`); `page-view.tsx` se nemění.“
- „`?prompt=` auto-odesílá: vyžadovat režim draft“ → doplnit: „`?prompt=` navíc cílí nejnovější existující vlákno agenta. Draft handoff musí (a) nastavit `initialInput` bez `autoSendInitial`, (b) explicitně zvolit novou session, (c) nepřepsat rozepsaný draft cílové session.“
- „stávající message metadata/provenance“ → „Server persistuje jen `ask_submission`. O4 přidá whitelistovaný `page_context` {workspace_id, page_id, slug, name, snapshot_at} a zobrazí ho jako provenance chip.“
- „Pokud server musí kontext znovu načíst…“ → „Serverové znovunačtení pro agenta je samostatná schopnost (+2–3 dny), mimo úzkou verzi; kontext se skládá na klientu z DTO autorizovaného pro člověka a vkládá jako nedůvěryhodný text.“

**§11 O5/A5**
- Doplnit: „Obsah žije v `cmd/crewship/seeddata/packs/<role>` a ověřuje se `crewship seed verify`; FE `STARTER_TEMPLATES` odcházejí s #2562. Pack je parametrizovaný (`orders_source_url`, `credentials_required: [{type: api_key}]`, `egress_targets`), takže volba role mění jen YAML a texty.“
- Akceptace „před spuštěním nebo při odmítnutí“ → „Při odmítnutí: existující 422 (doplnit stejný toast do cesty Run again). Před spuštěním: pouze pokud vznikne `GET /pipelines/{slug}/preflight` — nová schopnost, +1 d, volitelné.“

**§12 O3** doplnit: „#2562 ‚ask the lead to fix it‘ je O3 handoff bez těchto podmínek; před O2 se mění na copy-only nebo se přesouvá sem.“

**§14 pořadí** → „iterace 1 = opravy autorizace A1 (K1, V1) + dokumentace revokace; O2 začíná až po merge #2562 nebo jako izolovaná komponenta; A4 backend+CLI lze udělat kdykoli, UI po #2562; O5 obsah paralelně po rozhodnutí role.“

---

## 5. Upravený plán deseti postupných iterací

Změny oproti výchozímu návrhu: (1) **bezpečnostní opravy A1 jdou první** — jsou nezávislé na PRD, malé, Go-only a bez nich neplatí cílový průchod PRD ani akceptace A1/A3/A4; (2) O1 se posouvá na 2 (může běžet paralelně s 1, jiné soubory, jiný jazyk); (3) O2 „společný datový podklad“ **není serverový builder**; (4) iterace u issues je větší než u rutin; (5) A4 se dělí na backend (kdykoli) a UI (po #2562); (6) O4 zůstává poslední z desítky, protože potřebuje produktové rozhodnutí; (7) O5 obsah a závěrečný průchod jsou **mimo desítku** — obsah je blokován rozhodnutím o roli a může běžet paralelně, průchod je akceptace, ne implementace.

| # | Samostatný výsledek | Předpoklady | Odhad |
|---|---|---|---|
| 1 | Autorizace spouštění je konzistentní a hlavičky původu nejdou podvrhnout (A1-a) | — | 1–1,5 d |
| 2 | Konkrétní Pages v paletě, Recent per uživatel+workspace (O1) | — (paralelně s 1) | 0,5–1 d |
| 3 | `RunEvidenceView` kontrakt + čistý builder + fixture; korelace stampována; GetRun vrací původ (O2 základ) | — | 1–1,5 d |
| 4 | Panel „Reference a podklad“ u rutin (O2-R) | 3; merge #2562 nebo izolovaný soubor | 1–1,5 d |
| 5 | Detail agentního běhu u issues, assignment bez běhu (O2-I) | 3 | 1,5 d |
| 6 | Sekce „Původ“ v panelu (A3) | 4, 5 | 1 d (+0,5–1 volitelně) |
| 7 | Verze při opakování je slib, který server drží (A4) | backend hned; UI po #2562 | 1,5–2,5 d |
| 8 | `access/me` pro rutinu a credential + „Your access“ (A1-b/c) | 1; merge #2562 | 3–5 d |
| 9 | Credential → závislé rutiny, dopad odebrání, error≠empty (A2) | 8; rozhodnutí R3 | 2,5–3,5 d |
| 10 | Draft handoff v chatu + „Zeptat se na tuto Page“ (O4) | rozhodnutí R4; 10a předpoklad pro O3 | 2–3,5 d |
| mimo | O5 obsahový pack (paralelně po rozhodnutí R5); závěrečný E2E průchod scénáře §1 (po 10) | | 1–1,5 d; 0,5–1 d |

Společná pravidla pro každou relaci: přečíst PRD, tento report a klastrový report své funkce, handoff předchozí iterace; ověřit HEAD, `git status`, claims, stav #2562; claimnout issue před prvním commitem; jedna větev, jeden PR; `go test` cíleně + `go vet`, `pnpm lint`, `pnpm build` u UI; skutečné CodeRabbit review (`scripts/review-status.sh`), ne rate-limited zelená; CHANGELOG; nikdy nemergovat na červené CI; nenasazovat dev3 bez explicitního pokynu.

---

## 6. Iterace podrobně

### Iterace 1 — A1-a: autorizace spouštění a původ běhu
- **Vstupy:** klastr S §3 K1/V1/S5 + §7 (dva dočasné testy jako výchozí červené); vzory `pipelines_exec_capability_gate_test.go`, `pipelines_exec_trigger_forge_test.go`, `pages_actions_test.go`.
- **Rozsah:** (1) `pipelines_exec.go:166-167` — hlavičky `X-Crewship-Invoking-*` číst jen když `InternalTokenCrewFromContext(ctx) != ""`, jinak prázdné; (2) `pages_actions.go:503` + `router_pages_actions.go:46` → `roleInline` + `requireRoleOrCapabilityOrForbid(CapabilityRoutineRun)` (stejný tvar jako Run); totéž `runs/{id}/replay` a `pipeline-schedules/{id}/run`; `run_batch` zůstává MANAGER+ (spend); aktualizovat golden `TestMutationRouteRolesMatchManifest -update-route-roles`; (3) `/replay` nastaví `InvokingUserID`; (4) opravit zastaralé komentáře (N1); (5) `docs/security/*.mdx` odstavec „sémantika revokace za běhu“ (S4); (6) CHANGELOG Fixed + Security.
- **Nepatří sem:** nové role, per-routine granty (V10), `access/me`, UI, změna capability cache, page-action pin (V1 druhá část — poznamenat do handoffu).
- **Testy:** tabulkový test page action (MEMBER+`routine.run` → 202; MEMBER bez → 403; outsider → 404); forge test rozšířený o hlavičky (JWT → prázdné; interní crew-bound token → uloženo); replay `invoking_user_id` uložen; držet zelené: `TestRunEndpoint_CapabilityGate`, `TestPageAction_*`, `route_authz_invariant_test`, `route_roles_manifest_test`, `TestReplayRun_*`. Vždy `-run`, nikdy celý `internal/api`.
- **Podmínky dokončení:** oba dočasné testy z klastru S (přepsané jako trvalé) zelené; CodeQL bez nálezu; review skutečně proběhlo.
- **Handoff:** tabulka „která spouštěcí cesta honoruje `routine.run`“ s odůvodněním; poznámka, že page action pending run stále nepinuje verzi (pro iteraci 7).

### Iterace 2 — O1: Pages v paletě
- **Vstupy:** klastr P §5 iterace 1 + §7 (test „abort guard“ a „Recent not scoped“ jako vzor); `pageListWire` kontrakt; `normalizePageList`/`toPageView`; `PageGlyph`.
- **Rozsah:** desátý `apiFetch("/api/v1/pages?workspace_id=…")` ve fan-outu; skupina „Pages“ (glyph, název, sekundárně složka nebo crew název z `crews`, badge „app“ při `has_application`); value `${name} ${slug} page`; `data-href` + `go()` s `/pages/${encodeURIComponent(slug)}`; `RECENT_KEY` → `crewship.palette.recent:<userId>:<workspaceId>` (bez userId Recent nečíst/nepsat), jednorázový `removeItem` starého klíče; reset polí i ve větvi `!workspaceId`; `docs/guides/pages.mdx` odstavec; CHANGELOG Added.
- **Nepatří sem:** `usePages()` v paletě, nový endpoint, `limit`, hláška o neúplnosti, server-ověření Recent položek, redesign Recent, fulltext obsahu.
- **Testy:** Vitest `components/__tests__/command-palette.test.tsx`: skupina z fixture, slug s encode znaky → href, stejné názvy různých složek = dva řádky, badge app, 403/500 na `/pages` → ostatní skupiny stojí, malformed body → žádná skupina a žádný crash, URL bez `limit`, Recent klíč obsahuje user+workspace, položka jiného workspace se nenabídne, pozdní odpověď po změně workspace zahozena. Playwright `e2e/command-palette.spec.ts`: „a page opens that page“. Zelené: oba palette test soubory (38 testů), `TestPagesList_*`.
- **Podmínky dokončení:** klik i Enter otevřou `/pages/<slug>`; výpadek listu nevyřadí jiné skupiny; přepnutí workspace s pending requestem nezanechá cizí řádky; `pnpm lint` + cílené Vitest; žádná Go změna.
- **Handoff:** změna Recent klíče se dotkla všech skupin (historie se jednou ztratí).

### Iterace 3 — O2 základ: kontrakt, builder, korelace, původ z GetRun
- **Vstupy:** klastr D celý (§3 K3–K9, §4 bod 6 allowlist, §5 iterace 2, §7 probe 1 a 2); handoff §4 datový kontrakt; `hooks/use-trace.ts` `RunDetailResponse`, `IssueRun`, `runResponse`, `JournalEntry`.
- **Rozsah (dva PR):** *PR A (TS, bez UI):* `lib/run-evidence.ts` — `buildRunEvidence(input): RunEvidenceView` (čistá, `schemaVersion: 1`), `formatRunEvidenceText(view, {maxBytes: 16384, maxEvents: 20})` (UTF-8 byty přes `TextEncoder`, terminální chyba vždy zachována, chronologicky, oříznutí označeno), `runReferences()` → `{kind: pipeline_run|agent_run|assignment|issue, id}` + `sourceLinks` přes `entityHref`; allowlist ze §4; fixture soubory `lib/__fixtures__/run-evidence/*.json` (vstup/výstup) jako kontrakt pro O3; do TS typu `IssueRun` doplnit `hard_stop_result`, `hard_stop_at`, `mission_id`. *PR B (Go, aditivní):* `GetRun` vrací `invoking_user_id/crew_id/agent_id` (`pipeline_runs.go:168-176, 240-280`) + gen-openapi + `docs/api-reference/pipelines.mdx` + CHANGELOG; `journal.WithRunID(ctx, runID)` v `executor.go` před voláním runneru + regresní test (probe 1 z klastru D jako trvalý). Volitelně `MarkInterrupted` emituje terminální journal událost — jen pokud se vejde; jinak do handoffu.
- **Nepatří sem:** UI, nový endpoint, přepis trace_id/actor_id modelu, sidecar korelace, `credential.resolved`.
- **Testy:** `lib/__tests__/run-evidence.test.ts`: sentinely v zakázaných polích (`task`, `inputs`, `metadata`, `output`, `definition`, `sub_spans.input`) se v textu neobjeví; byte-limit s českým textem; řazení/duplicity/neplatný čas; rozpor `assignment.status=FAILED` vs journal RUNNING → status z řádku + `unavailableReasons`; „no run_id + FAILED“ → „Běh se nespustil“; `interrupted`; `NEEDS_HUMAN` ≠ selhání; exit 0 + outcome FAILED; exit 137 bez OOM tvrzení. Go: `TestPipelineRuns_GetRun_*` + nový test na `invoking_*`; `TestExecutor…RunIDInContext`; journal-groups test (nový typ jen pokud přidán).
- **Podmínky dokončení:** fixture commitnuté; `gen-openapi` bez diffu; docs-inventory strict zelené.
- **Handoff:** seznam polí mimo allowlist a proč; zda `MarkInterrupted` emit vznikl; upozornění, že podklad nesmí vznikat z `agent_id`/`mission_id` filtru.

### Iterace 4 — O2-R: panel u rutin
- **Vstupy:** iterace 3; stav #2562 (mergnuté → `failure`, `routineRunBanner` k dispozici; nemergnuté → nový soubor `routine-run-evidence-panel.tsx` a jediný řádek do `routine-run-detail.tsx`, aby rebase #2562 byl triviální).
- **Rozsah:** panel pod bannerem: reference (zkrácené ID v UI, plné do clipboardu), Kopírovat odkaz (`/routines?slug=&run=`), Kopírovat podklad s náhledem (`<details>` před tlačítkem), „Poslední zaznamenaná činnost · čas · načteno v“, warnings jako varování, `failure.kind` (#2562) jako „známý důvod“; sdílený clipboard helper s chybovým stavem (N3) — jen pro tento panel, ne refaktor 18 míst.
- **Nepatří sem:** druhý banner, LLM, `?prompt=`, změna Stop/Run again, issues.
- **Testy:** Vitest komponenty (clipboard reject → chyba, ne success toast; klávesnice; dlouhé hodnoty; loading/error/empty odlišené; terminální běh nevypadá aktivně); `routine-run-detail-claims.test.tsx`, `routine-run-presentation.test.ts` zelené; Playwright `e2e/routines-*` zelené.
- **Podmínky dokončení:** na izolované instanci (`CREWSHIP_DATA_DIR`, port mimo 8080–8097) rutina se záměrným selháním: screenshot + zkopírovaný podklad porovnán s `GET /pipeline-runs/{id}`; druhý souběžný běh jako kontrola korelace — podklad neobsahuje jeho ID; otevření a kopírování nevytvoří žádný run/inbox položku.
- **Handoff:** zda panel žije v izolovaném souboru; příklad anonymizovaného podkladu.

### Iterace 5 — O2-I: detail agentního běhu u issues
- **Vstupy:** iterace 3 a 4; klastr D K2, K7, K9.
- **Rozsah:** `RunDrillDown` (`drill-downs.tsx:231`) při `useTrace` 404 zkusí `GET /runs/{id}`; nová `agent-run-detail.tsx` (stav z assignment řádku / `runResponse`, outcome, hard stop, poslední činnost, sdílený panel z iterace 4; reference `assignment` a `agent_run` jako dvě položky); karta Runs: bez `run_id` + terminální stav → „Běh se nespustil · <důvod>“, PENDING/RUNNING bez `run_id` → „Čeká na spuštění“.
- **Nepatří sem:** změna `ListRuns` SQL, retry/restart akce, změna `IssueRun` DTO nad rámec typu.
- **Testy:** `issue-runs-card.test.tsx` (bez run_id × 3 stavy, hard stop); Vitest `agent-run-detail`; Go `TestIssueRuns_CarriesRunAndAgentLinks`, `TestIssueRuns_ExposesHardStopResult`, `TestRunHandler_Get_*` zelené.
- **Podmínky dokončení:** issue se dvěma assignmenty (jeden bez běhu) na izolované instanci; souběžný druhý issue téhož agenta — podklad neobsahuje jeho ID; deep link/reload drží identitu.
- **Handoff:** které stavy assignmentu zůstávají „neznámé“ (journal bez terminální události).

### Iterace 6 — A3: původ běhu
- **Vstupy:** iterace 3 (GetRun `invoking_*`), 4, 5; klastr D K3, K8; klastr S K1 (po iteraci 1 je původ důvěryhodný).
- **Rozsah:** sekce „Původ“ v panelu: trigger (`triggered_via` + `triggered_by_id` → odkaz na schedule/webhook/issue), iniciátor (`invoking_user_id` → „vy“/id; u assignmentů `run.started.payload.created_by_user_id`), verze (`pipeline_version` + `definition_hash` ze snapshotu, odkaz na versions; u nedohledatelné verze „verze nedostupná“), vstupy jen odkazem na `RoutineSavedInputs` (do exportu nejdou), credentials: „Deklarováno v použité verzi: <typy>“ ze snapshotu `definition.credentials_required` + „Použití: nezaznamenáno“. Volitelný samostatný PR: `credential.resolved` journal v `credential_resolver.go` s `ActorID: runID` (+ registry, journal-groups test, docs).
- **Nepatří sem:** A2, live join na dnešní definici, agentní credential důkaz.
- **Testy:** fixture s `executed_definition_json` ≠ aktuální definice → panel ukazuje snapshot; Go test `invoking_*`; Vitest „nezaznamenáno“ pro chybějící pole (nikdy 0/prázdný string jako hodnota).
- **Podmínky dokončení:** žádný údaj v sekci není odvozen z dnešní konfigurace (test: změnit definici po běhu, panel se nemění).
- **Handoff:** zda `credential.resolved` vznikl; seznam údajů, které zůstávají „nezaznamenáno“ pro agentní běhy.

### Iterace 7 — A4: verze při opakování
- **Vstupy:** klastr R §3 V1–V3, S1–S5, §5 iterace 6, §7 T1/T2 jako červené testy; stav #2556/#2562.
- **Rozsah (dva PR):** *PR A (Go + CLI, hned):* `expected_definition_hash` v `runRequestBody` → 409 `{"error": "routine was published since you opened this form", "head_version": N}`; nepinovat implicitně (webhooky/CLI); `openapi.gen.json` + `pipelines.mdx` (i `pinned_version`, dnes nedokumentovaný); CLI `crewship routine run --version N` (+ `--expect-hash`), řádek v `docs/cli/routine.mdx`; CHANGELOG. *PR B (UI, po merge #2562):* `prepareAgain` z čerstvého GET uloží `{head_version, definition_hash}`; volby „Current recipe (v6)“ / „Executed recipe · v4“; „Current“ posílá `expected_definition_hash`, ne-current `pinned_version`; 409 → hláška + „Načíst znovu“; 404 verze → „Verze vN už není v archivu“; 422 `missing_*` → stejný toast jako v detail panelu; text „Původní běh v4 · Spustíte aktuální v6 se stejnými uloženými vstupy. Vstupy jsou součástí historie běhu; nepoužívejte je pro hesla.“
- **Nepatří sem:** přesun UI na `/replay`, redakce vstupů, preflight endpoint, pin pro page action/slash/inbox retry (rozhodnout a zapsat), rollback UI.
- **Testy:** Go `TestManualRun_ExpectedHash*` (409 při posunu HEAD, 200 při shodě, pin drží); Vitest `routine-run-detail-claims.test.tsx` (409 → hláška, žádný `router.push`; label s číslem verze), `routine-run-inputs-dialog.test.tsx`; zelené: `TestReplayRun_*`, `TestReplayPreservesPin*`, `TestManualRun_PinnedVersion*`, `TestSchedulePresetGate_RollbackDoor`, „Run again retries an uncertain start with the same key“.
- **Podmínky dokončení:** v4→v6 rozlišeno textem s čísly; posun HEAD = 409 pokrytý Go i Vitest; CLI flag zdokumentován; docs-inventory strict zelené.
- **Handoff:** místa, kde se `/run` volá bez hashe, a rozhodnutí, že tam pin nepatří.

### Iterace 8 — A1-b/c: efektivní oprávnění bez přepisu RBAC
- **Vstupy:** iterace 1 mergnutá; #2562 mergnuté (jinak konflikt v `routine-card-detail`, `routine-identity-header`, `routines-detail-panel`); klastr S §5 iterace 7.
- **Rozsah:** `GET /api/v1/workspaces/{ws}/pipelines/{slug}/access/me` → `{view, run, run_reason, edit, approve, disable, replay, manage_schedules, reasons}` z `canRole`, `CapabilitiesForMemberE`, `gateRoutineStatus`, `workspace_visible`; `GET /api/v1/credentials/{id}/access/me` → `{view, edit, rotate, assign, reveal, reveal_blocked_by: [policy|role|capability|sealed|scope], delete}` z reveal gate L0–L3 + `credentialVisibilityFilter` (L9 interaktivní session / fresh login / reason endpoint předpovědět neumí — přiznat v odpovědi). Jedna komponenta „Your access“ (routine detail + credential sheet) nahrazuje `roleAtLeast`/CASL odhady na těchto dvou místech; Run button disabled s `run_reason`. CLI `crewship routine access <slug>`, `crewship credential access <id>`; gen-openapi, openapi.mdx totals, docs/cli, docs-inventory, CHANGELOG.
- **Nepatří sem:** per-routine granty, změna reveal gate, Members grid, jiné obrazovky.
- **Testy:** Go tabulkové testy obou endpointů: 5 rolí × {bez capability, `routine.run`, `credentials:reveal`} × status {active, proposed, disabled}; invariant „`access/me.run` == (Run ≠ 403)“ jedním testem voláním obou handlerů; Vitest komponenty (loading/error/unknown odlišené, nikdy default „smíte“); e2e druhým účtem ručně (PRD §15).
- **Podmínky dokončení:** UI na těchto dvou obrazovkách nečte `role` pro rozhodnutí o tlačítku; regresní `credential-detail-sheet*.test.tsx`, `routines-detail-panel.test.tsx` zelené.
- **Handoff:** gate, které endpoint nepředpovídá, a jak to UI popisuje.

### Iterace 9 — A2: závislosti credentialu a dopad změny
- **Vstupy:** iterace 8; rozhodnutí R3 (crew scope pravda) — pokud rozhodnuto „`credential_crews`“, oprava resolveru/probe je první commit této iterace; klastr S §5 iterace 8.
- **Rozsah:** `internal/pipeline`: export `ReferencedCredentialTypes(dsl) map[type][]ref{step, kind}` (union declared + http_ref + template); `GET /api/v1/credentials/{id}/dependents` (role read; hidden rutiny filtrovat pro non-MANAGER): pro každou rutinu workspace parse HEAD definice, match typu, `resolves_to_this` (reuse SQL resolveru s ORDER BY), `dynamic: {agent_run_routines: n}`, `recorded: {last_used_at, source: "sidecar_fetch"|null, run_attributable: false}`; sheet: sekce „Routines that would use this“ s odkazy, badge configured/would-resolve, přiznaná neúplnost; přehled před Delete a před změnou scope/status (ne u každé editace); oprava S6 (audit/fields error state); CLI `crewship credential dependents <id>`; docs; CHANGELOG.
- **Nepatří sem:** USE event z executoru (samostatná 9b, +1,5 d), statická analýza skriptů, verdikt „unused“, rekurze `call_pipeline` (rozhodnout).
- **Testy:** Go tabulkový test dependents (declared/http_ref/template/žádný; newest-wins; crew-scope A/B; hidden routine pro MEMBER skryta, pro MANAGER vidět); credential bez běhu má `recorded.last_used_at=null` a `configured` neprázdné; Vitest: error state auditu ≠ empty; refetch error dependents → „neověřeno“, ne prázdný seznam, i když cache drží předchozí hodnoty.
- **Podmínky dokončení:** `TestRunEndpoint_*`, `pipeline_credentials_gate_test.go`, `credentials_resolution_test.go` zelené; docs-inventory strict.
- **Handoff:** typy referencí, které skener nevidí.

### Iterace 10 — O4: draft handoff a „Zeptat se na tuto Page“
- **Vstupy:** rozhodnutí R4 (cílový agent, nová session); klastr P §5 iterace 9a/9b, §3 V2/V3/S1/S3/S5.
- **Rozsah (dva PR):** *10a draft handoff:* `chat-client.tsx` čte jednou `?page=<slug>` a/nebo `?draft=1` (referenční, necitlivé); při draftu `initialInput` bez `autoSendInitial`; `?new=1` → `startConversation`; nepřepsat neprázdný draft cílové session (chip místo textu); opravit komentář v `routine-create-dialog.tsx`. *10b tlačítko a kontext:* `pages-layout.tsx` SubBar „Ask about this Page“ (jen `selectedSlug && !editing && detail.page`) → `/chat/<agent>?page=<slug>&new=1`; v chatu `usePage` (lidská autorizace) → chip s názvem, časem snapshotu a křížkem; při odeslání `content = text + ohraničený blok „Page context (untrusted, snapshot <ISO>)“` (název, slug, id, owner, stavy panelů, `last_produced_at`; žádný payload panelů, žádná data aplikace, žádný DOM) + `metadata.page_context`; Go `bridge.go` whitelist `page_context` (validace tvaru a délky) a persist; `turn-renderer` provenance chip (vzor `askProvenanceForTurn`); docs `chat-sessions.mdx`; CHANGELOG.
- **Nepatří sem:** serverové znovunačtení pro agenta, výběr řádků uvnitř aplikace, agentní read tool, změna `workspace_overview`, změna `?prompt` sémantiky pro routine dialog.
- **Testy:** Vitest chat-client handoff (draft se neodesílá; po změně session se nepřenáší; refresh nezdvojí); Go tabulkový test bridge (validní `page_context` přežije, cizí klíče ne, `ask_submission` beze změny); Vitest: chip odstranitelný před odesláním → žádný blok v content ani metadata; obnova chatu s persistovaným `page_context` vykreslí chip; revokace: `usePage` 404 před odesláním → chip „no longer accessible“, blok se nevloží. Zelené: `pages-layout.test.tsx`, `page-view.test.tsx`, `chat-panel-history-metadata.test.tsx`, `ask-provenance.test.tsx`, `go test ./internal/chatbridge -run Metadata`.
- **Podmínky dokončení:** správná Page a agent; žádné auto-send; žádná duplikace při změně session; revokace; obnova chatu s původem.
- **Handoff:** limity: kontext je lidský snapshot v textu, agent ho nemůže ověřit; 10a je předpoklad pro O3.

### Mimo desítku
- **O5 obsahový pack** (po rozhodnutí R5, paralelně s 7–10): `cmd/crewship/seeddata/packs/<role>/` — 2 `RoutineDef` (po termínu; porovnání týdnů) s `inputs`, `credentials_required`, `egress_targets`, Page panel `status.v1`; 1 ask form (návrh odpovědi: order_id, zpráva, tón; ≤ 6 polí); 4 `suggested_prompts` pro jednoho agenta; `seed verify` scénář; `docs/guides/routines-cookbook.mdx`. Testy: `cmd/crewship -run 'Seed.*Routines|Packs|AskForms'`, `internal/askforms`, skript testy. Nepatří: konektor, preflight endpoint, LLM v deterministickém sběru, šablony pro všechny agenty.
- **Závěrečný průchod scénáře PRD §1** (po 10): `e2e/first-agenda-<role>.mjs`: paleta → Page → formulář → Run (s vN v dialogu) → výsledek/čekání → Kopírovat podklad → Run again s volbou verze; druhý účet s `routine.run` (spustí, neupraví, neodhalí). Měření §14 na 3 uživatelích ručně. Před tím opravit write-side položky, které se nevešly (MarkInterrupted emit, `credential.resolved`).

---

## 7. Rozhodnutí vyžadující produktový vstup

Jen skutečná; technická rozhodnutí jsou uvedena níže jako již učiněná.

| # | Rozhodnutí | Doporučení oponentury | Blokuje |
|---|---|---|---|
| R1 | **#2562 „ask the lead to fix it“**: nechat mutující auto-odeslaný handoff s raw chybou v URL (a přepsat hranici O2/O3 v PRD), nebo v review zredukovat na copy-only? | Copy-only před mergem; je to změna cizího PR (session crewship-1). | 4, 10; konzistenci PRD §6/§12 |
| R2 | **Které spouštěcí cesty má `routine.run` pokrývat?** Dnes přímý run + slash ano; page action, replay, run_batch, schedule-run ne. | Page action + replay + schedule-run ano („spusť schválenou definici“), `run_batch` ne (spend). | 1, 7 |
| R3 | **Pravda o crew scope credentialu:** `credential_crews` (UI, visibility) vs `credentials.crew_id` (resolver, sidecar boot, probe). Oprava mění, komu se tajemství injektuje. | Resolver/probe přejít na `EXISTS credential_crews`; oprava jako první commit iterace 9. | 9 |
| R4 | **Cílový agent a session pro „Ask about this Page“:** lead crew vlastníka (u `user/<id>` vlastníka není) / poslední agent / výběr; vždy nová session? Rozsah kontextu u Pages Apps (jen metadata + stavy panelů, nebo i shrnutí payloadů)? | Lead owner crew s fallbackem na agent picker; vždy nová session; jen metadata + stavy panelů. | 10 |
| R5 | **Role a datový zdroj pro O5:** kdo je zákaznická role a odkud čte objednávky (HTTP API + `api_key`, nebo soubor na share)? | Bez rozhodnutí připravit jen parametrizovaný pack. | O5 pack |
| R6 | **Viditelnost pro VIEWER v O2:** respektovat dnešní model (každý člen vidí vše včetně raw vstupů), nebo VIEWER z kopírování vyloučit? Bez nového RBAC nelze scopovat víc než workspace. | Respektovat dnešní model, allowlist exportu; scope řešit až s A1-b. | 3–5 |
| R7 | **Credential evidence pro 1.0:** přijmout „nezaznamenáno“, nebo zařadit write-side `credential.resolved` (jen http kroky; agentní běhy bez důkazu)? | Přijmout „nezaznamenáno“ pro 1.0; write-side jako volitelný PR v iteraci 6. | 6, 9 |
| R8 | **Per-rutinový execute grant** (AWX „Execute na template“, příklad s Martičkou): 4–6 d, nová tabulka + 4 gate + UI. | Mimo 1.0; příklad v PRD přeformulovat na workspace-wide `routine.run`. | PRD §7 text |
| R9 | **Zákaz „citlivých“ názvů polí ve vstupech rutin** (V9): triviální lint, ale může rozbít existující definice. | Do 1.0 jen text v dialogu. | 7 |
| R10 | **Recent klíč pro všechny skupiny palety:** historie Issues/Agents Recent se jednou ztratí. | Přijatelné. | 2 |

**Technicky rozhodnuto oponenturou (bez produktového vstupu):** O2 builder je klientská čistá funkce s fixture-kontraktem, ne endpoint; `access/me` endpointy počítají z týchž funkcí jako handlery (ne z duplikované tabulky — jinak vznikne třetí drift vedle CASL a `tiers.ts`); hlavičky invoking crew se vážou na crew-bound interní token; A4 zůstává na `/run` s `expected_definition_hash` (ne `expected_head_version`, kvůli zastaralému `head_version` u legacy řádků); paleta používá `apiFetch`, ne `usePages()`, a neposílá `limit`; O4 kontext jde do textu zprávy + whitelistovaná provenance, bez serverového re-loadu; O5 obsah žije v seeddata, ne ve FE šablonách; preflight endpoint mimo minimální verzi.

---

## 8. Co oponentura neověřila

- Bypass trust grantu přes podvrženou invoking crew za běhu (K1) — jen čtení `waitpoints.go`, bez běhu s grantem.
- Env-delivered credential v běžícím kontejneru po revokaci (S4) — z kódu, bez živého kontejneru.
- Zda uživatel v prázdném chatu uvidí ask formuláře před prvním odesláním (R-N6) — neověřeno v prohlížeči.
- Živý read/write/revoke průchod druhým účtem (PRD §15) — oponentura účty nezakládala; zůstává akceptací iterací 1 a 8.
- Nic nebylo nasazeno na dev instanci; všechny reprodukce jsou httptest/Vitest na fixture nebo `go test` v tomto klonu.
