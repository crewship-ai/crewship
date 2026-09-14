# Pages Apps — validation and remaining delivery gates, 2026-09-10

Follow-up to issue #2472 and the user's request to fix the remaining problems.
The [implementation handoff](pages-apps-hardening-handoff-2026-09-09.md) records
code changes, deployment, live acceptance evidence and recovery details.

## CI verified from GitHub

The three final September 9 runs have completed on the expected commits:

| Layer | Commit | Run | Result |
|---|---|---|---|
| source | a9ff7ad7 | [34377781669](https://github.com/crewship-ai/crewship/actions/runs/34377781669) | one Frontend Test failure; all Go/race/macOS and Pages jobs passed |
| server | 70068341 | [34378372215](https://github.com/crewship-ai/crewship/actions/runs/34378372215) | success |
| UI and preceding code layers | 01c0e9fa | [34378375514](https://github.com/crewship-ai/crewship/actions/runs/34378375514) | success |

The source failure was the credential reveal expiry test at line 57: the DOM
already contained the value, but the test's setTimeout spy did not yet contain
the passive effect's timer. Finding DOM content does not guarantee that every
passive effect has run.

## Fixes in this follow-up

- Reveal expiry now flushes the resolved request and React effects with async
  `act`, then advances controlled timers. It checks visibility just before the
  deadline and removal at the deadline, including clearing the reason and
  disabling another reveal. It restores real timers after unmounting.
- The broader credentials suite exposed another passive-effect race in the
  device-login error test: the alert was present while the callback still
  reported `starting`. That test now waits for the expected `error` callback.
- These are test-only changes. Production credential behavior, Pages runtime,
  policies, service configuration and the deployed binary are unchanged.

Source PR commits: `e3c7c4cb` and `04bf425e`. Both fixes are propagated through
all five stacked PRs and preserved in the integration worktree (`adcf3c09`).
Focused reveal tests: 16 passed. Credentials suite: 20 files / 419 tests passed.
ESLint for both changed files passed. Full frontend verification passed:
701 files / 8,356 tests in 268.76 seconds (`/tmp/pages-2472-frontend-final-20260910.log`).

New CI runs: [source 34459514236](https://github.com/crewship-ai/crewship/actions/runs/34459514236)
and [UI 34459629052](https://github.com/crewship-ai/crewship/actions/runs/34459629052).
The UI stack requires an explicit dispatch because its PR does not target main.
Source Frontend Test is now confirmed successful on `04bf425e`, including the
previously failing test. Source Go lint, Playwright and harness jobs passed.
Longer Go/race jobs and the full UI run are still in progress; do not infer
their final result from the earlier green runs.

## PRD acceptance status

The [current PRD](pages-apps-v1.md#acceptance-criteria-and-evidence) is written,
but the product is not accepted as complete.

| Criterion | Evidence/status |
|---|---|
| 1. Agent authors and edits in chat, preview within 15 minutes | Not passed: Engineering guided policy returned 403. Explicit permission for a separate trusted test crew requested; no policy bypass. |
| 2. Human publication and concurrency fences | Implemented and regression-tested; live published demonstration exists. Criterion 1's complete author-to-publish journey remains unproven. |
| 3. Action, receipt and resulting data | Live action completed and wrote both panels; receipt isolation has automated coverage. |
| 4. Reader and revocation | Live VIEWER opened the application; grant revocation returned 404 and removed its iframe. Temporary membership removed. |
| 5. Installation, backup/restore, restart, DNS/TLS | CLI restart/backup/restore and distribution build passed. Production separate-site DNS/TLS remains unconfigured/unverified. Live full-workspace backup was blocked by stopped crew containers; the stopped-service snapshot excludes crew volumes. |
| 6. Panel loading and browser/loop behavior | Automated and live browser evidence recorded in the handoff. Same-origin dev3 is not production process-isolation evidence. |
| 7. Retention and lifecycle regressions | Passed; final macOS concurrent initialization and both race lanes are now confirmed green. |
| 8. Mandatory CI and reviewed delivery | Tests are wired; this follow-up CI remains pending. Actual reviews and merging remain incomplete. |

All five PRs are open: #2475, #2477, #2479, #2480, #2481. The source PR's
September 9 approval covered `0b7c78df`, before the lease initialization and
current test fixes. The other four have no submitted reviews.
The September 10 review request was rate-limited; the bot reported about
56 minutes until the next included review (approximately 10:10 UTC).
No green status or rate-limit reply is treated as completed review. No merges.

The two explicit user questions remain pending: permission for the isolated
trusted authoring test crew, and the separately registrable runtime domain with
its DNS/TLS management. Do not mark these criteria complete by changing the PRD.
