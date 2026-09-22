# Scheduler preparation for shared admission (#2643)

## Guard implemented, durable migration not yet implemented

On base `f2275809f`, `triggerAgent` logged a failed `CreateRun` and continued
into `RunAgent`. The new regression observed 13 container Exec calls despite
the run-record failure. These include preparation commands; this is evidence
of crossing into execution, not a claim that a real LLM completed a task.

The scheduler now returns before RunAgent when CreateRun fails. It retains the
occurrence reservation and leaves schedule_next_run / schedule_last_run
unchanged. An IPC failure can hide a successful commit, so forgetting the key
and creating another run identity would be unsafe. Logs include the agent,
run and occurrence key for investigation. The legacy reservation has its
existing TTL; this is not a new durable reconciliation mechanism. The old
record may still need operator investigation. No automatic retry of the
ambiguous run-record write is introduced.

`TestTriggerAgent_RunRecordFailurePreventsExecutionAndPreservesReservation`
checks no Exec, no terminal write, unchanged schedule timestamps, retained
run identity and dedup of a repeated occurrence. A positive control verifies
that a distinct occurrence with a successful CreateRun still reaches Exec.
The existing stream/conversation test no longer injects a CreateRun failure;
it still injects the terminal UpdateRun failure and verifies the response is
preserved. No assertion was weakened to keep the unsafe path running.

Observed validation on this working tree:

- Before fix: targeted regression exit 1, 13 Exec calls.
- After fix: `GOMAXPROCS=4 go test -race ./internal/scheduler -count=1`,
  exit 0, package time 4.332s.
- `GOMAXPROCS=4 go vet ./...`, exit 0.
- Logs: `/srv/crewship/backups/crewship_2/i7-20260922/scheduler-record-{red,green,vet}.log`.

SQLite is real and temporary; resolver and container transport are test
doubles. No live scheduler or real CLI claim is made for this change. No
deployment was performed for this guard.

## Occurrence audit independently reproduced and fixed

The GLM audit from `/tmp/opencode/sched-audit` was copied (original preserved)
and rerun on `a3ba343f6` before changes: exit 1, four audit tests passed and the
missing-agent reproducer failed with two CreateRun calls. The two existing
same/distinct-occurrence tests were explicitly included and passed. The
original audit's listed `-run TestOccurrenceAudit` command alone would not
have executed those two existing tests.

The read-error fallback is a real defect: a failed occurrence lookup generated
a wall-clock key unrelated to the persisted due timestamp. The function now
returns an error, and triggerAgent stops before reservation, chat or run
creation. A successful read of NULL/empty still uses the legacy force-fire
fallback; a missing agent row is an error, not a NULL schedule value.

Evidence limits matter: the original whole-path reproducer deletes the agent
while its mock resolver continues to resolve that agent. It proves scheduler
fail-open behavior, not a full production transient-lock double execution.
Its run count is a CreateRun count, not proof of two live CLI processes. The
real SQLITE_BUSY test deliberately uses rollback-journal mode to force a read
error; production WAL lock behavior is not inferred from it.

The retained regression checks the concrete SQLite error code (5), no invented
identity, no chat/run creation while locked, then an unlocked retry deduping
against the original reservation with the due timestamp untouched. The
missing-agent regression remains, with its harness limitation documented.
Busy-skip state and reserve-error schedule preservation tests were retained.

Validation: entire scheduler package with `-race` passed (5.359s, exit 0),
whole-tree `go vet` passed. Logs in the same evidence directory:
`occurrence-audit-original.log`, `occurrence-fixed.log`, `occurrence-vet.log`.
An overlay restoring only the read-error wall-clock fallback made both the
SQLITE_BUSY and missing-agent regressions fail (exit 1), while leaving tracked
production code intact: `occurrence-mutation.log`.

## Next ownership change

The GLM task is complete and its tests have been taken over by the main session
in `internal/scheduler/occurrence_audit_test.go`. Its original worktree is intact.

The actual migration must implement and verify all of these together before
production wiring changes:

1. Durable occurrence identity and work acceptance, ingress checks, and schedule
   advancement in one SQLite transaction. A failed acceptance must not advance
   the due timestamp. Duplicate ticks must return the original work identity.
2. An explicit backlog policy: accepted work is never silently discarded or
   expired without a deadline. Replacing busy-skip with queuing changes behavior.
   Decide separately whether downtime implies replaying missed occurrences;
   do not accidentally infer that policy from wall-clock fallback.
3. A runtime adapter using the attempt run ID and the existing stop/creation
   gate contract, plus fresh authorization of agent, crew, workspace and the
   schedule at dispatch. Preserve scheduled prompt, turn cap, conversation,
   billing and run metadata; do not disguise a schedule as a webhook payload.
4. One claim/retry/recovery owner for webhook and scheduled work. Once cron
   accepts work it must never call RunAgent directly or fall back to that path
   on an acceptance error. Stop old dispatch and drain/reconcile active work
   during transition; legacy reservations must not be mistaken for new work.
5. Cross-source capacity tests, cancellation before/during creation, ambiguous
   CreateRun results, restart after acceptance before launch, duplicate ticks,
   permission changes, and upgrade from legacy reservations. No external call
   may happen inside the acceptance transaction.

Until that change is wired and tested, scheduler remains a legacy producer
behind the process-local RunAgent fence, not a completed I7 producer.
