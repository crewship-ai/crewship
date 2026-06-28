# PRD — Routines Maximization 2026 (trigger.dev-informed, no backend rewrite)

**Status:** Design draft. Captured 2026-06-28.
**Problem:** Routines' engine is already strong (durable resume, DAG, retry, waitpoints, versioning, grader loops) but has (a) one safety/UX class-bug — a `type: code` step with an unwired runtime *saves cleanly and fails at every run*; (b) a too-weak deterministic compute primitive (`expr` = one boolean comparison); and (c) a set of observability/ergonomics gaps that the trigger.dev feature set throws into relief.
**Pattern:** Close the silent-failure class first → generalize the compute primitive → borrow the high-leverage trigger.dev primitives that fit our shape → stop explicitly at the features that would require a heavy backend rewrite (ClickHouse analytics, multi-region, container code sandbox beyond WASM).
**Why now:** A live routine (`cost-spike-probe`) failed on dev2 with `code runtime "bash" not available`. Root-caused to a stale seeded definition + a validator that accepts unwired runtimes. The user asked whether routines are "best practice" and whether we should match trigger.dev. This PRD is the answer: a batched, no-heavy-rewrite plan to maximize Routines.

> **Hard constraint (user):** no heavy backend rewrite. Every item below lands behind an existing seam (the `CodeRunner` interface, the `Step` DSL struct, the `pipeline_runs` table, the WS hub) or as an additive migration. Items that would force an architectural change are listed as **Non-goals**, not deferred work.

---

## Ground truth — what we ALREADY have (do NOT rebuild)

Verified against the codebase 2026-06-28:

- **Step types:** `agent_run`, `call_pipeline`, `http`, `code` (only `expr` wired), `wait` (approval/datetime/event), `transform` (jq-subset). — `internal/pipeline/types.go`
- **Step flow control:** `if:` (conditionals), `needs:` (DAG parallelism), `retry` (`MaxAttempts` + exponential backoff — `executor_retry.go`), `on_fail` (escalate_tier / abort / retry_step), `validation`, `outcomes`, `complexity`/`model_override`, `timeout_seconds`.
- **Durable resume:** `resume.go` persists `current_step_id` + `step_outputs_json` per step boundary; restores completed steps, re-runs in-flight (at-least-once), waitpoint runs re-register on the original token across restart. **This is trigger.dev's headline "checkpointing/durable execution" — we already ship it.**
- **`pipeline_runs` columns already present** (`migrate_consts_v83`): `error_fingerprint` (pre-indexed!), `idempotency_key` (indexed), `concurrency_key`, `inputs_json` ("captured for replay"), `step_outputs_json`, `current_step_id`, `triggered_via`, `triggered_by_id`, `invoking_crew/agent/user_id`, `definition_hash`, `pipeline_version`, `cost_usd`, `duration_ms`, `mode`, `failed_at_step`.
- **Versioning + rollback**, **dry-run**, **test_run gate before save**, **scheduled cron triggers**, **wake-gate probes**, **webhooks**, **concurrency keys**, **egress allowlist**, **cost estimation/budgets**, **grader/escalation loops**, **full CLI parity** (`cmd_routine_*`), **WS hub** with `session:{id}` channels.

Equivalent to trigger.dev's: durable resume/checkpoint, retry+backoff, idempotency keys, waitpoints (human-in-the-loop approval), versioned artifacts+rollback, declarative-ish cron, cost metering. **Skip all of these as borrows.**

---

## Wave 0 — Close the silent-failure class (hygiene; one PR; TDD)

The bug that triggered this PRD. Small, high-value, ships together.

### 0.1 Validator rejects unwired code runtime (warning → hard error)
- **Problem:** `internal/pipeline/dsl_validate_egress.go:50` accepts `expr | python | go | bash` as legal runtimes, so a `runtime: bash` step *saves cleanly* and fails at every invocation (e.g. a 3 AM cron). `internal/manifest/routine_warnings.go` only emits an **advisory** warning — and the `routine save` / seed paths don't even run it.
- **Approach:** Add a single source of truth `wiredCodeRuntimes` (today: `{"expr"}`; grows as Wave 1 wires more). Make the DSL validator return a **hard validation error** for any `type: code` step whose runtime isn't wired (gate it behind a capability flag once a sandbox exists, so enabling wazero auto-permits `runtime: wasm`). Enforce on `test_run`, `save`, and `apply`.
- **Touch:** `dsl_validate_egress.go`, `routine_warnings.go` (keep advisory for *config-not-loaded* cases), `cmd_pipeline.go` save path.
- **Effort:** **LOW.** No schema change.

### 0.2 Seed-validation test asserts wired runtime
- **Problem:** `cmd/crewship/seed_routines_validate_test.go` does **not** check that seeded `type: code` steps use a wired runtime — which is exactly how the broken `runtime: bash` `cost-spike-probe` shipped.
- **Approach:** Extend the seed test: for every seeded routine, assert each `type: code` step's runtime ∈ `wiredCodeRuntimes`. Bonus: execute every agentless/deterministic seeded routine once and assert it doesn't FAIL.
- **Touch:** `seed_routines_validate_test.go`.
- **Effort:** **LOW.**

> Note: dev1/dev3 likely carry the same stale `cost-spike-probe` (seeded pre-fix). Reconcile via `crewship routine save` (see `project_stale_seeded_routines` memory), not nuke+reseed.

---

## Wave 1 — Generalize the deterministic compute primitive

Answers the user's "can't it be done generally / properly sandboxed" question. Both items sit behind the **existing** `CodeRunner` interface (`executor.go:240`) — selected by `runtime`, no DSL change.

### 1.1 `expr` → CEL (Google Common Expression Language, `cel-go`)
- **Problem:** `runtime: expr` (`runner_code_expr.go`) does exactly ONE binary comparison (`a > b`). Real probes need `&&`/`||`, arithmetic, `contains`, list membership, null checks — today you'd chain steps or drop to an LLM (token cost + nondeterminism).
- **Approach:** Add a `CelCodeRunner` behind `CodeRunner`. CEL is non-Turing-complete (guaranteed to terminate → preserves token-zero + no-RCE), pure-Go, and the industry standard (Kubernetes admission, Envoy). Keep `expr` as a back-compat alias that routes single comparisons through CEL. Expose inputs as CEL variables (`inputs.spend_usd`).
- **Touch:** new `runner_code_cel.go`, wire in executor's runtime switch, add `cel-go` to go.mod, add to `wiredCodeRuntimes`.
- **Effort:** **MED.** No schema/DSL change. Net-new dep (Apache-2.0, OK).

### 1.2 Sandboxed code runtime — DROPPED (2026-06-28 decision)

**Status: not building.** CEL (1.1, shipped) already covers the
"general deterministic logic" gap that motivated this PRD, and real
shell is already served by `agent_run` against a shell-tool agent. A
WASM/container code runtime is a large, security-sensitive,
self-contained build for low near-term value — trigger.dev itself only
*roadmaps* microVM isolation, so we won't ship arbitrary-code execution
ahead of the incumbent. Revisit only if a concrete need for in-process
BYO-language compute (that `agent_run` can't serve) appears; then prefer
`wazero`/`starlark-go` (pure-Go, sandbox-by-construction) behind the
existing `CodeRunner` interface. Original design retained below for that
future revisit.

<details><summary>Deferred design (not in scope)</summary>

#### (former) Sandboxed code runtime via WASM (`wazero`)
- **Problem:** `CodeStep` already documents a sandboxed container model (cap-drop, egress allowlist, timeout — `types.go:285`) but no runner is wired. We do NOT want a Docker/microVM rewrite.
- **Approach:** Implement a `WasmCodeRunner` (wazero — pure-Go, **no CGO, no Docker daemon**, sandbox-by-construction: no filesystem/network unless explicitly granted). Two viable surfaces, pick in design:
  - **(a) `runtime: wasm`** — author supplies/compiles a WASM module; we run it with inputs on stdin, stdout = output. Maximally general, BYO-language.
  - **(b) `runtime: starlark`** (`starlark-go`, also pure-Go) — a Python-dialect deterministic sandbox (Bazel's config language), no I/O by default. Lower author friction than raw WASM for "real logic" steps; **recommended default** for the "between expr and LLM" gap, with wasm as the power-user escape hatch.
- **Why this is "valid" not a hack:** wazero/starlark are sandboxed by construction (no ambient authority), deterministic, terminate under our existing `TimeoutSec`, and need zero infra. trigger.dev itself is still *roadmapping* microVM isolation for arbitrary code — so shipping real bash/python containers would be ahead of even the incumbent's risk appetite. WASM/Starlark gives real compute **without** that surface.
- **Touch:** new `runner_code_wasm.go` (+ maybe `runner_code_starlark.go`), executor runtime switch, go.mod, `wiredCodeRuntimes`, docs.
- **Effort:** **MED–HIGH.** Behind existing interface; no backend rewrite.
- **Tiering:** core gets `expr`/CEL/Starlark (token-zero, safe); `runtime: wasm` BYO-module could be an EE gate if we want a paywall line (matches the licensing memory).

</details>

---

## Wave 2 — Observability borrows (highest-leverage trigger.dev catch-up)

### 2.1 Bulk replay + fingerprint grouping
- **trigger.dev:** Errors page (v4.4.3/v4.4.5) — fingerprint grouping, occurrences-over-time, **bulk replay** of failed runs.
- **Us:** `error_fingerprint` is **already a column + index**; `inputs_json` already captures invocation inputs "for replay". The feature is unbuilt, the plumbing is half-there.
- **Approach:** (1) Populate `error_fingerprint` on failure (normalize error class + failed step). (2) `GET .../pipelines/runs?group_by=fingerprint` returns grouped failures + counts. (3) `POST .../pipelines/runs/bulk_replay` reads `inputs_json` for selected/filtered failed runs and re-triggers, re-entering the DSL (respect definition-drift guard already in `runs.go`). (4) CLI `crewship routine replay --failed --since ...` + UI list.
- **Effort:** **MED.** Additive endpoints + CLI; reuses existing columns.

### 2.2 `is_replay` flag in run context
- **trigger.dev:** `ctx.run.isReplay` (v4.4.5) lets a run short-circuit side effects on replay.
- **Approach:** Add `is_replay INTEGER DEFAULT 0` to `pipeline_runs`; set it on replay-triggered runs; inject `{{ run.is_replay }}` into the render context so steps (and `if:`) can branch. Pairs with 2.1.
- **Effort:** **LOW.** One column + render-context field.

### 2.3 Run tags
- **trigger.dev:** tags at trigger + runtime (`tags.add()`), max 10/run, dashboard + `runs.list({tag})` filtering, tag-based realtime subscribe, no auto child propagation.
- **Us:** no `tags` anywhere (verified empty). Directly advances the "pipelines are workspace-scoped shared assets / cross-crew discovery" thesis (`project_pipeline_reuse_vision`).
- **Approach:** `run_tags` (join table or JSON column + a denormalized indexed `tags_text`); set at invoke + a `tags` step output channel; filter in `routine runs --tag`; surface in UI; tag the routine definition too (for discovery, not just runs).
- **Effort:** **LOW.**

### 2.4 Run metadata blob
- **trigger.dev:** `metadata.set/get/increment/decrement/append/remove/flush`, `metadata.parent/root`, readable mid-run, 256 KB.
- **Approach:** `metadata_json TEXT DEFAULT '{}'` on `pipeline_runs`; a typed scratchpad passed between steps (`{{ run.metadata.x }}`), mutable from `agent_run`/code steps, surfaced in the inbox + run detail. Start with set/get/increment/append (cover 90%).
- **Effort:** **LOW–MED.**

### 2.5 Parent/child run tree surface
- **trigger.dev:** parent tooltips with child-run breakdowns (Jun 8).
- **Us:** `triggered_via=call_pipeline` + `triggered_by_id=parent run_id` already recorded.
- **Approach:** Render the existing parentage as a tree in the run detail UI; `routine runs --tree`.
- **Effort:** **LOW.** Pure read/surface.

---

## Wave 3 — Triggering & durability ergonomics

### 3.1 Waitpoint completion token via HTTP callback URL
- **trigger.dev:** `wait.forToken()` — complete a waitpoint via Management API **or HTTP callback URL**, with timeout + idempotency; one token can unblock N runs.
- **Us:** approval waitpoints + `WaitpointResumer` exist (inbox-driven). Gap = external-system completion via a callback URL.
- **Approach:** Extend `wait` step (kind `token`): mint a completion URL/token; `POST .../waitpoints/{token}/complete` (already partially exists for approval) accepts an external caller (scoped token) + payload that lands in run metadata. Keep the existing timeout semantics.
- **Effort:** **MED.**

### 3.2 Run-level TTL, trigger `delay`, `idempotencyKeyTTL`
- **trigger.dev:** `ttl` (expire if not started), `delay` (start later), `idempotencyKeyTTL` (key scope window).
- **Approach:** additive invoke options: `ttl` → mark `expired` if not dispatched in window; `delay` → schedule first dispatch (reuse the scheduler); `idempotency_key_ttl` → bound the existing dedupe window. Small columns/fields.
- **Effort:** **LOW.**

### 3.3 Debounce (consolidate burst triggers)
- **trigger.dev:** debounce key + window + `maxDelay` (v4.4.0) — coalesce a burst of identical triggers into one run.
- **Approach:** `debounce: { key, window_ms, max_delay_ms }` on invoke; a pending-trigger row keyed by `(pipeline, debounce_key)` that collapses re-fires within the window. Complements `concurrency_key` (serialize) with "coalesce".
- **Effort:** **MED.**

### 3.4 Batch trigger
- **trigger.dev:** `batchTrigger` (N runs of one task over an array), `batchTriggerAndWait`.
- **Us:** `call_pipeline` is single nested fan-out; DAG parallelizes steps, not invocations.
- **Approach:** `crewship routine run <slug> --batch inputs.jsonl` / `POST .../run_batch` → N runs sharing a `batch_id`; surface batch progress (reuse run-list filtered by `batch_id`).
- **Effort:** **MED.**

### 3.5 Priority + queue controls
- **trigger.dev:** per-trigger `priority`; `queues.pause/resume/overrideConcurrencyLimit`.
- **Us:** `concurrency_key` exists; `QUEUE-MECHANISM-2026.md` already drafts per-crew admission control.
- **Approach:** `priority INTEGER` on invoke → ordering when the queue from `QUEUE-MECHANISM` lands (agent-triggered yields to human-triggered); `routine queue pause/resume`. **Sequence after QUEUE-MECHANISM.**
- **Effort:** **LOW–MED** (rides the queue PRD).

---

## Wave 4 — Author ergonomics & live control

### 4.1 Step/routine lifecycle hooks
- **trigger.dev:** `middleware`, `onStartAttempt/onSuccess/onFailure/onComplete/onWait/onResume/onCancel`.
- **Approach:** declarative `hooks:` on a routine/step (before/after-step, before/after-routine, on-cancel) that can run a `code`/`http` step for setup/teardown — a clean home for cross-cutting concerns we do ad hoc (credential acquire, egress scoping, cost meter open/close). Keep it data-driven (no plugin code).
- **Effort:** **MED.**

### 4.2 Per-step prompt/model override without version bump (AI Prompts borrow)
- **trigger.dev:** AI Prompts (v4.5.0-rc.0) — prompts versioned in code, **overridable from the dashboard without redeploy**.
- **Us:** routines are versioned; `agent_run` steps carry `prompt`/`model_override`. Gap = operator tweak without bumping the routine version.
- **Approach:** a per-step **override layer** (workspace-scoped, separate row) applied at run time over the versioned step prompt/model; UI editor + `routine step-override set`. Directly advances `project_ai_authored_pipelines_vision`.
- **Effort:** **MED.**

### 4.3 Run signal / input-stream injection (steer a running routine)
- **trigger.dev:** Input streams (v4.4.2) — `.send(runId, data)` into a running task; `.wait()/.once()/.on()/.peek()`.
- **Us:** `wait:event` step exists but no payload-injection API.
- **Approach:** `POST .../runs/{id}/signal` writes to a run channel; extend `wait:event` to consume the payload (`.once()` semantics) and a non-blocking `peek` into render context. Enables mid-run human steering / cancel-with-reason.
- **Effort:** **MED–HIGH.** Reuses WS hub + waitpoint machinery.

### 4.4 Routine-run React hooks (live run UI)
- **trigger.dev:** `useRealtimeRun`, `useRealtimeRunsWithTag`, `useWaitToken`.
- **Us:** WS hub + `ws-token` + `session:{id}` channel already exist.
- **Approach:** add a `routine:{runId}` channel + `useRoutineRun(runId)` hook streaming step-by-step status + `agent_run` output; live runs list filtered by tag (needs 2.3).
- **Effort:** **LOW–MED** (infra exists).

### 4.5 Declarative cron in the versioned DSL (+ IANA tz, next-5 preview)
- **trigger.dev:** declarative `schedules.task({cron})` synced on deploy; IANA timezones + DST; payload `upcoming` (next 5).
- **Us:** scheduled cron triggers exist but configured outside the versioned artifact.
- **Approach:** allow `schedule: { cron, timezone }` inside the routine JSON so schedule changes are versioned + rollback-able with the steps; show next-5 in dry-run/detail. Verify IANA tz handling (small gap if UTC-only today).
- **Effort:** **LOW.**

---

## Non-goals (would require a heavy backend rewrite / wrong product shape)

Explicitly NOT in this PRD — documented so we don't relitigate:

- **TRQL / ClickHouse analytics + custom dashboards** — needs a columnar analytics store + query engine. Our SQLite run-list + tags/metadata filtering covers the 90% without it.
- **Real bash/python **container** runtime / microVM isolation** — wazero/Starlark (Wave 1.2) is the sanctioned substitute. trigger.dev itself only roadmaps microVMs; we won't ship arbitrary-binary execution first.
- **Multi-region execution (`region`)**, **machine presets / OOM-retry-to-bigger-box**, **AWS PrivateLink**, **HIPAA BAA**, **Electric-SQL realtime transport** — single-binary + our WS hub make these N/A or infra-tier, not routine features.
- **chat.agent / Sessions runtime** — that's trigger.dev's *agent framework*; we already have Crews/orchestrator. Not a routine primitive.
- **Build extensions, Vercel/Supabase/GitHub deploy integrations** — different product surface.

---

## Sequencing & batching

- **PR A (Wave 0):** 0.1 + 0.2 together. Closes the bug class. TDD, tiny.
- **PR B (Wave 2 low-effort batch):** 2.2 `is_replay` + 2.3 tags + 2.4 metadata + 2.5 tree — all small additive migrations + surfacing; ship as one "Routines observability" PR. **Best value/effort.**
- **PR C (Wave 2.1):** bulk replay + fingerprint grouping (depends on 2.2's column).
- **PR D (Wave 1.1):** CEL. **PR E (Wave 1.2):** wazero/Starlark sandbox (own PR; design doc for surface choice first).
- **PR F (Wave 3 ergonomics batch):** 3.1 callback tokens + 3.2 ttl/delay/key-ttl + 3.3 debounce (+ 3.4 batch trigger if scope allows).
- **PR G (Wave 4):** 4.2 prompt override + 4.4 React hooks first (infra exists); 4.1 hooks + 4.3 signal injection + 4.5 declarative cron as follow-ups. Priority/queue (3.5) rides `QUEUE-MECHANISM-2026`.

Every PR: failing test first (red) → implement → green; docs in `docs/guides/routines*.mdx` + `docs/manifest/routine.md`; CLI parity per Core rule #3.

---

## Appendix — trigger.dev feature → Crewship status (completeness map)

Compiled from exhaustive release-note + changelog + docs research (2026-06-28). HAVE = ship it; GAP = in a wave above; N/A = non-goal.

| trigger.dev feature | Crewship status |
|---|---|
| Durable resume / checkpointing / warm starts | **HAVE** (`resume.go`) |
| Retry (maxAttempts/factor/min-max/jitter), catchError, AbortError | **HAVE** (`executor_retry.go`, `on_fail`) |
| Idempotency keys | **HAVE**; +TTL → 3.2 |
| Waitpoints / HITL approval | **HAVE**; +callback-URL token → 3.1 |
| Versioned artifacts + rollback + dashboard override | **HAVE** versioning; override → 4.2 |
| Cost metering (`usage`, costInCents) | **HAVE** (`cost_usd`) |
| Scheduled cron + timezones + declarative | **HAVE**; declarative-in-DSL → 4.5 |
| Concurrency limits / `concurrencyKey` | **HAVE** |
| Errors page: fingerprint grouping + **bulk replay** | **GAP → 2.1** (fingerprint col already exists) |
| `isReplay` | **GAP → 2.2** |
| Run tags | **GAP → 2.3** |
| Run metadata (set/get/increment/append) | **GAP → 2.4** |
| Parent/child run tree | **GAP → 2.5** (parentage already recorded) |
| `wait.forToken` HTTP callback | **GAP → 3.1** |
| `ttl` / `delay` / `idempotencyKeyTTL` | **GAP → 3.2** |
| Debounce (+maxDelay) | **GAP → 3.3** |
| `batchTrigger` / batchTriggerAndWait | **GAP → 3.4** (have single `call_pipeline`) |
| `priority` + queue pause/resume/override | **GAP → 3.5** (rides QUEUE-MECHANISM) |
| Lifecycle hooks / middleware | **GAP → 4.1** |
| AI Prompts (versioned + dashboard override) | **GAP → 4.2** |
| Input streams (`.send`/`.wait`/`.once`/`.on`/`.peek`) | **GAP → 4.3** |
| Realtime run subscriptions + React hooks | **GAP → 4.4** (WS hub exists) |
| Richer deterministic compute (`expr` only does 1 compare) | **GAP → 1.1 CEL** |
| Real code runtime (python/go/bash) | **GAP → 1.2 wazero/Starlark** (container/microVM = Non-goal) |
| TRQL / ClickHouse query engine + custom dashboards | **N/A** (non-goal) |
| Machine presets / OOM-retry / region / PrivateLink / HIPAA | **N/A** (infra/non-goal) |
| chat.agent / Sessions runtime | **N/A** (we have Crews/orchestrator) |
| Build extensions / Vercel / Supabase / GitHub deploy | **N/A** (different surface) |
| OTEL metrics pipeline + GenAI cost spans | **Partial / future** (we emit journal+cost; full OTEL = EE infra) |
| MCP server tool expansion (query/dashboard/dev-server tools) | **N/A** for routines (separate MCP-gateway track) |

**Cross-refs:** `project_trigger_dev_competitive`, `project_stale_seeded_routines`, `project_pipeline_reuse_vision`, `project_ai_authored_pipelines_vision`, `QUEUE-MECHANISM-2026.md`, `ROUTINES-DURABILITY-QUICKWINS.md`.
