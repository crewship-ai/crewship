# Design note — tool_loop detector should terminate runs

**Status:** design only — architectural change crossing the
quartermaster/orchestrator boundary. Above the loop's 200 LoC
threshold once tests are included.
**Source:** `audit/iterations/2026-05-22--polish-2/wave6/a6-1.md`
finding #2 (MEDIUM) — "Tool-call loop bound is delegated entirely to
external CLIs. The paymaster budget is the only effective
per-conversation amplification cap. Quartermaster `tool_loop`
detector observes but does not terminate."
**Author:** audit-loop, 2026-05-22.

## Current state

`internal/quartermaster/metrics.go:115-163` runs
`detectFailureModes(steps)` to produce a `FailureModes []string`
annotation on each completed run. `tool_loop` is one of those modes,
fired when the same `ToolName` repeats >5 times within a 2-minute
elapsed window.

Where the signal lands:

- `internal/quartermaster/metrics.go:128` — adds the string to the
  `FailureModes` slice.
- `internal/quartermaster/replay.go:92` — emits it as a metrics
  field.

Where it does NOT land:

- The orchestrator's per-step processing loop has no consumer for
  this signal. The trajectory step that triggered the loop is allowed
  to complete and the next assistant turn proceeds as normal.
- No abort path. No 429-like response to the agent CLI. No journal
  `run.aborted` entry.

**Result:** the audit's evidence is right — *paymaster budget* is the
only effective per-conversation amplification cap. A tool-call loop
on a high-budget account can run for the full budget before paymaster
cuts it.

## Architectural problem

`detectFailureModes` runs on a **completed** `[]TrajectoryStep`, i.e.
post-hoc. It cannot abort what's still running because by the time it
sees the data, the run is over.

To make the signal load-bearing, it has to move upstream into the
step-processing path:

```
adapter_*.go (parses CLI events)
    ↓
parser_*.go (normalises into TrajectoryStep)
    ↓
orchestrator/exec_stream.go (consumes the stream, drives the loop)
    ↓
**NEW: per-step tool-loop tracker** ← decision point
    ↓
adapter_*.go (forwards next assistant turn OR aborts)
```

## Proposed design

### Component 1 — `LoopGuard` (new, `internal/quartermaster/loopguard.go`)

```go
// LoopGuard is a sliding-window counter for tool-call repetition.
// Thread-safe by design (one orchestrator goroutine writes; nothing
// reads in parallel). Returns true when the current call should abort
// the run.
type LoopGuard struct {
    threshold int           // e.g. 5
    window    time.Duration // e.g. 2 * time.Minute
    history   []toolEvent   // bounded ring; pruned on each Observe
}

func NewLoopGuard(threshold int, window time.Duration) *LoopGuard
func (g *LoopGuard) Observe(toolName string, at time.Time) (loop bool)
```

The existing `detectToolLoop` function in `metrics.go` becomes a thin
wrapper that constructs a `LoopGuard` and replays the trajectory
through `Observe`. Same algorithm, new shape — no behaviour change for
post-hoc metrics emission.

### Component 2 — orchestrator integration (`internal/orchestrator/exec_stream.go`)

The step-processing loop adds:

```go
loopGuard := quartermaster.NewLoopGuard(5, 2*time.Minute)

for step := range parsedSteps {
    if step.EntryType == "exec.command" && step.ToolName != "" {
        if loopGuard.Observe(step.ToolName, time.Now()) {
            // Abort the run: emit a typed journal entry, cancel
            // the worker context, surface the failure mode to the
            // caller.
            emitJournal(ctx, "run.aborted", map[string]any{
                "reason":     "tool_loop",
                "tool_name":  step.ToolName,
                "threshold":  5,
                "window_ms":  120000,
            })
            cancelRun(ctx, runID, "tool_loop")
            return ErrToolLoopAborted
        }
    }
    // ... existing forward to next assistant turn
}
```

### Component 3 — caller side

`internal/api/eval_handler.go`, `internal/api/webhook.go`,
`internal/orchestrator/agent_run.go` already handle errors from
`RunAgent`. They need:

- A new sentinel error `ErrToolLoopAborted` in
  `internal/orchestrator/errors.go`.
- A wire-level status mapping: 429 (Too Many Requests) feels wrong
  because the cause is server-side detection, not rate-limit;
  prefer custom 599 or 422 (Unprocessable Entity) with a
  machine-readable `reason: "tool_loop"` body.
- Run status DB column update: `status = "ABORTED"`,
  `failure_reason = "tool_loop"`.

### Component 4 — tunability

The threshold (5) and window (2m) are currently hard-coded in
`detectFailureModes`. For an abort path, they need to be:

- Per-workspace or per-crew tunable (some workflows legitimately
  call the same tool many times).
- Disabled-by-default on first ship, opt-in via a `crew.policy`
  flag. Otherwise we risk false-positive aborts on legitimate
  workloads.

This means a migration to extend `crews.policy_json` with a
`tool_loop_abort` block:

```json
{
  "tool_loop_abort": {
    "enabled": false,
    "threshold": 5,
    "window_seconds": 120
  }
}
```

## Estimated LoC

| File | Net LoC |
|---|---|
| `internal/quartermaster/loopguard.go` (new) | ~80 |
| `internal/quartermaster/loopguard_test.go` (new) | ~150 |
| `internal/quartermaster/metrics.go` (refactor `detectToolLoop` → wrapper) | -20, +30 |
| `internal/orchestrator/exec_stream.go` (integration point) | ~50 |
| `internal/orchestrator/errors.go` (sentinel) | ~5 |
| `internal/orchestrator/exec_stream_test.go` (abort scenarios) | ~120 |
| `internal/api/eval_handler.go` + 2 callers (sentinel-error handling) | ~30 |
| Migration v? to extend `crews.policy_json` | ~40 |
| Migration test | ~40 |

Total: ~525 LoC. Well above the 200 LoC loop threshold.

## Why this can't be a loop PR

1. **Crosses three packages** (quartermaster, orchestrator, api). A
   loop tick should keep churn local to one boundary.
2. **Needs a migration** (policy_json schema extension). Migrations
   need careful testing under the project's migration framework
   conventions, which is its own ramp.
3. **Behaviour change is observable to users** (runs can now abort
   that previously completed). This needs a feature-flag default of
   OFF and a documented opt-in path. Skipping that breaks workflows
   in the field.

## Out of scope (intentionally deferred)

- Generalising the loop-detector to other repetition shapes (same
  tool-args sequence, alternating tool-pair ping-pong).
- Emitting an early-warning at `threshold/2` so operators see a
  yellow-flag before the abort.
- Wiring this into the chat UI as a visible "loop aborted" banner.

## How to pick this up

1. Spec the policy_json migration first (`pkg/database/migrations/`
   under the convention seen in `migrate_consts_v107_*.go`).
2. Branch `feat/audit-tool-loop-abort` from `main`.
3. Build the `LoopGuard` and its test in isolation. Replay the
   audit's mutation-test trajectory through it to confirm fire.
4. Refactor `detectToolLoop` to delegate.
5. Wire the orchestrator integration behind the policy flag.
6. Ship with `enabled: false` default, document the opt-in in
   `docs/security/cost-controls.mdx`.
