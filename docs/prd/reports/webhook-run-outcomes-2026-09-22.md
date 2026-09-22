# Webhook run history after cancellation — #2652

Date: 2026-09-22. Scope: work-owned webhook runs; not completion of the full
Routines PRD or the broader #2643 durable-admission work.

## Problem and implementation

The production-shaped cancellation regression reproduced a cancelled work item
and stopped process with a `run.failed` journal event. The webhook wrapper wrote
that event before dispatcher settlement. The journal correctly refused to rewrite
an already terminal run, preserving the wrong first result.

Webhook execution now captures output/usage in its bound work-attempt row. The
existing fenced settlement transaction records the authoritative run status.
Only after both facts exist may the internal update handler publish the terminal
journal entry. Caller-supplied status and usage cannot override that outcome.
Cancel intent and `context.Canceled` alone remain insufficient proof of stopping.
A successful completion which beats cancellation remains completed. A confirmed
failed process may have a failed run while its external effects still require
work reconciliation.

A durable pending-result query retries only journal projection, including after
a new runtime/store instance. It never calls the agent. Synchronous journal
persistence and a stable terminal-entry ID make lost acknowledgements and
concurrent deliveries idempotent. Failed deliveries rotate behind other pending
results, avoiding starvation by a batch of conflicting old histories. Each flush
is capped at 100 records and five seconds; its partial index excludes acknowledged
and unconfirmed attempts. A flush can delay admission by up to that bound;
active-run supervision continues independently. The added outcome probe has its
own stop-grace timeout and a separate persistence deadline, so an unavailable
provider cannot block settlement indefinitely.

Migration `20260922182944_work_run_outcomes.sql` adds nullable result/acknowledgement/
attempt timestamps, a default-empty constrained status, and the pending index.
Existing rows are not backfilled. Payloads are capped at 512 KiB, below the internal
API envelope limit. Existing work retention owns the attempt rows.

## Validation evidence at PR creation

- Production-shaped router/HTTP/IPC/database/journal/dispatcher regression:
  confirmed cancellation produces one cancelled event, no failed event, no
  invented exit code, and retains captured cost. The agent process is a test
  provider; this is not a new live external-model test.
- Race-enabled entire work and dispatcher packages passed. Focused race-enabled
  webhook/internal-run suites and the new projection tests passed.
- Regressions cover tenant/binding isolation, stale generations, late results,
  transactional rollback, unconfirmed stop, late cancellation after success,
  journal failure, lost acknowledgement, concurrent delivery, legacy terminal
  retries, and contradictory historical status.
- Fairness regression passes after reconstructing the store, proving that a
  failed delivery does not monopolize a one-record batch.
- `go vet ./...` passed. Full Go suite and final-head CI are still pending at
  document creation; merge/deployment evidence must be recorded on the PR.

## Limits and remaining acceptance

Historical incorrect terminal events are not rewritten. Legacy finished-run
retries acknowledge their existing event. A newly authoritative outcome conflicting
with an existing terminal event returns 409 and stays pending for investigation;
other pending outcomes continue progressing. A storage failure while capturing
output parks the work rather than repeating external actions. It cannot be
interpreted as a failed execution or a cancellation, even when a late stop request
arrives; a failing-first regression covers a lost capture acknowledgement after
successful execution. Shutdown uses the same result-before-cancel ordering;
review found and regressions reproduced the earlier shutdown bypass for both
unconfirmed capture and an already successful execution. This cannot recover
an output never durably captured before a hard process crash.

No new frontend design is included. This fixes the data consumed by existing
Activity/run views. It is not a full-product performance benchmark, all-provider
recovery proof, the remaining #2643 producer integration, or human usability
acceptance. PRD §11 remains NOT VERIFIED until representative user sessions are
recorded. DEV1 deployment is pending; DEV2/DEV3 are out of scope.
