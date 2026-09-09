# Pages Apps — delivery handoff, 2026-09-09 15:40 UTC

User requested independent counter-review, then “vše oprav a pokračuj”.
Issue/claim: #2472. Integration worktree: `.claude/worktrees/pages-apps-project`,
branch `feat/pages-apps-project`. The root clone is another session's branch;
do not switch it or overwrite its WIP.

## Findings and implementation

The independent counter-review is `pages-apps-counter-review-2026-09-09.md`.
It records the original state, not the current implementation. The alleged
missing refresh route was a moved manifest line, not a missing route. Additional
reproduction found a workspace permanently blocked by 512 failed builds.

Implemented: workspace retention/admission recovery; separate Git compaction
lease so artifact readers continue; bounded Git ancestors with exact retained
checkpoint hashes and backup boundaries; packed checkpoint writes; explicit
maintenance/fsck; publication retry reports historical receipt and actual live
state; Page detail exposes application presence/version; panel-only Pages skip
app loading; withdrawal clears executable cache; compiler persistence has its
own budget; profile/path/dependency validation and worker fingerprint; desktop
Chromium fallback policy; navigation stop; routine-definition drift provenance.
Routine provenance does not pin execution to an old routine revision.

Current contract: `pages-apps-v1.md`. Historical implementation notes:
`pages-apps-v1-history-2026-09-09.md`. Installation and limitations:
`../guides/pages-apps-operations.mdx` (experimental).

## Measured verification

- Full integration Go suite completed with `GOGC=30 GOMAXPROCS=2 go test -p 1
  ./... -count=1 -timeout=30m`. Only failure was a missing documentation stability
  label; after adding it, docs-surface-check and docs-inventory passed. API took
  1419.905s, CLI 258.920s, database 405.060s. Log:
  `/tmp/pages-2472-main-full-go.log`; affected rerun `pages-2472-docs-final-check.log`.
- `go vet -p 1 ./...` passed. Frontend 46 files / 598 tests passed; lint zero
  errors / 31 warnings; production static build passed. Migration and agent
  invariants passed.
- Real Docker compiler/API/MCP/seed/Node collector and separate-site Chromium
  suite passed without skipped required tests. Evidence:
  `/tmp/pages-2472-integrated-ci/`, `/tmp/pages-2472-integrated-ci.log`.
- Real CLI daemon restart and backup/restore acceptance passed, 35.032s:
  `/tmp/pages-2472-main-live-cli.log`.
- Consistent read-only copy of dev3 database upgraded from 294 to 300 migrations;
  integrity check returned `ok`. Live database was not modified by this check.
- Maximum 256-file Git checkpoint benchmark: 3 iterations, 0.804s/op.

## Review delivery and new CI findings

Original implementation preserved at `1bbae70e`; hardening at `3185ee52`;
main integrated at `1a128896`. The giant checkpoint was not submitted as a PR.
Dependency stack (all below 100 changed files):
1. #2475 source/compiler/profile and mandatory core CI (46 files).
2. #2477 API/storage/backup/MCP/config (84 files before follow-up fixes).
3. #2479 CLI/seed/examples and expanded CI (40 files).
4. #2480 UI (44 files).
5. Documentation PR prepared on top of UI.

Actual CodeRabbit review on #2475 arrived around 15:36 UTC, with nine actionable
comments. Follow-up fixes are being verified: streaming UTF-8 decoding, isolated
TypeScript modules, opaque-frame-safe action keys, HTTPS on non-loopback runtime,
empty Git quota/boundary handling, error diagnostics and preserved smoke failures.
Do not equate a green status or review command acknowledgement with a review.
The service currently allows one included review per hour; #2477 manual request
at 15:24 was explicitly throttled, next slot quoted in 48 minutes.

CI additionally caught missing Docker build path filters (examples/tools),
noncanonical SQL timestamps and a test tearing down storage before its background
build ended. Fixes are in progress; the two API regressions passed 20 repetitions
locally (59.952s). macOS concurrent-save 503 still needs diagnostic rerun.
Source Go Race hit the 25-minute CLI package cap while starting another test;
no race was reported. Its bounded package deadline is being raised to 35 minutes
inside the existing 55-minute job. Do not claim all remote CI green yet.

## Remaining acceptance / deployment

Dev3 still runs the prior release via `/srv/crewship/dev3-pages-release/start.sh`
and `crewship-ws@3`. New build fingerprint requires matching rebuilt tools image
and server. The earlier candidate `6ebf92a8` is superseded by compiler review fixes;
do not deploy it with the new image.

Live workspace backup API failed because stored crew containers were stopped.
Engineering was started successfully for the intended authoring acceptance, but
backup next failed on the unrelated stopped credential-lab crew. Do not start
unrelated crews merely to work around backup. Prepare a consistent rollback
snapshot before deploying only dev3; never use another instance.

Still required: finish CI/review fixes, publish docs PR, actual chat-agent
create/edit/build acceptance, separately authorized reader/action/revocation
checks, and review-confirmed merges in dependency order. Live LLM authoring is
not proven by the MCP transport tests. Release issue claim when finished/stopping.

User question remains pending: separately registrable runtime domain outside
unifylab.cz and DNS/TLS management. Existing dev3 same-origin mode is explicitly
an internal development exception; it does not establish production isolation.


## Verified live progress at 15:59 UTC

Dev3 now runs `a4aaecb3`, process SHA256
`717f400d199e64733dc325bd9bf5fd1af49e1a92e48127019d255613010f3f23`.
A stopped-service snapshot of database, Pages storage and old release exists at
`/srv/crewship/dev3-pages-release/rollback-2472-20260909`; it excludes crew volumes.
Matching compiler: `sha256:f2ba48d349d2d89b8d1e54f9d779c36bd10d37d2a9f70c39965c6d39145d244b`.

The complete real Docker/API/MCP/seed/collector/Chromium script passed after its
HTTP test fixtures were updated for the stricter HTTPS contract. A deterministic
Docker test streams every input byte separately and preserves Czech text/emoji.
Compiled SDK action/status/history browser testing passed and is now required by
the final CI script. The example's own action-key generator was corrected too.
Live Chromium loading/responsiveness passed; WebKit correctly offers panels and
creates no application iframe. WebKit used the already-installed Playwright Docker
image because host system libraries were missing.

Live browser action generated pending `pnd_cmtua33x1001c0fbdec96`, run
`run_cmtua364y000ac5f80683`, completed. Read-only verification finds BOTH panel
writes at sequence 342, produced 15:53:48Z by that exact run. This proves the
actual UI → confirmation → queue → routine → Page-data loop.

Actual chat authoring did NOT complete. Alex invoked init/save_page correctly,
but Engineering is guided. `page_create` returned 403 with `pending_review: true`;
no actionable approval-queue item exists. No Page/source/build was created.
The user was asked for explicit authorization for a separate trusted test crew;
no policy was loosened. Do not call transport tests or this blocked attempt a
successful LLM authoring demonstration.

Documentation PR is #2481. API/env documentation moved into the server layer so
its strict docs-inventory now passes without depending on later CLI flags.
Remote CI found an HTTP same-origin fixture incompatible with the new HTTPS
policy; it now uses literal loopback. Repeated focused publication/concurrency
checks passed. Signal-interrupted Unix locks also retry within their deadline.
These latest fixture/lock changes are being propagated; no merge is claimed.
Machine-readable evidence: `reports/pages-apps-hardening-2026-09-09.json`.


## Final code deployment at 16:06 UTC

Dev3 was updated to `529a3298` (clean build), verified live process SHA256
`d020592a333345028f9c03a2cd569b4cc2829ac432179e9f56707bc2aded1c7d`.
The compiler profile is unchanged from the successful real browser/action run.
The complete final script now includes required UTF-8 chunk coverage and the
compiled SDK action/status/history browser branch; it passed in
`/tmp/pages-2472-verified-final.log`. Focused final source/publication/concurrency
checks passed ten repetitions; vet of the changed Go packages passed.

Live fsck reports healthy: 3 sources, 3 checkpoints, 3 artifacts, 26 Git objects,
zero failures. One commit-hook lint was blocked by a full shared disk, then
passed after deleting only our completed Next intermediates and obsolete test
executables. No shared caches or unrelated worktrees were pruned.

Final dependency updates are being pushed. All API/environment contracts are
with the server layer; CLI maintenance flags are with CLI. Final CI and external
review remain open, as do the two explicit user inputs above. No merge yet.
