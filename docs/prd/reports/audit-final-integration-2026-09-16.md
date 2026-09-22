# Final integration of the September CLI/documentation audit

Status: integration prepared; final combined Go/frontend runs and GitHub CI pending.
Base: `fc2ebb8410bb8bb7a0a7d6828468f840ff50d01e`.

This branch consolidates the remaining reviewed audit PRs: #2594, #2595,
#2596, #2598, #2599, #2600, #2601, #2602, #2603 and #2611. Their source
commits are retained as merge parents. The original PRs were closed as
superseded on 16 September, before integration reached main; that closure is
not a merge. PR #2617 reconciles this integration (#2616) with independently
reproduced assignment and backup-schema fixes (see the linked handoff). It replaces repeated sequential rebases and
whole-suite runs, not the requirement to validate the combined result.

Integration conflicts were resolved by retaining both changelog entries,
combining Inbox per-user read semantics with the accurate take-over action,
and combining Journal's trace limitation with its general run filter.
Regenerating OpenAPI produced no further diff. Seven obsolete CLI parity
exemptions, eighteen flag-baseline entries and two heading exemptions were
removed. The allowlist unit test now uses a remaining legacy entry; its
assertions for new errors and stale entries are preserved.

Observed validation so far:

- Complete generator and both documentation-gate test packages passed.
- Strict inventory: 684 operations, 934 commands, 528 API paths, no missing
  command outside 18 explicit exemptions.
- Surface check passed. Existing debt remains: 166 flag-section baseline
  entries and 17 legacy unescaped headings. These are not reported as fixed.
- An early frontend run was stopped because integration was still changing
  source files. It is not a passing-suite result. Final runs use a frozen tree.

Prior per-PR CI and older combined runs are supporting evidence, not results
for this final head. Logs are retained under the instance's
`audit-closure-2026-09-16/resume-close` backup directory.

The collector diagnostics PR does not fix the underlying intermittent timeout
(#2610). The macOS watcher investigation (#2609) also remains open. A passing
rerun does not close either investigation. This work does not claim release
parallelism PRD completion or live dev2 validation. No deployment is included.

Reconciliation and final delivery evidence: [integration handoff](../HANDOFF-2026-09-16-PR-INTEGRATION.md) and [PR #2617](https://github.com/crewship-ai/crewship/pull/2617). The preparation results above describe #2616 before reconciliation.
