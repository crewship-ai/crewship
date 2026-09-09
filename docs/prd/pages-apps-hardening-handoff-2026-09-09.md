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
