# Create-surface parity audit

> **Snapshot, not a live report.** Audited **2026-08-23**; extracted **2026-08-29**
> from commit **8d1b1b1f** out of `components/features/design/`, which was deleted
> in the same commit that created this file. Nothing regenerates it. Every count
> below describes the tree as it stood on the audit date — check a row against the
> source before acting on it.

## 1. Provenance

`/design` was a page inside the product: a create-surface unification proposal that
argued its case by rendering the real surfaces, next to the ledger of what it found.
It carried no data, no API and no CLI command, and its own header said to delete it
along with `components/features/design/` once the audit had no rows left to fix.

It was deleted with rows still open. This file is what it knew, in a form that does
not ship to users and does not need a running frontend to read:

- **§2** the twelve doors, before and after — the argument for one shared shell
- **§3** token drift, the same disease one level down
- **§4** the parity ledger — what the API and CLI can do against what the UI reaches
- **§5** the sweep — the same question measured once across the whole surface
- **§6** the surface specimens — the shapes the proposal specified, which code
  comments throughout `components/features/` still cite by section number

The consoles half of this argument — `/settings` and `/admin` — was reworked
separately and is not in scope here; §4 covers the create surfaces only.

## 2. Shell divergence — the twelve doors

What every SubBar action opened when the audit was taken, and what it became. The BEFORE columns are the argument for a shared shell.

| Page | Action | Component | Shell | Width | Primary | ⌘↵ | Phone | Proposed |
|---|---|---|---|---|---|---|---|---|
| Issues | New Issue | `features/orchestration/create-issue-modal.tsx` | radix | 640 | `raw <button> h-7 bg-primary` | yes | no | md · reference |
| Issues | New Project | `features/orchestration/create-project-modal.tsx` | radix | 720 | `raw <button> h-7 bg-primary` | yes | no | md |
| Routines | New routine | `features/routines/routine-create-dialog.tsx` | hand-rolled | 576 → 672 → 768 | `<Button size=sm>` | no | no | lg · fixed |
| Routines | Import | `features/routines/routines-layout.tsx:392` | hand-rolled | 672 | `<Button size=sm>` | no | no | sm |
| Pages | New page | `features/pages/page-editor.tsx:939` | hand-rolled-blur | 1100 | `<Button size=sm>` | no | no | xl |
| Pages | Import | `features/pages/page-import-dialog.tsx:128` | hand-rolled-blur | 560 | `<Button size=sm>` | no | no | sm |
| Crews | New crew | `features/crews/create-crew-dialog.tsx` | radix | 680 → 940 | `raw <button> "✓ Create crew"` | yes | no | lg · fixed |
| Crews | New agent | `features/crews/create-agent/create-agent-dialog.tsx` | radix | 640 | `raw <button>` | no | no | md → shipped lg |
| Skills | Import | `skills/import-dialog.tsx` | radix | 512 | `<Button> default h-9` | no | no | sm → shipped md |
| Credentials | Add secret | `features/credentials/add-secret-sheet.tsx` | radix | 680 | `wizard-owned` | no | yes | md |
| Credentials | Connect via OAuth | `features/credentials/connect-oauth-dialog.tsx` | radix | 448 | `form-owned` | no | no | sm |
| Integrations | Add integration | `features/integrations/add-integration-dialog.tsx` | radix | 672 | `none — picking closes it` | no | no | xl |

**Totals, computed from the rows above.** 12 doors · 3 shell implementations · 11 distinct widths · 2 overlay treatments · 8 distinct primary-button constructions · 3 of 12 supported ⌘↵ · 1 of 12 handled a phone.

### 2.1 What landed

- **Issues → New Issue** (2026-08-23) — The reference. Its shape became the shell; its pills, popovers and Create-more switch lifted across unchanged.
- **Issues → New Project** (2026-08-23) — Pill row moved below the description into the slot the shell has for it — a visible reorder, flagged rather than hidden.
- **Routines → New routine** (2026-08-23) — Pinned at lg. CodeMirror, the YAML/JSON conversion and the /test_run → saveToken → /save chain survived intact; the insides came onto the kit afterwards.
- **Routines → Import** (2026-08-23) — Now sm. The collision rule is a control instead of a paragraph.
- **Pages → New page** (2026-08-23) — The blur and the hand-rolled overlay are gone, and it gained a focus trap it never had. First real user of the dirty guard.
- **Pages → Import** (2026-08-23) — Keeps the unresolved-reference worklist, and is the first real user of CreateSurfaceRefusal's field list.
- **Crews → New crew** (2026-08-23) — Width pinned at lg. Later rebuilt to the four-step shape /design proposed: Runtime folded into Container, egress open by default, MCP gone.
- **Crews → New agent** (2026-08-23) — Landed at lg, not the md this row proposed. All sixteen fields of createAgentRequest verified present against the Go struct after the move.
- **Skills → Import** (2026-08-23) — Landed at md, not the sm this row proposed. Three sources behind one door, on the shell's Choice instead of a Radix tab strip.
- **Credentials → Add secret** (2026-08-23) — The one door that already handled a phone. Its full-screen takeover became the shared bottom sheet, and its DetailCards became the kit's sections.
- **Credentials → Connect via OAuth** (2026-08-23) — OAuthForm is shared with the MCP picker, so it publishes its primary rather than surrendering it — the picker keeps its inline row, this door puts Authorize in the footer.
- **Integrations → Add integration** (2026-08-23) — Kind → service kept. Renders no primary, which the shell now allows rather than treating as a contradiction.

All 12 of 12 migrated. The BEFORE rows are kept deliberately: an audit that rewrites itself as the work lands leaves the page asserting a conclusion with nothing behind it.

## 3. Token drift

Counted across `components/**` and `app/**` on 2026-08-23. Not about the modals — the same disease one level down. Any unification that stops at the dialog shell leaves these in place.

| What | Detail | Count | Fix |
|---|---|---|---|
| Hairline borders | border-white/[0.04 · 0.05 · 0.06 · 0.07 · 0.08 · 0.1 · 0.10 · 0.12 · 0.15 · 0.20] | 10 alphas · 260 uses | border-hairline (a --border mix, so it works in light mode too) |
| Modal shells outside the three primitives | fixed inset-0 z-50 written by hand in feature components | 9 files | CreateSurface for creates, Sheet for inspectors, AlertDialog for confirms |
| Typography scales | .type-* (app) and .type-page-* (Pages) describe the same four roles | 2 scales + raw text-[11.5px] / text-[12.5px] / text-[15px] | one scale; .type-page-* folds into .type-* |
| Raw <button> in feature code | hand-styled buttons next to the shared <Button> in the same files | 477 raw vs 419 shared | shared variants; raw <button> only where there is genuinely no variant |

## 4. Parity ledger

65 rows across 22 surfaces: 22 blocker, 38 gap, 2 deferred, 3 fine. 47 of them are `ui: none` — not reachable anywhere in the web UI. Every row was read out of the source and carries its reference; a row without one is a claim nobody checked.

`ui` states: **create** = the create surface itself can set it · **detail** = only after the thing exists, on its own screen · **none** = not reachable anywhere. `detail` is frequently the right answer, and becomes a defect only when the thing cannot be created in a working state without it.

### 4.1 Issues → New issue

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Sub-issue (parent) | both | `issue_handler_create.go:41` | `--parent-issue-id` | none | **gap** | The API fences it and guards cycles, and the UI RENDERS sub-issue trees it cannot create. Breaking an epic apart means dropping to the CLI. |
| Due date | both | `issue_handler_create.go:38` | `--due-date` | detail | **gap** | Every issue filed from the UI starts undated, so planning is a second editing pass over issues you just typed. |
| Estimate | both | `issue_handler_create.go:40` | `--estimate` | detail | **gap** | Same shape as due date — accepted at create by the API, missing from the modal. |
| Milestone | both | `issue_handler_create.go:42` | `--milestone-id` | detail | **gap** | Accepted at create; the modal has no control, so milestone assignment is always a follow-up edit. |
| Routine inputs | api | `issue_handler_create.go:50` | — | none | **gap** | You can bind a routine to an issue from either client but never parameterise it, so it always fires with {}. No CLI flag either. |
| Assign to a person | api | `issue_handler_create.go:36` | — | none | **gap** | assignee_type accepts "user"; the CLI hard-rejects it and both pickers only fetch /agents. A human cannot be assigned an issue from any client. |

### 4.2 Issues → New project

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Summary field is discarded | api | `project_handler.go:182-193` | — | create | **blocker** | The modal posts `summary` and `labels`; the create struct has neither, readJSON does not reject unknown fields, and there is no column. You type it, you get a success toast, it is gone. |
| Milestones | both | `milestone_handler.go:108` | `milestone create` | none | **blocker** | Full CRUD in the API and the CLI; the UI only reads them, and the modal's “+ Add milestone” button has no onClick at all. Worse than the audit thought: MilestoneHandler.Create 404s unless the project already exists, so the control could never have worked from a create modal — and there is no post-create surface either. |
| Human as project lead | api | `project_handler.go:189` | `--lead-type` | none | **gap** | lead_type accepts "user"; the picker lists agents only. |

_Closed in the working tree, with a test that fails without the fix. The row stays: a ledger that deletes what it repaired cannot be read as a record of what was wrong._

- **Summary field is discarded** (closed 2026-08-23) — Both controls removed rather than wired — there is no `summary` column and no `project_labels` table to wire them to. A standing test now transcribes the Go request struct and fails on any key the modal sends that the handler does not bind. Whether projects SHOULD have a summary or labels is handed back as a product decision.
- **Milestones** (closed 2026-08-23) — The dead section is gone, so the gap is visible instead of fake. Creating a milestone is still impossible anywhere in the web UI — project-sidebar.tsx has 697 lines of working milestone CRUD and is imported by nothing, while /orchestration/projects/[projectId] redirects to /issues.

### 4.3 Routines → New routine

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Acting agent (author_agent_id) | both | `pipelines_crud.go:567` | `--author-agent` | none | **blocker** | Without it, a routine with a `crewship`/issue.comment step is refused at save with 422 — an entire verb is UI-unauthorable, and the error names a flag the user cannot reach. |
| Change summary on save | both | `pipelines_crud.go:594` | `--change-summary` | none | **gap** | The versions tab DISPLAYS change summaries. Nothing in the UI can write one, so every UI-authored version is unlabelled. |
| The whole routine DSL | both | `schemas/routine.v1.json:9-144` | — | create | **fine** | 23 top-level keys and ~18 step options, all typeable in the advanced editor with schema-driven completion. No form controls, and that is the right call for a DSL. |

### 4.4 Routines → Import

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Import a bundle at all | both | `pipelines_crud.go:289-292` | `routine import --crew` | none | **blocker** | Import requires author_crew_id, export deliberately omits it, and the dialog posts the pasted JSON verbatim with no crew picker. Export → Import in the UI always 400s. |
| Inlined scripts materialised | cli | `cmd_routine_extra.go:546-557` | `routine import` | none | **gap** | The endpoint has no script handling, so a script-bearing bundle imported through the UI lands with `type: script` steps that have no files behind them. |

### 4.5 Routines → Schedules

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Wake gate (wake_pipeline, inputs, fail_closed) | both | `pipeline_schedules.go:124-131` | `--wake-slug --wake-inputs --fail-closed` | none | **gap** | The mechanism that stops a 5-minute cron burning tokens when there is nothing to do. The UI renders a read-only badge for it and offers nowhere to click. |
| Catch-up policy after an outage | both | `pipeline_schedules.go:136` | `--catchup` | none | **gap** | Decides whether an hourly routine fires once or replays 40 backlogged occurrences. Exactly what you want to change after a bad night. |
| Circuit breaker (max_consecutive_failures) | both | `pipeline_schedules.go:141` | `--max-failures` | none | **gap** | Zero hits anywhere in the web tree. |
| Pin a trigger to a version | both | `pipeline_schedules.go:115` | `--pin-version` | none | **gap** | Typed in both hooks and set by neither, so a production schedule cannot be pinned and every routine edit silently changes what fires at 03:00. |

### 4.6 Routines → Webhooks

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Inputs template | both | `pipeline_webhooks.go:130` | `--inputs-template` | none | **gap** | Hardcoded to {} in the UI, so a UI-created webhook can only pass the raw body through — no constant injection, no remapping. |

### 4.7 Crews → New crew

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| “Never” auto-stop means 4 hours | both | `submit.ts:30 + crews_create.go:297-301` | `--ttl 0` | create | **blocker** | The wizard omits container_ttl_hours when the chip says “Never”, and the server reads absent as its 4-hour default. The one chip that promises not to stop your crew is the one that stops it. The CLI got this right. |
| Sidecar services (Postgres, Redis…) | api | `crews_create.go:74` | — | none | **gap** | Reachable from neither the UI nor the CLI — `crew services` only lists what someone else set. The only field in this audit with no operator surface at all. |
| Private-endpoint egress | both | `crews_create.go:64` | `--allow-private-endpoints` | detail | **deferred** | Deliberately absent from the wizard and documented in cli-parity.test.ts — an ADMIN-tier egress grant does not belong behind a self-serve MANAGER wizard. |

_Closed in the working tree, with a test that fails without the fix. The row stays: a ledger that deletes what it repaired cannot be read as a record of what was wrong._

- **“Never” auto-stop means 4 hours** (closed 2026-08-23) — submit.ts now always sends the field, and “Never” sends 0. The suite contained a test ASSERTING the omission — it had encoded the bug as correct behaviour, and had to be inverted. The step's CLI hint said “(no --ttl)”, which after the fix would have printed a command producing a different crew; it now reads --ttl 0.

### 4.8 Crews → New agent

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Every field of createAgentRequest | both | `agents_create.go:15-36` | `agent create` | create | **fine** | All 16 request fields are in the dialog. This is the one surface already at struct parity — which is why it was the right one to keep. |
| Give the agent a credential | both | `agents_create.go:280-284` | `credential assign` | detail | **blocker** | The handler's own comment claims the dialog prompts for one after the 201. It does not. A fresh agent has no API key and nothing on the create path says so — you find out when the first run fails. |
| Hire an ephemeral agent | both | `agents_hire.go:45-51` | `crewship hire --ttl --reason --parent-lead` | none | **gap** | The UI can only approve or rehire what the CLI created, and its rehire hardcodes 60 minutes and a canned reason. There is no way to initiate a hire from the browser. |
| Workspace-wide (crewless) agent | api | `agents_create.go:18` | — | none | **gap** | requiresCrew is a literal `true` in the dialog while the API accepts crew_id: null for every non-LEAD role. The warning banner still tells you to pick a “Coordinator” role the select no longer offers. |
| Skills, MCP servers, channels | both | `router_crews.go:347 / :266 / router_orchestration.go:316` | `skill assign · agent mcp add` | detail | **deferred** | Correctly deferred: each is a list you curate over the agent's life, and all three already have working post-create surfaces on the agent canvas. |
| Granting one agent revokes it from the rest | both | `agent_config.go:1234-1246 · :1291-1293` | `integration access` | detail | **blocker** | A server with zero bindings is handed to EVERY agent. The moment one agent binds it, every agent without its own binding loses it — so the first grant is a silent workspace-wide revocation. No surface warns about this, and it is the single most surprising rule in the integration model. |
| Per-agent tool allowlist | cli | `integration_resolve.go:161-163 · cmd_agent_mcp.go:194` | `agent mcp update --tools` | none | **gap** | Re-filed. Earlier recorded as a UI gap; it is worse than that. config_override_json is written by the API, read only by the read-only resolver where it overwrites config_json wholesale, and never parsed for `tools`. The CLI help states the runtime filters on it. It does not. The flag writes a string that is stored, echoed back and ignored. |

_Closed in the working tree, with a test that fails without the fix. The row stays: a ledger that deletes what it repaired cannot be read as a record of what was wrong._

- **Granting one agent revokes it from the rest** (closed 2026-08-27) — The audience is a stored column (default_access: all | bound-only) on both server tables, so a binding is purely additive and cannot change what any other agent resolves. Fixed in BOTH cascades — the read-only resolver and resolveAgentMCPServers in agent_config.go, which is what the container actually gets; the runtime copy was worse, its binding count was not workspace-scoped, so a binding in a DIFFERENT workspace could revoke a server. The migration backfills every server that already carries a binding to bound-only, so nobody's effective access moves on upgrade — verified against a real database, before and after are identical.

### 4.9 Credentials → Add secret

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| ENDPOINT_URL credentials | both | `credentials_types.go:59-70` | `--type ENDPOINT_URL` | none | **blocker** | The type is inferred from the credential's NAME and no wizard shape maps to it — yet the Keeper panel asks the operator to pick an ENDPOINT_URL credential. A dead end for anyone pointing Crewship at a local Ollama or LiteLLM. |
| Edit custom fields after creation | both | `credential_fields.go:150-155` | `credential field set` | create | **blocker** | The wizard writes them once; the detail sheet only lists them. Fixing a typo'd AWS `region` means deleting the credential and starting over. |
| Re-slot or unbind | both | `credential_bindings.go:107-113` | `credential bind --slot` | create | **gap** | Bindings are create-once, AGENT scope has no UI at all, and DELETE /credentials/bindings/{id} has no caller anywhere in the frontend. |
| Per-agent env var, priority, TTL lease | both | `agent_credentials.go:216-221` | `credential assign --env-var-name --priority --ttl` | none | **gap** | env_var_name is auto-derived, priority is hardcoded 0, and short-lived leases have no control — so failover ordering and “give this agent the prod token for two hours” are CLI-only. |
| Rotation endpoint + cancel | both | `credential_rotation.go:65-67` | `credential rotate --auth-token --header` | none | **gap** | The dialog can rotate a value but cannot set the endpoint that performs it, and a rotation in flight cannot be cancelled from the browser. |

### 4.10 Skills → Import

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Path globs on a repo import | both | `skills_bulk_import.go:37` | `--paths` | none | **gap** | Importing from a monorepo pulls everything or nothing. |
| Delete a workspace skill | both | `router_crews.go:502` | `skill delete` | none | **gap** | The detail panel only uninstalls from an agent. A bad import cannot be removed from the workspace in the browser. |
| Assign to a whole crew | cli | `cmd_skill.go:690-692` | `--to-crew` | none | **gap** | The UI offers per-agent checkboxes only, so a ten-agent crew is ten clicks. |

### 4.11 Integrations → Add integration

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Workspace-scoped MCP server | both | `workspace_integrations.go:57-67` | `integration add` | none | **blocker** | Every UI create posts to /crews/{id}/integrations. One server shared by every crew means the CLI, or N duplicates kept in sync by hand. |
| HTTP headers / auth (config_json) | api | `crew_integrations_crud.go:24` | — | none | **blocker** | Read by the adapter, written by neither the CLI nor the UI. A remote MCP server behind an API-gateway header — the commonest shape of a hosted server — is unconfigurable outside a manifest. |
| Per-agent tool allowlist | both | `agent_bindings.go:36` | `agent mcp update --tools` | none | **gap** | Crew-level tool switches exist; narrowing one agent to a subset does not. |
| api_key / basic MCP auth | both | `agent_bindings.go:32-34` | `--cred-type --cred-header --env-var` | none | **gap** | cred_type is hardcoded to "bearer" in the UI, so the other two auth shapes and stdio credential injection are unreachable. |
| Notification events + raw shoutrrr URL | both | `notification_channels.go:51 / :60` | `--events --url` | none | **gap** | `events` is typed in the hook and rendered by no control, so every UI channel fires on everything. Categories and min-priority are create-only — the UI patches nothing but `enabled`. |
| Connector catalogue | both | `connectors_handler.go:110-112` | `connector install --field --crew` | none | **blocker** | ConnectorCatalog and ConnectorConnectSheet are built and tested — and mounted on no page. The whole manifest-driven connector flow is dead on arrival in the browser. |
| “Test” on the Add-MCP wizard | both | `add-mcp-wizard.tsx:213-223 · integration_test_connection.go:42/:54` | `integration crew test <crew> <id>` | detail | **blocker** | A setTimeout(…, 400) that returns a hard-coded “Configuration looks valid.” No network call of any kind. If anyone reports “test passed but the server is broken”, this is why. |
| Deleting a server leaves its tool rows behind | api | `migrate.go:1107-1117 · crew_integrations_crud.go:309/:333` | — | none | **gap** | mcp_tool_bindings has no foreign key to crew_mcp_servers, so no cascade. Deletion clears agent bindings and the server row and never the tool bindings — every deleted integration orphans its rows permanently. Not a UI gap; found while looking at one. |
| Discover a server's tools | api | `mcp_tool_bindings.go:174-295 · cmd_integration_tools.go` | `integration tools refresh --tool/--tools-file (records, does not discover)` | none | **blocker** | Filed first as “the UI is behind the CLI”. That was wrong, and the correction matters: `POST …/tools/refresh` UPSERTS a client-supplied tools[] and performs no discovery. #1884 gave the CLI --tool/--tools-file so it can at least carry a catalogue you already have, but neither surface discovers anything. The only code that speaks tools/list is the sidecar's gateway, whose catalogue never leaves the container — and stdio servers are never discovered at all. This is backend work, not a UI gap. |
| Add a raw MCP server at all | cli | `app/(dashboard)/integrations/page.tsx:69 · lib/feature-flags.ts:25` | `integration add` | none | **blocker** | The MCP wizard, detail sheet and expanded panel only render behind NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS, which is off by default. In the shipped build there is NO way to add an MCP server from the browser — only the CLI. The doc comments still describe the flag-off path as a “coming soon placeholder”; it actually renders the full Composio UI. |
| Per-tool switches actually block a tool | api | `agent_config.go:1322 · mcp_gateway.go:398-455` | — | detail | **gap** | mcp_tool_bindings reaches the agent as TEXT in the [CONNECTED INTEGRATIONS] prompt block and nothing else. Neither .mcp.json nor the gateway's CallTool consults it, so a tool switched off in the UI is still callable — the switch is a suggestion to the model, not a control. |

_Closed in the working tree, with a test that fails without the fix. The row stays: a ledger that deletes what it repaired cannot be read as a record of what was wrong._

- **“Test” on the Add-MCP wizard** (closed 2026-08-26) — Removed, not wired. Both real probes load transport/endpoint/command out of the database, so neither can be aimed at a draft the wizard has not written yet — wiring one would have meant inventing a draft-test endpoint, which is a backend decision, not a repair to this control. The step now names the two surfaces that do test: “Test connection” on the server's row and `crewship integration crew test`. The ui state moves create → detail: the capability is deferred to after create, which is where it can actually run.
- **Discover a server's tools** (closed 2026-08-23) — Copy only, deliberately. The button keeps re-reading the database (worth having — it picks up another admin's toggles) and stops calling itself “Refresh”; the empty state now says discovery is not wired instead of instructing you to click a button that cannot populate it. Wiring the endpoint would have traded a working re-read for a guaranteed no-op plus a 403 for every non-ADMIN who can view the tab.

### 4.12 Pages → New page

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Put data into a panel | both | `router_pages.go:63` | `page set <slug>/<panel> --data -` | none | **blocker** | Every option in every payload schema is unreachable from the browser. You can author the frame and never fill it — a panel is fed only by a producer routine, the CLI, or an inbound webhook. |
| Give a page to a crew | both | `pages_handler.go:315` | `page create --owner crew/<slug>` | none | **gap** | The editor deliberately never sends `owner`, so a UI-authored page always belongs to the person who made it. |
| The rest of the page document | both | `pages_handler.go:122-185` | `page create --file` | create | **fine** | Panels, SLAs, spans, tabs, actions, wake gates, grants, public links, webhooks, versions, rollback and export are all reachable. Spec-side parity is near-total; it is the payload side that is empty. |

### 4.13 Integrations → Composio

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| The default connector silently drops other servers | api | `agent_config.go:1254-1256` | — | none | **blocker** | With COMPOSIO_DEFAULT_CONNECTOR on, every non-Composio workspace and crew MCP server is dropped from the resolved runtime config without the rows being deleted — and GET …/integrations/resolved does not model this, so the screen that exists to answer “what will this agent get” shows servers the container will not receive. |

### 4.14 Everywhere

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Components built, tested, and mounted nowhere | both | `project-sidebar.tsx · connectors/ · assign-credential-dialog.tsx` | — | none | **blocker** | Three so far, found by three separate audits: 697 lines of milestone CRUD, the whole connector catalogue, and the credential-assignment dialog that would have exposed priority. Each is finished work that no route imports — which means the gap is not “nobody built it” but “nobody hooked it up”, and that is a much cheaper fix than it looks. |

### 4.15 Chat

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Message reactions | api | `router_orchestration.go:457` | — | create | **blocker** | The picker works and writes to localStorage. Reactions never reach the server, so two teammates looking at the same conversation see different ones forever. |
| A just-streamed turn has no server id | api | `use-chat.ts · stores/feedback-store.ts` | — | create | **blocker** | A streaming assistant turn gets a client-side uuid() that is never reconciled with the persisted message id, and the reactions handler validates nothing — so a reaction on the turn you are watching POSTs under a UUID, returns 204, and is orphaned. After a refresh the turn has a different id and the reaction is gone. `feedback-store.ts` posts `message_id: turnId` and has the identical gap, so thumbs-up on a live turn is lost the same way. Closing it needs the `done` WS frame to carry the persisted id — a server change. |
| Steer a running agent | both | `router_orchestration.go:523` | `chat steer` | none | **gap** | You can redirect a running agent from a terminal but not from the chat window you are watching it in. |

_Closed in the working tree, with a test that fails without the fix. The row stays: a ledger that deletes what it repaired cannot be read as a record of what was wrong._

- **Message reactions** (closed 2026-08-23) — PARTIALLY. The store is now an optimistic layer over the three real endpoints (the audit said two — there is also a GET, without which a teammate's reaction can never be shown at all), and the value grew from a bare count to {count, mine} because clicking a teammate's 👍 would otherwise decrement THEIR reaction. localStorage is dropped, not migrated: the old record never stored the chat id the endpoint requires nor who reacted, so replaying it would mean attributing strangers' reactions to whoever is logged in. Works end-to-end on reloaded history. NOT on a turn you just watched stream — see the next row.

### 4.16 Missions → Timeline

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Fork a checkpoint | both | `router_orchestration.go:257` | `checkpoint fork` | none | **blocker** | The fork dialog posts to /missions/{id}/fork, which is not a registered route. The only fork is /checkpoints/{id}/fork — the button is broken, not missing. It read the 404 as toast.info(“Not yet wired to backend”), so the failure presented as an unfinished feature. |

_Closed in the working tree, with a test that fails without the fix. The row stays: a ledger that deletes what it repaired cannot be read as a record of what was wrong._

- **Fork a checkpoint** (closed 2026-08-23) — Posts to the checkpoint route with the label, shows the server's error inline and keeps your typed label for the retry. A second defect underneath it: the checkpoint id lives in the journal entry's `refs`, not `payload`, so the timeline was always sending the journal row id — fixing only the URL would have swapped a 404-on-route for a 404-on-id. Restore was sending the same wrong id and had therefore never worked either.

### 4.17 Crews → Runtime

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Start / stop a container | both | `router_crews.go:175` | `crew start · crew stop` | none | **gap** | The Docker tab shows the containers and offers no power switch. |
| See and revoke exposed ports | both | `router_orchestration.go:914` | `expose list · expose revoke` | none | **gap** | Agents can open externally reachable URLs. Nobody can see or close them from the browser — this one is security-relevant. |

### 4.18 Runs

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Why is this run held? | both | `router_orchestration.go:200` | `capacity` | none | **gap** | The UI already says “Waiting for host capacity” and never fetches the endpoint that says which gate is holding. A held run is indistinguishable from a hung one. |

### 4.19 Routines → Operations

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Trust grants, state, replay, signals, pending runs | both | `router_pipelines.go:76-78 · :174` | `routine trust · state · step-run · bulk_replay · signal` | none | **gap** | The whole operational half of routines. A run parked on wait:event has no browser-side release, and the error-fingerprint comment promises a bulk-replay view that was never built. |

### 4.20 Admin

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Lifecycle hooks registry | both | `router_orchestration.go:544-549` | `hooks list/add/enable` | none | **gap** | Arbitrary shell and webhook code fires on platform events like pre_tool_call. The registry is invisible and unkillable from the browser. |
| Feature flags and instance settings | both | `router_orchestration.go:182-193` | `feature-flag · instance settings` | none | **gap** | lib/feature-flags in the frontend is a hardcoded constants module, not this API. Flags cannot be flipped without a shell. |

### 4.21 Inbox

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| See the diff you are approving | both | `router_orchestration.go:597` | `consolidate diff · explain` | none | **blocker** | The inbox lets you approve or reject a memory-consolidation proposal without ever showing the byte-level diff or the evidence behind it. |

### 4.22 Issues → Triage

| Capability | Where | Ref | CLI | UI | Severity | Note |
|---|---|---|---|---|---|---|
| Triage rules and recurring issues | both | `router_orchestration.go:162-172` | `triage · recurring` | none | **gap** | Incoming issues are routed by rules nobody can read, and issues appear on a schedule with no screen explaining where they came from. |

## 5. Sweep

Walked from the built CLI binary (depth 3, no truncation) and from the router registrations, then cross-referenced against every `/api/v1/...` literal in `app/ components/ lib/ hooks/ stores/`, with each non-match hand-verified against the dynamic URL builders.

| Measure | Count |
|---|---|
| CLI leaf commands | 636 |
| CLI top-level commands | 109 |
| Route registrations | 654 |
| Unique paths | 507 |
| `/api/v1/internal/*` sidecar-IPC paths (excluded) | 62 |
| Candidate paths — ones a screen could exist for | 425 |
| **Confirmed to have no web UI at all** | 90 |
| Endpoints the UI has and the CLI does not | 2 |

**Method caveat, recorded because it changes how much weight the number carries.** The read-side sweep was received complete and spot-verified; three commissioned sub-audits of the mutation side did not return, so mutation-side coverage is the auditor's own direct reading. Treat 90 as a floor.

Excluded from `noWebUi`: the 62 `/api/v1/internal/*` sidecar-IPC routes, the NextAuth shim, CLI device pairing, health, the agent-daemon callback and the unauthenticated external-callback token surfaces. Those are not screens anyone was ever going to build.

## 6. Surface specimens

What the proposal specified for each door. These are the sections code comments
cite: when a component says it is built "the way §6.3 asks", this is the claim
it is answering to. The specimens were live React on the deleted page; what
survives here is the specification, not the rendering.

### 6.1 Issues → New issue

The reference door. Its shape became the shared shell — pill row for the
metadata that is a choice, popovers for the ones with too many options to
show, a Create-more switch in the footer. It is a compose surface used dozens
of times a day, which is the case where a popover earns its hiding.

### 6.2 Issues → New project

The opposite case, and specified deliberately against §6.1: a project is
created rarely and deliberately, so status, priority and the two dates are
**chip rows and date fields in the body**, not pills and popovers. A popover
hides both the options and the fact that a choice was available. The pill row
keeps only the control that is genuinely a search — the lead picker, because a
workspace can have hundreds of agents and a chip row of hundreds is not a chip
row. Milestones are a **read-only notice** saying out loud that they cannot be
created here or anywhere else in the web UI, and naming the CLI command that
can (see §4.2, which is why).

### 6.3 Crews → New crew · Container step

**Base image and preinstalled tooling lead the step as sections; everything
else folds away.** The step carries five aspects — image, tooling, language
runtimes, network, sizing — and a tab strip is chrome a create surface has
nowhere to put. Two decisions on the page, three one click deep.

Two consequences the components implement:

- The image is **one summary row saying what the crew runs on, with "Change"
  on the right, and the catalogue as a PANEL the surface swaps to** — header,
  body and footer replaced, back arrow returns, the same shape as the icon
  picker. That is what lets a create step hold a nine-item catalogue without
  becoming a list of lists. The panel is owned by the create surface rather
  than by the runtime editor, so the catalogue is not drawn twice on one step;
  state still flows through `devcontainerConfig`.
- The tooling browser is **tiles**, not the table the crew Settings editor
  uses. Settings is a config editor where a row per feature carrying name,
  ref, publisher, tier and a Switch is the right density; a create step is not.

The Review step spells the image row **"Runs on" and always shows it**,
default included — a Review that says nothing about which of the nine images
won is the one place the choice cannot be checked before it is committed.

Language runtimes and the privileged flags are **folded, not dropped** — they
are settable here today, and removing them from the create path would be a
capability change wearing a redesign's clothes. Each disclosure's summary says
whether there is anything inside.

### 6.4 Crews → New crew · Lineup step

Who the crew starts with: **one grid of tiles**. No list, no picker dialog.

### 6.5 Crews → New agent

The product's largest create form — twenty fields on `AgentDraft` — rebuilt
field-for-field so what is judged is the shell, not a reduced form. One change
of substance: the advanced fields sit behind a disclosure that **names its
contents and shows the current values while closed**, instead of an "Advanced"
link that says nothing about what is inside. Nothing removed, nothing promoted.

Role is a **chip row, not a dropdown**: two short options, both visible without
opening anything, and on a phone the target is the whole chip rather than a
16px caret. Tool profile further down the same form already worked this way.

Proposed at `md`, shipped at `lg` — the proposal was made from the field list
rather than the built screen, and sixteen fields with a template browser and
four disclosures do not fit a 640px column (§2).

### 6.6 Skills → Import

Three sources behind **one door, on the shell's Choice control** rather than a
Radix tab strip. The consequence that matters: **Licensing is a disclosure
below the source, where every source has it**. The licence gate used to be
reachable from the repository source only while all three sources send
`allow_unsafe_license`, so a URL import of a skill whose licence the SPDX
scanner cannot identify was refused by the server with the one control that
would have let it through rendered on a different tab.

Proposed at `sm`, shipped at `md`: three source tiles side by side do not fit
480px.

### 6.7 Credentials → Add secret

The only door in the product that already handled a phone. Its three steps
survive unchanged; the full-screen takeover became the shared bottom sheet and
its DetailCards became the kit's sections.

### 6.8 Credentials → Connect via OAuth

The provider shortcuts are a **tile list: glyph, name, and the scopes the
provider will be asked for** — the fact a person needs before handing over
access, and the one a pill row has no room to carry. The same form is shared
with the MCP credential picker, which keeps its inline pill row; the form
therefore **publishes its primary action** rather than surrendering it, and
this door puts Authorize in the footer.

It was one of two doors still using the shared `DialogContent` default `p-6`
and its 18px title, which is why §2 counted it as looking like a different
design system rather than a different width.

### 6.9 Routines → New routine

The three entry tiles are **three routes, not one route and two greys**: each
gets its own colour and a word saying what you trade — fastest, or full
control. Two of the three used to carry `accent="slate"` and no meta, so the
picker read as "the blue one, and some other options".

Pinned at `lg` (it stepped 576 → 672 → 768). CodeMirror, the YAML/JSON
conversion and the `/test_run` → `saveToken` → `/save` chain are unaffected by
the shell.

### 6.10 Routines → Import

`sm`. The collision rule is a control rather than a paragraph.

### 6.11 Pages → New page, Pages → Import

The two surfaces that frosted the page behind them and the only two that were
not Radix Dialogs at all, so neither had a focus trap. Both move onto the
shared shell — `xl` and `sm` — the blur and the hand-rolled overlay go, and
nothing else changes. Import keeps the one genuinely good idea in the surfaces
being replaced: **an install that fails prints its unresolved references as a
form**, so the next attempt is fields to fill rather than a paragraph to
decode.

### 6.12 Integrations → Add integration

Keeps its two-step kind → service shape, which was the right call and is why
it is `xl`. It renders **no primary action** — picking the service closes it —
which the shell allows rather than treating as a contradiction.

### 6.13 The shared shell, desktop and phone

Every mobile class is written twice: once as `max-sm:` (the real breakpoint)
and once under `data-[mobile=true]` / `group-data-[mobile=true]/surface`, a
preview frame that forces the phone layout at desktop width. The pair cannot
be generated at runtime — Tailwind's scanner reads source text, so a composed
class name is never emitted.

`CreateSurfaceFrame` is that chrome without a Dialog around it. The retired
proposal page was its first user, rendering a surface's phone version inside a
handset frame at desktop width, which a portalled dialog cannot do. The second
use is the one that outlives the page: a create flow that has outgrown a modal
can be embedded in a route with its geometry intact rather than forked. Its
header must therefore not use Radix's `DialogTitle`, whose primitives throw
outside a Dialog root.
