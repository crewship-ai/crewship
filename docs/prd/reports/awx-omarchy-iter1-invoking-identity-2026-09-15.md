# Iterace 1 — původ běhu z ověřené identity (handoff)

Datum 2026-09-15. Zadání: [plán iterací §Iterace 1](../awx-omarchy-implementation-iterations-2026-09-15.md), [PRD §7 A1a](../awx-omarchy-product-improvements-2026-09-14.md), [oponentura K1](awx-omarchy-prd-opponent-2026-09-15.md). Bezpečnostní část iterace; matice spouštěcích oprávnění je zde jen zmapována (§4), rozsah `routine.run` se nemění.

Výchozí HEAD `5a0ad11f` (main = origin/main). Souběžné PR #2562/#2556/#2558 se dotčených souborů nedotýkají (`gh pr view <n> --json files`, průnik prázdný). Cizí untracked dokumenty v `docs/prd` zachovány.

## 1. Co bylo zjištěno nad rámec oponentury

Oponentura našla, že `POST …/pipelines/{slug}/run` čte `X-Crewship-Invoking-Crew/-Agent` z libovolného JWT požadavku. Při implementaci se ukázalo, že ta hlavička **nikdy neměla legitimního odesílatele**:

- veřejná `/run` route je za `RequireAuth` (`internal/api/middleware.go:251`), který přijímá jen session JWT a CLI tokeny; sidecarův `X-Internal-Token` by dostal 401;
- sidecar hlavičky sice nastavoval (`internal/sidecar/pipelines.go`, `handlePipelinesRun`), ale `proxyIPCJSON` staví nový upstream požadavek a přenáší jen `X-Internal-Token` — test `TestHandlePipelinesRun_InvokerHeadersForwarded` to od začátku přiznával jako `t.Skip("KNOWN GAP…")`;
- agentní běhy s atribucí jdou přes MCP `run_routine` → `POST /api/v1/internal/pipelines/run` (`InternalRun`), kde identita cestuje v těle.

Hlavičkové čtení tedy bylo čistě útočná plocha. Druhá polovina nálezu: `InternalRun` kontroloval `invoking_crew_id` jen vůči crew-bound tokenu (#1186); `invoking_agent_id` nekontroloval nikdo a master/workspace-bound token mohl pojmenovat crew z cizího workspace. Třetí: `routineTrust` (`internal/pipeline/waitpoints.go`) bral posturu **invoking** crew přednostně před autorskou, takže i legitimně ověřená invoking crew s `full` mohla uvolnit gate strict autora.

## 2. Změny

| Soubor | Změna |
|---|---|
| `internal/api/pipelines_exec.go` | `Run`: hlavičky se nečtou; `InvokingCrewID/AgentID` jsou prázdné (user-driven), `InvokingUserID` = ověřený volající. `InternalRun`: po `assertBoundCrewWorkspaceDB` nově `assertInvokingIdentity`. |
| `internal/api/pipelines_exec_identity.go` (nový) | `assertInvokingIdentity`: neprázdná crew musí být živý řádek `crews` v `body.WorkspaceID`; neprázdný agent živý řádek `agents` v tomtéž workspace a (je-li crew uvedena) `agents.crew_id == crew`. Jinak 403, nic se tiše nepřepisuje. Prázdné hodnoty = neatribuováno (fallback na autorskou crew v executoru zůstává). |
| `internal/pipeline/waitpoints.go` | `routineTrust`: nejprve autorská crew — je-li čitelná a `strict`, výsledek je strict bez ohledu na invokera; jinak beze změny (invoking crew má přednost, fallback autor). Nový helper `crewAutonomy` (nečitelná crew = strict). Chybějící `pipelines` řádek při přítomném invokerovi zachovává původní chování (existující fixture bez `pipelines` řádku). |
| `internal/sidecar/pipelines.go` | `handlePipelinesRun` už hlavičky nenastavuje; forged per-agent token dál → 403. Komentář popisuje skutečný stav (route nemá kanál pro identitu). |
| `internal/sidecar/pipelines_test.go` | Skipnutý „KNOWN GAP“ test nahrazen `TestHandlePipelinesRun_NoInvokerHeadersUpstream` (hlavičky ani z requestu ani ze sidecaru neodcházejí upstream, `X-Internal-Token` ano). |
| `internal/api/pipelines_exec_invoking_identity_test.go` (nový) | viz §3 |
| `internal/pipeline/waitpoints_trust_invoker_test.go` (nový) | viz §3 |
| `docs/api-reference/workspaces.mdx` | Řádek hlavičky nahrazen odstavcem o user-driven atribuci a o tom, kde se agentní identita ověřuje. |
| `CHANGELOG.md` | `### Security` položka. |

Žádný nový endpoint, flag, migrace ani změna OpenAPI (`go run ./cmd/gen-openapi` bez diffu).

## 3. Testy a skutečné výsledky

Červené na HEAD `5a0ad11f` (ověřeno dočasným `git checkout HEAD -- internal/api/pipelines_exec.go` resp. path-scoped stash `waitpoints.go`), zelené s opravou:

| Test | Co dokazuje | HEAD | Po opravě |
|---|---|---|---|
| `TestRun_InvokingHeadersFromJWTCallerAreIgnored` (`internal/api`) | MEMBER+`routine.run` s podvrženými hlavičkami (`full` crew, vymyšlený agent): 200, uložená provenance crew/agent prázdná, `invoking_user_id` = volající. **HTTP → řádek → trust:** trust grant pro (pipeline, `publish`, hash běhu) + strict autor → `CreateApproval` = `pending`. | FAIL (`crew="crew-full-sibling" agent="agent-i-made-up"`) | PASS |
| `TestInternalRun_InvokingIdentityIsVerified` (`internal/api`, 13 případů) | crew-bound: vlastní crew+agent 200; bez agenta 200; sibling crew 403 (#1186 beze změny); vlastní crew + sibling agent 403; + agent z cizího workspace 403; + neznámý agent 403. Master token: sibling crew + její agent 200 (workspace-wide zůstává); cizí workspace crew 403; sibling crew + cizí agent 403; neznámá crew 403. Workspace-bound: cizí crew 403; sibling crew + agent 200. Master token bez crew + sibling agent → 200 s crew doplněnou z agenta. Odmítnutí nezanechá `pipeline_runs` řádek; povolené případy mají uloženou přesně tu identitu a `invoking_user_id` prázdné. | 5 případů FAIL (200 místo 403) | PASS |
| `TestCreateApproval_InvokingCrewCannotRelaxStrictAuthor` (`internal/pipeline`) | Řádek běhu s `invoking_crew_id` = `full` crew, autor `strict`, aktivní grant → `pending`; autor `guided` → `approved` (cross-crew cesta funguje dál). | FAIL (`approved`) | PASS |
| `TestHandlePipelinesRun_NoInvokerHeadersUpstream` (`internal/sidecar`) | Upstream request nenese `X-Crewship-Invoking-*` (ani z requestu), nese `X-Internal-Token`. | n/a (nahrazuje skip) | PASS |

Regresní běhy (vše v popředí, `-count=1`):

```
go vet ./...                                                           clean
go test ./internal/pipeline/ ./internal/sidecar/                       ok 30.2 s / ok 62.0 s (plné balíčky)
go test ./internal/api/ -run 'Pipeline|InternalRun|RunEndpoint|Trust|Waitpoint|Replay|Schedule|Slash|PageAction|Invoking|CallPipeline|Deferred|Pending|RouteRoles|RouteAuthz'
                                                                       ok 77.0 s
go run ./cmd/gen-openapi                                               bez diffu
go run ./scripts/docs-surface-check                                    0 unguarded
go run ./scripts/docs-inventory -strict                                clean
go run ./scripts/agents-invariants                                     4 hold
```

Neběženo: celý `internal/api` (12min+ suite; CI ho spustí), Docker-závislé testy (self-skip), frontend (žádná FE změna).

## 4. Matice spouštěcích cest (inventura, beze změny)

Ověřeno na HEAD po této změně. „Capability“ = per-člen `routine.run` (`internal/api/capabilities.go`).

| Cesta | Route / handler | Brána v routeru | Brána v handleru | `routine.run` | Identita uložená do běhu |
|---|---|---|---|---|---|
| Přímý run | `POST …/pipelines/{slug}/run` → `Run` (`router_pipelines.go:46`) | `roleInline` | MANAGER+ ∨ `routine.run` (`pipelines_exec.go:93`) | **ano** | `invoking_user_id`; crew/agent prázdné (po této změně vždy) |
| Slash paleta | `slash_commands_handler.go:251` | — | `canRole(create) ∨ routine.run` | **ano** | jako přímý run |
| Deferred run (delay/debounce) | `Run` → `enqueueDeferredRun` (`pipeline_deferred.go:23`) | jako přímý run | jako přímý run; dispatcher roli nepřeověřuje | **ano** | `invoking_user_id`; crew/agent prázdné |
| Page action | `POST /pages/{slug}/panels/{panelId}/actions/{actionId}` → `DispatchAction` (`router_pages_actions.go:45`) | `roleCreate` | `requireRole "create"` (`pages_actions.go:503`) | **ne** (MANAGER+) | pending run bez pinu, user |
| Replay | `POST …/pipelines/runs/{runId}/replay` → `ReplayRun` (`router_pipelines.go:212`) | `roleCreate` | `requireRole "create"` | **ne** | `triggered_by_id` = zdrojový run, `invoking_user_id` **není** nastaven (R-S1) |
| Schedule run now | `POST …/pipeline-schedules/{scheduleId}/run` → `RunSchedule` (`:136`) | `roleCreate` | `requireRole "create"` (`pipeline_schedules.go:867`) | **ne** | — |
| Batch | `POST …/pipelines/{slug}/run_batch` → `RunBatch` (`:47`) | `roleCreate` (záměrně, spend) | `requireRole "create"` | **ne** | — |
| Agentní (MCP `run_routine`) | sidecar `runPipeline` → `POST /api/v1/internal/pipelines/run` → `InternalRun` | `internalAuth` | `assertInternalTokenWorkspace` + `assertBoundCrewWorkspaceDB` + **`assertInvokingIdentity`** | n/a (agent) | `invoking_crew_id` = crew tokenu, `invoking_agent_id` = ověřený agent crew; `triggered_via=call_pipeline` |
| Sidecar IPC `POST /pipelines/{slug}/run` | proxy na veřejnou `/run` s `X-Internal-Token` | `RequireAuth` → **401** | — | — | mrtvá cesta (viz §6) |
| Vnořený `call_pipeline` krok | `executor.go:2378` | — | — | — | invoking = autorská crew/agent rodiče (interní, ne HTTP) |

Nesoulad, který zůstává (rozhodnutí R2 v oponentuře, neřešeno zde): operátor s `routine.run` spustí rutinu z detailu i palety, ale Page action, Replay, Schedule-run vrací 403. Rozšíření je oddělená změna po produktovém rozhodnutí.

## 5. Autorizační kontrakt po změně

- Veřejná `/run`: provenance = `invoking_user_id` (ověřený uživatel / CLI token); `invoking_crew_id`, `invoking_agent_id` vždy prázdné. Žádná hlavička není čtena.
- `InternalRun`: `invoking_crew_id` = crew tokenu (crwv1, doplní se při vynechání) nebo crew v bound workspace (wsv1) nebo libovolná crew v `body.WorkspaceID` (master); vždy živý řádek. `invoking_agent_id` prázdný, nebo živý agent téhož workspace a člen uvedené crew; je-li agent uveden bez crew (jen master/wsv1), crew se doplní z řádku agenta, takže uložená dvojice je vždy konzistentní. Jinak 403 bez vytvoření běhu.
- Trust grant (`wait(approval)`): strict **autorská** crew vždy blokuje; jinak postura invoking crew (je-li) nebo autora. Nečitelná crew = strict. Nečitelný `pipelines` řádek bez invokera = strict; s invokerem = postura invokera (původní chování, kryto existujícími testy).
- Sémantika revokace za běhu se nemění (dokumentace odložena — viz §7).

## 5a. Review

CodeRabbit byl při otevření PR rate-limited (status zelený, review žádné); retrigger požádán. Nezávislé code-review (Claude, úroveň high, celý diff + okolní kód) nenašlo chybu správnosti; jeden nit (agent bez crew u master/wsv1 volajícího → nekonzistentní „From“ dvojice) opraven doplněním crew z řádku agenta; redundantní čtení postury autora odstraněno. Stav strojového review v době merge je v PR.

## 6. Limity a co není hotové

- **Staré řádky `pipeline_runs.invoking_crew_id/agent_id` nejsou zpětně důvěryhodné.** Cokoli zapsané veřejnou cestou před tímto commitem mohlo být podvržené; iterace 6 (původ běhu) nesmí historickou invoking identitu označit za ověřenou. Řádky z `InternalRun` před tímto commitem měly crew ověřenou (crwv1) ale agenta ne. Zpětná migrace/označení se neprovádí.
- Sidecar IPC `POST /pipelines/{slug}/run` a `/dry_run` proxy na veřejnou JWT route → v produkci 401. Byla to mrtvá cesta už před touto změnou; ponechána (ne rozsah iterace), kandidát na odstranění nebo přesměrování na `InternalRun`.
- `ReplayRun` nezapisuje `invoking_user_id` (oponentura R-S1) — patří do iterace 7 spolu s rozhodnutím R2.
- Dokumentace „sémantika revokace za běhu“ (oponentura S4) do `docs/security` **nebyla** v této relaci napsána — zadání úlohy 1 ji nejmenuje; doporučuji ji přidat v iteraci 8 (access/me), kde se vysvětlují efektivní práva.
- Živý průchod druhým účtem (PRD §15) neproveden; reprodukce jsou httptest nad reálnými migracemi.
- Capability cache 30 s (`capabilities_check.go`) beze změny.

## 7. Vstup pro další relaci (iterace 2 — Pages v paletě)

- Vycházet z merge commitu tohoto PR (nebo z jeho větve, pokud ještě neprošel review) — iterace 2 je na této změně nezávislá (FE only).
- Pro iteraci 6/8: `GetRun` dál nevrací `invoking_*` (aditivní pole plánováno v iteraci 3); po tomto commitu je `invoking_crew_id` neprázdné jen u `InternalRun`/`call_pipeline` běhů a je ověřené.
- Otevřené produktové rozhodnutí R2 (které cesty honorují `routine.run`) blokuje sjednocení Page action / Replay / Schedule-run; tento commit ho nepředjímá.
