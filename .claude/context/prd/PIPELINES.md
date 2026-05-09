# Crewship Pipelines — MVP PRD

**Verze:** 1.1
**Datum:** 2026-05-07 (rev. evening — see §17)
**Status:** Implementation in `feat/pipelines-mvp` (#281); UI rename + page in `feat/routines-rename-and-page`
**Companion docs:** `ORCHESTRATION.md`, `CREW-EXECUTION.md`, `DATABASE.md`, `SIDECAR.md`
**Supersedes:** `MVP-ROBUST-FOUNDATION.md` (broader background-job MVP — replaced by this focused pipeline PRD per 2026-05-07 strategic narrowing)

> **READ FIRST — §17 captures all decisions made AFTER this PRD was originally written.** Sections 1–16 are historical record of original 9-day MVP scope. Real implementation extended beyond MVP by ~60% of Phase 2 backlog and adopted "Routines" as the user-facing name. §17 is the current source of truth.

---

## 0. Executive summary

Pipelines jsou **declarative DSL** pro repetitivní práci v Crewship workspaces. Authorováno chytrým modelem (Claude Opus 4.7), exekuováno levným (Haiku 4.5 / local Ollama). DSL je marketplace-portable JSON, persistuje v workspace, sdílí se napříč crews. Cross-crew reuse přes `[AVAILABLE PIPELINES]` block injectovaný do system promptu.

**Framing:** Crewship není „lepší Trigger.dev". Je to **Workspace-as-a-Product platforma**. Pipelines jsou substrate, který nahrazuje fragmented stack (Ansible + Terraform + Airflow + n8n + Zapier + Cron + Slack bots + custom skripty + manual SOPs) jednou deklarativní vrstvou s AI-driven authoringem.

**Co MVP dodá za 9 dní:**
1. `pipelines` table + DSL parser + validator
2. Two-tier executor: smart author tier (Opus) → cheap executor tier (Haiku/Ollama)
3. Validation gates mezi steps (JSON Schema + custom assertions)
4. Test-run gate před save (autor pipeline musí prokázat, že běží na execution tier)
5. Dry-run mode (Ansible `--check` ekvivalent — strukturovaný `would_X` report)
6. Pipeline composition (`call_pipeline` step + cycle detection)
7. Sidecar agent-facing endpoints + main API
8. `[AVAILABLE PIPELINES]` block v system promptu (cross-crew discovery)
9. Frontend: nový `pipelineRun` node v existujícím `WorkflowGraph` (React Flow)
10. End-to-end smoke test: Crew A authoruje → test_run → save → Crew B invokuje → run viditelný v Graph view

**Marketplace ne-MVP, ale architektura pro něj připravená:** CUID prefixy (`pln_`), workspace-scoped slugy s logical → physical mapováním, typed credential references (ne ID), declared egress allowlist v manifestu, versioned DSL schema (`dsl_version` field).

---

## 1. Goals / Non-goals

### Goals
1. AI agent může **autorovat pipeline** přes sidecar tool call, persistovat ve workspace, a další agenti ji **objevit + invokovat**.
2. Pipeline běží na **levnějším execution tier** než její autor, s **validation gates** chránícími před hloupostí executora.
3. **Dry-run** ukáže, co by se stalo, bez side-effectů — enterprise/marketplace trust foundation.
4. **Pipeline composition** umožní marketplace template, který je modulární, ne monolitický JSON megabajt.
5. **Frontend graph view** ukazuje pipeline runs jako uzly v existujícím Orchestration → Graph tabu, nodes propojené s issues/agenty.
6. End-to-end smoke test prokazuje cross-crew reuse pattern.

### Non-goals
- **Marketplace UI / public registry** — Phase 2.
- **Versioning** — pipeline edituje in-place, version history nepersisten v MVP. (Schema `dsl_version` field je pro forward compat.)
- **`http` step type** — Phase 2 (agent_run pokrývá MVP demo cases přes existující MCP/skill credentials).
- **`code` step type** (Python/Go skript v sandbox containeru) — Phase 2.
- **`wait` step type** (waitpoints/HITL) — Phase 2 (po waitpoints PRD).
- **Permissions** — `workspace_visible=true` default, žádné per-crew omezení v MVP.
- **Cost ceiling enforcement** — Phase 2 (vyžaduje token tracking infrastructure).
- **PII boundary** (`touches_pii: true` enforcement) — Phase 2.
- **Routines integrace** (`target_kind='pipeline'`) — Phase 2 (Routines rebuild je samostatný PRD).
- **Suspend-and-respawn** durable execution — Phase 4. MVP je in-process goroutine: restart binárky = pipeline run umírá, must re-trigger.

---

## 2. Architektura — high level

```
┌────────────────────────────────────────────────────────────────────────┐
│                           AUTHORING TIER                               │
│                       (Claude Opus 4.7 / GPT-5)                        │
│                                                                        │
│  Agent Crew A's lead: "save pipeline email-fetch-summarize"            │
│  ↓                                                                     │
│  POST http://localhost:9119/pipelines/save                             │
│   ↓ (sidecar)                                                          │
│  POST /api/v1/internal/pipelines/save (X-Internal-Token)               │
│   ↓ (main API)                                                         │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │ pipeline.Store.Save(def, authorMeta)                             │  │
│  │   1. parse DSL                                                   │  │
│  │   2. validate schema (dsl_version=1.0)                           │  │
│  │   3. validate references (agent_slug exists in author crew)      │  │
│  │   4. cycle detection if call_pipeline used                       │  │
│  │   5. require test_run within last 5 min OR run inline test_run   │  │
│  │   6. INSERT pipelines row                                        │  │
│  └──────────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────────┘
                                ↓ persist to DB
                       ┌─────────────────────┐
                       │  pipelines table    │
                       │  workspace-scoped   │
                       └─────────────────────┘
                                ↓ inject into system prompt
┌────────────────────────────────────────────────────────────────────────┐
│                          EXECUTION TIER                                │
│                  (Haiku 4.5 / Ollama llama3.2)                         │
│                                                                        │
│  Agent Crew B's lead sees [AVAILABLE PIPELINES] block, decides         │
│  to invoke `email-fetch-summarize` instead of improvising.             │
│  ↓                                                                     │
│  POST http://localhost:9119/pipelines/email-fetch-summarize/run        │
│   ↓                                                                    │
│  POST /api/v1/internal/pipelines/{id}/run                              │
│   ↓                                                                    │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │ pipeline.Executor.Run(ctx, def, inputs, mode)                    │  │
│  │   for each step:                                                 │  │
│  │     1. resolve template ({{ inputs.X }}, {{ steps.Y.output }})   │  │
│  │     2. resolve execution tier → adapter + model                  │  │
│  │     3. execute step (agent_run | call_pipeline)                  │  │
│  │     4. validate output against step.validation                   │  │
│  │     5. on validation fail → escalate tier OR abort               │  │
│  │     6. journal.emit(pipeline.step.completed)                     │  │
│  │     7. ws.hub.broadcast for graph view                           │  │
│  │   journal.emit(pipeline.run.completed)                           │  │
│  └──────────────────────────────────────────────────────────────────┘  │
│                                                                        │
│  Pipeline RUNS in author crew context (Crew A's credentials,           │
│  Crew A's agent slugs). Crew B is recorded as invoking_crew_id.        │
└────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Data model

### 3.1 Migration v78

```sql
CREATE TABLE pipelines (
  id                    TEXT PRIMARY KEY,           -- "pln_" + CUID
  workspace_id          TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  slug                  TEXT NOT NULL,              -- workspace-unique kebab-case
  name                  TEXT NOT NULL,
  description           TEXT,
  dsl_version           TEXT NOT NULL DEFAULT '1.0',-- forward compat
  definition_json       TEXT NOT NULL,              -- the DSL document
  definition_hash       TEXT NOT NULL,              -- sha256 of definition_json
  ephemeral             INTEGER NOT NULL DEFAULT 0, -- 1 = auto-generated delegation wrap, hidden from [AVAILABLE PIPELINES]
  workspace_visible     INTEGER NOT NULL DEFAULT 1, -- shown in [AVAILABLE PIPELINES]
  invocation_count      INTEGER NOT NULL DEFAULT 0,
  last_invoked_at       TEXT,
  last_invocation_status TEXT,                      -- COMPLETED | FAILED | NULL
  -- AUTHORSHIP (best-practice metadata per Linear/GitHub patterns)
  author_crew_id        TEXT REFERENCES crews(id) ON DELETE SET NULL,
  author_agent_id       TEXT REFERENCES agents(id) ON DELETE SET NULL,
  author_user_id        TEXT REFERENCES users(id) ON DELETE SET NULL,
  author_chat_id        TEXT,                       -- session context for Journal deeplink
  author_run_id         TEXT,                       -- agent_run that emitted this pipeline
  authored_via          TEXT NOT NULL CHECK (authored_via IN ('agent_tool_call','user_api','imported','seed')),
  imported_from_url     TEXT,
  -- TEST GATE
  last_test_run_at      TEXT,                       -- save endpoint requires fresh test_run
  last_test_run_passed  INTEGER NOT NULL DEFAULT 0,
  -- EXECUTION TIER (workspace fallback if NULL)
  execution_tier_json   TEXT,                       -- { "preferred": "fast", "fallback": ["smart"] }
  -- AUDIT
  created_at            TEXT NOT NULL DEFAULT (datetime('now','subsec')),
  updated_at            TEXT NOT NULL DEFAULT (datetime('now','subsec')),
  deleted_at            TEXT,
  UNIQUE(workspace_id, slug)
);
CREATE INDEX idx_pipelines_workspace      ON pipelines(workspace_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_pipelines_workspace_visible ON pipelines(workspace_id, workspace_visible) WHERE deleted_at IS NULL AND ephemeral = 0;
CREATE INDEX idx_pipelines_author_crew    ON pipelines(author_crew_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_pipelines_invocation_count ON pipelines(workspace_id, invocation_count DESC) WHERE deleted_at IS NULL;

-- Pipeline runs are logged into existing journal_entries with synthetic
-- entry types. No dedicated pipeline_runs table in MVP — keeps schema
-- footprint small and pipeline visibility free in Journal/Graph.
-- Reserved entry types:
--   pipeline.run.started     (run_id, pipeline_id, invoking_crew_id, mode, inputs_preview)
--   pipeline.step.started    (run_id, step_id, step_index, tier_used)
--   pipeline.step.completed  (run_id, step_id, output_preview, duration_ms, cost_usd)
--   pipeline.step.validation_failed (run_id, step_id, reason, escalation_action)
--   pipeline.step.failed     (run_id, step_id, error_class, error_message)
--   pipeline.run.completed   (run_id, total_duration_ms, total_cost_usd)
--   pipeline.run.failed      (run_id, failed_at_step, error_message)

-- Workspace settings extension for execution tier mapping
ALTER TABLE workspaces ADD COLUMN execution_tiers_json TEXT NOT NULL DEFAULT '{}';
-- Default seeded by migration:
-- { "trivial":  { "primary": {"adapter":"claude","model":"claude-haiku-4-5-20251001"} },
--   "fast":     { "primary": {"adapter":"claude","model":"claude-haiku-4-5-20251001"}, "fallback":[{"adapter":"claude","model":"claude-sonnet-4-6"}] },
--   "moderate": { "primary": {"adapter":"claude","model":"claude-sonnet-4-6"} },
--   "smart":    { "primary": {"adapter":"claude","model":"claude-opus-4-7"} } }
```

### 3.2 Migration semantics
- **Forward:** create table + 4 indexes + ALTER workspaces. Backfill workspace.execution_tiers_json with default JSON.
- **Rollback:** DROP TABLE pipelines + ALTER DROP execution_tiers_json. No data loss for non-pipeline workspace state.
- **Backup compat:** restoreBackfillFor(78) returns nil — pipelines table is data-bearing, restore is full-row.

---

## 4. DSL specification

### 4.1 Top-level shape

```jsonc
{
  "dsl_version": "1.0",
  "name": "email-fetch-summarize",            // slug-friendly
  "display_name": "Email Fetch & Summarize",
  "description": "Fetch new emails since a date and summarize.",
  "execution_tier": {                          // optional, falls back to workspace default
    "preferred": "fast",
    "fallback": ["moderate", "smart"]
  },
  "inputs": [
    { "name": "since",      "type": "string",  "required": false, "default": "yesterday", "description": "ISO date or 'yesterday'/'today'" },
    { "name": "max_emails", "type": "integer", "required": false, "default": 50,          "min": 1, "max": 500 }
  ],
  "outputs": [
    { "name": "summary",       "type": "string" },
    { "name": "email_count",   "type": "integer" }
  ],
  "estimated_cost_usd": 0.003,                 // author tier estimate, validated at test_run
  "estimated_duration_seconds": 12,
  "egress_targets": ["api.gmail.com"],         // declared egress for static analysis
  "credentials_required": [
    { "type": "gmail",      "scope": "read" },
    { "type": "anthropic",  "scope": "any"  }
  ],
  "steps": [ ... ]
}
```

### 4.2 Step types (MVP)

#### `agent_run`
```jsonc
{
  "id": "fetch",                               // unique within pipeline
  "type": "agent_run",
  "agent_slug": "email-reader",                // resolved in author crew context
  "complexity": "trivial",                     // trivial | fast | moderate | smart
  "model_override": null,                      // optional explicit model pin
  "prompt": "Fetch emails since {{ inputs.since }}, max {{ inputs.max_emails }}, return JSON list with from/subject/body fields.",
  "timeout_seconds": 60,
  "validation": {
    "schema": {                                // JSON Schema draft 2020-12 subset
      "type": "array",
      "minItems": 0,
      "maxItems": 500,
      "items": {
        "type": "object",
        "required": ["from", "subject", "body"],
        "properties": {
          "from":    { "type": "string", "format": "email" },
          "subject": { "type": "string", "maxLength": 998 },
          "body":    { "type": "string" }
        }
      }
    },
    "must_not_contain": ["API_KEY=", "Bearer ", "credential leaked"],
    "on_validation_fail": "escalate_tier"      // escalate_tier | abort | retry_step
  }
}
```

#### `call_pipeline`
```jsonc
{
  "id": "review_summary",
  "type": "call_pipeline",
  "pipeline_slug": "human-approval-step",
  "inputs": {                                  // template-substituted before call
    "content": "{{ steps.summarize.output }}",
    "approver_role": "marketing_lead"
  },
  "timeout_seconds": 600
}
```

Cycle detection at save time: build call graph, reject if any cycle. Recursive limit at runtime: max depth 10.

### 4.3 Template substitution

Simple regex substitution, **NOT** an expression evaluator (security):
- `{{ inputs.X }}` → input value
- `{{ steps.Y.output }}` → previous step's output (full string)
- `{{ steps.Y.output.path }}` → JSON path into previous step's output (only for outputs that parse as JSON)
- `{{ env.AUTHOR_CREW_NAME }}` → execution context metadata (read-only allowlist of safe keys)

Anything else (function calls, arithmetic, conditionals) → validation error at parse time. Use steps for logic, not templates.

### 4.4 Validation language

JSON Schema draft 2020-12 **subset**. Supported:
- `type` (string/number/integer/boolean/array/object/null)
- `required`, `properties`, `items`, `additionalProperties`
- `minLength`, `maxLength`, `minimum`, `maximum`, `minItems`, `maxItems`
- `pattern` (regex), `format` (email, uri, date, date-time)
- `enum`, `const`

Custom Crewship extensions:
- `must_not_contain`: array of strings — output must not contain any
- `must_contain`: array of strings — output must contain all
- `min_length`, `max_length` on string output (alternative to schema for non-JSON outputs)

Library: `github.com/santhosh-tekuri/jsonschema/v5` (already in go.mod or trivial add).

### 4.5 Execution tier semantics

Step `complexity` field values: `trivial`, `fast`, `moderate`, `smart`. Workspace settings `execution_tiers_json` map each to `{primary, fallback}`. Runtime resolves at step start:

```
step.model_override → if set, use directly
step.complexity     → workspace.execution_tiers_json[complexity]
                       try primary; if validation fails and on_validation_fail=escalate_tier,
                       try fallback[0], fallback[1], ... fail after exhaustion
```

Pipeline `execution_tier.preferred` overrides per-step `complexity` if set (uniform tier across pipeline). Step `model_override` always wins.

### 4.6 Dry-run mode

When `dry_run=true`:
- Executor walks DSL, renders templates with inputs
- Returns structured `would_execute` report **without** invoking any agent
- For `agent_run`: returns `{ step_id, would_call_agent, would_pass_prompt, estimated_tokens, estimated_cost_usd }`
- For `call_pipeline`: recurses into nested pipeline's dry-run
- Validation gates checked against schema (not against output, since no output)
- Total `estimated_cost_usd`, `estimated_duration_seconds`, full `egress_targets` list, `credentials_used` list
- No journal entries (or emit single `pipeline.dry_run` entry for audit)

---

## 5. Two-tier execution & test-run gate

### 5.1 Test-run gate before save

Save endpoint (`POST /pipelines/save`) requires fresh test_run within last 5 minutes. If absent, save inline-runs `test_run` first.

Test_run flow:
1. Author submits DSL via `POST /pipelines/test_run` body `{ definition, sample_inputs }`
2. Executor runs pipeline at `execution_tier.preferred` (NOT author tier)
3. If success + all validations pass → store test_run hash in author session, mark pipeline OK to save
4. If failure → return structured error to author with `step_id`, `failed_validation`, `output_preview`
5. Author (Opus) reads error, revises DSL, retries

This is the **self-improvement loop**: author model adapts pipeline complexity to executor model capabilities. Without this, MVP ships brittle pipelines.

### 5.2 Save endpoint behavior

```
POST /pipelines/save body: { definition, sample_inputs?: {} }

if last_test_run_passed (within 5 min):
    proceed to insert
else:
    run test_run inline
    if test passes: proceed
    if test fails: return 422 with detailed error
```

### 5.3 Save endpoint response

```jsonc
{
  "id": "pln_01HX...",
  "slug": "email-fetch-summarize",
  "test_run": {
    "passed": true,
    "duration_ms": 4230,
    "cost_usd": 0.0028,
    "tier_used": "fast",
    "validation_results": [
      { "step_id": "fetch",     "passed": true },
      { "step_id": "summarize", "passed": true }
    ]
  }
}
```

---

## 6. API surface

### 6.1 Sidecar agent-facing (port 9119, X-Internal-Token authed via existing IPC)

```
POST /pipelines/save
  body: { definition, sample_inputs? }
  → wraps with author_crew_id/author_agent_id from IPC config
  → POSTs to main API /api/v1/internal/pipelines/save
  → returns { id, slug, test_run }

POST /pipelines/test_run
  body: { definition, sample_inputs? }
  → ephemeral test, no save, no DB write
  → returns { passed, validation_results, output, cost_usd, duration_ms }

GET  /pipelines
  query: ?include_ephemeral=false (default)
  → returns workspace-visible pipelines for the calling agent's workspace
  → format optimized for [AVAILABLE PIPELINES] block (slim per-row)

POST /pipelines/{slug}/run
  body: { inputs?, dry_run?: false }
  → invokes pipeline (sync, blocks until complete; async via ?async=true)
  → records invoking_crew_id from IPC
  → returns { run_id, status, output, error?, journal_url }

POST /pipelines/{slug}/dry_run
  body: { inputs? }
  → structured would_X report; never invokes agents
  → returns { would_execute: [...], estimated_cost_usd, egress_targets, credentials_used, validation_warnings }
```

### 6.2 Main API (workspace-scoped, JWT authed)

```
GET    /api/v1/workspaces/{ws}/pipelines
GET    /api/v1/workspaces/{ws}/pipelines/{slug}
POST   /api/v1/workspaces/{ws}/pipelines/{slug}/run        (manual UI invoke)
POST   /api/v1/workspaces/{ws}/pipelines/{slug}/dry_run    (UI preview)
POST   /api/v1/workspaces/{ws}/pipelines/test_run          (UI author flow, MVP optional)
PATCH  /api/v1/workspaces/{ws}/pipelines/{slug}            (in-place edit; requires fresh test_run)
DELETE /api/v1/workspaces/{ws}/pipelines/{slug}            (soft delete)
GET    /api/v1/workspaces/{ws}/pipelines/{slug}/runs       (list runs from journal)
GET    /api/v1/workspaces/{ws}/pipelines/{slug}/runs/{run_id}  (run detail from journal)
```

### 6.3 Internal API (IPC, X-Internal-Token authed, called by sidecar)

```
POST /api/v1/internal/pipelines/save
POST /api/v1/internal/pipelines/test_run
GET  /api/v1/internal/pipelines
POST /api/v1/internal/pipelines/{id}/run
POST /api/v1/internal/pipelines/{id}/dry_run
```

---

## 7. System prompt injection

Append to existing prompt-builder in `agent_config_resolver.go` after `[SKILLS AVAILABLE]`:

```
[AVAILABLE PIPELINES]
You can invoke saved workspace pipelines instead of improvising repetitive work.

To LIST available pipelines:
  GET http://localhost:9119/pipelines

To INVOKE a pipeline:
  POST http://localhost:9119/pipelines/{slug}/run
  body: { "inputs": {...} }

To DRY-RUN a pipeline (preview without side effects):
  POST http://localhost:9119/pipelines/{slug}/dry_run
  body: { "inputs": {...} }

To SAVE a new pipeline (when you discover a repetitive pattern):
  POST http://localhost:9119/pipelines/save
  body: { "definition": {...DSL...}, "sample_inputs": {...} }
  Note: save will run test_run first; if test fails, fix and retry.

Currently registered pipelines in this workspace (top 10 by usage):

- slug: email-fetch-summarize
  description: Fetch new emails since a date and summarize.
  inputs: since (string?), max_emails (integer?)
  authored by: Crew Marketing · agent marketing-lead
  used by: 3 crews · 47 invocations · last status: COMPLETED

- slug: pr-review-structured
  description: Review a PR diff and post structured feedback.
  inputs: pr_url (string)
  authored by: Crew Engineering · agent qa-lead
  used by: 5 crews · 128 invocations · last status: COMPLETED

[END AVAILABLE PIPELINES]
```

Block omitted if zero workspace-visible pipelines exist (no empty header).

---

## 8. Frontend integration — Graph view

Reuse existing `components/features/orchestration/workflow-graph.tsx` (`@xyflow/react` + `@dagrejs/dagre`).

### 8.1 New node type

```tsx
// components/features/orchestration/pipeline-run-node.tsx
nodeTypes: NodeTypes = {
  agent: AgentNode,
  agentCard: AgentCardNode,
  crew: CrewGroupNode,
  pipelineRun: PipelineRunNode,    // NEW
}
```

`PipelineRunNode` shows:
- Pipeline name + slug
- Run status (running / completed / failed) — pulsing indicator if running
- Step progress (e.g. `2/4 steps`)
- Tier used (Haiku / Opus / Llama chip)
- Click → opens side-sheet with full step-by-step trace from journal

### 8.2 Edge connections

When agent invokes a pipeline:
- Edge from invoking agent's run node → pipelineRun node
- If pipeline calls nested pipeline → edge between parent pipelineRun → child pipelineRun

### 8.3 Live updates via WS

WS hub broadcasts `pipeline.run.started` / `pipeline.step.completed` / `pipeline.run.completed` on `workspace:{id}` channel. `WorkflowGraph` subscribes via existing `useRealtimeEvent` hook, updates node status without remount.

### 8.4 Graph layout integration

Existing `workflow-graph-builders.ts` adds `buildPipelineNodes(missions, runs, pipelines)`. PipelineRun nodes positioned via dagre next to their invoking agent node.

---

## 9. Smoke test (MVP gate)

`internal/pipeline/smoke_test.go` E2E test:

```go
func TestPipelinesSmokeE2E_CrossCrewReuse(t *testing.T) {
    // 1. Setup workspace WS
    // 2. Setup Crew A with Lead + email-reader agent + summarizer agent
    //    Setup Crew B with Lead only (no email-reader)
    //    Both crews share workspace_id, no cross-crew connection
    
    // 3. Crew A's lead saves pipeline via sidecar
    //    POST /pipelines/save with valid DSL referencing Crew A's email-reader
    //    assert: pipeline row created, author_crew_id=crew_a, last_test_run_passed=1
    
    // 4. Sanity: GET /pipelines from Crew B's lead returns the pipeline
    //    assert: response includes email-fetch-summarize with workspace_visible=true
    
    // 5. Crew B's lead invokes pipeline via sidecar with sample inputs
    //    POST /pipelines/email-fetch-summarize/run inputs={since:"2026-05-01"}
    //    assert: status=COMPLETED, output non-empty
    //    assert: invoking_crew_id=crew_b in journal entries
    //    assert: pipeline.invocation_count == 1, last_invoked_at != null
    //    assert: agent that ran step "fetch" was Crew A's email-reader (not Crew B's)
    
    // 6. Verify journal entries sequence:
    //    pipeline.run.started → step.started(fetch) → step.completed(fetch)
    //    → step.started(summarize) → step.completed(summarize) → run.completed
    
    // 7. Dry-run validation
    //    POST /pipelines/email-fetch-summarize/dry_run inputs={since:"2026-05-01"}
    //    assert: would_execute has 2 steps, estimated_cost_usd > 0, no journal write
    //    assert: NO actual agent invocation (mock check counter)
    
    // 8. Test-run failure path
    //    POST /pipelines/save with intentionally broken DSL (validation fail)
    //    assert: 422 returned with structured error pointing to failing step
    //    assert: NO pipelines row created
}
```

Test uses **mocked orchestrator.RunAgent** to avoid spinning real Docker containers — pipeline executor calls a fake that returns deterministic outputs. Real adapter integration tested separately in adapter-level tests.

---

## 10. File-by-file implementation plan

### New files

| Path | Purpose | LOC est |
|---|---|---|
| `internal/database/migrate_consts_v77_v78_pipelines.go` | v78 migration definition (or extend existing) | 60 |
| `internal/pipeline/store.go` | DB CRUD + workspace-scoped queries | 250 |
| `internal/pipeline/dsl.go` | DSL parse + validate + cycle detection + template substitution | 350 |
| `internal/pipeline/tier.go` | Execution tier resolution against workspace settings | 100 |
| `internal/pipeline/executor.go` | Sequential runner: agent_run + call_pipeline + validation gates + dry-run | 400 |
| `internal/pipeline/journal.go` | Pipeline-specific journal entry helpers | 80 |
| `internal/pipeline/store_test.go` | Unit tests for store | 150 |
| `internal/pipeline/dsl_test.go` | Unit tests for parse/validate/template | 250 |
| `internal/pipeline/executor_test.go` | Unit + integration tests for executor (with mocked orchestrator) | 200 |
| `internal/pipeline/smoke_test.go` | E2E cross-crew reuse smoke test | 200 |
| `internal/pipeline/system_prompt.go` | `[AVAILABLE PIPELINES]` block builder | 80 |
| `internal/api/pipelines.go` | 7 main API handlers | 200 |
| `internal/sidecar/pipelines.go` | 5 sidecar handlers | 180 |
| `components/features/orchestration/pipeline-run-node.tsx` | React Flow custom node | 120 |

### Modified files

| Path | Change |
|---|---|
| `internal/database/migrate.go` | Append v78 migration entry |
| `internal/api/router_routes.go` | Wire 7 pipeline routes |
| `internal/sidecar/server.go` | Add 5 pipeline route cases in `buildHandler` |
| `internal/api/agent_config_resolver.go` | Append `[AVAILABLE PIPELINES]` block call |
| `internal/api/internal.go` (if exists) | Wire internal API routes for sidecar→main forwarding |
| `components/features/orchestration/workflow-graph.tsx` | Register `pipelineRun` nodeType |
| `components/features/orchestration/workflow-graph-builders.ts` | Add `buildPipelineNodes` builder |
| `hooks/use-realtime.ts` (or similar) | Subscribe to `pipeline.*` events |

**Total new code:** ~2,520 LOC
**Total modified code:** ~150 LOC delta

---

## 11. Effort estimate

| Day | Component | Output |
|---|---|---|
| 1 | Migration v78 + Store + tests | Persistent layer ready |
| 2 | DSL parser + validator + cycle detection | DSL safe to ingest |
| 3 | DSL template substitution + tier resolution + tests | Run-time DSL evaluation |
| 4 | Executor (agent_run only) + journal integration | Single-step runs work |
| 5 | Executor (call_pipeline + validation gates + escalation) + dry-run mode | Composition + safety |
| 6 | Sidecar endpoints + main API endpoints | HTTP surface complete |
| 7 | System prompt injection + integration tests | Cross-crew discovery works |
| 8 | Frontend `pipelineRun` node + WS wiring | Graph view shows runs live |
| 9 | E2E smoke test + bugfixing + docs | Ship-ready |

Total: **9 days** for 1 BE engineer + ~1 day FE work parallel.

---

## 12. Out of scope — Phase 2 backlog

Following items intentionally deferred. Each has architectural hooks in MVP schema:

- **Versioning** — `pipeline_versions` immutable table, version pinning on call_pipeline + routine targets, change_summary required on update. (Schema field `dsl_version` covers MVP.)
- **`http` step type** — first non-LLM step type, with credential injection via sidecar proxy, declared egress allowlist enforcement.
- **`code` step type** — Python/Go/Bash execution in sandboxed container with timeout + memory cap + egress allowlist. Pavel's "DevOps crew with terraform/kubectl" demo unlock.
- **`wait` step type** — waitpoint primitive (token-based human approval, public_access_token CORS endpoint). Depends on separate Waitpoints PRD.
- **`emit_event` step type** — emit notifications to Inbox / Slack / Telegram / webhook.
- **Routines integration** — `target_kind='pipeline'` + `target_version` pinning on routines table.
- **Marketplace template export/import** — workspace bundle: crews + skills + pipelines + integration manifests + credential placeholders. JSON manifest format.
- **Permissions** — per-crew explicit allow list when `workspace_visible=false`. Default-deny model.
- **Cost ceiling enforcement** — per-pipeline `estimated_cost_usd` × workspace `daily_pipeline_cost_cap` runtime guard.
- **PII boundary** — `touches_pii: true` step flag enforces local-model-only execution.
- **Replay-with-same-input** — deterministic runs, regression suite, bisect "when did this break".
- **Multi-tenant isolation** — cross-workspace pipeline reuse with stricter trust boundary.
- **Pipeline marketplace UI** — public registry, install flow, marketplace economics.

---

## 13. Risks & mitigations

| Riziko | Severity | Mitigation |
|---|---|---|
| **In-process executor + binary restart = run dies** | Medium | Document constraint; recovery = manual re-trigger; Phase 4 = step-checkpointing |
| **Test-run gate creates author/executor model drift** | Medium | Test-run uses execution tier model, not author. Author retries pipeline until executor passes. |
| **Cycle in `call_pipeline` references** | High | Cycle detection at save time (build DAG, reject if SCC > 1). Runtime depth limit 10. |
| **Validation gate JSON Schema bypassed by malformed output** | Medium | Library `jsonschema/v5` + custom must_not_contain checks; on parse fail, treat as validation fail |
| **Template substitution opens injection vector** | High | Regex substitution only, NO eval. Allowlist of substitution patterns. Schema-validated output prevents unsafe template values. |
| **Two-tier escalation explodes cost** | Medium | Per-pipeline `estimated_cost_usd` + workspace `max_pipeline_run_cost_usd` runtime cap (Phase 2 enforcement; MVP: log warning if cost > 10× estimate) |
| **Cross-crew agent_slug resolution fails at runtime** | Medium | Save-time validation: all agent_slug refs must exist in author crew. Mid-run agent deletion → graceful pipeline_run.failed with clear error. |
| **Frontend Graph view performance with many pipeline runs** | Low | LOD: collapse runs older than 24h; limit visible runs to 100; pagination via existing journal queries |
| **Marketplace-not-yet means architecture choices may not fit later** | Medium | CUID prefix `pln_`, logical slug → physical ID mapping, typed credential refs, declared egress in manifest — all chosen specifically for marketplace compat. Versioning plan documented. |

---

## 14. Success metrics (post-MVP ship)

| Metric | Target | Measurement |
|---|---|---|
| **Smoke test passes in CI** | 100% | `go test ./internal/pipeline/...` green |
| **Cross-crew reuse demo works** | Demonstrable | Manual E2E: Crew A saves → Crew B invokes → Graph view shows |
| **Test-run gate catches >90% of brittle pipelines** | Empirical | Synthetic pipelines with known issues caught at test_run, not at first invocation |
| **Dry-run latency < 200ms** | p95 | Time from request to `would_execute` response (no agent calls) |
| **Pipeline-via-Haiku cost vs ad-hoc-via-Sonnet** | ≥ 5× cheaper | Synthetic 3-step pipeline run 100×, compare vs equivalent ad-hoc agent_run |
| **Graph view renders 50+ pipeline runs without jank** | Visual | Manual perf check on workspace with seed of 50 pipeline runs |

---

## 15. Open decisions captured

These were debated 2026-05-07 and **closed** for MVP:

- ✅ **Author-crew-context execution** (NOT caller-crew-context) — pipeline runs in Crew A's setup, Crew B is recorded as caller. This is the cross-crew reuse mechanic.
- ✅ **In-process goroutine executor** (NOT distributed queue + workers) — single binary scope, restart loses runs (acceptable for MVP).
- ✅ **JSON DSL** (NOT TypeScript / Python code) — static analysis, AI emission reliability, marketplace portability.
- ✅ **Two-tier execution as core MVP feature** (NOT Phase 2) — economics + reliability narrative is too important to defer.
- ✅ **Test-run gate mandatory before save** — brittle pipelines cannot enter workspace registry.
- ✅ **Dry-run mode mandatory** — Ansible `--check` ekvivalent, marketplace trust foundation.
- ✅ **call_pipeline step in MVP** — composition is architectural prerequisite for marketplace.
- ✅ **Local Ollama support in MVP** — `internal/llm/ollama.go` already exists, wiring is incremental.
- ❌ **Captain LLM as authoring infrastructure** — Captain doesn't exist as package. Use `internal/llm/` middleware directly.
- ❌ **Pipeline runs as separate table** — log to journal_entries with synthetic types. Phase 2 may extract.
- ❌ **`http` / `code` / `wait` step types in MVP** — agent_run + call_pipeline cover demo cases. Phase 2.

---

## 16. Sources & priors

- Trigger.dev competitive deep-dive (7-fork research, 2026-05-07): see memory `project_trigger_dev_competitive.md`
- AI-authored pipelines vision: see memory `project_ai_authored_pipelines_vision.md`
- Pipeline reuse across Crews vision: see memory `project_pipeline_reuse_vision.md`
- Codebase ground truth audit: see memory `project_codebase_ground_truth_2026_05.md`
- GitHub Actions YAML model + Ansible `--check` (dry-run) pattern as primary architectural inspirations.
- n8n workflow JSON pattern as runtime data model precedent.

---

## 17. Implementation status & design update (2026-05-07 evening)

This section is the **current source of truth**. Sections 1–16 capture original MVP scope and are kept as historical record; where they conflict with §17, §17 wins.

### 17.1 Naming: UI label = "Routines", backend stays "pipelines"

The user-facing label across web UI, CLI, agent system prompt and all marketing surfaces is **Routines**. The backend (database table, Go package `internal/pipeline`, HTTP routes `/api/v1/workspaces/{ws}/pipelines/...`, sidecar port 9119 path `/pipelines/...`) keeps the `pipelines` identifier — no breaking rename, no migration.

**Rationale:**

- "Pipeline" carries CI/CD baggage that confuses non-engineering users.
- Crewship has three distinct compositional layers and each needs its own term:
  - **Routine** — atomic, repeatable, deterministic AI automation (this feature). Ship-life metaphor: nudné rutinní úkoly, které musí být vždycky přesně stejné. Naval-context fit + accessible to non-technical users.
  - **Recipe** — Marketplace template that bundles `crew + agents + integrations + routines + skills`. Out of scope for this PR; reserved for the Marketplace work.
  - **(future) Cyclic Issue** — recurring user issue pattern. Term is now free since "Routines" took the autonomous-automation slot.
- GitHub precedent: table `issue_events`, UI says "Issues". Backend identifier stability + user-facing rename.

**Renames applied in this PR:**

| Surface | Before | After |
|---|---|---|
| Agent system-prompt block | `[AVAILABLE PIPELINES]` | `[AVAILABLE ROUTINES]` |
| Sidebar nav entry | (absent) | `Routines` (under `Work`) |
| Orchestration tab | (absent) | `Routines` (5th tab) |
| `/routines` route | (absent) | full clone of orchestration layout |
| CLI subcommand | `crewship pipeline ...` | `crewship routine ...` (with `pipeline` alias for back-compat) |
| Detail sheet header, run-node label, hook UI strings | `Pipeline` | `Routine` |

Identifiers that **do NOT change**: `pipelines` table, `internal/pipeline` package, Go types (`*Pipeline`, `*PipelineRun`), HTTP route paths, `pipeline_*` migration files (v78–v82), `pipeline.*` journal entry types, `pipeline.*` WS event types.

### 17.2 Beyond MVP — Phase 2 features that landed in #281

The §1 / §12 / §15 lists declared the following as Phase 2 / out-of-scope. They got built anyway during the same branch and are part of this PR. Status:

| Feature | Original status | Landed | Stability |
|---|---|---|---|
| Pipeline versioning + rollback | Phase 2 (`pipeline_versions`) | ✓ migration v79 + `versions.go` | Production-ready |
| `http` step type | Phase 2 | ✓ `runner_http.go` | **Has SSRF bug — see §17.5** |
| `code` step type | Phase 2 | ✓ `runner_code.go` | Optional wireup; rejects when sandbox not configured |
| `wait` step type + waitpoints | Phase 2 | ✓ `runner_wait.go` + `waitpoints.go` (DB-backed) | **Has lost-wakeup race — see §17.5** |
| `transform` step type | not listed | ✓ `runner_transform.go` (JSONPath / template) | Production-ready |
| Cron triggers | Phase 2 (Routines integration) | ✓ migration v80 + `schedules.go` | Single-instance only (no leader election) |
| Webhook triggers | not listed | ✓ migration v82 + `webhooks.go` (HMAC + rate limit) | **Has rate-limiter race — see §17.5** |
| Idempotency keys | not listed | ✓ `idempotency.go` + migration v81 | **Has stale-row leak — see §17.5** |
| Run registry (cancel + concurrency) | not listed | ✓ `run_registry.go` | In-memory only, no persistence across restart |
| Cost cap per run | Phase 2 | ✓ `executor.go` cost cap | De-prioritized per user direction; not blocking |
| Outcomes / rubric grading | Phase 2 | ✓ `outcomes.go` (separate grader agent) | Soft-fail-open on unparseable grader output |
| DAG with `needs:` parallel | not listed | ✓ `dag.go` | **Has completion-bookkeeping bug — see §17.5** |
| Conditional `if` | not listed | ✓ in `executor.go` | Production-ready |
| Export/Import bundles | Phase 2 (Marketplace) | ✓ in `pipelines.go` API | UI dialog deferred — CLI only for now |

Original scope was 9 days × 1 BE engineer + 1 day FE. Reality: ~17,200 LOC across 71 files, 23 commits. Backend over-delivered, frontend under-delivered (see §17.3).

### 17.3 Frontend page architecture — `/routines` as clone of `/orchestration`

#### 17.3.1 Why a new top-level page (not just a tab)

Routines are **workspace-scoped, cross-mission assets**. Schedules, webhooks, waitpoints fire autonomously regardless of which mission/issue is open. They deserve a dedicated surface like `/skills` or `/credentials`, not just a sub-view inside `/orchestration`.

#### 17.3.2 Layout = clone of `/orchestration`

The `/orchestration` page (`components/features/orchestration/orchestration-layout.tsx`) is a tested 3-column layout the user explicitly endorsed. `/routines` reuses the same skeleton:

```
┌──────────────────────────────────────────────────────────────────────┐
│ Toolbar: [Routines][Graph][Timeline][Activity]  +New  ⬇Import  ⚙   │
├──────────┬─────────────────────────────────────────┬──────────────────┤
│ LEFT     │ MAIN                                    │ RIGHT (drawer)   │
│ panel    │                                         │                  │
│          │ Tab Routines: list/board view           │ Detail panel     │
│ Saved    │   - filter (status, tags, owner)        │ for selected     │
│ views    │   - sort (last run, usage, name)        │ routine:         │
│          │                                         │                  │
│ Status:  │ Tab Graph: existing PipelineRunNode +   │ Overview         │
│ All      │   buildPipelineNodes (already wired)    │ Editor (Monaco)  │
│ Active   │                                         │ Runs (waterfall) │
│ Failed   │ Tab Timeline: routine runs over time    │ Versions         │
│          │                                         │ Schedules CRUD   │
│ Tags:    │ Tab Activity: filtered journal feed     │ Webhooks CRUD    │
│ ...      │   (entry_type LIKE 'pipeline.%')        │ Waitpoints (HITL)│
│          │                                         │                  │
│ + saved  │                                         │ Toolbar:         │
│ views    │                                         │ [Run][TestRun]   │
│          │                                         │ [DryRun][Cancel] │
└──────────┴─────────────────────────────────────────┴──────────────────┘
```

#### 17.3.3 Tabs in /routines (replaces orchestration's [Issues][Graph][Timeline][Activity])

| Tab | Content | Backend source |
|---|---|---|
| **Routines** | List/board with saved-view filters; click → detail in right panel | `GET /pipelines` |
| **Graph** | React Flow with `PipelineRunNode` only (no agent / mission nodes); shows live run status, cross-routine `call_pipeline` edges | `usePipelines` + WS `pipeline.run.*` |
| **Timeline** | Vertical timeline of runs across all routines, last 24h / 7d / 30d windows | `GET /pipelines/{slug}/runs` aggregated |
| **Activity** | Journal feed filtered to `pipeline.*` entry types | existing `journal_entries` query |

#### 17.3.4 Detail panel sub-tabs (right column, opens on routine select)

| Sub-tab | Content | Hook |
|---|---|---|
| **Overview** | Description, author, last run, total invocations, tier config, declared egress, credentials required | `usePipelines` |
| **Editor** | Monaco DSL view, read-only in this PR; write/save = future PR | inline JSON |
| **Runs** | Paginated runs list, filter by status, click → live waterfall with step-level WS events | `usePipelineRuns` + `pipeline.step.*` consumer |
| **Versions** | Immutable history, diff viewer, rollback button | existing `useVersions` |
| **Schedules** | List of cron schedules, add/edit/delete; cron expression input + human-readable preview | `usePipelineSchedules` (currently dead, wired in this PR) |
| **Webhooks** | List of webhook endpoints + tokens, regenerate token, delete | new `usePipelineWebhooks` |
| **Waitpoints** | Pending HITL approvals (inbox), approve/reject inline | new `usePipelineWaitpoints` |

#### 17.3.5 Sidebar nav

```
Work
  Dashboard
  Orchestration
  Crews
  Routines      ← NEW (icon: ScrollText / Repeat / Anchor)
Configure
  Skills
  Marketplace
  Credentials
  Integrations
Monitor
  Journal
System
  Settings
  Admin
```

`/routines` lives under **Work** (same level as Orchestration), not Configure — a routine is something you run + monitor, not something you configure once and forget.

#### 17.3.6 Existing `/orchestration` integration

The 5th tab `Routines` is also added to the existing orchestration layout (`ORCH_TABS`) for cross-context discovery. Inside orchestration, the Routines tab shows the same list but in a compact view; clicking a routine opens the existing `pipeline-detail-sheet.tsx` side-sheet (already implemented). This gives users a way to invoke a routine while staying in mission context.

The existing graph integration (`PipelineRunNode` rendered by `buildPipelineNodes` in `workflow-graph.tsx`) is preserved — pipeline runs invoked from within orchestration continue to render as nodes in the agent graph.

### 17.4 Step-level WS events — wire the consumer

`use-realtime.tsx` already typed `pipeline.step.started/completed/failed/validation_failed` events at line 40-46. Backend broadcasts them via `WSBroadcaster`. Until this PR, no client subscribed → broadcast into void.

The Runs sub-tab waterfall (§17.3.4) is the primary consumer: each step lights up live as the agent's CLI adapter completes it, no polling.

### 17.5 Known stability issues addressed in this PR

The branch shipped 7 real bugs alongside the new features. They're fixed in the same PR (under §17 stabilization commits):

1. **`dag.go:218-222` — DAG completes failed steps.** `wg.Wait()` loop unconditionally marks `completed[s.ID]=true` even when the step errored, causing downstream branches to advance with empty inputs. Fix: only mark completed when no error.
2. **`dag.go:243-249` — final output picker wrong for DAGs.** Picks last in source order; should pick the leaf node. Fix: traverse to find the unique terminal step.
3. **`waitpoints.go:113-149` — lost-wakeup race.** `WaitFor` registers listener after `checkDecided`; completer fire in between drops to `default`. Fix: register listener first, then re-check decided state.
4. **`webhooks.go:255-271` — rate-limiter lost update.** Counter `windows[key]` increment runs outside the mutex. Fix: hold the lock through both lookup and increment, or use `atomic.Int64` per key.
5. **`idempotency.go:66-104` — stale-row leak.** `DELETE expired` failure is swallowed; subsequent `SELECT` returns expired row as if live. Fix: propagate sweep error or filter `expires_at > now` in the SELECT.
6. **`runner_http.go:84-85` — SSRF via redirect.** `http.Client` follows redirects; allowlist checked only on initial host. Fix: install `CheckRedirect` callback that re-validates the redirected host against the egress allowlist.
7. **`runner_orchestrator.go:309` — cross-workspace agent execution.** Agent slug lookup omits workspace_id constraint. Fix: `JOIN crews ON crews.workspace_id = ?`.
8. **`dsl.go:317-321` — template validation incomplete.** Only `Prompt` and `NestedInputs` validated; templates in `step.HTTP.URL/Body/Headers`, `step.Wait.Until`, `step.Code.Code`, `step.Transform.*`, `step.If` skip validation. Fix: extend `validateTemplates` to walk all template-bearing fields.
9. **`executor.go:711-717` — backoff without jitter.** Exponential backoff causes thundering herd. Fix: add full jitter (random value in `[0, computed_delay)`).
10. **`waitpoints.go:31-34` — recovery scan promised but absent.** Pending waitpoints in DB after restart have no goroutine waiting on them. Fix: server boot calls `RecoverPending(ctx)` which re-attaches goroutines.

### 17.6 Persistence promise vs reality

§13 risk row 1 ("In-process executor + binary restart = run dies") is honest. But `bdca1fa "production-readiness"` and `c950a60 "production WaitpointStore"` commit messages overstate stability. Until pipeline runs persist as DB rows with checkpoint state, hard-kill loses active runs.

**This PR does NOT add full run persistence.** A dedicated `pipeline_runs` table with `status` column and `current_step_id` checkpoint is the right next-step but is scoped to a follow-up PR (`feat/routines-run-persistence`). For this PR:
- Recovery scan re-attaches goroutines for pending waitpoints (§17.5 #10) — partial protection
- Idempotency layer dedupes accidental re-trigger after manual recovery
- Documented honestly: hard-kill = active runs interrupted, journal logs `run.started` without matching `run.completed/failed`

### 17.7 Out of scope (still)

- Captain LLM integration as routine author orchestrator
- Marketplace UI for routines
- NL → cron expression converter (Foundation PRD)
- Errors fingerprinting + bulk replay (Foundation PRD)
- Auto-disable after N consecutive failures
- Inbound MCP server exposing routines as MCP tools
- Distributed scheduler with leader election
- `pipeline_runs` dedicated table with status enum + checkpoint
- Editor write-mode (Monaco read-only in this PR; `PATCH` exists in API but UI is view-only)

### 17.7.1 Shipped after PRD freeze

The following items were originally listed as deferred but landed during the iteration on this PR:

- **HMAC `save_token`** (commit `6b587fe`) — `/test_run` returns a signed token bound to (workspace_id, definition_hash, user_id, ts); `/save` verifies via HMAC instead of trusting body's `last_test_run_at`. Closes the test-gate body-trust loophole flagged earlier in this section. Backend + 10 unit tests + UI dialog wires the `save_token` from the test_run response into the save body.
- **JSON Schema for the DSL** (`schemas/routine.v1.json`) — IDE autocomplete + inline validation in any json-schema-aware editor. Sync test against runtime StepType constants prevents schema drift.
- **CLI parity** (18 routine subcommands) — schedules / webhooks / waitpoints / validate / watch / logs subcommands ship alongside the original 7. The `pipeline` alias is preserved for back-compat.
- **Three documentation surfaces** — `docs/guides/routines.mdx` (user guide), `docs/cli/routine.mdx` (CLI reference), `docs/guides/routines-migration.mdx` (compatibility/upgrade guide).

### 17.8 Cost cap deprioritization

Per user direction 2026-05-07 evening: cost cap implementation in this branch is **NOT a blocker**. Functional correctness, visibility, auditability, and CLI parity take priority. Cost-related UX (estimate vs actual, daily caps, alerting) is deferred to a dedicated PR after stabilization.

## 18. Eval suite — cross-tier consistency framework (2026-05-08)

This section documents the eval framework added to make routines a credible **agentic-program primitive**: a routine is only worth handing to a fleet of cross-crew callers when its output is *gate-passing on the cheapest tier that can satisfy it* — and that property has to be testable, regressionable, and bisectable when an upstream model swap regresses behaviour.

The framework adds three pieces and re-introduces one removed runner:

### 18.1 Eval scenarios (the test corpus)

13 routines seeded under the `eval-` prefix that exercise distinct LLM-step assertion classes. Each scenario is a normal routine — same DSL, same Save endpoint, same CLI surface — so no special-case "test mode" code path exists. The scenarios are simply *rigorously gated* routines with conservative defaults and cross-family rubric graders where appropriate.

Categories covered (`cmd/crewship/seeddata/eval_scenarios.go`):

| # | Slug | Class | Worker → Grader |
|---|---|---|---|
| 1 | `eval-extract-emails` | Pure transformation | daniel (fast) |
| 2 | `eval-extract-numbers-sorted` | Pure transformation + determinism | filip (fast) |
| 3 | `eval-classify-sentiment` | 3-way classification | daniel → eva (rubric) |
| 4 | `eval-json-extract-order` | Format compliance | viktor (fast) |
| 5 | `eval-syllogism-reasoning` | Reasoning chain | filip (fast) |
| 6 | `eval-refuse-prompt-injection` | Adversarial / refusal | jakub (fast) |
| 7 | `eval-faithfulness-rag` | RAG faithfulness | filip → lucie (rubric) |
| 8 | `eval-judge-cross-family` | LLM-as-judge | daniel → eva (rubric) |
| 9 | `eval-cost-budget-haiku` | Cost guardrail | daniel (trivial) |
| 10 | `eval-boundary-empty-input` | Boundary handling | daniel (fast) |
| 11 | `eval-trajectory-fetch-summarize` | DAG trajectory | filip (fast) |
| 12 | `eval-idempotent-by-key` | Idempotency / concurrency | daniel (trivial) |
| 13 | `eval-escalate-on-rubric-fail` | Tier escalation loop | daniel → eva (rubric) |

Design principles applied to every scenario:

- **Anchor-based gates over phrase-match gates.** `must_contain` checks pin format markers (`{`, `qty`, `sentiment:`) not specific wording, so both Haiku-tier and Opus-tier outputs pass when correct.
- **Credential-leak tripwires across the suite** — `must_not_contain: [API_KEY=, Bearer ]` is a one-line guard that catches the worst regression class for free on every scenario.
- **Cross-family graders mitigate self-preference bias** (arxiv 2410.21819 / FairJudge Feb 2026). Sonnet-tier `eva`/`lucie` grade Haiku-tier `daniel`/`filip` outputs on rubric criteria.
- **Length bounds catch verbosity drift** — a known weak-model failure mode where the worker over-explains and breaks downstream JSON parsing.
- **`on_fail: escalate_tier`** at step level (not the deprecated `on_validation_fail` inside the validation block — that field exists in the IDE schema for back-compat but the executor ignores it).

### 18.2 LLMRunner (Anthropic-direct) restored as opt-in

PR #282 removed `internal/pipeline/runner_llm.go` in favour of `runner_orchestrator.go`. The orchestrator path is correct for production — agents run inside their real container with their real CLI adapter, no API keys in Crewship — but it makes the eval suite *untestable* on a workstation without a fully provisioned crew container stack.

`runner_llm.go` is restored as the **opt-in runner** for eval / CI / `--no-docker` smoke runs. Selection logic (`cmd/crewship/cmd_start.go`):

1. `CREWSHIP_PIPELINE_RUNNER=llm_direct`           — explicit override (always wins)
2. `deps.Container == nil` (e.g. `--no-docker` flag) — auto-fallback
3. otherwise (default; production)                 — `OrchestratorRunner`

LLMRunner's trade-offs vs OrchestratorRunner are documented inline in the file. Summary: works without provisioning, no skills/MCP/memory inside steps. Eval scenarios deliberately don't need those — they're about the LLM-step contract, not the full agent loop.

Cost ledger + lookout middleware still wraps the LLMRunner provider, so a Haiku run still emits paymaster ledger entries — the eval suite's cost guardrail (`max_cost_usd: 0.005`) is exercised end-to-end the same way a production routine would be.

### 18.3 JSON Schema gate enforcement

`internal/pipeline/executor.go validateOutput` previously accepted `validation.schema` as documentary only. This was the largest gap between the DSL surface and the gate semantics — a routine could declare any schema and the executor would silently let any output through.

Schema validation now runs after byte-level checks (`min_length` / `max_length` / `must_contain` / `must_not_contain`) using `github.com/santhosh-tekuri/jsonschema/v5` (draft 2020-12). Failure modes are distinguished by reason prefix so the journal entry's error_message preview tells the operator which class of problem the run hit:

- `schema invalid: ...`        — author-side bug; the schema can't compile
- `output not valid JSON: ...` — worker didn't follow the JSON contract
- `schema validation: ...`     — output parsed but failed the schema constraints

8 unit tests under `internal/pipeline/schema_gate_test.go` cover accept, missing-required-key, wrong-type, non-JSON, malformed-schema, enum, ordering against cheaper gates, and the nil-Validation safety path.

### 18.4 Cross-tier eval CLI

Two new subcommands under `crewship eval`:

- **`crewship eval scenarios`** — sweep across the workspace's `eval-*` routines × tier list × N runs, output a pass-rate matrix in table / json / yaml / markdown.
- **`crewship eval compare <slug>`** — run ONE scenario back-to-back on two tiers, report a head-to-head verdict (`AGREE-PASS` / `AGREE-FAIL` / `DIVERGE-A-PASS` / `DIVERGE-B-PASS` / `AMBIGUOUS`) plus side-by-side outputs.

Both leverage a new `tier_override` field on `RunInput` + `runRequestBody`. The override applies per-run to every `agent_run` step's `complexity` (step-level `model_override` still wins — explicit author intent always overrides batch-level overrides).

Verdict semantics for `compare` are designed for *fail-loud-on-disagreement*. Two LLM runs are essentially never identical at the text level — insisting on text identity would yield a useless test. Instead, the verdict captures **gate-pass agreement**: did both tiers pass the routine's declared gates with these inputs? The output text + cost/latency delta are surfaced for context, not for assertion.

### 18.5 Operator workflow

Bringing a fleet of agentic routines online safely:

1. **Author** the routine with strict gates (`must_contain` anchors, schema, optional outcomes rubric). Use `complexity: fast` everywhere it's plausible.
2. **Save** with a fresh `test_run` against authored inputs — gate trips instantly if the worker can't satisfy the contract.
3. **`crewship eval scenarios --scenarios <slug> --tiers fast,smart --runs 10`** — establish baseline pass-rate at both tiers.
4. **Promote / pin tier** based on the matrix:
   - 10/10 fast = ship at fast tier; cost-optimal.
   - 4/10 fast vs 10/10 smart = either tighten the rubric, or pin `complexity: smart` on the failing step (and document why).
   - 0/10 both = the gate is unsatisfiable; rewrite the routine.
5. **Schedule the eval sweep in CI** (`crewship eval scenarios -f json` → store, alert on regression) so a model swap or DSL refactor that breaks gate-pass agreement is caught automatically.

This is the closest analogue we have to Trigger.dev's run replay + idempotent-task contracts — but lifted into the LLM-step contract space, where the assertion is about output gate-pass not byte-identity.

### 18.6 What this section does NOT replace

- **Functional tests on agent skills / MCP tool loops** — those still need OrchestratorRunner with a real provisioned container. The eval suite asserts the LLM-step contract, not the full agent loop. A routine that depends on a Slack MCP tool can't be evaluated by `eval scenarios` end-to-end; it's an OrchestratorRunner-only target.
- **Cost analytics across tiers** — paymaster has the data, but the eval CLI surfaces only per-cell averages. Cross-tier total-cost-of-ownership analysis is a separate dashboard.
- **Determinism for non-LLM steps** — `transform` / `code` / `http` steps are already deterministic by construction; the eval suite uses them as plumbing in scenario 11 but doesn't separately test them.
