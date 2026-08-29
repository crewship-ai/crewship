/**
 * The parity ledger: what the API and the CLI can do, against what the UI lets
 * a person reach.
 *
 * The unification work so far answered "do the twelve doors look alike". This
 * answers the harder question underneath it — "does a person who only has the
 * web UI have the same product as a person with the CLI". CLAUDE.md already
 * fixes one direction of that contract ("Every API endpoint gets a CLI
 * command"); nothing has ever fixed the other, so the UI has been free to
 * expose whatever each dialog's author happened to need.
 *
 * Every row is read out of the source and carries the reference. A row without
 * a `ref` is a claim nobody checked and should be deleted rather than trusted.
 *
 * ── The three UI states, and why the middle one is not a failure ──────────
 *
 *   create   the create surface itself can set it
 *   detail   only reachable after the thing exists, on its own screen
 *   none     not reachable anywhere in the web UI
 *
 * `detail` is frequently the RIGHT answer. A crew's MCP servers do not belong
 * in a four-step create wizard; putting them there is how New crew grew a
 * fifth step nobody wanted. So the ledger separates "missing" from "deferred",
 * and only `none` is automatically a bug. `detail` becomes a bug only when the
 * thing cannot be created in a working state without it.
 */

export type Where = "api" | "cli" | "both"
export type UiState = "create" | "detail" | "none"
export type Severity = "blocker" | "gap" | "deferred" | "fine"

export interface ParityRow {
  /** "Crews → New agent" — matches a door id in surfaces/registry.tsx. */
  surface: string
  /** What a person is trying to do, in their words. */
  capability: string
  /** Where the capability exists today. */
  where: Where
  /** `file.go:LINE` — the claim's receipt. */
  ref: string
  /** CLI flag or subcommand, when there is one. */
  cli?: string
  ui: UiState
  severity: Severity
  /** One line: why someone would want this, or why deferring is correct. */
  note: string
  /**
   * Closed in the working tree, with a test that fails without the fix.
   *
   * The row STAYS after it is fixed. A ledger that deletes what it repaired
   * cannot be read as a record of what was wrong, and the next person to open
   * this file deserves to see that the wizard used to promise "Never" and mean
   * four hours — that is the argument for the guard, not a footnote to it.
   */
  fixed?: { on: string; how: string }
  /**
   * The claim itself was WRONG, or has gone stale since it was written.
   *
   * Distinct from `fixed`, and the distinction is the point: `fixed` means the
   * product changed, `corrected` means the ledger was mistaken and something
   * else in the tree already said so. A row that quietly flips to `fixed` when
   * nobody touched the product is how an audit launders its own errors into a
   * record of progress.
   */
  corrected?: { on: string; how: string }
}

/**
 * Read out of the source on 2026-08-23 by five parallel audits, one per slice.
 * Not every capability — the full sweep found roughly 170 — but every one that
 * changes what a person can do, plus the handful where the UI is ahead.
 *
 * These rows describe the product AS IT SHIPS, not the proposal above them.
 * `Issues → New issue` still reads 0/6 here even though the proposed surface on
 * this page now carries due date, estimate, milestone and parent — measuring
 * the fix instead of the defect is how a ledger stops being worth reading.
 *
 * ── Re-verified 2026-08-29, and it did not survive intact ─────────────────
 *
 * Forty of these claims were re-read against the tree by six parallel audits,
 * each asked to come back with FALSE, TRUE-BUT-DELIBERATE, or TRUE-DEFECT
 * rather than to confirm. Roughly one in six did not hold:
 *
 *   · three were simply wrong (sidecar services, skill→crew, the missing
 *     cascade — the last one fixed by a migration three days AFTER the row
 *     was written);
 *   · four Issues rows had gone stale because the fix shipped in #2056;
 *   · three were real but graded as bugs when they are documented decisions
 *     (raw MCP behind the Composio flag, Pages payloads, the default
 *     connector);
 *   · one cited an event, `pre_tool_call`, that does not exist.
 *
 * Those rows now carry `corrected`. The rest held up — the credentials rows
 * were, if anything, understated. Two lessons worth keeping: a ledger that is
 * never re-read decays at a measurable rate, and "the UI cannot do this" and
 * "we decided the UI should not do this" look identical in a table that has no
 * column for intent. This one now has one.
 *
 * The subset that matches the release-1.0 bar is narrow, because that bar
 * (docs/prd/PRD-RELEASE-1-0-QUALITY-AUDIT.md) says nothing about UI↔CLI
 * parity. What it does contain, in the shape of #1781 and #1783, is "enforce
 * it or remove the claim" — so the rows that ship as 1.0 work are the ones
 * where the product asserts something untrue: #2166, #2167, #2168, #2169.
 */
export const PARITY: ParityRow[] = [
  /* ── Issues → New issue ───────────────────────────────────────────────── */
  {
    surface: "Issues → New issue",
    capability: "Sub-issue (parent)",
    where: "both",
    ref: "issue_handler_create.go:41",
    cli: "--parent-issue-id",
    ui: "none",
    severity: "gap",
    note: "The API fences it and guards cycles, and the UI RENDERS sub-issue trees it cannot create. Breaking an epic apart means dropping to the CLI.",
    corrected: {
      on: "2026-08-29",
      how: "Stale. It landed in the real modal in #2056: create-issue-modal.tsx declares the state at :157-166, renders a picker for it, and posts it at :464-467. The header note below the PARITY array explains that these rows deliberately measured the shipped product rather than the proposal on this page — that was right when written, and the shipped product then caught up without anyone ticking the row. The parent picker fetches crew-scoped candidates with the same three-state pattern as Assignee (:336-360).",
    },
  },
  {
    surface: "Issues → New issue",
    capability: "Due date",
    where: "both",
    ref: "issue_handler_create.go:38",
    cli: "--due-date",
    ui: "detail",
    severity: "gap",
    note: "Every issue filed from the UI starts undated, so planning is a second editing pass over issues you just typed.",
    corrected: {
      on: "2026-08-29",
      how: "Stale. It landed in the real modal in #2056: create-issue-modal.tsx declares the state at :157-166, renders a picker for it, and posts it at :464-467. The header note below the PARITY array explains that these rows deliberately measured the shipped product rather than the proposal on this page — that was right when written, and the shipped product then caught up without anyone ticking the row.",
    },
  },
  {
    surface: "Issues → New issue",
    capability: "Estimate",
    where: "both",
    ref: "issue_handler_create.go:40",
    cli: "--estimate",
    ui: "detail",
    severity: "gap",
    note: "Same shape as due date — accepted at create by the API, missing from the modal.",
    corrected: {
      on: "2026-08-29",
      how: "Stale. It landed in the real modal in #2056: create-issue-modal.tsx declares the state at :157-166, renders a picker for it, and posts it at :464-467. The header note below the PARITY array explains that these rows deliberately measured the shipped product rather than the proposal on this page — that was right when written, and the shipped product then caught up without anyone ticking the row.",
    },
  },
  {
    surface: "Issues → New issue",
    capability: "Milestone",
    where: "both",
    ref: "issue_handler_create.go:42",
    cli: "--milestone-id",
    ui: "detail",
    severity: "gap",
    note: "Accepted at create; the modal has no control, so milestone assignment is always a follow-up edit.",
    corrected: {
      on: "2026-08-29",
      how: "Stale. It landed in the real modal in #2056: create-issue-modal.tsx declares the state at :157-166, renders a picker for it, and posts it at :464-467. The header note below the PARITY array explains that these rows deliberately measured the shipped product rather than the proposal on this page — that was right when written, and the shipped product then caught up without anyone ticking the row.",
    },
  },
  {
    surface: "Issues → New issue",
    capability: "Routine inputs",
    where: "api",
    ref: "issue_handler_create.go:50",
    ui: "none",
    severity: "gap",
    note: "You can bind a routine to an issue from either client but never parameterise it, so it always fires with {}. No CLI flag either.",
  },
  {
    surface: "Issues → New issue",
    capability: "Assign to a person",
    where: "api",
    ref: "issue_handler_create.go:36",
    ui: "none",
    severity: "gap",
    note: "assignee_type accepts \"user\"; the CLI hard-rejects it and both pickers only fetch /agents. A human cannot be assigned an issue from any client.",
  },

  /* ── Issues → New project ─────────────────────────────────────────────── */
  {
    surface: "Issues → New project",
    capability: "Summary field is discarded",
    where: "api",
    ref: "project_handler.go:182-193",
    ui: "create",
    severity: "blocker",
    note: "The modal posts `summary` and `labels`; the create struct has neither, readJSON does not reject unknown fields, and there is no column. You type it, you get a success toast, it is gone.",
    fixed: {
      on: "2026-08-23",
      how: "Both controls removed rather than wired — there is no `summary` column and no `project_labels` table to wire them to. A standing test now transcribes the Go request struct and fails on any key the modal sends that the handler does not bind. Whether projects SHOULD have a summary or labels is handed back as a product decision.",
    },
  },
  {
    surface: "Issues → New project",
    capability: "Milestones",
    where: "both",
    ref: "milestone_handler.go:108",
    cli: "milestone create",
    ui: "none",
    severity: "blocker",
    note: "Full CRUD in the API and the CLI; the UI only reads them, and the modal's “+ Add milestone” button has no onClick at all. Worse than the audit thought: MilestoneHandler.Create 404s unless the project already exists, so the control could never have worked from a create modal — and there is no post-create surface either.",
    fixed: {
      on: "2026-08-23",
      how: "The dead section is gone, so the gap is visible instead of fake. Creating a milestone is still impossible anywhere in the web UI — project-sidebar.tsx has 697 lines of working milestone CRUD and is imported by nothing, while /orchestration/projects/[projectId] redirects to /issues.",
    },
  },
  {
    surface: "Issues → New project",
    capability: "Human as project lead",
    where: "api",
    ref: "project_handler.go:189",
    cli: "--lead-type",
    ui: "none",
    severity: "gap",
    note: "lead_type accepts \"user\"; the picker lists agents only.",
  },

  /* ── Routines → New routine ───────────────────────────────────────────── */
  {
    surface: "Routines → New routine",
    capability: "Acting agent (author_agent_id)",
    where: "both",
    ref: "pipelines_crud.go:567",
    cli: "--author-agent",
    ui: "none",
    severity: "blocker",
    note: "Without it, a routine with a `crewship`/issue.comment step is refused at save with 422 — an entire verb is UI-unauthorable, and the error names a flag the user cannot reach.",
  },
  {
    surface: "Routines → New routine",
    capability: "Change summary on save",
    where: "both",
    ref: "pipelines_crud.go:594",
    cli: "--change-summary",
    ui: "none",
    severity: "gap",
    note: "The versions tab DISPLAYS change summaries. Nothing in the UI can write one, so every UI-authored version is unlabelled.",
  },
  {
    surface: "Routines → New routine",
    capability: "The whole routine DSL",
    where: "both",
    ref: "schemas/routine.v1.json:9-144",
    ui: "create",
    severity: "fine",
    note: "23 top-level keys and ~18 step options, all typeable in the advanced editor with schema-driven completion. No form controls, and that is the right call for a DSL.",
  },

  /* ── Routines → Import ────────────────────────────────────────────────── */
  {
    surface: "Routines → Import",
    capability: "Import a bundle at all",
    where: "both",
    ref: "pipelines_crud.go:289-292",
    cli: "routine import --crew",
    ui: "none",
    severity: "blocker",
    note: "Import requires author_crew_id, export deliberately omits it, and the dialog posts the pasted JSON verbatim with no crew picker. Export → Import in the UI always 400s.",
  },
  {
    surface: "Routines → Import",
    capability: "Inlined scripts materialised",
    where: "cli",
    ref: "cmd_routine_extra.go:546-557",
    cli: "routine import",
    ui: "none",
    severity: "gap",
    note: "The endpoint has no script handling, so a script-bearing bundle imported through the UI lands with `type: script` steps that have no files behind them.",
  },

  /* ── Routines → after creation (schedules & webhooks) ─────────────────── */
  {
    surface: "Routines → Schedules",
    capability: "Wake gate (wake_pipeline, inputs, fail_closed)",
    where: "both",
    ref: "pipeline_schedules.go:124-131",
    cli: "--wake-slug --wake-inputs --fail-closed",
    ui: "none",
    severity: "gap",
    note: "The mechanism that stops a 5-minute cron burning tokens when there is nothing to do. The UI renders a read-only badge for it and offers nowhere to click.",
  },
  {
    surface: "Routines → Schedules",
    capability: "Catch-up policy after an outage",
    where: "both",
    ref: "pipeline_schedules.go:136",
    cli: "--catchup",
    ui: "none",
    severity: "gap",
    note: "Decides whether an hourly routine fires once or replays 40 backlogged occurrences. Exactly what you want to change after a bad night.",
  },
  {
    surface: "Routines → Schedules",
    capability: "Circuit breaker (max_consecutive_failures)",
    where: "both",
    ref: "pipeline_schedules.go:141",
    cli: "--max-failures",
    ui: "none",
    severity: "gap",
    note: "Zero hits anywhere in the web tree.",
  },
  {
    surface: "Routines → Schedules",
    capability: "Pin a trigger to a version",
    where: "both",
    ref: "pipeline_schedules.go:115",
    cli: "--pin-version",
    ui: "none",
    severity: "gap",
    note: "Typed in both hooks and set by neither, so a production schedule cannot be pinned and every routine edit silently changes what fires at 03:00.",
  },
  {
    surface: "Routines → Webhooks",
    capability: "Inputs template",
    where: "both",
    ref: "pipeline_webhooks.go:130",
    cli: "--inputs-template",
    ui: "none",
    severity: "gap",
    note: "Hardcoded to {} in the UI, so a UI-created webhook can only pass the raw body through — no constant injection, no remapping.",
  },

  /* ── Crews → New crew ─────────────────────────────────────────────────── */
  {
    surface: "Crews → New crew",
    capability: "“Never” auto-stop means 4 hours",
    where: "both",
    ref: "submit.ts:30 + crews_create.go:297-301",
    cli: "--ttl 0",
    ui: "create",
    severity: "blocker",
    note: "The wizard omits container_ttl_hours when the chip says “Never”, and the server reads absent as its 4-hour default. The one chip that promises not to stop your crew is the one that stops it. The CLI got this right.",
    fixed: {
      on: "2026-08-23",
      how: "submit.ts now always sends the field, and “Never” sends 0. The suite contained a test ASSERTING the omission — it had encoded the bug as correct behaviour, and had to be inverted. The step's CLI hint said “(no --ttl)”, which after the fix would have printed a command producing a different crew; it now reads --ttl 0.",
    },
  },
  {
    surface: "Crews → New crew",
    capability: "Sidecar services (Postgres, Redis…)",
    where: "api",
    ref: "crews_create.go:74",
    ui: "none",
    severity: "gap",
    note: "Reachable from neither the UI nor the CLI — `crew services` only lists what someone else set. The only field in this audit with no operator surface at all.",
    corrected: {
      on: "2026-08-29",
      how: "Wrong. `crewship apply` writes it: internal/manifest/kinds/crew.go:190-193 has a typed Services spec with validation, serialised into services_json for crew create and update (:771-874), and examples/manifests/python-with-services.crew.yaml is a maintained example referenced from docs/guides/auto-managed-credentials.mdx. What is missing is a --services flag on `crew create` — a flag gap, not an operator-surface gap. The row was written from the handler struct and never checked `apply`.",
    },
  },
  {
    surface: "Crews → New crew",
    capability: "Private-endpoint egress",
    where: "both",
    ref: "crews_create.go:64",
    cli: "--allow-private-endpoints",
    ui: "detail",
    severity: "deferred",
    note: "Deliberately absent from the wizard and documented in cli-parity.test.ts — an ADMIN-tier egress grant does not belong behind a self-serve MANAGER wizard.",
  },

  /* ── Crews → New agent ────────────────────────────────────────────────── */
  {
    surface: "Crews → New agent",
    capability: "Every field of createAgentRequest",
    where: "both",
    ref: "agents_create.go:15-36",
    cli: "agent create",
    ui: "create",
    severity: "fine",
    note: "All 16 request fields are in the dialog. This is the one surface already at struct parity — which is why it was the right one to keep.",
  },
  {
    surface: "Crews → New agent",
    capability: "Give the agent a credential",
    where: "both",
    ref: "agents_create.go:280-284",
    cli: "credential assign",
    ui: "detail",
    severity: "blocker",
    note: "The handler's own comment claims the dialog prompts for one after the 201. It does not. A fresh agent has no API key and nothing on the create path says so — you find out when the first run fails.",
  },
  {
    surface: "Crews → New agent",
    capability: "Hire an ephemeral agent",
    where: "both",
    ref: "agents_hire.go:45-51",
    cli: "crewship hire --ttl --reason --parent-lead",
    ui: "none",
    severity: "gap",
    note: "The UI can only approve or rehire what the CLI created, and its rehire hardcodes 60 minutes and a canned reason. There is no way to initiate a hire from the browser.",
  },
  {
    surface: "Crews → New agent",
    capability: "Workspace-wide (crewless) agent",
    where: "api",
    ref: "agents_create.go:18",
    ui: "none",
    severity: "gap",
    note: "requiresCrew is a literal `true` in the dialog while the API accepts crew_id: null for every non-LEAD role. The warning banner still tells you to pick a “Coordinator” role the select no longer offers.",
  },
  {
    surface: "Crews → New agent",
    capability: "Skills, MCP servers, channels",
    where: "both",
    ref: "router_crews.go:347 / :266 / router_orchestration.go:316",
    cli: "skill assign · agent mcp add",
    ui: "detail",
    severity: "deferred",
    note: "Correctly deferred: each is a list you curate over the agent's life, and all three already have working post-create surfaces on the agent canvas.",
  },

  /* ── Credentials → Add secret ─────────────────────────────────────────── */
  {
    surface: "Credentials → Add secret",
    capability: "ENDPOINT_URL credentials",
    where: "both",
    ref: "credentials_types.go:59-70",
    cli: "--type ENDPOINT_URL",
    ui: "none",
    severity: "blocker",
    note: "The type is inferred from the credential's NAME and no wizard shape maps to it — yet the Keeper panel asks the operator to pick an ENDPOINT_URL credential. A dead end for anyone pointing Crewship at a local Ollama or LiteLLM.",
  },
  {
    surface: "Credentials → Add secret",
    capability: "Edit custom fields after creation",
    where: "both",
    ref: "credential_fields.go:150-155",
    cli: "credential field set",
    ui: "create",
    severity: "blocker",
    note: "The wizard writes them once; the detail sheet only lists them. Fixing a typo'd AWS `region` means deleting the credential and starting over.",
  },
  {
    surface: "Credentials → Add secret",
    capability: "Re-slot or unbind",
    where: "both",
    ref: "credential_bindings.go:107-113",
    cli: "credential bind --slot",
    ui: "create",
    severity: "gap",
    note: "Bindings are create-once, AGENT scope has no UI at all, and DELETE /credentials/bindings/{id} has no caller anywhere in the frontend.",
  },
  {
    surface: "Credentials → Add secret",
    capability: "Per-agent env var, priority, TTL lease",
    where: "both",
    ref: "agent_credentials.go:216-221",
    cli: "credential assign --env-var-name --priority --ttl",
    ui: "none",
    severity: "gap",
    note: "env_var_name is auto-derived, priority is hardcoded 0, and short-lived leases have no control — so failover ordering and “give this agent the prod token for two hours” are CLI-only.",
  },
  {
    surface: "Credentials → Add secret",
    capability: "Rotation endpoint + cancel",
    where: "both",
    ref: "credential_rotation.go:65-67",
    cli: "credential rotate --auth-token --header",
    ui: "none",
    severity: "gap",
    note: "The dialog can rotate a value but cannot set the endpoint that performs it, and a rotation in flight cannot be cancelled from the browser.",
  },

  /* ── Skills → Import ──────────────────────────────────────────────────── */
  {
    surface: "Skills → Import",
    capability: "Path globs on a repo import",
    where: "both",
    ref: "skills_bulk_import.go:37",
    cli: "--paths",
    ui: "none",
    severity: "gap",
    note: "Importing from a monorepo pulls everything or nothing.",
  },
  {
    surface: "Skills → Import",
    capability: "Delete a workspace skill",
    where: "both",
    ref: "router_crews.go:502",
    cli: "skill delete",
    ui: "none",
    severity: "gap",
    note: "The detail panel only uninstalls from an agent. A bad import cannot be removed from the workspace in the browser.",
  },
  {
    surface: "Skills → Import",
    capability: "Assign to a whole crew",
    where: "cli",
    ref: "cmd_skill.go:690-692",
    cli: "--to-crew",
    ui: "none",
    severity: "gap",
    note: "The UI offers per-agent checkboxes only, so a ten-agent crew is ten clicks.",
    corrected: {
      on: "2026-08-29",
      how: "Wrong. AssignToCrewDialog (skills-detail-panel.tsx:202, :594-660) picks one crew and applies the skill to every agent in it in a single submit — it has existed since the first skills sprint (baa7d8e5). The real, much smaller limitation is that it does not cover agents added to the crew later, documented at :400-405.",
    },
  },

  /* ── Integrations → Add integration ───────────────────────────────────── */
  {
    surface: "Integrations → Add integration",
    capability: "Workspace-scoped MCP server",
    where: "both",
    ref: "workspace_integrations.go:57-67",
    cli: "integration add",
    ui: "none",
    severity: "blocker",
    note: "Every UI create posts to /crews/{id}/integrations. One server shared by every crew means the CLI, or N duplicates kept in sync by hand.",
  },
  {
    surface: "Integrations → Add integration",
    capability: "HTTP headers / auth (config_json)",
    where: "api",
    ref: "crew_integrations_crud.go:24",
    ui: "none",
    severity: "blocker",
    note: "Read by the adapter, written by neither the CLI nor the UI. A remote MCP server behind an API-gateway header — the commonest shape of a hosted server — is unconfigurable outside a manifest.",
  },
  {
    surface: "Integrations → Add integration",
    capability: "Per-agent tool allowlist",
    where: "both",
    ref: "agent_bindings.go:36",
    cli: "agent mcp update --tools",
    ui: "none",
    severity: "gap",
    note: "Crew-level tool switches exist; narrowing one agent to a subset does not.",
  },
  {
    surface: "Integrations → Add integration",
    capability: "api_key / basic MCP auth",
    where: "both",
    ref: "agent_bindings.go:32-34",
    cli: "--cred-type --cred-header --env-var",
    ui: "none",
    severity: "gap",
    note: "cred_type is hardcoded to \"bearer\" in the UI, so the other two auth shapes and stdio credential injection are unreachable.",
  },
  {
    surface: "Integrations → Add integration",
    capability: "Notification events + raw shoutrrr URL",
    where: "both",
    ref: "notification_channels.go:51 / :60",
    cli: "--events --url",
    ui: "none",
    severity: "gap",
    note: "`events` is typed in the hook and rendered by no control, so every UI channel fires on everything. Categories and min-priority are create-only — the UI patches nothing but `enabled`.",
  },
  {
    surface: "Integrations → Add integration",
    capability: "Connector catalogue",
    where: "both",
    ref: "connectors_handler.go:110-112",
    cli: "connector install --field --crew",
    ui: "none",
    severity: "blocker",
    note: "ConnectorCatalog and ConnectorConnectSheet are built and tested — and mounted on no page. The whole manifest-driven connector flow is dead on arrival in the browser.",
  },
  {
    surface: "Integrations → Add integration",
    capability: "“Test” on the Add-MCP wizard",
    where: "both",
    ref: "add-mcp-wizard.tsx:213-223 · integration_test_connection.go:42/:54",
    cli: "integration crew test <crew> <id>",
    ui: "detail",
    severity: "blocker",
    note: "A setTimeout(…, 400) that returns a hard-coded “Configuration looks valid.” No network call of any kind. If anyone reports “test passed but the server is broken”, this is why.",
    fixed: {
      on: "2026-08-26",
      how: "Removed, not wired. Both real probes load transport/endpoint/command out of the database, so neither can be aimed at a draft the wizard has not written yet — wiring one would have meant inventing a draft-test endpoint, which is a backend decision, not a repair to this control. The step now names the two surfaces that do test: “Test connection” on the server's row and `crewship integration crew test`. The ui state moves create → detail: the capability is deferred to after create, which is where it can actually run.",
    },
  },
  {
    surface: "Integrations → Add integration",
    capability: "Deleting a server leaves its tool rows behind",
    where: "api",
    ref: "migrate.go:1107-1117 · crew_integrations_crud.go:309/:333",
    ui: "none",
    severity: "gap",
    note: "mcp_tool_bindings has no foreign key to crew_mcp_servers, so no cascade. Deletion clears agent bindings and the server row and never the tool bindings — every deleted integration orphans its rows permanently. Not a UI gap; found while looking at one.",
    corrected: {
      on: "2026-08-29",
      how: "Already fixed when this was re-read, and fixed three days AFTER the row was written: migration 20260826190607_mcp_default_access.sql adds cascade triggers for both the workspace and crew server deletes plus an insert-time FK guard, and sweeps the existing orphans. Covered by migrate_mcp_default_access_test.go:176-246 and documented in docs/guides/migrations.mdx:635-642. The clearest argument in this file for re-verifying a ledger instead of trusting it.",
    },
  },
  {
    surface: "Integrations → Add integration",
    capability: "Discover a server's tools",
    where: "api",
    ref: "mcp_tool_bindings.go:174-295 · cmd_integration_tools.go",
    cli: "integration tools refresh --tool/--tools-file (records, does not discover)",
    ui: "none",
    severity: "blocker",
    note: "Filed first as “the UI is behind the CLI”. That was wrong, and the correction matters: `POST …/tools/refresh` UPSERTS a client-supplied tools[] and performs no discovery. #1884 gave the CLI --tool/--tools-file so it can at least carry a catalogue you already have, but neither surface discovers anything. The only code that speaks tools/list is the sidecar's gateway, whose catalogue never leaves the container — and stdio servers are never discovered at all. This is backend work, not a UI gap.",
    fixed: {
      on: "2026-08-23",
      how: "Copy only, deliberately. The button keeps re-reading the database (worth having — it picks up another admin's toggles) and stops calling itself “Refresh”; the empty state now says discovery is not wired instead of instructing you to click a button that cannot populate it. Wiring the endpoint would have traded a working re-read for a guaranteed no-op plus a 403 for every non-ADMIN who can view the tab.",
    },
  },

  /* ── Pages ────────────────────────────────────────────────────────────── */
  {
    surface: "Pages → New page",
    capability: "Put data into a panel",
    where: "both",
    ref: "router_pages.go:63",
    cli: "page set <slug>/<panel> --data -",
    ui: "none",
    severity: "deferred",
    note: "Every option in every payload schema is unreachable from the browser. You can author the frame and never fill it — a panel is fed only by a producer routine, the CLI, or an inbound webhook.",
    corrected: {
      on: "2026-08-29",
      how: "Re-graded from blocker to deferred. Feeding a panel by hand from the browser was never in the design: pages_handler.go:4-10 states a page holds no query, no datasource, no connection string and no credentials, and renders the last payload a producer pushed; docs/prd/pages.md:5-8 specifies panel data as produced by a routine or a script in a crew container, pushed via CLI or sidecar. The empty-state copy already points at `crewship page set`. The resulting gap is real — a page authored entirely in the UI is inert until someone touches the CLI — but it is the architecture working as specified, not a missing screen.",
    },
  },
  {
    surface: "Pages → New page",
    capability: "Give a page to a crew",
    where: "both",
    ref: "pages_handler.go:315",
    cli: "page create --owner crew/<slug>",
    ui: "none",
    severity: "gap",
    note: "The editor deliberately never sends `owner`, so a UI-authored page always belongs to the person who made it.",
  },
  {
    surface: "Pages → New page",
    capability: "The rest of the page document",
    where: "both",
    ref: "pages_handler.go:122-185",
    cli: "page create --file",
    ui: "create",
    severity: "fine",
    note: "Panels, SLAs, spans, tabs, actions, wake gates, grants, public links, webhooks, versions, rollback and export are all reachable. Spec-side parity is near-total; it is the payload side that is empty.",
  },

  {
    surface: "Integrations → Add integration",
    capability: "Add a raw MCP server at all",
    where: "cli",
    ref: "app/(dashboard)/integrations/page.tsx:69 · lib/feature-flags.ts:25",
    cli: "integration add",
    ui: "none",
    severity: "deferred",
    note: "The MCP wizard, detail sheet and expanded panel only render behind NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS, which is off by default. In the shipped build there is NO way to add an MCP server from the browser — only the CLI. The doc comments still describe the flag-off path as a “coming soon placeholder”; it actually renders the full Composio UI.",
    corrected: {
      on: "2026-08-29",
      how: "Re-graded from blocker to deferred. This is a documented product decision, not an oversight: commit cec5fc7a states that the hand-rolled self-hosted MCP connector UI is being retired in favour of a managed platform (Composio) and hidden behind a build-time flag, default OFF; docs/guides/integrations.mdx:9-16 tells users the Composio UI is now the default and that the legacy UI is restorable with NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS=true, with no legacy code or API removed. The flag is named LEGACY_, not DISABLED_, which only makes sense as a deliberate rollback path. CODEX-WORK-ORDER-RELEASE-1-0.md:610 lists whether the legacy catalogue is part of the 1.0 surface as an open ask-first decision. What IS a genuine defect is the stale copy around it — page.tsx:543 still calls the flag-off path a coming-soon placeholder when it renders the full Composio UI.",
    },
  },
  {
    surface: "Integrations → Composio",
    capability: "The default connector silently drops other servers",
    where: "api",
    ref: "agent_config.go:1254-1256",
    ui: "none",
    severity: "gap",
    note: "With COMPOSIO_DEFAULT_CONNECTOR on, every non-Composio workspace and crew MCP server is dropped from the resolved runtime config without the rows being deleted — and GET …/integrations/resolved does not model this, so the screen that exists to answer “what will this agent get” shows servers the container will not receive.",
    corrected: {
      on: "2026-08-29",
      how: "Re-graded from blocker to gap. The drop itself is documented and matches the code: docs/guides/integrations.mdx:104-105 says legacy non-Composio servers are turned off at resolve time while the flag is on and are not deleted, and agent_config.go:1254-1256/:1391-1395 does exactly that. COMPOSIO_DEFAULT_CONNECTOR defaults to false (config.go:207) and is set nowhere in this repo or in ~/crewship-infra. What survives is narrower than the row claims: ResolveAgentIntegrations (integration_resolve.go:72) genuinely does not model the flag — but no frontend calls that endpoint, so the inconsistency is CLI-side, not the misleading screen the note describes.",
    },
  },
  {
    surface: "Crews → New agent",
    capability: "Granting one agent revokes it from the rest",
    where: "both",
    ref: "agent_config.go:1234-1246 · :1291-1293",
    cli: "integration access",
    ui: "detail",
    severity: "blocker",
    note: "A server with zero bindings is handed to EVERY agent. The moment one agent binds it, every agent without its own binding loses it — so the first grant is a silent workspace-wide revocation. No surface warns about this, and it is the single most surprising rule in the integration model.",
    fixed: {
      on: "2026-08-27",
      how: "The audience is a stored column (default_access: all | bound-only) on both server tables, so a binding is purely additive and cannot change what any other agent resolves. Fixed in BOTH cascades — the read-only resolver and resolveAgentMCPServers in agent_config.go, which is what the container actually gets; the runtime copy was worse, its binding count was not workspace-scoped, so a binding in a DIFFERENT workspace could revoke a server. The migration backfills every server that already carries a binding to bound-only, so nobody's effective access moves on upgrade — verified against a real database, before and after are identical.",
    },
  },
  {
    surface: "Crews → New agent",
    capability: "Per-agent tool allowlist",
    where: "cli",
    ref: "integration_resolve.go:161-163 · cmd_agent_mcp.go:194",
    cli: "agent mcp update --tools",
    ui: "none",
    severity: "gap",
    note: "Re-filed. Earlier recorded as a UI gap; it is worse than that. config_override_json is written by the API, read only by the read-only resolver where it overwrites config_json wholesale, and never parsed for `tools`. The CLI help states the runtime filters on it. It does not. The flag writes a string that is stored, echoed back and ignored.",
  },
  {
    surface: "Integrations → Add integration",
    capability: "Per-tool switches actually block a tool",
    where: "api",
    ref: "agent_config.go:1322 · mcp_gateway.go:398-455",
    ui: "detail",
    severity: "gap",
    note: "mcp_tool_bindings reaches the agent as TEXT in the [CONNECTED INTEGRATIONS] prompt block and nothing else. Neither .mcp.json nor the gateway's CallTool consults it, so a tool switched off in the UI is still callable — the switch is a suggestion to the model, not a control.",
  },

  /* ── Cross-cutting: capabilities with no screen at all ────────────────── */
  {
    surface: "Everywhere",
    capability: "Components built, tested, and mounted nowhere",
    where: "both",
    ref: "project-sidebar.tsx · connectors/ · assign-credential-dialog.tsx",
    ui: "none",
    severity: "blocker",
    note: "Three so far, found by three separate audits: 697 lines of milestone CRUD, the whole connector catalogue, and the credential-assignment dialog that would have exposed priority. Each is finished work that no route imports — which means the gap is not “nobody built it” but “nobody hooked it up”, and that is a much cheaper fix than it looks.",
    corrected: {
      on: "2026-08-29",
      how: "The roster is out of date in both directions. project-sidebar.tsx and assign-credential-dialog.tsx were DELETED in #2116, so two of the three named files no longer exist. ConnectorCatalog/ConnectorConnectSheet are still orphaned — built 2026-05-14 (9d592616, 069a27f1) and superseded by the Composio pivot (cec5fc7a) six weeks later, so this one is abandoned-in-place rather than unfinished. And a fourth has surfaced: CredentialsManager (agent-canvas-managers.tsx:167-260) is exported and imported nowhere, which is why an agent cannot be given a credential from any screen — tracked as #2169.",
    },
  },
  {
    surface: "Chat",
    capability: "Message reactions",
    where: "api",
    ref: "router_orchestration.go:457",
    ui: "create",
    severity: "blocker",
    note: "The picker works and writes to localStorage. Reactions never reach the server, so two teammates looking at the same conversation see different ones forever.",
    fixed: {
      on: "2026-08-23",
      how: "PARTIALLY. The store is now an optimistic layer over the three real endpoints (the audit said two — there is also a GET, without which a teammate's reaction can never be shown at all), and the value grew from a bare count to {count, mine} because clicking a teammate's 👍 would otherwise decrement THEIR reaction. localStorage is dropped, not migrated: the old record never stored the chat id the endpoint requires nor who reacted, so replaying it would mean attributing strangers' reactions to whoever is logged in. Works end-to-end on reloaded history. NOT on a turn you just watched stream — see the next row.",
    },
  },
  {
    surface: "Chat",
    capability: "A just-streamed turn has no server id",
    where: "api",
    ref: "use-chat.ts · stores/feedback-store.ts",
    ui: "create",
    severity: "blocker",
    note: "A streaming assistant turn gets a client-side uuid() that is never reconciled with the persisted message id, and the reactions handler validates nothing — so a reaction on the turn you are watching POSTs under a UUID, returns 204, and is orphaned. After a refresh the turn has a different id and the reaction is gone. `feedback-store.ts` posts `message_id: turnId` and has the identical gap, so thumbs-up on a live turn is lost the same way. Closing it needs the `done` WS frame to carry the persisted id — a server change.",
  },
  {
    surface: "Chat",
    capability: "Steer a running agent",
    where: "both",
    ref: "router_orchestration.go:523",
    cli: "chat steer",
    ui: "none",
    severity: "gap",
    note: "You can redirect a running agent from a terminal but not from the chat window you are watching it in.",
  },
  {
    surface: "Missions → Timeline",
    capability: "Fork a checkpoint",
    where: "both",
    ref: "router_orchestration.go:257",
    cli: "checkpoint fork",
    ui: "none",
    severity: "blocker",
    note: "The fork dialog posts to /missions/{id}/fork, which is not a registered route. The only fork is /checkpoints/{id}/fork — the button is broken, not missing. It read the 404 as toast.info(“Not yet wired to backend”), so the failure presented as an unfinished feature.",
    fixed: {
      on: "2026-08-23",
      how: "Posts to the checkpoint route with the label, shows the server's error inline and keeps your typed label for the retry. A second defect underneath it: the checkpoint id lives in the journal entry's `refs`, not `payload`, so the timeline was always sending the journal row id — fixing only the URL would have swapped a 404-on-route for a 404-on-id. Restore was sending the same wrong id and had therefore never worked either.",
    },
  },
  {
    surface: "Crews → Runtime",
    capability: "Start / stop a container",
    where: "both",
    ref: "router_crews.go:175",
    cli: "crew start · crew stop",
    ui: "none",
    severity: "gap",
    note: "The Docker tab shows the containers and offers no power switch.",
  },
  {
    surface: "Crews → Runtime",
    capability: "See and revoke exposed ports",
    where: "both",
    ref: "router_orchestration.go:914",
    cli: "expose list · expose revoke",
    ui: "none",
    severity: "gap",
    note: "Agents can open externally reachable URLs. Nobody can see or close them from the browser — this one is security-relevant.",
  },
  {
    surface: "Runs",
    capability: "Why is this run held?",
    where: "both",
    ref: "router_orchestration.go:200",
    cli: "capacity",
    ui: "none",
    severity: "gap",
    note: "The UI already says “Waiting for host capacity” and never fetches the endpoint that says which gate is holding. A held run is indistinguishable from a hung one.",
  },
  {
    surface: "Routines → Operations",
    capability: "Trust grants, state, replay, signals, pending runs",
    where: "both",
    ref: "router_pipelines.go:76-78 · :174",
    cli: "routine trust · state · step-run · bulk_replay · signal",
    ui: "none",
    severity: "gap",
    note: "The whole operational half of routines. A run parked on wait:event has no browser-side release, and the error-fingerprint comment promises a bulk-replay view that was never built.",
  },
  {
    surface: "Admin",
    capability: "Lifecycle hooks registry",
    where: "both",
    ref: "router_orchestration.go:544-549",
    cli: "hooks list/add/enable",
    ui: "none",
    severity: "gap",
    note: "Arbitrary shell and webhook code fires on platform events like pre_tool_call. The registry is invisible and unkillable from the browser.",
    corrected: {
      on: "2026-08-29",
      how: "The gap is real; the framing was not. `pre_tool_call` DOES NOT EXIST — cmd_hooks.go:33-35 says so outright: there is no interception point before a tool executes, so nothing could ever fire it. Only post_tool_call exists (internal/hooks/types.go:65). Registration is roleManage (OWNER/ADMIN) and handler_kind: shell is OWNER-only (hooks_write.go:79-92). So this is an operability gap — an OWNER cannot review or disable from the browser what they registered from a terminal — not the low-privilege shell-execution hole the note implies.",
    },
  },
  {
    surface: "Admin",
    capability: "Feature flags and instance settings",
    where: "both",
    ref: "router_orchestration.go:182-193",
    cli: "feature-flag · instance settings",
    ui: "none",
    severity: "gap",
    note: "lib/feature-flags in the frontend is a hardcoded constants module, not this API. Flags cannot be flipped without a shell.",
  },
  {
    surface: "Inbox",
    capability: "See the diff you are approving",
    where: "both",
    ref: "router_orchestration.go:597",
    cli: "consolidate diff · explain",
    ui: "none",
    severity: "blocker",
    note: "The inbox lets you approve or reject a memory-consolidation proposal without ever showing the byte-level diff or the evidence behind it.",
  },
  {
    surface: "Issues → Triage",
    capability: "Triage rules and recurring issues",
    where: "both",
    ref: "router_orchestration.go:162-172",
    cli: "triage · recurring",
    ui: "none",
    severity: "gap",
    note: "Incoming issues are routed by rules nobody can read, and issues appear on a schedule with no screen explaining where they came from.",
  },
]

/**
 * The other half of the question, measured once rather than row by row.
 *
 * Walked from the built CLI binary (depth 3, no truncation) and from the router
 * registrations, then cross-referenced against every `/api/v1/...` literal in
 * `app/ components/ lib/ hooks/ stores/`, with each non-match hand-verified
 * against the dynamic URL builders.
 *
 * Excluded from `noWebUi`: the 62 `/api/v1/internal/*` sidecar-IPC routes, the
 * NextAuth shim, CLI device pairing, health, the agent-daemon callback and the
 * unauthenticated external-callback token surfaces. Those are not screens
 * anyone was ever going to build.
 *
 * Method caveat, recorded because it changes how much weight the number
 * carries: the read-side sweep was received complete and spot-verified; three
 * commissioned sub-audits of the mutation side did not return, so mutation-side
 * coverage is the auditor's own direct reading. Treat 90 as a floor.
 */
export const SWEEP = {
  cliLeafCommands: 636,
  cliTopLevel: 109,
  routeRegistrations: 654,
  uniquePaths: 507,
  internalPaths: 62,
  /** Non-internal, non-auth, non-health paths — ones a screen could exist for. */
  candidatePaths: 425,
  /** Confirmed to have no web UI at all. */
  noWebUi: 90,
  /** Endpoints the UI has and the CLI does not. Both are convenience aggregates. */
  uiAheadOfCli: 2,
} as const

export interface ParitySurfaceSummary {
  surface: string
  total: number
  create: number
  detail: number
  none: number
  blockers: number
}

/** Per-surface rollup, computed so it cannot drift from the rows. */
export function summarise(rows: ParityRow[]): ParitySurfaceSummary[] {
  const bySurface = new Map<string, ParityRow[]>()
  for (const r of rows) {
    const list = bySurface.get(r.surface) ?? []
    list.push(r)
    bySurface.set(r.surface, list)
  }
  return [...bySurface.entries()].map(([surface, list]) => ({
    surface,
    total: list.length,
    create: list.filter((r) => r.ui === "create").length,
    detail: list.filter((r) => r.ui === "detail").length,
    none: list.filter((r) => r.ui === "none").length,
    blockers: list.filter((r) => r.severity === "blocker").length,
  }))
}

/** Headline numbers for the page. */
export function parityTotals(rows: ParityRow[]) {
  return {
    capabilities: rows.length,
    reachable: rows.filter((r) => r.ui !== "none").length,
    unreachable: rows.filter((r) => r.ui === "none").length,
    blockers: rows.filter((r) => r.severity === "blocker").length,
    deferred: rows.filter((r) => r.ui === "detail").length,
  }
}
