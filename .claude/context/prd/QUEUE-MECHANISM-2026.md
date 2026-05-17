# Queue mechanism — per-crew admission control for agent dispatch

**Status:** Design draft, no implementation. Captured 2026-05-17.
**Problem:** Concurrent agent dispatch into a single crew container overwhelms its memory budget; Docker OOM-kills the container (`exit 137`) and every in-flight run on it crashes.
**Pattern:** "Queue if no memory, run when free" — not "raise the limit and pray".
**Why now:** Observed live on dev1 (15 issues → 4 crews → 11/15 FAILED with `exit 137`, see PR #389). Memory bump in #389 raises the floor; admission control is the actual fix.

---

## Current state (post #389)

```text
Issue dispatcher → mission_tasks.go → for each task:
  EnsureCrewRuntime()    # start container if cold
  AgentRun()             # spawn agent process inside container
```

There is no concurrency limiter. Three issues firing simultaneously against the same crew dispatch three agent processes into the same container, each pulling claude/gemini CLI + MCP servers into memory. With 8 GiB containers (post #389), about 2-3 agents fit; #4 trips OOM. **Failure mode is silent run.failed journal entries**, not graceful "wait your turn".

## Goal

A dispatcher that respects a per-crew concurrency budget:

- **Decide pre-dispatch:** is there room?
- **If yes:** dispatch immediately (current behaviour, no regression).
- **If no:** stamp the assignment as `QUEUED`, do nothing else. The dispatcher revisits when a slot frees.
- **Free signal:** every `run.completed` / `run.failed` triggers a re-poll of the queue for that crew.
- **UI/operator surface:** dashboard widget reads "3 running, 9 queued" — not "12 errors". Inbox can show a queue depth alert if it exceeds a threshold.

## Non-goals

- Cross-crew fairness scheduling. Each crew has its own queue; cross-crew interference is bounded by the docker host's resource pool, not by a global scheduler.
- Priority queues. FIFO is enough for the v1; priority is a known follow-up (see "Future work").
- Replacing the existing pipeline scheduler. `pipeline_schedules` already fires pipelines on a cron — the queue mechanism applies _after_ the dispatcher kicks, regardless of who kicked it.

## Design

### State

The same lifecycle has to widen in **two** tables — `assignments` and `agent_runs` — because the dashboard widget reads from `agent_runs.status` while the dispatcher tracks `assignments.status`. Drifting them is exactly the bug that produces "12 errors" instead of "12 queued" in the UI.

```sql
ALTER TABLE assignments ADD COLUMN queued_at TEXT;
-- Existing status CHECK on assignments widens to include 'QUEUED'.
-- agent_runs.status CHECK is widened in the same migration so the
-- dashboard ("3 running, 9 queued") and the dispatcher see the same
-- enum.
```

Statuses become: `PENDING → QUEUED → RUNNING → (COMPLETED | FAILED | CANCELLED)`. Same ordering on both tables.

`QUEUED` is the new state. `PENDING` keeps its current meaning (created but not yet looked at by the dispatcher).

### Concurrency budget

A crew's budget is `floor(container_memory_mb / agent_memory_estimate_mb)`, where `agent_memory_estimate_mb` is a config knob (default 2048 — claude CLI + 3-4 MCP servers warmed up consume ~1.5-2 GiB). For an 8 GiB crew container: budget = 4 concurrent agents.

Override per-crew via a new `crews.max_concurrent_agents` column (NULL → compute from memory).

### Dispatcher logic

The "check inflight → set RUNNING" sequence is **not** safe as two reads-then-write — two dispatchers can both read `inflight < budget` and both transition to RUNNING, blowing the budget by 1 each time. The fix is to make slot-claim atomic: a single `UPDATE … WHERE` that succeeds for **exactly one** caller per available slot, and falls through to QUEUED for the rest.

```go
func dispatch(ctx, assignmentID) {
  ass := loadAssignment(assignmentID)
  claimed, err := claimCrewSlot(ctx, ass.ID, ass.CrewID)
  if err != nil {
    return err
  }
  if !claimed {
    // budget full — stamp QUEUED + emit. UI sees the assignment hold
    // its position until pumpQueue picks it up.
    if err := setStatus(ctx, ass.ID, "QUEUED", queuedAt: now); err != nil {
      return err
    }
    journal.Emit(ctx, "assignment_queued", crew: ass.CrewID, ahead_of: queueDepth(ass.CrewID))
    ws.Emit(ws_channel(ass.WorkspaceID), "assignment_queued", payload)
    return
  }
  // Claim succeeded → row already at RUNNING with the slot counted.
  // If runAgent setup fails, the deferred rollback below releases the slot.
  releaseOnError := true
  defer func() {
    if releaseOnError {
      _ = releaseCrewSlot(ctx, ass.ID, ass.CrewID)
    }
  }()
  if err := runAgent(ctx, ass); err != nil {
    return err
  }
  releaseOnError = false // success path: terminal status handler releases
}
```

`claimCrewSlot` is the contract:

```sql
-- Atomic CAS in one statement: succeed iff this row is still PENDING
-- AND the crew's current RUNNING count is below budget. SQLite
-- evaluates the WHERE subquery + the UPDATE under the same write
-- lock, so two callers can't both win.
UPDATE assignments
   SET status = 'RUNNING', running_at = datetime('now','subsec')
 WHERE id = ?
   AND status = 'PENDING'
   AND (
     SELECT COUNT(*) FROM assignments
      WHERE crew_id = ? AND status = 'RUNNING'
   ) < (
     SELECT COALESCE(max_concurrent_agents, ?)
       FROM crews WHERE id = ?
   );
-- Caller checks RowsAffected: 1 = slot claimed, 0 = budget full.
```

The fallback `?` in the second subquery is the computed default (`floor(container_memory_mb / agent_memory_estimate_mb)`) supplied by the dispatcher — so a NULL `max_concurrent_agents` falls back to the memory-derived value without a separate read.

On completion:

```go
func onRunDone(ctx, assignmentID, terminalStatus) {
  ass := loadAssignment(assignmentID)
  setStatus(ctx, assignmentID, terminalStatus)
  journal.Emit(ctx, "run_" + terminalStatus, ...)
  pumpQueue(ctx, ass.CrewID)  // NEW
}

func pumpQueue(ctx, crewID) {
  // Atomically claim the oldest QUEUED slot for this crew, transition
  // it to RUNNING + emit assignment_unqueued, and recurse until the
  // CAS fails (no QUEUED rows OR budget full). Same UPDATE…WHERE
  // pattern as claimCrewSlot above, scoped to status='QUEUED' and
  // ordered by queued_at ASC.
  // Each successful claim spawns a goroutine for runAgent; the next
  // iteration re-reads inflight (the claim already incremented it
  // via the status flip, so the next CAS sees the new total).
}
```

**Naming convention** for events is **underscored**: `assignment_queued`, `assignment_unqueued`, `assignment_running`, `assignment_completed`, `assignment_failed`, `run_completed`, `run_failed`. Both `journal.Emit` and the WS channel use the same strings — no mapping function, no drift. Existing journal types already use this shape (`agent.status_change`, `run.completed` are the legacy dotted form; the new events stick to underscored to avoid mixing within one feature).

### WS events

New events on the `workspace:{wsID}` channel:

- `assignment_queued` `{assignment_id, crew_id, ahead_of, queued_at}`
- `assignment_unqueued` `{assignment_id}` (when state transitions QUEUED → RUNNING)

Existing `assignment_running` / `assignment_completed` / `assignment_failed` stay.

### Dashboard widget

Today's widget reads `agent_runs.status` and counts terminal statuses ("12 errors"). After the migration widens both `assignments.status` AND `agent_runs.status` to include `QUEUED`, the widget query becomes:

```text
12 agents: 3 running, 9 queued, 0 idle, 0 errors
```

This is a UI-only change once the backend emits the new events AND `agent_runs.status` carries the new state.

### Inbox

Optional: if a crew's queue depth exceeds 20, emit an inbox item ("research crew backlog: 22 queued"). Out of scope for v1; flag in `crews.alert_on_queue_depth` (NULL = off).

## Migration plan

1. **Migration v93**: add `assignments.queued_at`, widen status CHECK, add `crews.max_concurrent_agents` (nullable).
2. **Code**: extract a `dispatcher` package out of `internal/orchestrator/mission_tasks.go` (the gate logic at line ~385 is the right injection point). Add `pumpQueue`. Wire into `onRunDone` paths in `mission_tasks_completion.go`.
3. **WS events** in `internal/orchestrator/journal.go` (or wherever assignments emit today).
4. **Tests**:
   - 5 concurrent dispatch with budget=3 → 3 RUNNING + 2 QUEUED
   - Complete 1 → first QUEUED transitions to RUNNING
   - Complete remaining → all transition to RUNNING in FIFO order
   - Cancel a QUEUED assignment → drops from queue, doesn't run
   - 100 concurrent completions don't double-dispatch a single QUEUED row (race test)
5. **UI**: dashboard widget reads `agent_runs.status` including QUEUED. Inbox is later.

## What stays the same

- Issue start path doesn't change; the issue handler still POSTs an assignment, the dispatcher inside the orchestrator now decides immediate vs queued.
- Pipeline schedules still fire pipelines on cron; the queue is downstream of dispatch, not of scheduling.
- crew_connections gate stays in place (PR #389's fix); a queued assignment for a cross-crew agent must still pass the connection check at dispatch time.
- Memory caps + scrubber etc. stay unchanged.

## Failure modes

- **Stuck QUEUED rows.** If a process crashes between `setStatus(QUEUED)` and `pumpQueue`, queue rows linger. Mitigation: a periodic sweeper (5 min) that picks up QUEUED rows older than 1 min and re-runs `pumpQueue` for their crew. Equivalent to the harbormaster timeout sweeper pattern already in `internal/harbormaster/gate.go:238`.
- **Budget misconfiguration.** Setting `max_concurrent_agents = 0` would deadlock all assignments. Validation: `CHECK (max_concurrent_agents IS NULL OR max_concurrent_agents > 0)`.
- **Crew memory bump after queue is populated.** Operator raises `container_memory_mb`; existing QUEUED rows should benefit on next pump. Computed budget is read at pump time, not cached.

## Future work (out of scope for v1)

- Priority queues (urgency flag on assignments).
- Cross-crew fairness on a shared docker host (host-level memory budget).
- Backpressure to the issue handler — if 50+ assignments queue in 1 min, reject new issue.start calls with 503 + a Retry-After hint.
- Adaptive `agent_memory_estimate_mb` based on observed RSS per agent (paymaster-style rolling stats).
- Queue-priority hints from the LEAD agent during mission planning.

## Why not just raise container memory more?

The #389 bump (512 MiB → 8 GiB) already gives breathing room. More memory delays the OOM threshold but does not solve the fundamental contention: a busy crew still trips the limit eventually, and OOM is always silent. Admission control is the right shape — Docker OOM should never be the back-pressure signal. The bump and the queue mechanism stack: 8 GiB sets the floor; the queue makes sure we never go over it.

## Estimated effort

- Migration + schema: 0.5 day
- Dispatcher refactor + pumpQueue: 1 day
- Tests: 1 day
- WS event + dashboard widget: 0.5 day
- Total: **~3 days** for a v1 that lands behind a feature flag.

## Open questions

- Should `agent_memory_estimate_mb` live in `config.go` (single global) or be per-agent (some agents are heavier — gemini-cli + ruby MCP server + ...)? Probably global v1, per-agent later if observed RSS diverges enough to matter.
- WS event for queue position change (e.g. "you moved from #9 to #5") — useful for UI but adds chatter. Defer until a UI design asks for it.
