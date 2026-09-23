# Klastr S: A1 (oddělená práva a delegované spuštění) + A2 (credential → závislé rutiny)

## 0. Ověření prostředí

Branch `main`, HEAD `5a0ad11f`, git status jen untracked `docs/prd/*` (cizí WIP, nedotčeno); v `internal/api` leží cizí dočasný `zz_opp_R_test.go` jiného oponenta — ponechán. Můj `zz_opp_S_test.go` byl po obou bězích smazán (zdroj + výstup v §7). Dočasný ref `refs/opp/pr2562` (fetch PR #2562) smazán.

## 1. Verdikt per funkce

**A1 — upravit rozsah a předsunout.** Oddělená práva už na backendu z velké části existují: 5 rolí × 5 tříd akcí (`canRole`) + 10 per-člen capabilities včetně `routine.run` (execute bez edit) a `credentials:reveal` (reveal bez ohledu na roli). Chybí (a) jednotná aplikace této matice napříč spouštěcími cestami — page button, replay, run_batch a `pipeline-schedules/{id}/run` capability `routine.run` ignorují, takže cílový průchod PRD (Page → spustit) pro operátora dnes vrací 403; (b) per-resource „effective access“ endpoint pro rutinu/credential (existuje jen workspace-level `currentUserCapabilities` a per-page `access/me`); (c) UI dnes práva odhaduje z názvu role (`roleAtLeast(role,"MANAGER")`, CASL), přesně to, co PRD zakazuje. Audit odhalil i dva bezpečnostní nálezy (forgeovatelná hlavička `X-Crewship-Invoking-Crew` z JWT požadavku; multi-crew credential nerezolvuje pro druhý crew). Doporučení: A1 audit + opravy = iterace 0/2 (před ostatními), UI vysvětlení práv = iterace 7 nad novým `access/me` endpointem.

**A2 — implementovat v užší verzi, po iteraci 0/2.** Vazba credential ↔ rutina není uložená, je vypočítaná: rutiny referencují credential podle TYPU (`credentials_required`, `credential_ref.type`, `{{ secrets.<type> }}`), resolver vybere „nejnovější ACTIVE v author-crew scope“. Statická analýza definic je proveditelná (3 zdroje referencí, jeden JSON parse na rutinu), ale musí přiznat neúplnost pro `agent_run` kroky (sidecar). „Zaznamenané skutečné použití“ na úrovni rutiny NEEXISTUJE: executor při rezoluci `{{ secrets.* }}` nezapisuje ani `credential_audit USE`, ani `last_used_at`; jediný USE záznam je sidecar fetch na úrovni crew bez run_id. Detail sheet dělá N+1 fan-out na `/agents/{id}/credentials`, zdroj Used by = agenti, ne rutiny. Chyba refetche auditu se maskuje jako „Nothing has happened“.

## 2. Tabulka funkcí

| ID | Současný stav (HEAD) | Souběžné PR | Doporučený rozsah (min. verze) | Závislosti | Odhad | Důkaz v kódu (soubor:řádek) |
|---|---|---|---|---|---|---|
| A1-a: matice práv backend | Role VIEWER/MEMBER/MANAGER/ADMIN/OWNER, akce read/create/update/manage/delete; capabilities `routine.run`, `routine.create`, `credential.create/rotate`, `credentials:reveal`, `page.create`… Run = role MANAGER+ OR `routine.run` | žádný | Nic nového nepřidávat; sjednotit: page action, replay, `run_batch`, schedule run mají honorovat `routine.run` (rozhodnout, které) | — | 0,5–1 den (fix + tabulkový test) | `internal/api/helpers.go:516-563`, `capabilities.go:35-51`, `capabilities_check.go:268-313`, `pipelines_exec.go:88-97`, `router_pipelines.go:46-47,136,212`, `router_pages_actions.go:46`, `pages_actions.go:503` |
| A1-b: effective access per resource | Workspace-level: `GET /workspaces` → `currentUserRole`+`currentUserCapabilities`; per-page `GET /pages/{slug}/access/me`; per-member admin-only `GET .../members/{id}/capabilities` (manage) | žádný | Nový `GET /workspaces/{ws}/pipelines/{slug}/access/me` a `GET /credentials/{id}/access/me` vracející boolean rozhodnutí spočtená TÝMIŽ funkcemi jako handlery (canRole, CapabilitiesForMemberE, gateRoutineStatus, reveal gate L0–L3 bez L9/fresh-login) + `reasons[]` | A1-a | 2–3 dny (2 endpointy + gen-openapi + openapi.mdx totals + 2 CLI příkazy + docs-inventory + CHANGELOG) | `workspaces.go:252`, `pages_folder_acl.go:784-820`, `workspaces_member_capabilities.go:133-165`, `credentials_reveal.go:258-335` |
| A1-c: UI vysvětlení | Routine detail: `canEdit = roleAtLeast(role,"MANAGER")`, Run button gated jen statusem (VIEWER vidí aktivní Run → 403); credential sheet: `canUpdate/canReveal` z CASL+capability+reveal-policy (4 z 9 gate) | **#2562** přepisuje `routine-card-detail.tsx`, `routine-identity-header.tsx`, `routines-detail-panel.tsx` (stejná roleAtLeast logika) | Jedna komponenta „Your access“ (routine + credential) nad `access/me`; do merge #2562 neupravovat tyto soubory | A1-b, merge #2562 | 1–2 dny | `routine-card-detail.tsx:113`, `routines-detail-panel.tsx:454`, `credential-detail-sheet.tsx:277-311,927`, `lib/permissions/tiers.ts:1-30` (dokumentovaný drift CASL vs server) |
| A1-d: sémantika revokace za běhu | Executor lidskou identitu nekonzultuje (InvokingUserID jen pro `notify to: trigger`); deferred run dispatcher nepřeověřuje roli; sidecar reaper 60 s pro proxy-injektované klíče; env-delivered tajemství v běžícím kontejneru neodvolatelná | žádný | Jen dokumentovat (docs/security) | — | 0,25 dne | `pipelines_exec.go:170-175`, `pending_dispatcher.go:163-235`, `credstore_reap.go:16,112-130`, `executor.go:811-821` |
| A2-a: závislé rutiny (konfigurace) | Neexistuje endpoint ani UI; `pipeline.RequiredCredentialTypes` pokrývá jen `credentials_required`; `secretTypesInStep` neexportovaný | #2556 mění `internal/pipeline/executor.go`, `types.go` (bez kolize s resolverem) | `GET /credentials/{id}/dependents` → konfigurace s referencemi declared, http_ref a template, údaj `resolves_to_this`, dynamické vazby a evidence; CLI `crewship credential dependents` | rozhodnutí o crew scope (nález K2) | 2–3 dny | `pipeline/dsl_validate_credentials.go:45-66`, `pipeline/executor_render.go:68-120`, `pipeline/credential_resolver.go:106-127,159-175` |
| A2-b: zaznamenané použití | `credential_audit USE` jen ze sidecar fetch (meta crew_id, bez run_id) a issue code links; executor nic nezapisuje | žádný | Minimálně: v odpovědi A2-a `recorded` s `last_used_at`, zdrojem sidecar_fetch nebo null a `run_attributable: false`; volitelně USE event z `resolveStepSecrets` s run_id (nová schopnost, samostatně) | A2-a | 0,5 dne (jen přiznat) / +1,5 dne (USE z executoru s debounce a scrub) | `internal_credentials.go:44-86`, `credential_audit.go:17-26`, grep `RecordCredentialEvent` v `internal/pipeline` = 0 výskytů |
| A2-c: sheet – error vs empty | Access: `accessError` + coverage OK a testováno; Audit/Fields: `.catch → []` → „Nothing has happened“; ne React Query, ruční `useEffect` | žádný | `auditError`/`fieldsError` stavy s viditelným alertem | — | 0,5 dne | `credential-detail-sheet.tsx:372-390,1089-1092`, test `credential-detail-sheet.test.tsx:427-448` |
| A2-d: rotace zachová vazby | Rotace = UPDATE in place (stejné id) → bindings/assignments zůstávají. Ale „nový credential stejného typu“ přebírá `{{ secrets.<type> }}` (newest wins) bez ohledu na vazby | žádný | Dokumentovat v dependents odpovědi `resolves_to_this` | A2-a | 0 | `credential_rotation.go:267`, `credential_resolver.go:119-121` |

## 3. Nálezy podle závažnosti

### Kritické

**K1 [POTVRZENO] Hlavičky `X-Crewship-Invoking-Crew/-Agent` se čtou z libovolného JWT požadavku a ukládají jako provenance běhu; invoking crew přepisuje autonomy posture pro trust grants.**
`internal/api/pipelines_exec.go:166-167` čte hlavičky bez ohledu na to, zda volá sidecar (interní token) nebo prohlížeč; middleware je nestripuje (grep `X-Crewship-Invoking` — jen sidecar setter a tento reader). Persistují se do `pipeline_runs.invoking_crew_id/agent_id` a `internal/pipeline/waitpoints.go:400-401` je používá místo author crew při `routineTrust` → `consumeTrust` (`waitpoints.go:444-452`): pokud author crew je `strict` (žádné zkratky) a útočník pošle id crew s jinou posturou, existující standing trust grant se pro `wait(approval)` spotřebuje. Reprodukce: test `TestOppS_RunHeaderForgesInvokingCrew` (§7) — MEMBER + `routine.run`, hlavička `crew-i-do-not-belong-to` → řádek běhu `invoking_crew_id="crew-i-do-not-belong-to" invoking_agent_id="agent-forged"`. Bypass trustu jsem NEREPRODUKOVAL během (vyžaduje grant) — ta část je [NÁVRHOVÉ RIZIKO] plynoucí z `waitpoints.go:400`. Stejná třída chyby jako už opravený `triggered_by_id` forge (`pipelines_exec_trigger_forge_test.go`). Dopad na PRD: A1 akceptace „modifikované HTTP tělo není eskalace“ dnes NEPLATÍ pro hlavičky; A3 (původ běhu) by zobrazoval podvržený původ. Oprava: hlavičky akceptovat jen když `InternalTokenCrewFromContext != ""` (jinak přepsat prázdným), test v `pipelines_exec_trigger_forge_test.go`.

### Vysoké

**V1 [POTVRZENO] Page action ignoruje capability `routine.run` → cílový průchod PRD (Page → spustit) pro operátora vrací 403.**
`router_pages_actions.go:46` registruje dispatch jako `roleCreate`; `pages_actions.go:503` navíc `requireRole(w, r, "create")`. Komentář v `pages_actions.go:65` cituje zastaralý stav `pipelines_exec.go:71 requireRole "create"` — přímý run je od té doby `roleInline` + `CapabilityRoutineRun` (`pipelines_exec.go:88-97`, `router_pipelines.go:34-46`). Reprodukce: `TestOppS_PageActionIgnoresRoutineRunCapability` — MEMBER s `["chat","routine.run"]`, člen crew-lookout → `403 Forbidden`; tentýž uživatel na `POST /pipelines/{slug}/run` → 200 (`TestOppS_OperatorCannotEditDefinitionOrCredential`, „run by operator: 200“). Stejně `run_batch` (`router_pipelines.go:47`, záměrně), `pipeline-schedules/{id}/run` (`:136`), `runs/{id}/replay` (`:212`, potvrzeno 403 v témže testu). Dopad: A1 „operátor spustí povolenou rutinu“ platí jen pro přímý endpoint a slash paletu (`slash_commands_handler.go:250`), ne pro Pages ani opakování (A4).

**V2 [POTVRZENO] Credential se scope na více crew rezolvuje jen pro první crew.**
`credentials_mutate.go:878-882`: `crew_ids=[A,B]` → `credential_crews` dva řádky, ale `credentials.crew_id = crewIDs[0]`. Resolver i probe (`credential_resolver.go:114,169`) filtrují jen `crew_id IS NULL OR crew_id = author`. Reprodukce `TestOppS_MultiCrewCredentialResolvesForSecondCrew`: probe pro crew-A `ok=true`, pro crew-B `ok=false` → rutina crew B dostane 422 „not present in the vault“, zatímco UI/visibility filter (`credentials_loaders.go:43-50`, join přes `credential_crews`) ji ukazuje jako dostupnou. Dopad na A2: jakýkoli výpočet „které rutiny tento credential používají“ musí říct, kterou pravdu bere; do rozhodnutí je mapa závislostí pro multi-crew credentialy nesprávná.

**V3 [POTVRZENO] „Execute-only“ existuje, ale je workspace-wide, ne per-rutina.**
`routine.run` (`capabilities.go:51`) dovoluje spustit KAŽDOU aktivní rutinu ve workspace, včetně `workspace_visible=false` (komentář `slash_commands_handler.go:238-241`, list filtr `pipeline/types.go:1046`). AWX-styl „Martička smí spustit Import objednávek“ (jen tu jednu) nemá ekvivalent. Odhad per-routine execute grantu: nová tabulka `pipeline_run_grants(pipeline_id,user_id|crew_id)`, gate v Run/page action/replay/schedule run, admin endpointy + CLI + UI = 4–6 dní — to je ta „nová obecná matice RBAC“, kterou PRD správně odkládá.

### Střední

**S1 [POTVRZENO] UI práva odhaduje z názvu role; žádný per-resource „access/me“ pro rutinu ani credential.**
`routine-card-detail.tsx:113` (`roleAtLeast(role,"MANAGER")`), `routines-detail-panel.tsx:454` (Run gated jen statusem — VIEWER vidí aktivní Run a dostane 403), `credential-detail-sheet.tsx:277-311` (canReveal = 4 z 9 serverových gate: chybí interaktivní session, crew scope, fresh login, reason). `lib/permissions/tiers.ts:1-30` sám dokumentuje dva drifty CASL vs server. Existující ekvivalenty: `GET /workspaces` → `currentUserRole`,`currentUserCapabilities` (`workspaces.go:252`, hook `use-abilities.ts`), `GET /pages/{slug}/access/me` (`pages_folder_acl.go:784`) — vzor pro nový endpoint. `GET .../members/{id}/capabilities` je manage-only (`workspaces_member_capabilities.go:145`), MEMBER se na sebe zeptat nemůže.

**S2 [POTVRZENO] Runtime použití credentialu rutinou se nezaznamenává.**
`internal/pipeline` neobsahuje žádné volání `RecordCredentialEvent`/`last_used_at`; `NewVaultCredentialResolver` jen dešifruje. USE event vzniká pouze v `maybeRecordSidecarUse` (`internal_credentials.go:44-86`, meta `crew_id`, bez run/routine, debounce 60 s) a `issue_code_links.go:253,357`. Dopad na A2: „nakonfigurováno“ vs „skutečně použito“ lze oddělit jen na úrovni crew, ne rutiny; PRD to má přiznat, ne slibovat.

**S3 [POTVRZENO] Sheet: chyba načtení auditu/fields se zobrazí jako prázdno.**
`credential-detail-sheet.tsx:376-390`: `r.ok ? r.json() : []` a `.catch(() => setAudit([]))` → `:1089-1092` „Nothing has happened to this credential yet.“ Access sekce chybu propaguje správně (`accessError`, `:928-930`, test `credential-detail-sheet.test.tsx:427`). Není React Query, žádný `isError`; stale data nevznikají (state se při otevření resetuje `:249-260`), vzniká „error = empty“.

**S4 [POTVRZENO] Used by = agenti přes N+1 fan-out, ne rutiny.**
`credential-detail-sheet.tsx:1440-1450` („There is no 'list the assignments of credential X' endpoint“), cap `MAX_ASSIGNMENT_LOOKUPS`, coverage přiznána („Checked n of m“). Serverový DTO nese `agent_names`, `crew_ids`, `_count_agent_credentials`; rutiny nikde.

**S5 [POTVRZENO] Revokace za běhu — současná sémantika (k zdokumentování, ne k opravě).**
Lidská práva: jen při přijetí požadavku (`pipelines_exec.go:88-97`); deferred/page-action běh se odpálí i když uživatel mezitím ztratil roli (`pending_dispatcher.go:163-235` nekontroluje `InvokingUserID`). Běžící executor lidskou identitu nepoužívá (`InvokingUserID` jen `notify to: trigger`). Credential: proxy-injektované klíče sidecar dropne do 60 s (`credstore_reap.go:16`), lease expiry lokálně; env/file-delivered hodnoty v běžícím kontejneru zůstávají do restartu [NÁVRHOVÉ RIZIKO — neověřeno během]. Preflight (integrace/resources/credentials) se opakuje při každém `executor.Run` v ModeRun (`executor.go:811-821`), takže smazaný credential zastaví DALŠÍ běh, ne běžící.

### Nízké

**N1 [POTVRZENO] Zastaralé komentáře jako důkaz.** `pages_actions.go:65` a `router_pages_actions.go:14-19` tvrdí „running a routine is MANAGER+“ — od zavedení `routine.run` nepravda; přesně ten typ „důkazu“, který brief zakazuje.

**N2 [POTVRZENO] VIEWER s `routine.run` může spouštět.** `pipelines_exec_capability_gate_test.go` case „viewer with routine.run runs“ PASS na HEAD; `lib/capabilities.ts:89` to dokumentuje. PRD věta „čtenář nemůže spustit“ platí jen pro VIEWER bez grantu — formulaci upřesnit.

**N3 [NÁVRHOVÉ RIZIKO] Capability cache 30 s** (`capabilities_check.go:55-58`): po revokaci `routine.run` může uživatel ještě ≤30 s spouštět (Invalidate se volá jen z PATCH handleru téhož procesu). Pro akceptaci „po revokaci další spuštění selže“ je třeba test volat `InvalidateCapabilityCache` nebo čekat.

## 4. Navržené přesné změny PRD

- **§7 odst. 1** za „Použít aktuální názvosloví produktu“ doplnit: „Backend už tato práva odděluje: workspace role (VIEWER…OWNER, `canRole`) a per-člen capabilities (`routine.run`, `routine.create`, `credential.create`, `credential.rotate`, `credentials:reveal`, `page.create`, …, `internal/api/capabilities.go`). ‚Operátor‘ = MEMBER/VIEWER s `routine.run`; grant je workspace-wide, per-rutinový grant neexistuje a není součástí tohoto PRD.“
- **§7 odst. 2** nahradit „Pokud již existuje ekvivalent, pouze jej propojit“ zněním: „Ekvivalent existuje jen na úrovni workspace (`GET /workspaces` → `currentUserRole`, `currentUserCapabilities`) a Pages (`GET /pages/{slug}/access/me`). Pro rutinu a credential se doplní `…/access/me`, který vrací rozhodnutí spočtená týmiž funkcemi jako mutační handlery; UI je pouze zobrazí.“
- **§7 odst. 3** doplnit větu: „Hlavičky `X-Crewship-Invoking-Crew/-Agent` smí server přijmout jen od sidecaru (crew-bound interní token); z JWT požadavku se ignorují.“ (nález K1)
- **§7 Akceptace** upravit: „čtenář bez `routine.run` nemůže spustit (VIEWER s grantem spustit smí — je to dokumentované chování); operátor spustí povolenou rutinu **z detailu, palety i Page** (dnes Page/replay/schedule-run 403 — nález V1); modifikované tělo **ani hlavička** není eskalace; po revokaci další spuštění selže do 30 s (capability cache) …“
- **§7 Akceptace** doplnit větu o sémantice: „Revokace za běhu: lidská oprávnění se ověřují při přijetí požadavku/zařazení do fronty; běžící ani odložený běh se nezastaví. Credential: proxy-injektované klíče sidecar odebere do 60 s, env-delivered hodnoty až restart kontejneru; smazaný credential zablokuje další běh preflightem.“
- **§8 odst. 2** nahradit „Zdroje vazeb ověřit v routine definicích, credential assignments/bindings a journalu“ zněním: „Vazba rutina→credential je odvozená podle TYPU (`credentials_required`, `credential_ref.type`, `{{ secrets.<type> }}`) a scope author crew; resolver vybírá nejnovější ACTIVE, takže „používá tento credential“ znamená „tento credential by byl vybrán“ (`resolves_to_this`). `agent_run` kroky credential vybírají za běhu přes sidecar — uvádět jako neúplnost. Journal ani `credential_audit` dnes nenesou run_id; „zaznamenané skutečné použití“ je na úrovni crew (sidecar fetch), ne rutiny.“
- **§8 Akceptace** doplnit: „Multi-crew credential musí rezolvovat pro každý crew v `credential_crews` (dnes jen první — nález V2); ‚Rotace stejné identity zachová vazby‘ = rotace in place (id se nemění), ale nový credential téhož typu vazby nepřebírá a `{{ secrets.<type> }}` na něj přejde — obojí zobrazit.“ Větu „Chyba refetche označí údaje jako neověřené“ konkretizovat: „Audit a fields sekce dnes chybu zobrazí jako prázdno (`credential-detail-sheet.tsx:376-390`) — opravit v rámci A2.“
- **§3 Inventura, řádek Oprávnění**: „Existující základ“ doplnit o „capabilities per člen (`routine.run`, `credentials:reveal`), `currentUserCapabilities`, `pages/{slug}/access/me`“.
- **§14 pořadí**: „krátká inventura a audit A1“ rozšířit na „audit A1 + opravy V1/K1 (page action honoruje `routine.run`, hlavičky jen od sidecaru) + dokumentace sémantiky revokace“ jako iterace 0/2 před O1.

## 5. Návrh iterací pro tento klastr

### Iterace 0/2 — A1 audit a opravy (před diagnostikou; ostatní iterace na autorizaci stojí)
- **Vstupy:** tento report; `pipelines_exec_capability_gate_test.go`, `pages_actions_test.go:357`, `pipelines_exec_trigger_forge_test.go` jako vzory.
- **Rozsah:** (1) `pages_actions.go:503` + `router_pages_actions.go:46` → `roleInline` + `requireRoleOrCapabilityOrForbid(... CapabilityRoutineRun ...)` (stejný tvar jako Run); aktualizovat golden `TestMutationRouteRolesMatchManifest -update-route-roles`; opravit zastaralé komentáře (N1). (2) Rozhodnout a sjednotit replay/`run_batch`/`schedules/{id}/run` (viz §6). (3) K1: `pipelines_exec.go:166-167` číst hlavičky jen když `InternalTokenCrewFromContext(ctx) != ""`. (4) docs/security: odstavec „sémantika revokace za běhu“ (S5). (5) Přidat `InvalidateCapabilityCache` poznámku do akceptačního testu revokace.
- **NEPATŘÍ:** nové role, per-routine granty, `access/me` endpointy, UI.
- **Testy:** nový tabulkový test page action (MEMBER+routine.run → 202; MEMBER bez → 403; outsider → 404), rozšíření forge testu o hlavičky (JWT → prázdné, interní crew-bound token → uloženo), `TestRunEndpoint_CapabilityGate` zelený, `TestPageAction_*` zelené, `route_authz_invariant_test`, `route_roles_manifest_test`.
- **Podmínky dokončení:** oba zz testy z §7 by (přepsané jako trvalé) prošly; CHANGELOG; CodeQL bez nálezu.
- **Handoff:** seznam cest, které `routine.run` honorují/nehonorují, s odůvodněním.

### Iterace 7 — vysvětlení efektivních oprávnění bez přepisu RBAC
- **Vstupy:** iterace 0/2 mergnutá; #2562 mergnuté (jinak konflikt v routine-card-detail/identity-header/detail-panel).
- **Rozsah:** `GET /api/v1/workspaces/{ws}/pipelines/{slug}/access/me` → `{view, run, run_reason, edit, approve, disable, replay, manage_schedules, reasons:{...}}` počítané z `canRole`, `CapabilitiesForMemberE`, `gateRoutineStatus`, `workspace_visible`; `GET /api/v1/credentials/{id}/access/me` → `{view, edit, rotate, assign(bind), reveal, reveal_blocked_by:[policy|role|capability|sealed|scope], delete}` z `credentials_reveal.go` gate L0–L3 + `credentialVisibilityFilter`. Jedna UI komponenta „Your access“ (shared pro routine detail + credential sheet), nahrazuje `roleAtLeast`/CASL odhady na těchto dvou místech; Run button v `routines-detail-panel.tsx` disabled s důvodem z `run_reason`. CLI: `crewship routine access <slug>`, `crewship credential access <id>`; gen-openapi, openapi.mdx totals, docs/cli, docs-inventory, CHANGELOG.
- **NEPATŘÍ:** per-routine granty, změna reveal gate, Members grid.
- **Testy:** Go tabulkové testy obou endpointů pro 5 rolí × {bez capability, s `routine.run`, s `credentials:reveal`} × status {active, proposed, disabled}; invariant „access/me.run == (Run vrátí ≠403)“ ověřený v jednom testu voláním obou handlerů; Vitest komponenty (loading/error/unknown odlišené); e2e průchod druhým účtem (PRD §15) ručně.
- **Podmínky dokončení:** UI nikde na těchto dvou obrazovkách nečte `role` pro rozhodnutí o tlačítku; regresní: `credential-detail-sheet*.test.tsx`, `routines-detail-panel.test.tsx` (po #2562).
- **Handoff:** které gate endpoint neumí předpovědět (L9 interactive session, fresh login, reason) a jak to UI popisuje.

### Iterace 8 — credential → závislé rutiny + dopad odebrání
- **Vstupy:** rozhodnutí §6 bod 2 (crew scope pravda); iterace 7 (sheet už má serverový access blok).
- **Rozsah:** `internal/pipeline`: exportovat `ReferencedCredentialTypes(dsl) map[type][]ref{step, kind}` (union declared + http_ref + template; `secretTypesInStep` zobecnit). `GET /api/v1/credentials/{id}/dependents` (role read; filtruje rutiny podle `workspace_visible` jen pro non-MANAGER, protože hidden = flag, ne ACL): pro každou rutinu ve workspace parse HEAD definice, match `UPPER(type)`, `resolves_to_this` = probe by vybral právě tento credential (reuse SQL resolveru s ORDER BY, vrátit id), `dynamic_agent_run: n`, `recorded: {last_used_at, last_use_source, run_attributable:false}`. Sheet: sekce „Routines that would use this“ s odkazy `/routines?slug=`, badge configured/would-resolve, přiznaná neúplnost; před Delete a před změnou scope/status zobrazit tento přehled (ne u každé editace). Opravit S3 (audit/fields error state). CLI `crewship credential dependents <id>`; docs; CHANGELOG.
- **NEPATŘÍ:** USE event z executoru (samostatná iterace 8b, +1,5 dne, vyžaduje scrub a debounce), statická analýza skriptů, „unused credential“ verdikt.
- **Testy:** Go: tabulkový test dependents (declared/http_ref/template/žádný; newest-wins; crew-scope A/B po opravě V2; hidden routine pro MEMBER skryta, pro MANAGER vidět); test, že credential bez běhu má `recorded.last_used_at=null` a `configured` neprázdné (akceptace „konfigurace ≠ použití“); Vitest: sheet error state auditu ≠ empty; refetch error dependents → „neověřeno“, ne prázdný seznam.
- **Podmínky dokončení:** `TestRunEndpoint_*`, `pipeline_credentials_gate_test.go`, `credentials_resolution_test.go` zelené; docs-inventory -strict.
- **Handoff:** seznam typů referencí, které skener nevidí (agent_run, call_pipeline transitivně — rozhodnout, zda rekurzivně).

## 6. Rozhodnutí vyžadující produktový vstup

1. **Které spouštěcí cesty má `routine.run` pokrývat?** Dnes: přímý run + slash paleta ano; page action, replay (A4!), `run_batch`, `schedules/{id}/run` ne. `run_batch` je záměrně MANAGER+ (spend). Návrh: page action + replay + schedule run ano (jsou to „spusť schválenou definici“), `run_batch` ne. Bez rozhodnutí A4 (opakování) pro operátora nefunguje.
2. **Pravda o crew scope credentialu:** `credential_crews` (UI, visibility) vs `credentials.crew_id` (resolver, sidecar boot, probe). Návrh: resolver/probe přejít na `EXISTS credential_crews` — ale to mění, komu se tajemství injektuje, proto produktové potvrzení.
3. **Chceme per-rutinový execute grant (AWX „Execute na template“)?** Odhad 4–6 dní, nová tabulka + 4 gate + UI. Doporučuji mimo 1.0; PRD příklad s Martičkou přeformulovat na workspace-wide `routine.run`.
4. **Má „operátor“ vidět hidden (`workspace_visible=false`) rutiny v dependents/paletě?** Dnes je může spustit slugem, ale paleta je nenabízí.

Technicky rozhodnuto mnou: `access/me` endpointy počítají z týchž funkcí jako handlery (ne z duplikované tabulky) — jinak vznikne třetí drift vedle CASL a tiers.ts; hlavičky invoking crew se vážou na crew-bound interní token, ne na nový allowlist.

## 7. Příloha: dočasné testy (zdroj + výstup), spuštěné příkazy

Spuštěno (popředí, 1 go test najednou):
```text
go test ./internal/api -run 'TestOppS_|TestRunEndpoint_CapabilityGate|TestPageAction_AuthorisationHasTwoHalves' -count=1 -timeout 300s -v
go test ./internal/api -run 'TestOppS_' -count=1 -timeout 300s -v
```
Soubor `internal/api/zz_opp_S_test.go` po každém běhu smazán (`git status` bez stop mimo cizí `zz_opp_R_test.go`).

### Zdroj, běh 1
```go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestOppS_PageActionIgnoresRoutineRunCapability(t *testing.T) {
	h, _, wsID, _ := newPageActionFixture(t)
	opID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER", `["chat","routine.run"]`, "opps-pageop")
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm-opps', 'crew-lookout', ?)`, opID); err != nil {
		t.Fatalf("add crew member: %v", err)
	}
	InvalidateCapabilityCache(wsID, opID)
	rr := pagesDispatch(t, h, wsID, opID, "MEMBER", pageActionID, `{"inputs":{"reason":"x"}}`, "")
	t.Logf("page action dispatch by MEMBER+routine.run: status=%d body=%s", rr.Code, rr.Body.String())
	if rr.Code != http.StatusAccepted {
		t.Errorf("PRD A1 target path: MEMBER+routine.run got %d from the page button, want 202 (direct /run admits the same caller)", rr.Code)
	}
}

func TestOppS_OperatorCannotEditDefinitionOrCredential(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedPipelineRowDef(t, h.db, wsID, "pipe-opps", "opps-probe", gateRunnableDef)
	opID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER", `["chat","routine.run"]`, "opps-op")
	InvalidateCapabilityCache(wsID, opID)

	rr := httptest.NewRecorder()
	h.Run(rr, runReqAs(t, opID, wsID, "opps-probe", "MEMBER", `{"inputs":{}}`))
	t.Logf("run by operator: %d", rr.Code)
	if rr.Code != http.StatusOK { t.Fatalf("operator run = %d, want 200; body=%s", rr.Code, rr.Body.String()) }

	rr = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/x", strings.NewReader(`{"definition":`+gateRunnableDef+`}`))
	h.Save(rr, withWorkspaceUser(req, opID, wsID, "MEMBER"))
	t.Logf("save by operator: %d %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	if rr.Code != http.StatusForbidden { t.Errorf("operator save = %d, want 403", rr.Code) }

	ch, _ := newCredHandler(t)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"name":"renamed"}`))
	req.SetPathValue("credentialId", "cred-does-not-matter")
	ch.Update(rr, withWorkspaceUser(req, opID, wsID, "MEMBER"))
	t.Logf("credential PATCH by operator: %d %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	if rr.Code != http.StatusForbidden { t.Errorf("operator credential PATCH = %d, want 403", rr.Code) }

	rr = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("runId", "run-none")
	h.ReplayRun(rr, withWorkspaceUser(req, opID, wsID, "MEMBER"))
	t.Logf("replay by operator: %d %s", rr.Code, strings.TrimSpace(rr.Body.String()))
	if rr.Code != http.StatusForbidden { t.Errorf("operator replay = %d, want 403", rr.Code) }
}

func TestOppS_RunHeaderForgesInvokingCrew(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineRowDef(t, h.db, wsID, "pipe-opps-forge", "opps-forge", gateRunnableDef)
	opID := seedMemberWithCapabilities(t, h.db, wsID, "MEMBER", `["chat","routine.run"]`, "opps-forge")
	InvalidateCapabilityCache(wsID, opID)

	req := runReqAs(t, opID, wsID, "opps-forge", "MEMBER", `{"inputs":{}}`)
	req.Header.Set("X-Crewship-Invoking-Crew", "crew-i-do-not-belong-to")
	req.Header.Set("X-Crewship-Invoking-Agent", "agent-forged")
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	if rr.Code != http.StatusOK { t.Fatalf("run = %d; body=%s", rr.Code, rr.Body.String()) }
	var crew, agent string
	if err := h.db.QueryRow(`SELECT COALESCE(invoking_crew_id,''), COALESCE(invoking_agent_id,'') FROM pipeline_runs WHERE pipeline_id = 'pipe-opps-forge'`).Scan(&crew, &agent); err != nil {
		t.Fatalf("read run row: %v", err)
	}
	t.Logf("persisted provenance: invoking_crew_id=%q invoking_agent_id=%q", crew, agent)
	if crew != "" || agent != "" {
		t.Errorf("a JWT caller's header was persisted as provenance: crew=%q agent=%q", crew, agent)
	}
}
```

### Výstup, běh 1
```text
--- PASS: TestPageAction_AuthorisationHasTwoHalves (4.58s)
--- PASS: TestRunEndpoint_CapabilityGate (1.11s)
    --- PASS: .../member_with_routine.run_runs (0.11s)
    --- PASS: .../viewer_with_routine.run_runs (0.13s)
    --- PASS: .../viewer_without_it_is_refused (0.09s)
    zz_opp_S_test.go:26: page action dispatch by MEMBER+routine.run: status=403 body={"detail":"Forbidden","instance":"/api/v1/pages/fleet-201/panels/sluzby/actions/restart-api","status":403,...}
    zz_opp_S_test.go:28: PRD A1 target path: MEMBER+routine.run got 403 from the page button, want 202 (direct /run admits the same caller)
--- FAIL: TestOppS_PageActionIgnoresRoutineRunCapability (0.16s)
    zz_opp_S_test.go:43: run by operator: 200
    zz_opp_S_test.go:52: save by operator: 403 {"error":"MANAGER+ role required to save routines"}
    zz_opp_S_test.go:63: credential PATCH by operator: 403 {"error":"Forbidden"}
    zz_opp_S_test.go:73: replay by operator: 403 {"detail":"Forbidden",...}
--- PASS: TestOppS_OperatorCannotEditDefinitionOrCredential (0.24s)
    zz_opp_S_test.go:101: persisted provenance: invoking_crew_id="crew-i-do-not-belong-to" invoking_agent_id="agent-forged"
    zz_opp_S_test.go:103: a JWT caller's header was persisted as provenance: crew="crew-i-do-not-belong-to" agent="agent-forged"
--- FAIL: TestOppS_RunHeaderForgesInvokingCrew (0.26s)
FAIL	github.com/crewship-ai/crewship/internal/api	6.930s
```

### Zdroj, běh 2
```go
package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestOppS_MultiCrewCredentialResolvesForSecondCrew(t *testing.T) {
	h, ownerID, wsID := newPipelineHandlerForCRUDTest(t)
	for _, c := range []string{"crew-A", "crew-B"} {
		if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`, c, wsID, c, c); err != nil {
			t.Fatalf("insert crew: %v", err)
		}
	}
	// Mirror what PATCH crew_ids=[A,B] writes: scope=CREW, crew_id=A (first), two junction rows.
	if _, err := h.db.Exec(`
		INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, crew_id, status, created_by, created_at, updated_at)
		VALUES ('cred-ab', ?, 'shared', 'enc', 'API_KEY', 'GITHUB', 'CREW', 'crew-A', 'ACTIVE', ?, datetime('now'), datetime('now'))`, wsID, ownerID); err != nil {
		t.Fatalf("insert credential: %v", err)
	}
	for _, c := range []string{"crew-A", "crew-B"} {
		if _, err := h.db.Exec(`INSERT INTO credential_crews (credential_id, crew_id) VALUES ('cred-ab', ?)`, c); err != nil {
			t.Fatalf("insert junction: %v", err)
		}
	}
	probe := pipeline.NewVaultCredentialProbe(h.db)
	for _, c := range []string{"crew-A", "crew-B"} {
		ok, err := probe(t.Context(), pipeline.RunScope{WorkspaceID: wsID, AuthorCrewID: c}, "API_KEY")
		t.Logf("probe API_KEY for author crew %s: ok=%v err=%v", c, ok, err)
		if err != nil || !ok {
			t.Errorf("routine authored by %s cannot resolve a credential the UI shows as scoped to it (ok=%v err=%v)", c, ok, err)
		}
	}
}
```

### Výstup, běh 2
```text
    zz_opp_S_test.go:35: probe API_KEY for author crew crew-A: ok=true err=<nil>
    zz_opp_S_test.go:35: probe API_KEY for author crew crew-B: ok=false err=<nil>
    zz_opp_S_test.go:37: routine authored by crew-B cannot resolve a credential the UI shows as scoped to it (ok=false err=<nil>)
--- FAIL: TestOppS_MultiCrewCredentialResolvesForSecondCrew (7.21s)
FAIL	github.com/crewship-ai/crewship/internal/api	7.375s
```

### Další příkazy (jen čtení)
`gh pr view 2562/2556/2558 --json files`, `git fetch origin pull/2562/head:refs/opp/pr2562` + `git diff main...refs/opp/pr2562 -- components/features/routines/...` (ref následně smazán `git update-ref -d`), grep/sed nad `internal/api`, `internal/pipeline`, `internal/sidecar`, `internal/policy`, `lib/permissions`, `hooks`, `components/features/{credentials,routines,pages}`, `docs`.
