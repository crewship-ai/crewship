# Pages Apps — current handoff, 2026-09-09 16:17 UTC

Task: independent counter-review, then user “vše oprav a pokračuj”.
Issue/claim: #2472. Integration: `.claude/worktrees/pages-apps-project`, branch
`feat/pages-apps-project`. Root clone is a different branch with other WIP;
do not switch it or overwrite its files.

## Current deployed state

Only dev3 was deployed, through the existing `crewship-ws@3` wrapper.
Live code commit: `529a3298`, clean build. Verified running executable SHA256:
`d020592a333345028f9c03a2cd569b4cc2829ac432179e9f56707bc2aded1c7d`.
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
  interrupted-lock retry, canonical SQL timestamps and CI build path filters.

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
- Final concurrency/publication/lease regressions passed ten repetitions; earlier
  admission-recovery/concurrent-save regressions passed twenty repetitions.
- Live Chromium loading/narrow viewport/reduced-motion passed; real WebKit offered
  panels without an iframe. Browser dependencies came from the existing Playwright
  Docker image, not host package installation.
- Live action pending `pnd_cmtua33x1001c0fbdec96` produced completed run
  `run_cmtua364y000ac5f80683`. Both panels have sequence 342 at 15:53:48Z from that
  exact run. This proves the real UI → confirmation → queue → routine → data loop.
- Live fsck: 3 sources, 3 checkpoints, 3 artifacts, 26 Git objects, zero failures.
- Final distribution Docker build passed:
  https://github.com/crewship-ai/crewship/actions/runs/34375031600

## Remaining gates — not complete

Actual chat authoring is blocked by Engineering's guided policy. Alex invoked
Page MCP init/create, but `page_create` returned 403 and no Page/source/build was
created. The response says `pending_review: true`; no actionable approval-queue
item exists. User was asked for explicit approval of a separate trusted test
crew. No existing policy was loosened. Do not substitute transport coverage for
successful live LLM authoring. Separate live reader/revocation acceptance remains;
automated authorization/cache regressions already pass.

User also needs to choose a separately registrable runtime domain outside
unifylab.cz and DNS/TLS management. Dev3's explicit same-origin exception is an
internal demonstration, not evidence of production process isolation.

## Review delivery

Preserved original 201-file implementation: `1bbae70e`. Integrated main remains
`1a128896` (checked again 16:02). No giant integration PR and no merges yet.

| PR | Layer | Files | Current head |
|---|---|---:|---|
| #2475 | source/compiler/core CI | 46 | 0b7c78df |
| #2477 | API/storage/backup/MCP/config/contracts | 87 | 038ecdc1 |
| #2479 | CLI/seed/examples/SDK browser CI | 40 | 20b9df93 |
| #2480 | UI | 44 | 3a60231e |
| #2481 | design/reviews/operations docs | 31 before this update | see branch |

Each PR is based on its predecessor. Preserve ancestry (merge commits) when
merging; retarget the next PR to main and verify the diff remains under 100 files.
CodeRabbit reviewed source at 15:36 and requested changes. Nine actionable
findings were addressed; response is posted. Final incremental review was
requested 16:14:34 and is actually pending. The service permits one included
review per hour: do not burst requests or merge based on green/throttled status.
Other four PRs have no actual review yet. Wait for actual reviews, resolve valid
findings and verify CI before merging. Release the claim when stopping/finished.

Final full CI: UI 34374672182, server 34374675235, source 34374044883. No failed
jobs observed at 16:17; some Go/race jobs still running. Earlier failures were
fixed (storage teardown, HTTP fixtures, timestamps, docs placement and flags);
one macOS concurrent save 503 was not independently reproduced. Diagnostics and
signal retry were added; inspect the final macOS result rather than assuming.

Shared disk filled during lint; only our completed `.next` intermediates and
obsolete test executables were deleted. Keep compiles serialized (`GOGC=30`,
`GOMAXPROCS=2`, `-p 1`); do not prune shared caches or unrelated worktrees.
