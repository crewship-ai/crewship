# Detached execution: closure scope (2026-09-21)

PR #2628 addresses false success after an exec stream detaches. It does not
complete the whole parallelism/memory PRD. Memory MCP #2627 is a separate open
follow-up; successful real-Claude task execution still needs valid credentials.

The independent audit of 754518a1 found four additional failures. The follow-up
changes and executable regressions are:

| Finding | Correction | Regression |
| --- | --- | --- |
| Admission waiting for capacity bypassed a newly published hold | Recheck the hold after acquiring capacity, before admitting the run. Registration and checking share the detached mutex. Runs already admitted before a hold appeared retain separate run-keyed holds. | `TestDetachedHold_AdmissionRechecksHoldAfterWaiting`, `TestDetachedHold_ConcurrentRunsRetainSeparateCapacity` |
| The watch deadline reopened admission while the runtime was still alive | The deadline alerts and backs off polling; it never releases the safety fence. | `TestDetachedHold_CapMustNotReopenAdmission` |
| Confirmed termination left credential-refresh HOME registration and credential cleanup behind | Transfer cleanup ownership to the watcher; notify run end, remove HOME, and release/clean file credentials before reopening admission. An immediately confirmed stop uses normal cleanup defers. | `TestDetachedHold_HoldCleansRunHome`, `TestDetachedHold_ConfirmedStopCleansImmediately` |
| Successful periodic stop left its watcher polling | Return from the watcher after stop confirmation and cancel the watcher on finalization. Finalization is idempotent. | `TestDetachedHold_RestopEndsWatcher` |

The monitoring loop logs once at DEBUG when monitoring begins. Polling after the
24-hour alert backs off to one minute without treating an unavailable provider
as evidence of termination. The finalizer does not invent an exit code for a
run whose terminal result was lost.

The follow-up CodeRabbit review identified three further observable gaps:
an immediately confirmed stop left the run record running, a persisted partial
chat reply did not notify the inbox, and a detached routine step discarded
observed usage. Each was reproduced with a failing regression before correction.
Confirmed stop bookkeeping uses a fresh bounded context so cancellation of the
original request does not prevent writing the terminal status. The test
transport also distinguishes stop probes from launch scripts by the stop-only
signal command, rather than the shared tmux cleanup command.

This remains process-local admission protection, not shared admission across
all producers or durable hold recovery across daemon restart. Previously
admitted concurrent runs are not retroactively denied. An unconfirmed process
keeps its fence until a later probe/stop confirms its end.

Validation uses fake provider transport with the production orchestrator and
real concurrency/cleanup paths; it is not a real-provider successful task test.
The red audit probes and local command logs are retained outside the checkout
under `/srv/crewship/backups/crewship_2/detached-fix-20260921/` and the earlier
`security-perf-20260920/` directory. Final command results and exact CI HEAD are
recorded on the PR so this document does not conflate an earlier successful
run with a later revision.

## Additional independent review (2026-09-22)

Two simultaneous hold closures could each observe the other before either
removed its map entry. Both then skipped `markAgentOnline`, leaving the roster
busy after all detached processes had ended. The added concurrency regression
failed on the prior head at iteration 64 with zero online transitions. A
completed non-last hold is now removed under the same lock that checks its
peers; the last hold stays registered through its presence update. Tracker I/O
still runs outside the mutex. The detached-hold suite, including 3,000 paired
closures per invocation, passed ten repetitions under the race detector.

This proves the in-process completion race correction, not durable recovery
across daemon restart. The broader final-head verification remains recorded
on PR #2628; earlier green CI is not evidence for this new change.
