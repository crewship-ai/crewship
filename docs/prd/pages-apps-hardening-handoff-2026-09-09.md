# Pages Apps — current handoff, 2026-09-09 16:43 UTC

> September 10 CI results, test fixes and current acceptance status:
> [follow-up validation](pages-apps-validation-2026-09-10.md).

Task: independent counter-review, then user “vše oprav a pokračuj”.
Issue/claim: #2472. Integration: `.claude/worktrees/pages-apps-project`, branch
`feat/pages-apps-project`. Root clone is a different branch with other WIP;
do not switch it or overwrite its files.

## Current deployed state

Only dev3 was deployed, through the existing `crewship-ws@3` wrapper.
Live code commit: `34fd43e5`, clean build. Verified running executable SHA256:
`98017c717556f2e43811870440f953948e340212ba76c03a2ef58e98517c9782`.
Compiler image configured in `/srv/crewship/dev3-pages-release/start.sh`:
`sha256:f2ba48d349d2d89b8d1e54f9d779c36bd10d37d2a9f70c39965c6d39145d244b`.

A consistent stopped-service snapshot of database, Pages data and old release
is at `/srv/crewship/dev3-pages-release/rollback-2472-20260909`. It excludes crew
volumes. Full live workspace backup API failed on stopped crew containers;
Engineering was started for the authoring test, but the unrelated credential-lab
crew was left stopped. Do not describe the snapshot as a full workspace backup.
Live DB has 300 migrations and integrity `ok`; before deployment its read-only
copy upgraded from 294 to 300 successfully.

## Implemented fixes

- Workspace-wide retention and pre-write recovery; protected draft/live/running
  roots survive. Maximum checkpoint benchmark: 256 files, 3 iterations, 0.804s/op.
- Shared artifact-reader lease is separate from exclusive Git compaction; bounded
  shallow history preserves retained hashes and round-trips through backup.
- Explicit maintenance/fsck; historical publish retries expose actual live state.
- Page detail exposes application presence/version; panel-only Pages skip the
  app loading barrier; withdrawal/revocation clears executable cache.
- Compiler errors and persistence failures stay distinct; save validates paths
  and dependencies against the same profile; image fingerprints prevent drift.
- Routine-definition provenance is visible and recorded at enqueue. Execution
  still uses current routine definitions/scripts; this is not revision pinning.
- Desktop Chromium policy, visible development-origin limitation and navigation
  stop. Non-loopback runtime requires HTTPS. SDK action keys work without UUID API.
- Streaming UTF-8 input, isolated TypeScript modules, empty Git quota/boundaries,
  interrupted-lock retry, exclusive first lock-file creation, canonical SQL timestamps and CI build path filters.

## Measured evidence

Machine-readable: `reports/pages-apps-hardening-2026-09-09.json`.
Historical independent findings: `pages-apps-counter-review-2026-09-09.md`.
Current contract: `pages-apps-v1.md`; install/recovery:
`../guides/pages-apps-operations.mdx` (experimental).

- Full Go integration: all code packages passed; only failure was missing MDX
  stability label. After correction, affected documentation tests passed.
  Do not claim the original full command exited zero. API took 1419.905s,
  CLI 258.920s, database 405.060s (`/tmp/pages-2472-main-full-go.log`).
- Full vet passed, then changed Go packages passed vet after review fixes.
  Frontend 46 files / 598 tests, zero lint errors / 31 warnings; static build passed.
  Migrations, agent invariants and final strict docs-inventory passed.
- Real CLI daemon restart and backup/restore acceptance passed, 35.032s.
- Final `scripts/test-pages-apps.sh` passed with no skipped required tests:
  Docker compiler/API/MCP/seed/Node collector, deterministic one-byte UTF-8 input,
  Chromium process isolation/loop removal and compiled SDK action/status/history.
  Evidence: `/tmp/pages-2472-verified-final/`, corresponding `.log`.
- Follow-up first-lock creation passed the lease suite ten times, its race
  regression five times and the actual API concurrent-save test thirty times
  (11.223s); vet passed. New macOS CI confirmation remains pending.
- Final concurrency/publication/lease regressions passed ten repetitions; earlier
  admission-recovery/concurrent-save regressions passed twenty repetitions.
- Live Chromium loading/narrow viewport/reduced-motion passed; real WebKit offered
  panels without an iframe. Browser dependencies came from the existing Playwright
  Docker image, not host package installation.
- Live action pending `pnd_cmtua33x1001c0fbdec96` produced completed run
  `run_cmtua364y000ac5f80683`. Both panels have sequence 342 at 15:53:48Z from that
  exact run. This proves the real UI → confirmation → queue → routine → data loop.
- Live fsck: 3 sources, 3 checkpoints, 3 artifacts, 26 Git objects, zero failures.
- Live VIEWER with only a read grant opened the application. Revoking that grant
  returned 404 from the application endpoint and removed the open iframe on
  revalidation. The temporary workspace membership was removed afterwards.
  Evidence: `/tmp/pages-2472-live-loading/live-reader.json`.
- Final distribution Docker build passed:
  https://github.com/crewship-ai/crewship/actions/runs/34375031600

## Remaining gates — not complete

Actual chat authoring is blocked by Engineering's guided policy. Alex invoked
Page MCP init/create, but `page_create` returned 403 and no Page/source/build was
created. The response says `pending_review: true`; no actionable approval-queue
item exists. User was asked for explicit approval of a separate trusted test
crew. No existing policy was loosened. Do not substitute transport coverage for
successful live LLM authoring. Live reader/revocation acceptance now passes,
as do automated authorization/cache regressions.

User also needs to choose a separately registrable runtime domain outside
unifylab.cz and DNS/TLS management. Dev3's explicit same-origin exception is an
internal demonstration, not evidence of production process isolation.

## Review delivery

Preserved original 201-file implementation: `1bbae70e`. Integrated main remains
`1a128896` (checked again 16:02). No giant integration PR and no merges yet.

| PR | Layer | Files | Current head |
|---|---|---:|---|
| #2475 | source/compiler/core CI | 46 | a9ff7ad7 |
| #2477 | API/storage/backup/MCP/config/contracts | 87 | 70068341 |
| #2479 | CLI/seed/examples/SDK browser CI | 40 | 916f43c8 |
| #2480 | UI | 44 | 01c0e9fa |
| #2481 | design/reviews/operations docs | 31 before this update | see branch |

Each PR is based on its predecessor. Preserve ancestry (merge commits) when
merging; retarget the next PR to main and verify the diff remains under 100 files.
CodeRabbit reviewed source at 15:36 and requested changes. Nine actionable
findings were addressed; response is posted. Second incremental review completed 16:23:11. Its sole new provider-seam
request was withdrawn after code-based rebuttal; source was approved 16:29:21.
A subsequent macOS CI failure identified concurrent first lock-file creation
returning ENOENT; follow-up `a9ff7ad7` still needs CI/review. It uses exclusive creation and
reopens the winner on EEXIST, with independent-handle first-open regressions.
It is deployed as `34fd43e5`; health, running hash and fsck verified. The service permits one included
review per hour: do not burst requests or merge based on green/throttled status.
Other four PRs have no actual review yet. Earliest next included slot is
about 17:14 UTC; do not submit a burst. No merge has been attempted. Wait for actual reviews, resolve valid
findings and verify CI before merging. Release the claim when stopping/finished.

Current follow-up CI: source 34377781669, server 34378372215, UI 34378375514.
The stacked server/UI runs were dispatched explicitly because CI only
automatically triggers on PRs targeting main. They are still pending.

Full CI before the lock initialization follow-up: UI 34374672182 passed Go,
shuffle, macOS, Linux arm64, frontend and browser isolation; race jobs remain
pending. Server 34374675235 failed macOS concurrent first save (200 + 500):
`openat .git-maintenance.lock: no such file or directory`. Source run is
34374044883. Do not treat the UI macOS pass as disproving the server failure.
Earlier storage teardown, HTTP fixture, timestamp and documentation failures
were fixed. The earlier EINTR retry did not fix this newly diagnosed open error.

Shared disk also filled while linking the final API test/server. The API test
passed with stripped symbols; the production build completed using our private
`/dev/shm` directory for temporary files. Deployment moved the generated
binaries into the release directory to avoid duplicate copies. Original
rollback snapshot remains; the temporary previous-executable links were removed
only after the new running hash, health and fsck passed. No integration binary
copy remains; use `/srv/crewship/dev3-pages-release/crewship` with port 8083.

Shared disk filled during lint; only our completed `.next` intermediates and
obsolete test executables were deleted. Keep compiles serialized (`GOGC=30`,
`GOMAXPROCS=2`, `-p 1`); do not prune shared caches or unrelated worktrees.
