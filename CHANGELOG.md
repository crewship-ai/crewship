# Changelog

All notable changes to Crewship are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 releases may introduce breaking changes in minor versions
(`0.x.0`); patch releases (`0.x.y`) are backwards-compatible fixes.

## [Unreleased]

(empty — next version's entries go here)

## [0.1.0-beta.1] — 2026-05-14

**First public beta release.** APIs and data models may break across
minor bumps until v1.0. See `RELEASING.md` for upgrade and rollback
guidance.

### TL;DR for beta testers

- Install: `brew install crewship-ai/tap/crewship` (macOS) or
  `docker pull ghcr.io/crewship-ai/crewship:v0.1.0-beta.1` (Linux/Docker).
- One adapter is production-ready in beta: **Claude Code (Anthropic)**.
  Codex / Gemini / OpenCode / Cursor / Factory Droid have scaffolds
  but lack parity testing — see README "Beta status & limitations".
- Telemetry (Sentry crash reporting) is **enabled by default** during
  v0.1 beta to give the solo maintainer signal from real installs.
  Disable any time with `crewship telemetry off`. Reverts to opt-in
  for v1.0 GA. See `RELEASING.md` Telemetry section.
- Storage is SQLite-only in v0.1; PostgreSQL is on the v0.2 roadmap.

### Added — Release infrastructure

- **Auto-snapshot before migrations.** `database.SnapshotBeforeMigrate`
  takes a `VACUUM INTO` copy as `<db>.pre-migrate-vN-to-vM-<UTC>.bak`
  whenever a migration is pending. Keeps 10 newest snapshots; opt out
  with `CREWSHIP_SKIP_MIGRATION_BACKUP=1`.
- **Migration lint in CI.** `.github/workflows/migration-lint.yml` +
  `scripts/lint-migrations` enforce append-only ordering — versions
  strictly increase, no rename of a version already shipped to `main`.
  In-tree Go tests guard monotonicity and uniqueness on every PR.
- **GHCR multi-arch Docker images.** linux/amd64 + linux/arm64,
  cosign keyless signed via GitHub OIDC. Tags published per release:
  `:vX.Y.Z`, `:vX.Y`, and `:latest` (last one ONLY on clean semver tags
  — pre-release tags never bump `:latest`).
- **Nightly channel.** `.github/workflows/nightly.yml` rebuilds on every
  push to `main`: `:nightly` and `:main-<sha>` Docker tags, plus a
  rolling `nightly` GitHub pre-release with prebuilt binaries.
- **One-line installer.** `scripts/install.sh` detects OS+arch, verifies
  sha256 + cosign signatures, installs to `~/.local/bin` (no sudo) or
  `/usr/local/bin`. Designed for `curl -fsSL https://crewship.ai/install | sh`.
- **Update notification.** `internal/update` queries GitHub Releases API
  daily (cached in `~/.crewship/cache`). CLI prints upgrade banner at
  startup; web UI surfaces a dismissable banner via
  `GET /api/v1/system/version`. Optional `GITHUB_TOKEN` to lift the
  60/h unauthenticated rate limit to 5000/h.
- **Sentry crash reporting (opt-out by default in beta).** New
  `internal/crashreport` package wraps `getsentry/sentry-go` behind a
  consent gate stored in `app_settings`. DSN injected at link time via
  ldflag from `SENTRY_DSN` GitHub Actions secret. Strict client-side
  scrubbing of headers, query strings, request bodies, User field, and
  device/runtime/culture contexts; server-side regex rules in Sentry UI
  cover email/Bearer/`sk-*`/`ghp_`/`xox*-` patterns in error messages.
  `CREWSHIP_SENTRY_DSN` env var redirects to a self-hosted/own Sentry.
- **`crewship telemetry on/off/status`** sub-commands manage consent at
  runtime; `status` shows the resolved endpoint host plus DSN source
  (vendor default vs env override). First-run prompt removed — beta
  default is enabled.
- **Sentry alert-rule provisioner** (`scripts/sentry-setup-alerts.sh`):
  idempotent bash script that calls the Sentry REST API to create the
  "New issue (beta)" and "Spike — 50+ events/hour" alert rules.
- **PR + repo hygiene.** Stale-bot workflow (issues 90d, PRs 44d, generous
  opt-out labels), PR template Migration Safety checklist,
  `scripts/setup-branch-protection.sh` one-shot for required checks +
  linear history. Hotfix runbook in `RELEASING.md`.
- **CODE_OF_CONDUCT.md** (Contributor Covenant 2.1 by reference) +
  `ee/README.md` scaffold for future dual-licensed enterprise add-ons.

### Added — Connectors (catalog → install → MCP)

- **`ConnectorCatalog`** tile-grid UI for browsing the bundled manifest
  catalog under `manifests/` (`feat/connector-catalog-impl`).
- **`SchemaForm`** five-field-type renderer (text/secret/select/toggle/
  number) with per-field validation and defaults.
- **`ConnectorConnectSheet`** wires SchemaForm into the install flow —
  validates inputs, persists credentials via the sidecar, hands off
  OAuth where applicable.
- **Backend connector handlers** — `ParseManifest`, `Validate`,
  `Resolve`, `MaterializeMCP`, `LoadAll`; HTTP routes for List / Get /
  Verify / Install (incl. credential persistence + OAuth handoff).

### Added — Auth + onboarding overhaul (PR #314)

Pre-beta sweep: account recovery, device pairing, split-screen
onboarding wizard, session-rotation + lockout primitives.

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

### Security — Pentest 2026-05-14 hardening pass

Internal pentest of `dev2` (`crewship-dev:8082`, build `a78e8ac`)
produced 11 findings across 7 surfaces. All fixes have PoCs that
confirm the bypass before and the block after (reports gitignored
under `.pentest-2026-05-14/`).

- **F-001 (HIGH):** SSRF in skills import via DNS-resolved hostname
  bypass — blocked.
- **F-002 (MEDIUM):** SSRF error messages leaked internal network
  state — generic error masking.
- **F-003 (MEDIUM):** `/metrics` exposed without auth — now gated.
- **F-004 (LOW):** Next.js SPA fallback masked 404 for sensitive paths.
- **F-005 (INFO):** Inconsistent path-traversal validation — unified.
- **F-006 (MEDIUM):** No backend Origin check on state-changing routes.
- **F-007 (HIGH):** Rate limiter bypassable via X-Forwarded-For
  rotation — IP resolution hardened.
- **F-009 (LOW):** Scrubber regex bypassable via zero-width characters.
- **F-011 (HIGH conditional):** Devcontainer features could request
  `Privileged` / dangerous `CapAdd` — denylist applied.
- **F-012 (MEDIUM):** `CREWSHIP_DISABLE_RATELIMIT=true` shipped in dev
  `.env.local`.
- **F-A1/A3/A4 (HIGH):** Workspace-IDOR on relations + parent_issue_id
  — workspace-scope enforcement.
- **F-B4 (LOW):** Capability-URL proxy leaked `Referer` to upstream.
- **G-002:** Memory injection guard hardening.

### Security — Pass-2 quickfixes

Four backlog items bundled (each <70 LOC, independently revertible):

- Sidecar credential reads now emit audit events.
- Emoji reactions XSS — payload validation tightened
  (`emoji_reaction_test.go` covers 24 cases including real XSS strings).
- `/admin/backups/metrics` redacted to drop cross-owner workspace IDs.
- WebSocket frames capped at 1 MiB; fan-out N-amplifier closed.

### Security — Supply chain

- All release artifacts signed with cosign keyless via GitHub Actions
  OIDC (SLSA-3-ish provenance chain). Verify with
  `cosign verify-blob --certificate-identity-regexp ...`.
- SBOMs in SPDX and CycloneDX shipped with every release.
- `migration-lint` CI gate prevents the rebase-collision class of
  schema-divergence bug that bricks customer DB on upgrade.
- Goreleaser builds are reproducible (`-trimpath`, fixed `GOFLAGS`).
- `gitleaks` + `govulncheck` + `grype` run on every PR via
  `.github/workflows/security.yml`.

### Changed

- **README** rewritten for honest beta status — every feature labeled
  ✅ stable / 🟡 early / 🚧 WIP. Adapter scaffolds for non-Anthropic
  CLIs explicitly marked WIP rather than equal-billing alongside the
  production-tested Claude Code adapter.
- **Distribution channels** documented in `RELEASING.md` — stable /
  beta / nightly with their respective Docker tag policies. `:latest`
  Docker tag only moves on clean semver tags; pre-releases NEVER
  overwrite `:latest`.
- **Hotfix workflow** documented in `RELEASING.md`: cherry-pick onto
  release branch, fix-forward (never untag), forward-port to `main`.

### Removed — Repo hygiene (PR #344, #348)

- `.claude/context/prd/*` and `.claude/context/wireframes/*` —
  ~52 000 lines of pre-implementation design docs untracked.
  Mintlify (`docs/`) is now the canonical user-facing docs source.
- `internal-docs/audit-archive/*`, `internal-docs/wireframes/*` —
  archived audit reports and HTML wireframes.
- `mockups/activity-rail-v{2,3}.html` — wireframes for the
  activity-rail feature shipped in #287.

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
