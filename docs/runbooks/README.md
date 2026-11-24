# Runbooks — operational procedures

**Purpose.** Repeatable procedures an operator follows to run, verify,
publish or recover something. A runbook is written against the machinery as
it exists; when the machinery changes, the runbook changes with it in the
same PR.

**Rule for a new file.** Imperative, stepwise, and honest about what is
verified versus assumed. Name the exact gates, artifact identities and
credential requirements an operator needs. Session narratives ("what I did
today on dev2") are not runbooks — those belong in
[private working context](../development/private-context.md). Generated
reports are ignored build output, not an operational procedure.

[Documentation map](../README.md) · [Contributor guides](../development/README.md)

## Contents

- [`ci-cd-implementation-2026-09-11.md`](ci-cd-implementation-2026-09-11.md) —
  the CI/CD publication runbook: CI Result composition, image and binary
  publication, exact-SHA gates, nightly/smoke verification, signing and
  release identities. Referenced from
  [CONTRIBUTING.md](../../CONTRIBUTING.md) and
  [RELEASING.md](../../RELEASING.md).

- [Trusted CI control inventory](ci-inventory-guard.md): main-owned metadata guard, explicit administrator exceptions and shared-App enforcement limits.

- [Assignment work ownership upgrade](assignment-work-upgrade.md) — queue cutover, ambiguous legacy exec refusal, cancellation and terminal projection.
- [Pull-request credential diagnostics](pull-request-credentials.md): distinguish local credential preconditions from provider failures without exposing secrets.

## Recover a pending crew runtime update

1. Use the support reference from chat to find the server log entry with
   `phase=runtime_admission`. Confirm the workspace and crew IDs before
   opening the crew journal. The chat message ID also appears on runtime
   upgrade events even when agent execution has yet to start.
2. Inspect `crewship crew container-status <crew>` in that workspace. Check
   run state, open terminal sessions, exposed ports and detached work with
   the crew owner. An empty run list alone cannot establish that the runtime
   is idle. Keep the container running while occupancy is uncertain.
3. For a failed census, use the underlying exec or fence error from the
   server log to restore the existing provider connection or fence helper.
   After correction, a new request repeats admission against the observed
   container. Check whether the original request has already produced a run
   before submitting it again.
4. If the crew uses a custom process tree and its owner has confirmed a
   maintenance window, an administrator can run
   `crewship crew restart-agents <crew>` followed by `crewship crew start <crew>` in the same workspace.
   Verify a current runtime contract, the required egress fence and a
   successful synthetic agent turn. Investigate provider authentication
   errors separately after runtime admission succeeds.
5. Before rollout, retain the previous signed release and a restorable
   backup. Reverting the server can introduce another runtime contract
   mismatch. Qualify that direction with a retained crew and verify the
   previous release's database compatibility before rollback.
