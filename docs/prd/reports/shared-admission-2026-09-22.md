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

The transport in these regression tests is substituted. The initial live probe
used a real Codex process; a live probe of the fixed build must be recorded
separately before claiming deployed behavior. Test logs and the initial live
probe are under `/srv/crewship/backups/crewship_2/i7-20260922/` on the test host;
credentials and webhook secrets are excluded from this report.

## Remaining release work

#2643 remains open: migrate all producer queues to one durable claim/retry/
recovery owner, retain dispatch-time authorization, implement ordered durable
chat mailbox and parent/child admission without slot deadlocks, enforce R7
revocation on LLM proxy, and prove T06/T07 on the required real adapter before
enabling parallel profiles. T14 OS/power-loss proof and human routine UX
acceptance are separate outstanding requirements. Memory MCP #2627 is deferred
by the user; enabled memory recovery is not thereby validated.
