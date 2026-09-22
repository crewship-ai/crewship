# Shared admission: first safety increment (#2643)

## Scope

The normative contract remains
[`WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md`](../WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md).
This increment enforces the current serial adapter profile at `RunAgent`, the
common runtime entry point. It does **not** complete I7 or enable the release
one-chat-plus-one-background profile.

Before this change, webhook admission counted only ledger work, while chat's
producer-side lock did not cover webhooks, direct runs or peer queries. A live
dev2 probe with the isolated `admission-audit-2643` agent, ChatGPT login and
`gpt-5.6-luna` observed chat and webhook runs simultaneously RUNNING for 19
samples spanning 10.49 seconds. Both real CLI executions exited successfully.
This is evidence of a serial-profile bypass, not T06/T07 acceptance.

## Implementation and limits

- All calls to `RunAgent` acquire a cancellable per-agent reservation before
  acquiring server execution capacity. Different agents remain independent.
- The normal cleanup releases both reservations. An unconfirmed detached
  process transfers both to the existing watcher until confirmed termination.
- Waiting requests retain their existing producer-side state and cancellation
  behavior. They are not automatically durable queued work; a webhook may be
  `starting` while waiting at this runtime boundary.
- The reservation is process-local and disappears on server restart. Existing
  durable recovery still applies only to producers already using it.
- Existing producer-side busy checks remain. There is no new public flag that
  enables unverified parallel execution.
- Synchronous peer queries request immediate admission and return HTTP 409 if
  either the agent or server capacity is occupied. They must not wait for a
  reservation potentially owned by their waiting parent. This refusal is not
  the eventual durable parent/child scheduling contract.

## Verification evidence

`TestRunAgent_SameAgentWaitsWithoutTakingServerCapacity` failed on the previous
implementation: “same agent started a second runtime”. It drives `RunAgent`
with different session identities, checks that the waiting call does not take
server capacity, cancels that call, then admits another run after completion.
The targeted race run passed five repetitions after the fix.

`TestDetachedHold_HoldCleansRunHome` additionally verifies that the per-agent
reservation remains unavailable while the detached process is alive and is
released after confirmed termination. Its targeted race run and the serial
admission test passed ten repetitions together.

`TestRunAgent_SynchronousChildRefusesBusyAdmission` covers both a child asking
for its parent's agent and a different agent blocked by server capacity. Both
must return `ErrAdmissionBusy`, launch no child process, and release any partial
reservation. Ignoring `NoAdmissionWait` made both cases fail on their context
deadline; the fixed targeted race run passed five repetitions. The peer-query
request builder test pins the production selection of this mode.

On `6a8228698`, complete race runs of `internal/orchestrator`, `internal/dispatch`,
`internal/chatbridge` and `internal/pipeline` passed, as did whole-tree `go vet`.
The subsequent peer-query and cancellation changes also passed whole-tree
`go vet`. The final targeted API race run (114.537 seconds) covered the new
uncreated-runtime regression, the vertical HTTP cancel/shutdown family, the
request builder, and a real query-handler test returning 409 while its parent
retains admission. Final orchestrator regression tests passed ten race-enabled
repetitions. Full-tree testing/CI is separate from these filtered runs.

The transport in these regression tests is substituted. The initial live probe
used a real Codex process; a live probe of the fixed build must be recorded
separately before claiming deployed behavior. Test logs and the initial live
probe are under `/srv/crewship/backups/crewship_2/i7-20260922/` on the test host;
credentials and webhook secrets are excluded from this report.

### Live fixed-build probe

Dev2 ran `6a8228698c5a9e9fedf63ef9e0a6da6995786695` for the probe. Health returned
OK; binary VCS stamping and the rebuilt frontend identified this revision.
`vcs.modified=true` reflected the preserved untracked session files; tracked
diff was empty.

- Chat `msg_1790080523321962400_87ec1966aa2210b7`: actual exec start
  12:35:23.884Z, end 12:35:42.732Z, exit 0, running false.
- Webhook run `cmucnq4co0003153b9f69`: exec start 12:35:44.488Z, end
  12:35:52.100Z, exit 0, running false.
- Work `cmucnq4ao000245a4a81a`: observed starting, running, succeeded. The
  webhook's real exec started 1.756 seconds after the chat's exec ended.

These actual process intervals establish serial execution on the fixed build,
not just non-overlapping work statuses. Evidence is in the `fixed/` subdirectory.
The subsequent synchronous-peer-query refusal is a separate change; this live
probe does not test it.

### Cancellation exposed an additional settlement bug

On the same build, cancelling waiting work `cmucnyath0005507ab6c0` returned
`requested`, but settled into `needs_reconciliation`: after RunAgent returned,
`phaseLocked` treated the launch as settled despite its creation gate never
having admitted a process. The provider probe then failed on the direct-exec
container without tmux. Chat `msg_1790080905602894031_1e3eb051f330a876` continued
and completed with exit 0. Durable `runtime_phase` remained `starting`, never
`requested`. This test work was manually resolved as cancelled using generation
1 and a recorded explanation; that manual action is not a passing cancel test.

The added regression `TestWebhookRuntime_ReturnBeforeCreationNeedsNoProviderProbe`
failed with “probe run uncreated-run: no exec in tests”. The fix retains the
declared phase after a return if creation was never requested or confirmed.
Once creation was requested, provider errors still mean uncertainty.

The repeat on dev2 build `b0594d4b335b900b181b3773dd47c9eca960fd49` passed:
work `cmuco6qxa0002d53065b7` received cancel while `starting` (`requested`), then
settled automatically to `cancelled`, without manual resolution. Chat
`msg_1790081299734185650_e96ef94492c85532` continued: actual exec interval
12:48:20.454Z–12:48:42.262Z, exit 0, running false, followed by run.completed.
Evidence: `fixed-cancel-b0594d4b3/` in the local evidence directory.

The production code in this build is also in PR #2646; later commits tighten
test synchronization or update this report. Full CI is explicitly dispatched
for the PR branch because its feature-branch base does not trigger automatic
pull-request CI. Do not treat targeted passes or a started CI run as a passed
full suite. The PR remains a draft until final checks/review are complete.

## Remaining release work

### Follow-up: the run projection also needs settlement

Read-only verification of the live cancelled work's run
`cmuco6qyq000381ba9b11` found `status=RUNNING`, `finished_at=null` despite the
correctly cancelled work item. The previous live check covered work state and
the unaffected parent, but did not assert this run projection. The wrapper
called `UpdateRun` with its already-cancelled execution context, so the IPC
write could not reach the server.

The fix gives the terminal write a separate 10-second deadline while preserving
context values. `CANCELLED` is used only when the launch gate proves a stop
preceded any requested or observed process; cancellation of a context alone is
insufficient. Such an uncreated process has no exit code. Detached live runs
still return before any terminal update.

The new HTTP/real-SQLite regression
`TestVerticalServer_CancelBeforeCreationClosesRunRecordAfterContextCancellation`
failed before the fix with no durable run.cancelled event. The fixed targeted
API race run (70.631s) also covered cancellation at the gate, shutdown before
the gate, and uncreated-runtime probes. Whole-tree go vet passed. These are
targeted checks, not the final full suite for this additional production fix.

### Still outstanding

#2643 remains open: migrate all producer queues to one durable claim/retry/
recovery owner, retain dispatch-time authorization, implement ordered durable
chat mailbox and parent/child admission without slot deadlocks, enforce R7
revocation on LLM proxy, and prove T06/T07 on the required real adapter before
enabling parallel profiles. T14 OS/power-loss proof and human routine UX
acceptance are separate outstanding requirements. Memory MCP #2627 is deferred
by the user; enabled memory recovery is not thereby validated.
