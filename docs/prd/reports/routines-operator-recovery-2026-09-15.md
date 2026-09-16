# Routines operator console — final recovery checks, 2026-09-15

PR: [#2562](https://github.com/crewship-ai/crewship/pull/2562), issue #2560.
Branch: `feat/routines-operator-console`.

This report records the September 15 recovery. The [September 16 integration protocol](https://github.com/crewship-ai/crewship/pull/2562#issuecomment-5696159682) records newer fixes, the exact DEV1 build, browser acceptance and pending merge gates. Consult subsequent validation comments in PR #2562 for later CI/merge/deployment results; the build identity below is historical.

## Scope and actual changes

The four initial review findings already had committed fixes when this verification resumed:

- `e11301fac`: Edit retains the envelope/revision it opened on; it does not borrow the latest revision before saving stale content.
- `ff113bcfd`: file state is present/missing/unverified; failed checks do not assert absence.
- `bcb22da65`: unsupported test-query options removed to restore the test-types gate.
- `d0e0ef49b`: Copy saves an unpublished draft, and a draft-only page can publish its first version.

Further inspection found and fixed related paths in `9b5e13883`:

- A combined name/input edit previously saved identity to the live recipe before attempting to save the draft. All combined changes now go into one draft write, leaving publication unchanged.
- A failed draft load is visible and blocks saves. The editor cannot silently continue from a guessed baseline.
- Input-only changes preserve name/purpose already authored in the saved draft.
- Edit's Steps & files tab now uses the same three-state file interpretation as the Files card; it no longer labels an unverified file missing.
- Draft revision guidance uses the envelope actually loaded, not stale list metadata.

## Regression evidence

The first three added regression cases failed before the corresponding fixes. Targeted Edit/New/Files tests then passed: 25 tests in three files, including a fourth case preserving draft identity. The existing stale-revision test remains in place. Product lint for changed files passed.

Test types on the final production code: no new diagnostics; 168 baseline diagnostics remain tracked in #2493. Go vet passed.

The first full frontend invocation overlapped intentional red regression cases and also reported an unrelated realtime timing failure; it was stopped. The four-worker run also overlapped the subsequent HTTP-conflict fix discovered by browser acceptance; its new HTTP-error tests could see an earlier cached module. A fresh final full run is started after production code is frozen; the final result is recorded below.

## Deployment incident

The first standard DEV1 reload compiled the frontend but failed typechecking on the generated `.next/dev/types/validator.ts` (TS1128). The service stopped. Only the regenerable dev-types directory was moved to an owned temporary backup, preserving its contents; systemd start then rebuilt the application. No product typecheck was disabled and no source stub was created.

## Boundaries

This is engineering validation, not the original PRD's human usability acceptance. No live model-quality experiment, general resume capability or new external effects were introduced. DEV2/DEV3 are outside this work. The original WIP is preserved. The feature PR remains separate from a release acceptance and is not merged by this work.

Browser acceptance also found that the real HTTP 409 message does not contain the words assumed by the UI. `RoutineDraftError` now carries HTTP status independently of message wording, including RFC 7807 detail, and Edit/Copy use status 409. Targeted Edit/New/draft HTTP tests: 23 passed.

## Final corrections and verification

`9ad433910` makes draft conflict handling depend on HTTP status, rather than guessing from English error text. `a9d5baafc` also keeps name-only and appearance changes to an unpublished copy in its draft; no live save or appearance endpoint is used before its first publication. The added unpublished-identity regression failed before the fix. Final focused verification passed 29 tests across four files.

The frozen final frontend suite passed **9,159 tests in 767 files** (309.39 seconds). All Go packages passed `go test ./... -count=1 -timeout=30m`; the wrapper subsequently failed while removing root-owned Docker test artifacts, which were then removed successfully from the owned temporary directory. This was a cleanup failure after Go returned zero, not a failing Go test. Go vet passed. Full frontend lint had zero errors and 31 warnings; changed product files passed scoped lint. Test types have no new diagnostics against the existing 168-diagnostic baseline. Production frontend build passed during deployment. Agents invariants, migration lint and OpenAPI generation passed, with no generated API diff.

The repeatable `e2e/routines-operator-recovery.mjs` browser check passed against DEV1:

1. Combined identity/input changes save together as a draft and leave the published version unchanged.
2. Two real editors open the same revision; the second save receives 409, retains its text, and leaves the first editor's saved content intact.
3. Renaming an unpublished copy remains a draft.
4. At 390 px, Copy creates an unrunnable draft; explicit Publish creates version 1. Copy uses the source publication, not its pending draft.

Desktop checks used 1440 px. The browser recorded no page errors. Both owned test routines and their drafts were removed (204); original WIP remained byte-for-byte intact (17 untracked files, safety stashes retained).

## Deployment evidence

DEV1 served commit `a9d5baafca8a009c04535eedea7852eecf98cd90`, build `2026-09-15T17:55:09Z`, `dirty=false`. The API identity and frontend build marker agreed. The running binary and `/tmp/crewship-1-dev` had SHA-256 `e2cd902ffa147dd4cd59e7d9657d26679bf0f7e5897e38984116053720bf9362`. Health returned `ok`.

The final follow-up commit only updates this report and the design document; it does not change deployed product code. DEV2 and DEV3 were not deployed.

## Review and release gate

PR #2562 remains open and stacked on unmerged #2556. Recent pushes triggered only PR Labeler; CI must be dispatched explicitly for the final documentation head and its live outcome is recorded in the PR completion comment. CodeRabbit skipped the review because the PR exceeds its 100-file limit; its green status is not completed review. Independent review and the original PRD §11 human usability acceptance remain open. This report does not claim release acceptance or permission to merge.
