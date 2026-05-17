# Queue mechanism — per-crew admission control for agent dispatch

**Status:** Design draft, no implementation. Captured 2026-05-17.
**Problem:** Concurrent agent dispatch into a single crew container overwhelms its memory budget; Docker OOM-kills the container (`exit 137`) and every in-flight run on it crashes.
**Pattern:** "Queue if no memory, run when free" — not "raise the limit and pray".
**Why now:** Observed live on dev1 (15 issues → 4 crews → 11/15 FAILED with `exit 137`, see PR #389). Memory bump in #389 raises the floor; admission control is the actual fix.

---

## Current state (post #389)

```
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

Extend `assignments` table:

```sql
ALTER TABLE assignments ADD COLUMN queued_at TEXT;
-- Existing status enum widens to include 'QUEUED' (CHECK constraint).
```

Statuses become: `PENDING → QUEUED → RUNNING → (COMPLETED | FAILED | CANCELLED)`.

`QUEUED` is the new state. `PENDING` keeps its current meaning (created but not yet looked at by the dispatcher).

### Concurrency budget

A crew's budget is `floor(container_memory_mb / agent_memory_estimate_mb)`, where `agent_memory_estimate_mb` is a config knob (default 2048 — claude CLI + 3-4 MCP servers warmed up consume ~1.5-2 GiB). For an 8 GiB crew container: budget = 4 concurrent agents.

Override per-crew via a new `crews.max_concurrent_agents` column (NULL → compute from memory).

### Dispatcher logic

```go
func dispatch(ctx, assignmentID) {
  ass := loadAssignment(assignmentID)
  budget, inflight := loadCrewBudget(ass.CrewID)
  if inflight >= budget {
    setStatus(ass.ID, "QUEUED", queuedAt: now)
    journal.Emit("assignment.queued", crew: ass.CrewID, ahead_of: inflight - budget + 1)
    return  // dispatcher returns immediately; ws emits queued state
  }
  setStatus(ass.ID, "RUNNING")
  runAgent(...)  // existing path
}
```

On completion:

```go
func onRunDone(ctx, assignmentID, terminalStatus) {
  setStatus(assignmentID, terminalStatus)
  journal.Emit("run." + terminalStatus, ...)
  pumpQueue(ctx, ass.CrewID)  // NEW
}

func pumpQueue(ctx, crewID) {
  // Read crew budget; for each QUEUED assignment in FIFO order while
  // inflight < budget, transition to RUNNING and dispatch. Single-pass.
  // Wraps in a transaction so two completions don't both grab the same
  // QUEUED row.
}
```

Race condition: two simultaneous completions trying to pump the queue. SQLite's WAL + the explicit transaction is sufficient — the second caller sees the first's transitions and re-reads inflight before deciding. No external coordination needed.

### WS events

New events on the `workspace:{wsID}` channel:

- `assignment_queued` `{assignment_id, crew_id, ahead_of, queued_at}`
- `assignment_unqueued` `{assignment_id}` (when state transitions QUEUED → RUNNING)

Existing `assignment_running` / `assignment_completed` / `assignment_failed` stay.

### Dashboard widget

Today's widget reads `agent_runs` and counts terminal statuses (12 errors). Update to also read pending+queued counts:

```
12 agents: 3 running, 9 queued, 0 idle, 0 errors
```

This is a UI-only change once the backend emits the new events.

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
