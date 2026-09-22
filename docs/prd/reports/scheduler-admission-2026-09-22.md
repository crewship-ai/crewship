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

## Next ownership change

The GLM task is limited to occurrence identity and legacy behavior tests in
`internal/scheduler/occurrence_audit_test.go`; production ownership remains
with the main session. Do not overlap those edits.

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
