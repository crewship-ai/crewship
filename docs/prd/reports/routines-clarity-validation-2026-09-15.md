# Routines clarity — validation 2026-09-15

PR: [#2556](https://github.com/crewship-ai/crewship/pull/2556). Branch: `feat/routines-clarity-release-1`.
Scope: [C1–C7](../ROUTINES-CLARITY-PRD-2026-09-15.md). This does not close the original PRD's human usability acceptance.

## Automated evidence

- Full frontend with frozen-lockfile dependencies: **9,104 tests, 760 files passed**. Local React was initially stale (19.2.8 versus locked 19.3.0); `pnpm install --frozen-lockfile` restored the declared environment. No Pages code was changed to hide that mismatch.
- Frontend lint: 0 errors, 30 existing warnings. Test types: no new diagnostics; 183 baseline errors remain tracked in #2493.
- Targeted Go input/API regressions passed. Invalid bounds/path are rejected before the stub agent is invoked.
- Runtime checker-tier limits tested with 0, 1 and 2; feedback reaches the next worker. Mutation proof: removing the cap makes limit=1/2 fail with six calls instead of two/four. The temporary mutation worktree was removed.
- Full Go vet, quick repository checks, agent invariants, migration lint passed. OpenAPI regeneration produced no changes. No migration is introduced.

- Full `go test ./... -count=1 -timeout=25m`: every reported package except `internal/database` passed, including API (1115.398s) and pipeline (37.830s). The database package reached its total 25-minute timeout during `TestV167_RestoreBackfillRepairsRestoredRows`, which had been running for eight seconds. This run is recorded as failed, not green. Database-only rerun with a RAM-backed temporary directory follows below.

## Interpretation limits

- Path format checks syntax only. It does not check file presence or access.
- Old type-only inputs preserve their previous server contract. Explicit bounds/format opt into validation.
- The rules summary is derived from a recipe or archived executed definition using current engine semantics. It is not an audit of historical permissions or proof of model quality.
- Declared structural validation for non-agent live runners is not enforced by the current engine. The new summary states this explicitly; this increment does not introduce a universal output gate.
- `max_iterations` caps configured worker/checker model tiers per execution attempt. It does not create same-tier revisions; transport and step retry budgets are separate.
- Timing diagnostics use loaded execution records only, exclude parent/child double counting, and identify partial histories. This is not a performance benchmark or a claim that the engine itself became faster.
- The feature branch is for joint dev1 acceptance, not a merged release. CodeRabbit initially returned a rate-limit notice instead of a review.

## DEV1 deployment and browser evidence

Production code deployed: `168d1f8138c0635d333ef8a730ff710b0087226d`, build `2026-09-15T10:00:01Z`, `dirty=false`. Public API identity, `.web-build-marker`, and the running binary agree. SHA-256 of `/tmp/crewship-1-dev` and `/proc/3407457/exe`: `ab4f38c2f1b5b1643d66135f9d5824220f792817a40fe00d4799de2b87232d8a`.

The first service reload ended with a control-process signal; the configured automatic restart restored the service. The subsequent mobile-fix reload completed normally. `/api/health` returned `{"status":"ok"}` and systemd reported active/running. Dev2 and dev3 were not changed.

Standalone acceptance: `e2e/routines-clarity.mjs`, authenticated through the real login screen, using owned temporary routines with local transforms:

- Invalid range and relative path rejected through API without creating a run.
- Desktop 1440px and phone 390px: inline errors, focus on first invalid input, reset to the recipe default, no horizontal page overflow.
- Real Run submits and persists typed zero/false; run history supplies those values to Run again, while Restore default still restores the recipe default.
- Recorded duration and archived behavior are available on completed runs.
- A deliberately failing second transform retains the first output and exposes the retained-evidence link and next-step guidance.
- No browser page errors; owned temporary routines are deleted after the test. Audit run history remains.

Browser validation exposed a mobile pointer/focus regression: showing a new blur error could move Run between pointerdown and click. The fix lets submit/reset/cancel actions own validation without moving their button mid-click. The updated acceptance passed on both widths; the focused form rerun passed 16 tests in two files. Product lint for the fix passed. The production build was repeated by the standard dev1 reload.

Private logs/screenshots/report are under `/tmp/crewship-1-clarity-*`; credentials are not committed. The full Go invocation also saw the pre-existing untracked claims tests; clean-checkout CI remains the independent PR check. The original 17 untracked files (including the earlier HTML map) were restored after clean builds, with safety stashes retained. No original WIP is included in this PR.

Latest completed browser acceptance: eight checks passed. Run IDs: `run_cmu2i5ziq0013863da063`, `run_cmu2i60f6001482485931`, `run_cmu2i60s300172c60e300`. Both temporary routines were deleted with HTTP 204.

## Acceptance mapping

| Criterion | Evidence and remaining boundary |
|---|---|
| C1 | Full frontend suite, focused form tests, real API rejection, desktop/mobile submit and focus. A screen reader session with a human was not performed. |
| C2 | Zero/false and history/reset verified in the browser; preset defaults covered by component regression. |
| C3 | Client/server regressions and real API/browser rejection. File existence/access deliberately outside scope. |
| C4 | API projection regression, component rendering and browser recipe summary; archived behavior present on live run. |
| C5 | Live controlled failure retains a prior output and exposes recovery guidance; existing status distinctions covered by frontend tests. |
| C6 | Unit tests for duration/repeated paths/retained outputs, completed run browser rendering. No new full performance baseline or huge partial-history browser run. |
| C7 | Actual runner path with controlled worker/checker responses and a failing mutation. No paid live-model repeatability experiment. |

Database-only rerun, `TMPDIR` in an owned `/dev/shm` temporary directory: **passed in 455.069s** with a 10-minute limit. The temporary directory was cleaned up. All Go packages therefore have a passing result across the full run and this isolated rerun; the original full invocation remains a failed run.

After the deployed commit, only the standalone browser acceptance script and documentation changed. A diff against `168d1f8138c0635d333ef8a730ff710b0087226d` for `app`, `components`, `hooks`, `lib`, `internal`, `schemas`, package/lock files and `web` was empty. DEV1 therefore contains all final production changes without an unnecessary documentation-only restart.

At the final documentation update, Frontend Test CI passed; the remaining CI jobs and CodeRabbit review were still pending. CodeRabbit's latest inspected state was THROTTLED, not reviewed. No merge was performed.
