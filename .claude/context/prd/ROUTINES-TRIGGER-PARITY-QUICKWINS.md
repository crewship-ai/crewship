# Routines: Trigger.dev parity quick-wins (PR #TBD)

**Status:** Implemented in this PR. Captured 2026-05-20.
**Problem:** A Trigger.dev gap-analysis surfaced one silent-bypass safety bug, one broken CLI output path, and two missing UI surfaces that turn would-be observability into "trust me bro". Each is small individually; bundled they close the most painful corners of the routines MVP without dragging in the multi-week durability work (wait checkpointing, multi-replica state) that needs its own design.
**Pattern:** Fix what's silently broken first; surface what we already compute second; defer what requires architectural change.
**Why now:** A user asked "are routines plnohodnotné?" (Czech for "fully featured / production-ready"). The honest answer was "73% of Trigger.dev parity, plus four embarrassments." Three of the four are quickwins.

---

## Scope — what ships in this PR

1. **`concurrency_key` empty-template fail-fast** (backend bug)
2. **CLI `routine dry-run` JSON decode** (broken since v83)
3. **Step-level cost + duration in Runs tab waterfall** (data we emit but didn't render)
4. **Dry-run report inline in detail panel** (response we computed and discarded)

## Out of scope — captured for follow-up PRs

- Wait checkpointing during `StepWait ≥ 5s` (1–2 weeks, needs design for resume-from-step semantics)
- DB-backed `RunRegistry` for HA (1 week, needs leader election + lock semantics)
- `pipeline_waitpoints.pipeline_run_id` FK cascade (migration + orphan cleanup, own PR)
- Cost ledger pre-step write + reconciliation on crash recovery (needs paymaster integration design)
- `metadata.stream()` + typed React hook for token streaming (own PR — useful but bigger surface)
- Tags on runs + `runs.list` filtering (own PR — schema + UI + CLI + API)
- Public Access Tokens with scope (security review surface; own PR)

The Trigger.dev gap-analysis lives in the conversation that produced this PR — not re-pasted here to avoid stale-copy drift.

---

## Change 1: `concurrency_key` empty-template fail-fast

### Today
`renderConcurrencyKey` (internal/pipeline/executor_render.go) returns `""` whenever:
1. The DSL omits `concurrency_key` entirely (intentional — "no gate")
2. The template references inputs that are missing or empty-string (**unintentional — silent bypass**)

Both cases reach `RunRegistry.Acquire` with `ConcurrencyKey=""` and the registry treats them identically: no gate engaged. A routine declaring `concurrency_key: "{{ inputs.account_id }}"` to serialise per-tenant runs silently fans out to unlimited parallelism when the caller forgets `account_id`.

### Fix
Three-arity signature:

```go
// (key, gated, err)
//   ("", false, nil)            → DSL didn't ask for a gate
//   ("foo-42", true, nil)       → gate engaged
//   ("", true, ErrConcurrencyKeyEmpty) → author asked, caller misconfigured
func renderConcurrencyKey(ctx, template, inputs) (string, bool, error)
```

Executor maps the third case to a clear "missing or empty input" error and frees the idempotency reservation (mirrors the existing `Acquire` failure path).

### Why a hard error, not a warning
Two reasons. **One**: silent bypass is a class of bug where the failure mode is "unlimited parallelism for a routine the author asked us to serialise" — that's a denial-of-self for sustained traffic. **Two**: Trigger.dev's `concurrencyKey` semantics match — an unresolved key value is a config error, not "no gate". Crewship users coming from Trigger.dev would be surprised by the opposite.

### Tests
`executor_render_concurrency_test.go` — 16 existing tests adapted to the new signature, 2 new tests pinning the fail-fast (`TestRenderConcurrencyKey_AllReferencesMissing_FailsFast`, `TestRenderConcurrencyKey_EmptyStringInput_FailsFast`).

## Change 2: CLI `routine dry-run` JSON decode

### Today
`cmd/crewship/cmd_pipeline.go:466` declares the response struct with `WouldExecute []…``json:"WouldExecute"`. Server emits `would_execute` (snake_case, see `internal/pipeline/types.go:701`). **The JSON tag has never matched.** Every dry-run since v83 prints "0 steps" regardless of what the server returned.

### Fix
One-line: `json:"WouldExecute"` → `json:"would_execute"`. Added a comment explaining why the wire name is load-bearing so the next person editing this struct doesn't re-introduce the bug.

### Why it wasn't caught
No CLI smoke test exercises dry-run end-to-end against a populated routine. **This PR adds the regression guard** as `cmd/crewship/cmd_pipeline_dryrun_decode_test.go` — it marshals a populated `pipeline.RunResult` through the wire and asserts the CLI's local decode struct round-trips it field-by-field. The second test (`TestDryRunCLIDecode_RejectsPascalCaseTag`) documents the original buggy shape as a negative case so a future copy-paste fix doesn't weaken the type.

## Change 3: Step-level cost + duration across UI + CLI

### Today
`journal.EntryPipelineStepCompleted` payload (internal/pipeline/journal.go:197) includes `step_id`, `cost_usd`, `duration_ms`. The Runs tab waterfall (`routine-runs-tab.tsx`) only rendered `step_id`. The CLI `routine logs` timeline rendered only `TIME EVENT SEVERITY SUMMARY` — cost / duration silently dropped on the floor on both surfaces. The data was on the wire; both clients ignored it.

### Fix
**UI**: extracted shared parsing + formatting into `components/features/routines/routine-cost-format.ts`:

- `extractStepMeta(payload)` — tolerates parsed-object / JSON-string / absent payloads, returns `{stepId, costUSD, durationMs}`
- `formatStepDuration(ms)` — em-dash for non-positive; `Xms` / `X.XXs` / `XmYYs` ramps
- `formatStepCost(usd)` — em-dash for non-positive; 4-decimal USD so micro-runs stay legible

Waterfall renders cost + duration columns right-aligned in tabular-nums, with a footer total row matching the per-run number on the Overview tab.

**CLI**: `cmd_routine_logs.go` timeline gains two columns:

```text
TIME          EVENT           SEVERITY  DURATION  COST     SUMMARY
18:42:03.421  step.completed  info      2.31s     $0.0021  …
```

`formatPayloadCost` / `formatPayloadDuration` helpers mirror the TS formatters byte-for-byte (same em-dash rule, same ramps, same precision). A user gets the same shape whether they look at the UI waterfall or pipe `crewship routine logs` into grep.

### Why em-dash, not $0.0000
"$0.0000" alongside "$0.0123" in a column is harder to scan than "—" alongside "$0.0123". The em-dash signals "not applicable" (started / failed / live-only events don't carry cost) without competing visually with real values.

### Tests
- `components/features/routines/__tests__/routine-cost-format.test.ts` — 14 unit tests pinning empty / zero / NaN behaviour, double-encoded JSON tolerance, format precision contract.
- `cmd/crewship/cmd_routine_logs_format_test.go` — 25 unit tests on the Go formatters using the same fixtures as the TS tests so a drift between surfaces fails CI before it fails users.

## Change 4: Dry-run report inline in detail panel

### Today
`routines-detail-panel.tsx:113` issues `POST /dry_run`, parses the response into `data`, **does nothing with `data.would_execute`**, and emits a toast that says "see Runs tab" — but dry runs don't write to the Runs tab (they don't run anything). The data the server computed is discarded.

### Fix
New `routine-dry-run-report.tsx` component renders a violet-tinted panel above the tab bar showing:

- Per-step ID + type (badged with the type-specific colour)
- Resolved tier (`tier_adapter:tier_model`)
- Estimated cost per step
- Total cost (sum of `estimated_cost_usd` across steps)
- Dismiss button — clears the panel without affecting the routine state

`triggerAction("dry_run")` in the detail panel now captures `would_execute` into `dryRunResult` state and the panel renders conditionally. A new dry-run replaces the prior result; closing dismisses it.

### Why above the tab bar, not in the Runs tab
The Runs tab is the "real runs" surface — dry runs don't increment `invocation_count` and don't land in `pipeline_runs`. Splicing dry-run-only rows in there would muddy the data model. The detail-panel-level surface is also where the **Dry run** button lives, so the report renders next to its trigger.

### Tests
The report is presentational; no unit test ships here. The data shape it consumes (`DryRunStep`) is pinned at the type level by the parser logic in `triggerAction`, which mirrors `internal/pipeline/types.go:DryRunStep` field-for-field.

---

## Docs updated

- `docs/guides/routines.mdx` — new "Dry-run preview" section under Triggers
- `docs/guides/routines-cookbook.mdx` — new "`concurrency_key` validation" subsection under Recipe 5
- `docs/cli/routine.mdx` — `dry-run` section gains an example output block

## What this PR explicitly does not change

- No DB migrations
- No schema changes to the `routine.v1.json` spec
- No new API endpoints
- No new CLI subcommands (one existing one — `dry-run` — gets its decode fixed)
- No agent-facing behaviour changes (sidecar pipelines.go untouched)

The blast radius is intentionally small. Three bug fixes + two new render surfaces over data the server already emits. Anything bigger lands in its own PR.
