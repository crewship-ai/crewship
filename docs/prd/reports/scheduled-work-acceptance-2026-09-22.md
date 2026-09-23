# Scheduled work acceptance — #2643

Historical acceptance-stage report. The subsequent production cutover is
recorded in [scheduled-work-cutover-2026-09-23.md](scheduled-work-cutover-2026-09-23.md).

Base: main `ce7710274` after rebasing the draft PR on 2026-09-23.
Earlier guards (#2648) are integrated through #2646.
This is the acceptance part of the migration, **not enabled in production**.
`AcceptDueTx` currently has test callers only. The cron producer still uses its
legacy executor. Do not merge/deploy this as a completed scheduler migration.

## Implemented contract

- Read the enabled schedule, agent/workspace/crew validity, current prompt and
  persisted due instant inside the caller's transaction. Missing or invalid due
  data fails closed; no wall-clock identity fallback.
- Accept one immutable background work plus its event, and advance the schedule
  cursor in that same transaction. Last-run is an execution fact and is untouched.
- One overdue persisted occurrence is accepted; next is the next future cron
  instant. There is no automatic enumeration of all missed intervals. Accepted
  work has no implicit deadline and is not evicted when the agent is busy.
- A unique partial index fences automatic acceptance by workspace, agent and due
  instant. Explicit replay is excluded. Duplicate acceptance preserves the first
  immutable input and precedes capacity checks.
- Preserve an unaccepted due instant on capacity/error rollback. Enforce per-agent
  scheduled backlog and common workspace count/byte limits. Non-webhook input is
  now included as UTF-8 bytes in the shared ingress budget, not just webhook bodies.
- `AcceptDue` runs the transaction through the dedicated bounded work.Acceptor
  after a disk guard. This wrapper also has test callers only; the cron producer
  has not switched to it.
- Refuse a live legacy occurrence reservation. This is an extra guard, not proof
  that old executors have drained: the legacy TTL is not process liveness evidence.

## Observed tests

Real migrated SQLite with production connection settings; no container or LLM.
Complete `internal/scheduler` and `internal/work` with `-race` passed (51.346s
and 63.558s respectively). Whole-tree `go vet ./...` passed. Logs are under
`/srv/crewship/backups/crewship_2/i7-20260922/schedule-acceptance-*.log`.

Coverage: immutable input; cursor advancement; stale duplicate while full;
12 concurrent ticks producing one work; rollback after a deliberately rejected
cursor UPDATE (no work/event survives); workspace isolation; disabled/deleted
agent, crew and workspace; refusal under byte pressure; legacy reservation;
unique-index protection against bypass; shared UTF-8 byte accounting and removal
from that budget after termination. Additional backlog boundary checks verify
that per-agent/workspace limits leave the rejected due instant unchanged.

## Still required before enabling this producer

1. Wire the bounded acceptance wrapper to the cron producer. Initialize missing
   schedule cursors explicitly; never silently derive occurrence identity on a
   read failure. Decide operator treatment of legacy ambiguous reservations.
2. Adapt scheduled execution to dispatch.Runtime while preserving prompt,
   unattended turn cap, conversation history, billing, metadata and run IDs.
   Use the existing creation/stop/unknown-outcome protocol, including detached holds.
3. `ScheduledAuthorizer` now implements the dispatch-side recheck as a
   separately tested primitive: disabled/deleted/moved schedules are refused,
   `PENDING_REVIEW` is deferred, and a prompt or cadence edit leaves an already
   accepted immutable input unchanged. It is **not wired to a dispatcher** yet;
   execution must not be enabled until it is wired with the runtime. The
   authorizer validates the accepted input checksum and source/agent/occurrence
   identity before consulting current scope.
4. Wire one dispatcher for both source/domain pairs. Switch cron to acceptance
   only, with no direct-execution fallback. Drain/reconcile legacy executions.
5. Prove actual bootstrap wiring, cross-source slot sharing, recovery after
   acceptance, cancellation, changed authorization and legacy upgrade behavior.

No current live-dev2, real-CLI, complete-I7, mailbox or release-profile claim is
made. No production scheduler configuration changed in this increment.
