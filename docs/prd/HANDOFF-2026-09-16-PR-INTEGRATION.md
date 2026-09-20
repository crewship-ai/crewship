# Remaining PR integration — 16 September 2026

Status: integration candidate. The final merge, CI and DEV1 deployment evidence
belongs on the integration PR linked from
[issue #2615](https://github.com/crewship-ai/crewship/issues/2615).
This document does not claim that a pending candidate is already deployed.

## Scope

The integration starts at main `fc2ebb8410bb8bb7a0a7d6828468f840ff50d01e`
(merged #2592). It preserves the original commits of these ten PRs:

| PR | Result |
| --- | --- |
| #2594 | Routine calendar, executions, artifacts, scheduled/async starts and remote fixture tests in the CLI. |
| #2595 | Human issue ownership, explicit review revisions and issue workflow documentation. |
| #2596 | Credential CLI acceptance tests and corrected pool/reveal documentation. |
| #2598 | Current memory inventory and chat/peer CLI coverage. |
| #2599 | OpenAPI response fields, receipt variants and status declarations checked against implementation. |
| #2600 | Published Page panel history and workspace Page theme in the CLI. |
| #2601 | Scoped run insights, issue status counts, journal references and realtime event recognition. |
| #2602 | API-path/CLI parity and command-specific documentation gates. |
| #2603 | Correct quick-action approval lists, Inbox source state and related documentation. |
| #2611 | Collector tests distinguish expected rejection from timeout or process failure. |

Original branches and other sessions' worktrees are retained. Merge ancestry,
rather than copied patches, lets the integration incorporate the original PRs.
The parallel integration #2616 is also retained as a merge parent, including its
heading-baseline cleanup and report. The source PRs were closed as superseded
before merge; their closed status is not evidence of delivery.

## Additional corrections

- A type-only `issue update --assignee-type ...` is refused before mutation.
  Previously a successful command could store an agent ID under the `user` type
  without setting the human owner. The regression drives the real CLI and API
  and checks both typed assignment slots and the legacy identity pair.
- Backup verify/restore response schemas accept absent diagnostic lists encoded
  as `null`. Tests validate the actual response structs against the generated
  schema, including nonempty lists, empty lists and invalid diagnostic types.
- Journal documentation keeps one cross-engine `run_id` filter and explains
  that `trace_id` alone covers ad-hoc agent runs. Inbox documentation preserves
  per-user read markers, shared resolution and the issue hold on `take_over`.
- Seven completed CLI-parity exemptions and eighteen obsolete flag baseline
  entries are removed. Routine documentation no longer promises the removed web
  fixture editor or an unconditional async acknowledgement for a fast run.

Both product/contract regressions failed before their corrections and passed
afterwards. Focused OpenAPI checks, full Go vet, strict documentation gates and
128 realtime frontend tests passed during preparation. Full-suite and deployment
results must be read with their exact commit in the integration PR.

## Boundaries

The human usability acceptance in the original Routines PRD §11 remains open.
Merging this integration does not establish acceptance by representative users.

Draft #2572 was closed as deferred by its owning session and is outside this candidate. Its GitHub review pilot still has review
questions about reviewer-profile restrictions, concurrent publication and the
attribution of historical status/outcome evidence. Its operator-assisted probe
does not validate an unattended production workflow.

Only DEV1 is in this takeover's deployment scope. DEV2 and DEV3 are not covered
by its deployment evidence. Existing untracked work is preserved separately.

## Closing state — 18 September 2026

The candidate described above landed as [PR #2617](https://github.com/crewship-ai/crewship/pull/2617),
merged `a99a8212235a34f7996a2ebe620c3f57675eb81e` at 2026-09-18T13:05:05Z with
a completed CodeRabbit review of head `22ed292` and green CI. Merge ancestry of
all ten source PRs and of #2616 was re-verified post-merge; both additional
corrections (assignee-type refusal, nullable backup diagnostics) are in main.
DEV1 runs the merged content (`22ed292cd`, content-identical to post-merge
main). Full verification and the R1–R10 status live in
[routines-validation-2026-09-18](reports/routines-validation-2026-09-18.md);
the follow-up fix for #2573 (routine deletion cascades to its schedules) is
[PR #2620](https://github.com/crewship-ai/crewship/pull/2620). Human
acceptance §11 remains open, as the boundaries below require.
