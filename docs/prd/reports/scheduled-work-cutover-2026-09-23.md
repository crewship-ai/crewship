# Scheduled agent work cutover — #2643

This change turns cron into a durable **producer**. A due schedule is read and
accepted with its cursor advancement in one SQLite transaction. The shared
agent-work dispatcher claims both `(webhook, agent_run)` and
`(schedule, agent_run)` under the same serial capacity policy. Production cron
callbacks no longer call `RunAgent`; the former direct executor lives only in
`legacy_direct_audit_test.go` to retain its historical regression tests.

At boot, schedules with no cursor receive their next future due instant;
existing overdue instants are preserved. An asynchronous boot sweep accepts
overdue work rather than waiting for the next daily cron tick. Each cron fire
uses the bounded acceptance handle and disk guard. Failure leaves the due
instant unchanged. The source/domain pair is checked at claim and routed to
the scheduled authorizer and runtime; the authorizer rechecks current agent,
crew, workspace and schedule state. The runtime uses the accepted prompt,
creates a run with the attempt's stable run ID, records process creation
before exec, and shares the webhook runtime's stop/probe and run-result
projection protocol.

The new vertical tests substitute only the container and agent process. They
exercise actual SQLite, the real HTTP resolver and run-record routes, the
cron boot sweep, dispatcher and both source kinds: one scheduled run/record, shared agent slot
with a webhook, disable while queued, cancel before dispatch and cancel while
running. Scheduler tests cover boot catch-up, missing-cursor initialization,
cron acceptance without direct execution and fail-closed startup without
durable admission. Test results belong in the PR checks; this document does
not claim a live CLI or OS-crash proof.

## Rollout boundary

The old scheduler's occurrence reservation is checked during acceptance.
That reservation's TTL is **not** proof that an old process has stopped after
it expires. A rolling multi-replica deployment with an old scheduled run still
alive therefore needs an explicit drain/reconciliation before the new
dispatcher is enabled. The process-local orchestrator admission protects
same-process overlap with chat/assignments; it is not cross-replica ownership.
Do not present this cutover as complete I7: assignments, pipeline agent steps,
chat and other producers have not migrated. The durable chat mailbox, R7 proxy
revocation, real OpenAI T06/T07 parallel proof and T14 OS-crash proof remain
open; the parallel profile remains disabled.
