# Changelog

All notable changes to Crewship are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 releases may introduce breaking changes in minor versions
(`0.x.0`); patch releases (`0.x.y`) are backwards-compatible fixes.

## [Unreleased]

### Added — CLI: AI-first 2026 (15 new commands and flags)

Major CLI surface expansion aligning Crewship with the 2026 agent-CLI playbook (long-running workflows, plan/act separation, headless scripting, real-time dashboards, model-tiering control). All additions live in `cmd/crewship` and `internal/cli`; one server endpoint added (`GET /api/v1/runs/{id}`).

**New top-level commands:**

- **`crewship -p "..."`** — headless one-shot prompt to the default agent. Sets quiet by default, exits non-zero on agent error. Pipe-friendly: `cat issue.md | crewship -p "summarise"`.
- **`crewship plan <prompt>`** + **`--plan`** flag on `run`/`ask` — plan/act separation. Read-only architect mode that outputs a step-by-step plan + files-to-touch + risks without executing tools. Prompt-engineered (no backend mode), so it composes with every adapter.
- **`crewship resume [chat-id|run-id|pr-url]`** — pick up the last session, an explicit one, or the session that produced a GitHub/GitLab/Bitbucket PR. No-arg form opens a `huh`-styled picker over the 10 most recent CLI sessions.
- **`crewship wait <run-id>`** — block until a run reaches a terminal status. Status-aware exit codes (0 done, 1 failed, 2 cancelled, 3 timeout). Use in scripts: `crewship wait $(crewship ask --no-stream -q "..." | jq -r .id) && echo done`.
- **`crewship tui`** — real-time Bubble Tea dashboard. Three panels: running runs, pending approvals, live journal stream (SSE-pumped). Keys: `q` quit, `r` refresh, `Tab` focus.
- **`crewship recap <chat-id>`** — LLM-generated summary of a chat session via the default agent. Output is a 4-section markdown brief (outcome / decisions / open threads / next prompt). Tunable bullet count via `--bullets`.
- **`crewship shell`** — interactive REPL. Slash commands: `/help`, `/agent <slug>`, `/workspace <slug>`, `/cd`, `/plan` (toggle), `/effort <level>`, `/think` (toggle), `/clear`, `/history`, `/quit`. `@file` fuzzy expansion inlines file content into prompts.
- **`crewship me`** — your missions + your pending approvals + your recent runs (3 parallel REST calls).
- **`crewship today`** — today's runs and spend.
- **`crewship now`** — live status: running runs, idle/busy agent counts, pending approvals.
- **`crewship cost forecast`** — projected cost before you spend tokens. Two modes: `--prompt @file` (token-count heuristic) or `--from-history <agent>` (average of last 20 runs). Renders rate table for Sonnet 4.6 / Opus 4.7 / Haiku 4.5 with output-ratio tuneable (`--output-ratio`, default 2.0×).
- **`crewship diff <run-a> <run-b>`** — side-by-side comparison of two existing runs (status, agent, output diff). Distinct from `eval compare` which re-runs an eval scenario.
- **`crewship notify`** — desktop notifications group. `enable` / `disable` / `status` / `test` / `send <title> <body>`. Auto-fires on long-running run completion (≥30 s) and pending approvals. Uses `osascript` on darwin, `notify-send` on linux, BurntToast on Windows (no-op when missing).
- **`crewship slash`** — manage user-defined slash commands. `slash list` enumerates loaded files; `slash init` scaffolds `~/.crewship/commands/review.md` as a starter.

**New flags on existing commands:**

- **`--format=ndjson`** (global) — line-delimited JSON output, pipe-friendly for `jq -c` / `fx` / stream-processing tools. Plumbed through `Auto` / `AutoDetail` so every list/detail command supports it uniformly.
- **`--plan`** on `run` / `ask` — plan-mode without a separate command.
- **`--effort=minimal|low|medium|high|xhigh`** on `run` / `ask` — reasoning effort passthrough, threaded into chat-creation metadata.
- **`--show-thinking`** on `run` / `ask` — surfaces full reasoning blocks on stdout (not the 100-char truncated stderr peek).

**User-defined slash commands** (`~/.crewship/commands/*.md`)

Markdown files with YAML frontmatter become first-class CLI subcommands at load time:

```markdown
---
name: review
description: Review a diff
agent: viktor
plan: true
vars:
  - target
---
Review this ${target} for $args.
```

`name`/`description`/`agent`/`effort`/`plan`/`vars` are honoured. `$VAR` / `${VAR}` substitution against positional args. Built-in commands always win on collision (the loader skips + warns).

**Server surface (one endpoint added):**

- **`GET /api/v1/runs/{id}`** — single-run lookup used by `wait`, `resume`, `diff`. Reuses the existing `journal.ListRuns` + enrichment path; 404 for unknown ids (cross-tenant masked).

**New internal helpers** (single-responsibility, all unit-tested):

- `internal/cli/runs.go` — `GetRun(ctx, id)`, `PollRun(ctx, id, interval, onTick)`, `ParsePRURL(s)`, `RunDetail`.
- `internal/cli/notify.go` — `OSNotify(title, body, level)`, `NotificationsEnabled(cfg)`, GOOS dispatch matrix.
- `internal/cli/slashcmd.go` — `LoadSlashCommands()`, `ParseSlashFile(path)`, `SlashCommand.Render(args)`, frontmatter loader.
- `internal/cli/repl.go` — `REPL` struct with slash-dispatch, `ExpandAtFiles(line)`, `ApplyPlanShellPrefix`.
- `internal/cli/tui/` (package) — Bubble Tea Model/Update/View, SSE journal pump with reconnect, lipgloss styling.
- Formatter: `NDJSON(v)`, `WriteNDJSONRow(v)`, `"ndjson"` routing in `Auto` / `AutoDetail`.

**Tests added (~30 new tests):**

- `runs_test.go` — `IsTerminal`, `ParsePRURL` (5 hosts), `GetRun` (200/404/empty-id), `PollRun` (3-poll convergence).
- `notify_test.go` — `NotificationsEnabled` (nil/false/true), `OSNotify` no-panic guard.
- `slashcmd_test.go` — frontmatter parse, no-frontmatter fallback, `$VAR` / `${VAR}` / `$args` substitution, name validation.
- `repl_test.go` — slash dispatch, unknown-slash warning, `@file` expansion (existing/missing/`@-`), plan shell prefix idempotency.
- `formatter_ndjson_test.go` — slice → multi-line, single object → one line, `WriteNDJSONRow`, `Auto` routing.
- `cmd_run_metadata_test.go` — `SetEffort` validation (5 levels + uppercase + whitespace + invalid), `ChatCreationBody` (default vs plan vs plan+effort), `ApplyPlanFlag` idempotency.

**Documentation:**

- README links to new commands inline (TODO: separate `docs/cli/` page in a follow-up).
- This CHANGELOG entry doubles as the design rationale for each addition.

### Added — Routines: Eval framework (PR follow-up to #281–#284)

Cross-tier consistency framework that makes routines a credible **agentic-program primitive**. Three new pieces and one resurrected runner:

- **13 eval scenarios** seeded under the `eval-` prefix (`cmd/crewship/seeddata/eval_scenarios.go`). Each is a normal routine with rigorous gates — no special test-mode code path. Categories covered: pure transformation × 2, classification, format compliance, reasoning chain, prompt-injection refusal, RAG faithfulness, cross-family LLM judge, cost guardrail, boundary handling, DAG trajectory, idempotency / concurrency, tier-escalation loop. Cross-family graders (Sonnet judges Haiku) mitigate self-preference bias on rubric-graded scenarios.
- **`crewship eval scenarios`** — batch runner: sweep eval-* routines × tier list × N runs, output pass-rate matrix in `table` / `json` / `yaml` / `markdown`. Use `--scenarios slug,slug` to scope, `--tiers fast,smart` to compare worker tiers, `--runs N` for variance, `--fail-fast` for early-exit on regression.
- **`crewship eval compare <slug>`** — head-to-head: run ONE scenario back-to-back on two tiers, report a verdict (`AGREE-PASS` / `AGREE-FAIL` / `DIVERGE-A-PASS` / `DIVERGE-B-PASS` / `AMBIGUOUS`) plus side-by-side outputs. Designed for *gate-pass agreement*, not text identity (two LLM runs are essentially never byte-identical).
- **`tier_override` field on `RunInput`** + JSON body `{"tier_override":"..."}` on the `/run` endpoint. Replaces every `agent_run` step's `complexity` for the duration of one run; step-level `model_override` still wins. Plumbed through CLI as `crewship routine run --tier-override fast|smart|...`.
- **JSON Schema gate enforcement** in `internal/pipeline/executor.go validateOutput`. Previously a no-op (`"documentation only"`); now uses `github.com/santhosh-tekuri/jsonschema/v5` (draft 2020-12). Distinct reason prefixes per failure class: `schema invalid:` (author bug), `output not valid JSON:` (worker didn't follow contract), `schema validation:` (output failed constraints).
- **LLMRunner restored** (`internal/pipeline/runner_llm.go`) as opt-in fallback. Removed in commit `8408f3e6` when OrchestratorRunner shipped; restored here so the eval suite is runnable on a workstation without a fully provisioned crew container stack. Selection at boot: `CREWSHIP_PIPELINE_RUNNER=llm_direct` (explicit override) → `--no-docker` (auto-fallback) → OrchestratorRunner (default; production unchanged).
- **`schemas/routine.v1.json`** picks up `outcomes`, `concurrency_key`, `max_concurrent` so IDE validation matches the server-accepted DSL surface.

Tests: 8 schema-gate cases, 9 tier-override sub-cases, 10 eval-CLI helper tests, 13 eval-scenario parse+validate tests — 40 new test cases total, all under `-race`.

### Added — Routines (PR #281 + #282)

Routines are AI-authored, workspace-scoped declarative workflow recipes — the substrate that replaces fragmented Ansible / Terraform / Airflow / n8n / Zapier / cron / Slack-bot / SOP stacks with one declarative recipe layer that any crew can invoke. Authored once (preferably by a smart model) and executed many times by the cheaper runtime tier.

User-facing label is **Routine**; backend identifiers (`pipelines` table, `internal/pipeline` package, `/api/v1/.../pipelines/...` HTTP routes) remain unchanged for backwards compat. Three-layer architecture: **Routine** (atomic) → **Recipe** (Marketplace template, future) → **Cyclic Issue** (recurring user issue, future).

#### Frontend

- **New `/routines` page** as a clone of `/orchestration`. 3-column layout: filter sidebar with saved-view facets (status / usage / authored-by / show ephemeral), 4 main tabs (Routines list / Graph / Timeline / Activity), right detail panel with 7 sub-tabs (Overview / Editor / Runs / Versions / Schedules / Webhooks / Waitpoints).
- **Sidebar entry** *Routines* under *Work* (icon `ScrollText`).
- **Orchestration tab** *Routines* — 5th tab in `/orchestration` for in-context discovery, reusing the existing detail sheet so users don't lose mission context.
- **DSL editor dialog** — paste/edit JSON with 3 starter templates. **Test & Save** runs `/test_run` first; on pass calls `/save`. Skip-test-gate checkbox surfaces only for OWNER/ADMIN roles.
- **Run / Test Run / Dry Run / Cancel** action toolbar.
- **Live waterfall** — Runs sub-tab subscribes to `pipeline.step.*` WebSocket events; auto-expands the most recent run on first visit.

#### Backend

Five database migrations: v78 (`pipelines` + `workspaces.execution_tiers_json`), v79 (`pipeline_versions` + `pipeline_waitpoints`), v80 (`pipeline_schedules`), v81 (`pipeline_run_idempotency`), v82 (`pipeline_webhooks`).

- **6 step types**: `agent_run`, `call_pipeline`, `http`, `code`, `wait`, `transform`.
- **DAG with `needs[]`** — independent steps execute in parallel; leaf-node final-output preference for multi-leaf graphs.
- **Conditional `if`** — any step can carry a template-rendered boolean; false → step skipped.
- **Two-tier execution** — workspace `execution_tiers_json` resolves `complexity` annotation to `(adapter, model)`; tier override flows through to the CLI adapter's `--model` flag.
- **Versioning + rollback** — every save creates a new immutable version; rollback creates a new HEAD pointing at the target's definition.
- **HITL waitpoints** — DB-backed approval primitive with timeout sweeper and boot-time recovery scan reporting stranded entries.
- **Cron schedules** + **HMAC-signed webhooks** + **idempotency keys** for safe redelivery.
- **Bundle export/import** for cross-workspace transfer.
- **Workspace-scoped `POST /api/v1/workspaces/{ws}/pipelines/save`** for UI authoring (MANAGER+ role); `skip_test_gate` flag honoured only for OWNER/ADMIN.
- **8 stability bug fixes** with regression tests under `-race`: DAG completion bookkeeping, multi-leaf output picker, waitpoint lost-wakeup, webhook rate-limiter race, idempotency stale-row leak, SSRF-via-redirect, cross-workspace agent execution, template validation breadth, exponential-backoff jitter.

#### CLI (17 routine subcommands)

| Group | Commands |
|-------|----------|
| Core | `list`, `get`, `save`, `run`, `dry-run`, `delete`, `runs` |
| Versions | `versions`, `rollback --to N` |
| Bundles | `export [--include-history]`, `import [file.json]` |
| Runs | `cancel`, `watch [--json] [--once]` |
| Authoring | `validate [file.json]` (offline DSL check, CI-friendly) |
| Schedules | `list`, `create`, `update`, `enable`, `disable`, `now`, `delete` |
| Webhooks | `list`, `create`, `url`, `delete` |
| Waitpoints | `list`, `show`, `approve`, `reject` |

The `pipeline` alias is preserved — every `crewship routine X` invocation also works as `crewship pipeline X`.

#### Documentation

- `docs/guides/routines.mdx` — user guide (concepts, three authoring paths, DSL anatomy, all step types, two-tier execution, triggers, HITL, validation gates, observability, RBAC, troubleshooting).
- `docs/cli/routine.mdx` — per-subcommand reference.

#### Seeded routines

`./dev.sh seed` now populates 5 starter routines on a fresh workspace: `summarize-text`, `fetch-and-summarize`, `pr-review-structured`, `daily-status-digest`, `incident-triage`. Each is independently runnable with default inputs.

## [0.1.0-beta.1] — 2026-05-03

First public beta. Self-hosted runtime for AI coding agents — every crew
gets its own Linux container, with cost budgets, approval gates, audit
logs, and encrypted credential vault out of the box.

### Added

- Self-hosted runtime: single Go binary with embedded Next.js UI, embedded
  SQLite DB, and a sidecar proxy for credential injection.
- Crew Journal — append-only event stream as canonical source of truth
  for every observable action; FTS5 search; SSE streaming to the
  `/journal` UI.
- Paymaster — hierarchical LLM cost budgets (workspace → crew → mission →
  agent), per-call ledger written before the LLM request leaves the box.
- Lookout — guardrails: prompt-injection detection, JSON-schema tool-arg
  validation, output parsing, secrets redaction.
- Harbormaster — human-in-the-loop approval queue with sync and async
  modes, configurable timeouts, full decision history.
- Cartographer — checkpoint/fork/restore over journal cursor; non-
  destructive restore returns divergence warnings instead of mutating.
- Quartermaster — eval suite with trajectory replay, regression detection,
  and an LLM-as-judge that uses rubric-shuffle anti-bias.
- Hooks framework — 15 lifecycle event types with shell, HTTP, and
  subagent handlers; `allowedShell=true` required at register time.
- Backup — AGE-encrypted, portable `.tar.zst` bundles at workspace and
  crew scope; retention rotation; advisory locking; forward-compatible
  manifest.
- Keeper — credential gatekeeping with AES-256-GCM versioned encryption
  and an Ollama-backed LLM evaluating per-request access.
- Multi-runtime container support — auto-detection of Docker, Podman,
  Colima, OrbStack, Rancher, nerdctl. Apple Containers on macOS Tahoe+.
- CLI adapters — Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor
  CLI, Factory Droid, all wired into the orchestrator dispatch table.
- Crew templates — Engineering, Quality, DevOps, and Research crews seed
  ready out of the box; `crewship template apply <slug>` to deploy.
- Issue tracker — Linear-style with labels, projects, sub-issues, and
  bulk operations; `crewship issue …` CLI.
- Multi-workspace support; OWNER/ADMIN/MANAGER/MEMBER/VIEWER server-side
  RBAC enforcement (UI for tier assignment ships in v0.2).
- OpenTelemetry GenAI spans with W3C trace-context propagation; OTLP HTTP
  exporter; every journal entry carries `trace_id`/`span_id`.
- Devcontainer provisioning with mise-managed runtimes, shared cache
  images, and 24-hour registry-digest checks.
- Per-IP rate limiting (10 req/min on auth endpoints, 120 req/min on the
  general API), security headers, single-use OAuth state with 15 min
  expiry.
- Goreleaser pipeline: cross-compiled binaries (Mac amd64+arm64, Linux
  amd64+arm64, Windows amd64), keyless cosign signatures, SPDX +
  CycloneDX SBOMs, Homebrew tap auto-publish.

### v0.2 roadmap

The following ship as packages but are not yet auto-wired into the
runtime in v0.1; they activate via manual API calls today and become
default behaviour in v0.2:

- Episodic memory — vector recall over the journal (selective embedding,
  SQLite BLOB cosine).
- Consolidate — daily Consolidator that extracts learned rules into
  crew memory + Compactor that rolls up low-signal old entries.

The following are planned for v0.2 but not in v0.1 at all:

- PostgreSQL primary database (SQLite is the only supported backend in
  v0.1).
- Kubernetes container provider.
- Skills marketplace (local skill imports work today).
- Workspace-scope memory tier (3-tier today: agent, crew, session).
- Stripe-backed billing tiers / edition gating (v0.1 ships fully
  Apache-2.0 with no edition gating).
- UI for assigning ADMIN/MANAGER/VIEWER workspace roles (server-side
  enforcement is already wired).
- Crew-to-crew handoff with critique exchange.

### Notes

- This is the first tagged release. Public APIs and data models may
  still change in `0.x` minor versions before `1.0`. Pin a commit SHA or
  a specific `v0.x.y` tag if you ship to production.
- The `release` branch tracks deployable state (a 5-minute systemd timer
  on the dogfood prod VM polls it). Push `main:release` to deploy.

[Unreleased]: https://github.com/crewship-ai/crewship/compare/v0.1.0-beta.1...HEAD
[0.1.0-beta.1]: https://github.com/crewship-ai/crewship/releases/tag/v0.1.0-beta.1
