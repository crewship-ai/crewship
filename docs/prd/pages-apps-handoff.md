# Pages Apps — implementation handoff, 2026-09-09

Authoritative scope: [Pages Apps v1](pages-apps-v1.md). The implementation is
uncommitted in `/srv/crewship/crewship_3/.claude/worktrees/pages-apps-project`,
branch `feat/pages-apps-project`, base `676e16e45897bf6767f47156a894fb01bfb56162`.
The root checkout is a different branch: do not move or discard its changes.
The recovery stash `pages-apps-p1-before-base-refresh` is retained; do not reapply
it over the already recovered implementation. Dev3 now runs the Pages demo release
via an instance-specific systemd override; the root checkout remains unchanged.

## Independent review package — 2026-09-09

Author self-audit: [complete rationale and findings](pages-apps-review-audit-2026-09-09.md).
Evidence identities: `reports/pages-apps-review-evidence-2026-09-09.json`.
No product code was changed or redeployed by the audit, and tests were not rerun.
It records current successful verification, four open release gates and 17
risk/limitation findings, including source/worker path-validation mismatch,
Git ancestor retention, toolchain/SDK compatibility and mutable backend routine
versions. Static findings are explicitly distinguished from reproduced failures.

## Smooth Page loading — current work, 2026-09-09

User reported white/black flash opening Operations Lab and requested controlled
animation/responsiveness. Removed explicit bg-white iframe. PageApplicationView
holds themed loading while publication metadata resolves (no panel fallback
flash); PagePreviewFrame keeps the iframe opacity zero and outside keyboard
navigation until the fixed bootstrap receives the first snapshot and reports
rendered after two requestAnimationFrames on the existing port. 300ms fade,
reduced-motion bypass, no replay on snapshot/theme changes. 20s visible-tab
load deadline; hidden tabs pause the deadline. No new RPC privileges.

Application controls wrap, Pages height uses 100dvh, mobile SubBar identity
cannot shrink underneath its actions. Live mobile screenshots caught that
pre-existing overlap. No demo project/build image update required; publication 3
continues to use the new host/bootstrap with the same immutable artifact.

Final host release deployed on dev3, binary SHA256
`f82b350d6c229892e25b8df7e66c1c745abc4826b226ff2d0b0e1aa7db531fe7`.
Previous release: `crewship.before-loading-20260909`; later mobile-predecessor
`crewship.before-loading-mobile-20260909`, both in the release directory.
Publication remains 3. Final build passed (`/tmp/pages-loading-final-build.log`),
502 affected frontend tests passed (`/tmp/pages-loading-responsive-tests.log`).
Vet passed; lint 0 errors/32 existing warnings. Full Go suite passed, exit 0: **139 tested packages + 11 with no tests**
(`/tmp/pages-loading-all-go.log`); full frontend passed, exit 0: **670 files /
8,192 tests** (`/tmp/pages-loading-all-frontend.log`). The full Go result
supersedes the earlier theme-era run that caught the now-fixed OpenAPI field.
No verification process for this task remains pending.

Live Chromium AND WebKit acceptance passed (`/tmp/pages-loading-final-browser.log`):
delayed bootstrap concealed, first rendered frame fade, desktop/900/390px layout,
no mobile heading/action overlap, mobile services and expanded snapshot history,
reduced-motion bypass, no Page runtime errors.
Screenshots `/tmp/pages-loading-browser/`, including visually checked WebKit
loading and mobile views. WebKit fixture initially included cancelled dashboard
fetch errors during login navigation and sampled resize/transition before paint;
final test uses a fresh authenticated tab, brings it forward and waits for computed
opacity/layout before assertions. No application CSS overflow workaround was needed.
Actual SDK action regression also passed after deployment:
`/tmp/pages-loading-live-action.log` — real Ops container run completed, trusted
confirmation, history, filter, stop/reopen, no JS errors. Temporary browser
credential env files removed. This verifies normal loading, NOT the outstanding
pathological stop-loop gate.


## Shared palette + animations — current work, 2026-09-09

User requested unified company colors while preserving completely custom Pages,
plus animations. Implemented workspace `pages_theme` JSON (six validated hex
colors), existing OWNER/ADMIN PATCH + membership-scoped list/get, audit and
workspace.updated event. New Settings/General appearance card with color inputs,
preview, reset and contrast hint. Host reuses the workspace store without extra
per-frame initial requests; refreshes on settings events/reconnect/focus. SDK
sets semantic CSS variables and exposes usePageTheme; agent project read includes
palette. Starter and portable Operations Lab use tokens; demo adds CSS entrance
and button feedback with reduced-motion override. No new external app libraries.

Migration: `20260909114221_workspace_pages_theme.sql`. New tools image built and
real seed build/publish tested:
`sha256:cca058230111eea5d07fa69c7537647ee46ea1b5b841233e3ff632e65b0dafb2`.
Dev3 now uses that image. Operations Lab is source revision/publication **3**,
build `cmtu1wjr400042f6e8ecd`, source digest
`4ad8095167662e7f55be7762114e9e2706949ce0360200d905bf6d928697ea16`,
Git `c464ac447263853764fe20ad63a8c1f5aae92048`, artifact
`c10d979889a7dbcd86caee7361124c0b4202aaa1d6abdfdf1da7d8668298c38e`.
Published through CLI CAS/check/publish; no live reseed or schedule duplication.
Latest host release includes the final OpenAPI required-field correction; binary SHA256
`7877da51ba45d867eadea3ee38b7c81640b29b1dd6005754781f94866829c4fe`.
Consistent pre-migration DB backup: `/srv/crewship/dev3-pages-recovery/before-pages-theme-20260909.sqlite`.
Previous binary/wrapper: `crewship.before-theme-20260909` and
`start.before-theme-20260909.sh` in the release directory. The migration is additive;
do not restore an old DB over subsequent writes for routine binary rollback.

Verification:
- Targeted Go workspace RBAC/persistence/audit tests passed
  (`/tmp/pages-theme-go-final-targeted.log`); actual SQLite TEXT-to-RawMessage
  scan failure was caught and fixed using a private string scan buffer.
- Whole frontend: 669 files / 8,185 tests passed (`/tmp/pages-theme-all-frontend.log`).
  Latest affected tests after the final refresh fix: 33 files / 364 tests passed
  (`/tmp/pages-theme-background-refresh-tests.log`).
- Go vet and TypeScript passed. Final Next static build passed
  (`/tmp/pages-theme-background-refresh-build.log`); final lint: 0 errors,
  32 existing warnings (`/tmp/pages-theme-background-refresh-lint.log`).
- Actual seed Docker lifecycle passed in 14.11s (`/tmp/pages-theme-seed-docker.log`).
- Whole Go run completed with 138 passing packages and one failing API contract
  test: Workspace.pages_theme was missing from the OpenAPI required list
  (`/tmp/pages-theme-all-go.log`). Added the required field and regenerated;
  targeted OpenAPI/workspace rerun passed (12.094s), and the entire generator
  package passed (1.136s). Logs: `/tmp/pages-theme-final-contract-tests.log`,
  `/tmp/pages-theme-final-generator-tests.log`. Final vet and strict docs inventory
  passed (`/tmp/pages-theme-final-vet.log`, `/tmp/pages-theme-final-docs.log`).
  Do not describe the original full run as green.
- Live Chromium acceptance passed (`/tmp/pages-theme-live-browser.log`): settings
  UI save, cross-tab realtime palette without iframe navigation, original palette
  restored, entrance animation, reduced motion, responsive layout, no JS errors.
  Screenshots in `/tmp/pages-theme-browser/`; settings and desktop visually checked.
- That live test initially found a real remount: background workspace loading
  replaced Page content. Fixed by preserving loaded state during refresh, with a
  regression test, rebuilt/redeployed host and reran acceptance successfully.
- Live functional acceptance also passed on publication 3
  (`/tmp/pages-theme-live-action.log`): filter, SDK history, stop/reopen, trusted
  confirmation, real Ops container routine, own-run status completed, no JS errors.
  Scheduled collector also produced a fresh snapshot after restart (12:12 UTC).

Source remains uncommitted; production runtime installation/browser-policy and
actual agent-chat authoring acceptance remain the PRD's outstanding gates.

## Demo seed integration — 2026-09-09

User requested the visible Operations Lab as a built-in default demo. Implemented
in `cmd/crewship/seeddata/pages_app.go`, `cmd_seed_page_app.go`, the existing
Pages/routine/file seed phases and `examples/pages-apps/embed.go`. The canonical
export, routine YAML and collector are embedded directly (no second asset copy).
Dockerfile now copies examples for the Go build. A fresh seed creates the Page,
Git source, bounded build, checks and publication through authenticated APIs;
missing worker/storage/runtime is reported without aborting other demo Pages.
Only exact reviewed built-in source + definition is auto-published. Reseeds
preserve custom projects/specs, all publication history and withdrawals, and
reuse a matching running/ready build. The routine/script retain normal seed
reapply semantics. No schedules or infrastructure configuration are auto-enabled.
Initial producer execution follows the existing asynchronous seed behavior;
Ops must be running (refresh after provisioning if the initial run failed).

Real Docker lifecycle test passed: fresh publication, duplicate-free reseeding,
withdrawal preservation, custom-source preservation, and disabled-worker source
retention (`/tmp/pages-seed-lifecycle.log`, 6.52s; subsequent combined verification
`/tmp/pages-seed-targeted4.log` passed). Collector fixture tests execute the actual
embedded Node script, validate both panel schemas and missing-accounting failure.
The live dev3 workspace was NOT globally reseeded/reset.
Additional real Docker regression `/tmp/pages-seed-final-lifecycle.log` passes
(10.17s), including an existing user-authored Page occupying the demo slug: its
definition survives and no app revision is attached. Final CLI rerun is
`/tmp/pages-seed-final-cli.log` (session 59731); final vet
`/tmp/pages-seed-final-vet.log` passed. The built release CLI validated the
embedded routine DSL (`/tmp/pages-seed-cli-validate.log`).

Final seed verification is green:
- `/tmp/pages-seed-all-go.log`: whole repository `go test ./... -p 4 -parallel 4
  -count=1 -timeout 30m`, exit 0, **139 tested packages + 11 with no tests**.
  API 850.025s, database 794.463s. This supersedes the earlier full run that
  caught the now-fixed Dockerfile COPY omission.
- `/tmp/pages-seed-final-cli.log`: full CLI rerun after the final Page-definition
  preservation guard, exit 0, 240.259s.
- `/tmp/pages-seed-final-vet.log`: whole-repository vet, exit 0.
- `/tmp/pages-seed-final-lifecycle.log`: actual offline Docker build and all seed
  preservation scenarios, exit 0. Collector tests and Dockerfile guard passed.
- No frontend implementation changed in this seed task; the same reviewed source
  and existing embedded Next export were used.

Deployed the binary including the seed to dev3 at ~10:54 UTC using the existing
instance-3 override. SHA256
`cde8261d4691f268c0520eb056ed7c4490be31badda1e141e2c28dbbc1818190`.
Prior binary retained as
`/srv/crewship/dev3-pages-release/crewship.before-seed-20260909T1054`.
Systemd active, public `/pages/custom-operations` HTTP 200, publication still 2
with its original source/artifact digests and two history entries. No live seed,
reset, schedule creation, root source switch, Git commit, push or merge occurred.
Implementation remains in the Pages feature worktree; main/root's other feature
branch is not the new seed source.

## Implemented and exercised

- Strict one-file source/bundle v2 transfer, inert imports, draft CAS, protected
  content-addressed source storage and backward-compatible panel v1 transfer.
- Real Git source+definition checkpoints, paginated history, historical reads,
  restore-as-new-draft; Studio Source history and CLI init/pack/unpack.
- One constrained offline Docker compiler, immutable artifacts, separate-site
  bootstrap and read SDK with bounded/coalesced snapshot delivery.
- Check and owner/admin publication with an atomic live pointer/Page definition
  update; idempotent receipts, CLI rollback, read application independent of worker.
- Published-only SDK runAction/getActionStatus through trusted host confirmation.
  Existing action RBAC/governance/queue, publication+definition fence held in the
  enqueue transaction, per-viewer idempotency/debounce and own-run status only.
- CLI `page action --publication`, `project action-status`, source/build/check/
  publish/retry/rollback acceptance against a real router, SQLite, Git and Docker.
- Two operation pilot definitions/routines, React action UI and bounded Python
  collectors in `examples/pages-apps/`. These are not installed into real crews.

- Direct agent chat authoring via `page_project` on the existing routines MCP:
  init/read/file-patch save/build/status/check, shared source/Git/CAS/compiler,
  owner crew + bound acting agent + page_create policy. No human user impersonation
  or agent publication. Read includes the exact SDK contract and Studio path.
  Agent snapshots survive DB dump/restore. No verified run provenance is available
  on this transport, so no run ID is claimed.

## Current delivery status — dev3 demo deployed 2026-09-09

**Previous checkpoint — live container integration:** publication 2 / source revision 2.
Current publication 3 and palette verification are recorded above.
`Obnovit měření` is a real SDK routine action with Studio confirmation, its own
pending/run status, and subsequent Page data. Browser end-to-end passed including
the actual container run; `/tmp/pages-dev3-container-browser.log`, screenshot
`/tmp/pages-dev3-browser/custom-operations-container.png`. First direct successful
run `run_cmtty27r50001ff0f4f41` completed in 1,388 ms/$0; actual browser action
`run_cmtty3e8c0002cae7a785` completed in about 1.9 s. The Ops container was started
through the CLI (normal provisioning refreshed its existing configured image).
Collector file installed through `crew files save`, not host ownership changes.

Routine `pages-operations-sample`, id `pln_cmttxyfpb0001c77a1dd7`, manually saved
and exercised before enabling schedule `psched_cmtty38r20001a61a4e70`: every minute
UTC, catchup skip, max 3 consecutive failures, concurrency key, 10s step bounds.
Panel SLA 3m. Data now measures the **Ops container**, replacing the earlier host
sample. No LLM steps. The routine cannot declare `agentless: true` because that
schema prohibits every `crewship` write (which could trigger agents); do not
claim an enforced agentless flag. This Page has no wake declarations.

Two real integration findings fixed with regression tests:
- `CREWSHIP_PAGE_STUDIO_ORIGIN` separates the iframe's public parent from
  `CREWSHIP_NEXTJS_URL`, which token sync/agent IPC use internally. Live wrapper
  now sets the latter to `http://127.0.0.1:8083` and the Page origin to public dev3.
  The earlier public internal requests hit Caddy's intended 404 gate. No proxy
  gate was weakened; token-sync 404s stopped after the corrected configuration.
- Whole-object `page.write` templates previously turned collector JSON into a
  string and failed panel validation. The initial live run
  `run_cmttxz89c00012bfdb11f` FAILED at services for this reason. The scoped fix
  preserves JSON object payload types and rejects invalid/null/scalar/array
  results before dispatch, without changing other verbs. Tests cover metric and
  status objects plus negative cases. The subsequent actual routine passed.

Verification at this checkpoint has been superseded by the completed seed-era
full Go/CLI/vet runs documented at the top. No test process remained pending at that earlier checkpoint; see current checks above.

The remaining text records publication 1 and the preceding deployment history.
The current export in `examples/pages-apps/custom-operations.page.yaml` is v2.

**Visible:** https://crewship-dev3.unifylab.cz/pages/custom-operations — Operations
Lab. Existing 7 Pages preserved; demo is the eighth. Reviewed React project:
source revision 1, publication 1, Git `c09a65d1e1f8862ac49ac2be2d059d90eb0eceab`,
build `cmttxhmsd0001d3e3a87d`, artifact
`d7ed4ea98390843c7186dd8bd5c4c1157777690a24b6bbe2fa59baae17e39f1e`.
Portable source/definition: `examples/pages-apps/custom-operations.page.yaml`.

The user requested an immediately visible custom Page on the existing domain.
An explicit **reviewed-code same-origin development mode** was implemented;
default separate-site validation is preserved. The switch is operator-only,
requires an exact origin match, preserves URL/TLS/sandbox/CSP/RBAC, and is
advertised by the authenticated API to the trusted UI. Application ETag includes
the switch. A malformed URL, another port or a sibling subdomain is not permitted
by this exception. This does NOT promise process isolation; stuck code may freeze
Studio. The user was told this limitation before deployment. No DNS/Caddy changes.

Live Chromium acceptance passes: normal demo-user login, actual compiled React
render and panel data, service filtering, real SDK history, stop/reopen, no JS
page errors. Log `/tmp/pages-dev3-live-browser.log`; final screenshot rerun
`/tmp/pages-dev3-live-browser-final.log`. The data producer takes real **host**
measurements once; it is not a crew routine or continuous monitor. The Page labels
this explicitly. No action button pretending to execute Ansible/MySQL was added.

P5 lifecycle UI/CLI, bounded SDK history, encrypted file backup/fork restore,
leases and retention remain implemented. Prior complete P5 Go gate passed all
138 packages. Current development-mode frontend tests (14) and build pass; lint
0 errors/32 warnings. Targeted publication regression passes after correcting its
unauthorized-viewer fixture to MEMBER rather than OWNER. Earlier broad targeted
run used the wrong fixture and failed; do not claim that invocation passed.
A full Go run started before the final API flag/frontend wiring was completed;
record it separately from final affected-package tests. Final vet has no findings; strict docs inventory passes after documenting the new
environment switch. Final affected-package checks pass (pagebuild/config/API);
log `/tmp/pages-dev3-demo-targeted-green.log`.

## Remaining release gates — reconciled after seed acceptance

Authoritative current checklist: `pages-apps-v1.md`, “Aktuální checklist před
uzavřením v1”. Historical checkpoints below do not reopen completed work.

1. Live customer-like agent-chat authoring/edit/review/publish/action acceptance,
   including viewer/revoked-user behavior. MCP/CLI/Docker and RBAC fixtures pass;
   the visible demo was authored through CLI, not by a live chat agent.
2. Production runtime installation/bootstrap and clean-install acceptance. Dev3's
   same-origin exception is development-only; it is not production isolation.
3. Browser support/fallback policy and corresponding UI behavior: Firefox/WebKit
   fail stop-loop tests. No portable hard browser CPU/RAM boundary is promised.
4. Git commit, actual review and merge. Source is still uncommitted in the Pages
   feature worktree despite the deployed development binary.

Whole Go/CLI/vet checks, dev3 render/actions/collector/schedule and the embedded
seed are complete; see the final evidence at the top. Longer-range charts,
collector/schedule UX inside the demo, and an agentic diagnostic/repair button
are optional follow-ups. Existing Check application is source/artifact/binding
verification, not an end-to-end agent health audit.

## Live dev3 deployment and recovery

Release directory: `/srv/crewship/dev3-pages-release` (binary embeds the real
Next export; 1,327 files checked by `scripts/embed-web-out.sh verify`). Initial
prepared `/tmp` candidate had only the Go embed placeholder; it was caught and
rebuilt with the existing verified static export **before live deployment**.
Do not reuse that old tarball as a UI release. Current startup wrapper sources
root `.env.local` without copying secrets, then pins port 8083, socket 3, absolute
existing SQLite path, public Studio origin, tools-image digest, runtime flag,
protected project store `/srv/crewship/dev3-pages-data`, and release sidecar paths.

Systemd override: `/etc/systemd/system/crewship-ws@3.service.d/pages-demo.conf`.
It changes only instance 3 to run the release as Type=simple, restart on failure
or explicit reload termination. Original root Next process is stopped; the Go
binary serves the embedded frontend. Use `systemctl status crewship-ws@3` and
`/srv/crewship/dev3-pages-release/crewship --server http://localhost:8083` for
operations. Root `dev.sh status` and old `/tmp/crewship-3-dev` describe the former
dev launcher and are not authoritative for this override.

Consistent pre-deploy SQLite backup:
`/srv/crewship/dev3-pages-recovery/before-pages.sqlite` (0600; integrity_check ok).
No live DB reset, seed, direct data write, root branch switch, merge or commit.
For an operator rollback, stop instance 3, remove only `pages-demo.conf`, reload
systemd, then start the original unit. Preserve the current DB/data; do NOT
restore an older database automatically and lose subsequent writes. The old
source checkout remains available. Review migration compatibility before rollback.

## Known boundaries

The custom iframe receives only readable panel snapshots. Source code still needs
review: CSP is not a complete anti-exfiltration boundary because a child can
navigate itself. Separate-site Chromium permits stopping an infinite-loop frame;
there is no portable browser CPU/memory guarantee. A queue receipt is not completed
execution, and a timeout/disconnect is not cancellation. Page Git does not pin the
backend routine implementation. Ordinary Page definition edits after publication
cause published actions to return 409 until the declaration agrees again.

No merge was performed. Dev3 deployment is described above. Follow the instance map in
CODEX.md and the repository claim/review workflow when those steps are needed. Keep this handoff and the
v1 PRD current with actual test outcomes rather than treating a partial green run
as a completed release gate.


## Previous P3 verification results

See the final P3 verification section of the v1 PRD for the exact limits of the
claim. 560 frontend tests, build, lint, vet, real CLI/Docker acceptance, Chromium
and final affected-package regressions passed. The full Go run found two guards
(raw CLI file write and missing FK indexes); both were fixed and their checks
rerun successfully. The whole repository was not run again as a single final
invocation. Preserve that distinction. Logs: `/tmp/pages-p3-*.log`.

## P4 final verification — 2026-09-09

Chat authoring is implemented and its targeted tests passed. Real sidecar MCP
HTTP → crew-bound API → Git → Docker build/check passed with two agent tokens;
real CLI Docker publication/rollback acceptance passed after fixing its opt-in
variable to `PAGES_TEST_BUILD_IMAGE`. API/policy/CAS/ownership tests, metadata
backup round-trip, route/OpenAPI guards, vet, migration lint, invariants,
`docs-inventory -strict` and `docs-surface-check` passed. Public Pages source,
build, publication and application API contracts and operator variables are now
covered in MDX. No UI runtime code changed in P4.

The final complete Go invocation exited 0 at approximately 08:19 UTC:
`GOMAXPROCS=4 go test ./... -p 2 -parallel 4 -count=1 -timeout 30m`.
All 138 packages with tests passed (11 packages had no tests): CLI 244s,
API 741s, backup 131s, database 606s, orchestrator 20s, sidecar 14s.
Log: `/tmp/pages-agent-all-go.log`. This supersedes the P3 limitation about
there being no single final all-green repository run. Opt-in Docker coverage
was separately exercised: MCP 6.9s and CLI publish/rollback 17.8s. Vet exited 0;
all migration/invariant/documentation guards listed above also exited 0.
No new frontend behavior was changed in P4; the prior 560 frontend tests/build
results remain historical P3 evidence, not a claimed P4 rerun.

Work remains uncommitted and undeployed in the same worktree. Root dev3 is still
on `feat/credentials-complete-ux`; do not deploy that root as if it contained this
Pages implementation. The recovery stash and tracked web embed placeholder were
preserved. Other logs: `/tmp/pages-agent-*.log`.

### Backup follow-up: inspected integration points

`runner_create.go` builds the DB dump at step 5c, then writes referenced memory
blobs at 5d. Add a separate Pages section selected from that SAME dump, not a
second live DB query. `ProjectStore` uses `<projects>/<sha256(workspace)>/`;
Git is below `git/<sha256(page)>/`, artifacts below
`<projects>/artifacts/<sha256(workspace)>/`. The archive should carry logical
workspace/Page/digest references and let the store compute destination paths;
never trust an archived absolute path or copy unrelated workspace directories.

`runner_restore.go` snapshots original identities before `RemapIDs`, and later
writes content-addressed memory blobs independently of container provisioning.
Pages need the same original-to-target identity pairing for workspace AND Page
namespaces. Stage and verify every referenced source/artifact/Git checkpoint
before enabling the restored live pointer. Preserve Git commit bytes and actor
snapshots as original provenance; author snapshots are not authorization grants.
Do not restore arbitrary Git config/hooks/remotes from an archive. A missing live
artifact must be an explicit failed/incomplete restore, not a green metadata-only
restore. Retention must honor in-flight backups so the snapshot's referenced files
cannot disappear between DB capture and file collection. These are design notes,
not implemented file backup or a passed fork-restore test.

## P5 in progress — 2026-09-09 09:10 UTC (supersedes remaining-work bullets above)

Implemented, uncommitted in this same worktree:

- Publication receipt UI with source inspection, reviewed rollback, and explicit
  owner/admin withdrawal. Withdrawal keeps the monotonic publication counter and
  leaves an immutable audit row. Confirmation is invalidated when the head changes.
  CLI `page project publications` and `page project unpublish` added.
- Full encrypted workspace backup now includes referenced canonical sources,
  artifacts and raw checksum-verified Git objects. No Git configs/hooks/packs are
  imported. Stage before target DB transaction; apply files before DB commit.
  Fork restore uses `firstWorkspaceID(dump)`, NOT `dump.WorkspaceID` (which remains
  the original scope after remapping). Page IDs are paired before/after remapping;
  immutable Git/actor provenance remains original.
- Shared/exclusive cross-process workspace file leases protect backup and all
  source/build/publication file handoffs against restore/retention. SQL ordering is
  lease then transaction. Running compiler finalization acquires a shared lease.
- Hourly bounded retention worker: 64 revisions, 64 completed builds, 32 receipts
  per Page plus draft/live/running-build dependencies. Monotonic counters are no
  longer lifetime quotas. SQL deletion precedes immutable orphan cleanup; failed
  cleanup leaves extra files. Git ancestors stay intact under the 128 MiB quota.
  Git repack is conditional, one thread, 16 MiB window budget, 30s deadline.
- Read SDK `getPanelHistory` now routes through the trusted host to a read-gated,
  publication-fenced endpoint: at most 20 items/1 MiB, cursor, no run metadata.
  New build image `crewship-pages-build:dev3-p5` built after SDK change; inspect
  its image ID before using opt-in Docker tests (P3 image has the old SDK).

Passed so far (NOT a final full P5 verification): lifecycle API/UI targeted tests;
clean and fork encrypted file restore; invalid digest/missing/corrupt/path/link/
duplicate archive rejection; dry preparation leaves target untouched; lease
exclusion/workspace isolation; retention keeps live/draft/running dependencies,
removes orphan source/artifact/Git and is idempotent. Logs `/tmp/pages-p5-*.log`.
An initial history test inserted nonexistent `run_id`; fixture fixed before rerun.
An initial source-quota test failure was fixed by excluding only the lock and Git
folder from source-file accounting. Do not report those obsolete logs as current.

At this checkpoint: frontend full suite, updated API/OpenAPI tests and browser
host-dependency dry-run are running. Full Go/vet, current frontend build/lint,
strict docs guards, current Docker/browser acceptance still need completion.
Check active session/logs before duplicating expensive runs.

Still required: negative extra/missing reachable Git-object restore tests; validate
quota admission on existing target; measured concurrent viewers/reconnect/live
RBAC/restart; actual isolated MySQL/Ansible pilot; public dev demo. Live dev3 remains
on root credentials branch, not this worktree, and has not been redeployed. A text
question is pending asking which OTHER primary domain (not unifylab.cz subdomain)
can host the runtime. User authorized deployment, but no domain answer is yet
available. Existing Caddy named Crewship hosts are all under unifylab.cz. Do not
weaken runtime site isolation. CODEX service uses root cwd; do not blindly reload
root and claim the Pages worktree was deployed.

### P5 verification update — 2026-09-09 09:29 UTC

- Full frontend suite: **666 files / 8,177 tests passed**. `pnpm build` passed;
  lint passed with 32 warnings and no errors. Full `go vet ./...` passed, followed
  by affected-package vet after adding acceptance tests. Strict docs inventory and
  surface guards passed; new routes are in OpenAPI and role manifest. Migration
  lint and agent invariants passed. Tracked embed placeholder preserved.
- New tools image ID is
  `sha256:4ec4b078ae1a480579a9202019cc973ec0e42e829bbee918363d755aae3e42d0`.
  Actual Docker source build/typecheck and MCP → API → Docker passed. Actual CLI
  history/build/publish/retry/rollback/action/status/publications/unpublish passed.
- Full Go suite is STILL RUNNING in exec session **32317**, log
  `/tmp/pages-p5-all-go.log`. Started ~09:12; API passed 713.647s, backup 102.579s,
  CLI 227.525s. Database package still running at this checkpoint. Do not restart
  or claim completion without waiting for exit status. New optional acceptance
  tests were added afterward and run separately, without production Go changes.
- Negative Git restore tests now reject missing reachable objects, extra valid
  objects and corrupt hashes before touching target. Existing-target Git quota
  preflight passed. Logs `/tmp/pages-p5-git-negative.log`, `...-quota.log`.
- Actual API load through CLI-token/workspace middleware: 24 simultaneous readers,
  480 conditional reads, **0 repeated artifact body bytes** (initial ~208,702 B).
  p95 ~25–45ms on this shared development host. Real workspace role revoke to an
  unrelated MEMBER yields 404 even with a cached ETag; restoration/reconnect works.
  A first test downgraded the Page OWNER and incorrectly expected loss of Page
  ownership: fixture corrected to a separate admin viewer; no production bug or
  cache bypass was found. Logs `...-cli-load.log`, `...-cli-pilot.log`.
- Real-service pilot passed: actual MySQL 8.4 authenticated SELECT 1, stopped DB,
  actual Ansible check success and failing playbook. CLI pushes all four resulting
  verdicts into Page snapshots, including failed producer state. Reproducible
  source is `examples/pages-apps/pilot/`; no human token enters those containers.
  This covers collector → CLI → API → panel, NOT an installed customer's active
  routine schedule. DSL validates for both supplied routines.
  MySQL image: `mysql@sha256:3466ba4a4828aa8d46fb7c3bc16b67b781c98413cf4ea0fac6feaa6e881faa26`;
  collector: `crewship-pages-pilot@sha256:a4fafb0a82fcf5d0212c8f263cede14528c350a90314ac57f19db8c8189f6a86`.
  `/tmp/pages-p5-pilot-reproducible.json` contains safe verdicts only.
- Physical restart acceptance starts two actual `crewship start --no-docker`
  processes in a private temp data dir, SIGKILL between them; no live dev instance
  touched. Withdrawal persistence passed. Extended test additionally republishes
  with builds disabled and checks artifact survives second boot; STILL RUNNING
  session **62982**, log `/tmp/pages-p5-cli-restart-final.log`. Initial no-sidecar
  startup failure fixed by `CREWSHIP_SKIP_SIDECAR=1` in the dashboard-only fixture.
- Browser host dependencies were avoided using a pinned Playwright container:
  `mcr.microsoft.com/playwright@sha256:eff16c30e6f3f4af0a03fa4b706120d5e9b0891c344a27d64559aff5900a4a27`.
  **Chromium passed** React rendering, snapshot update, DOM/storage/fetch denial
  and stopping an infinite-loop iframe. **Firefox and WebKit FAILED the stop-loop
  gate**: host click timed out. Functional checks before it passed. Firefox retry
  using separate registrable DNS names also failed the stop gate (an intermediate
  `--network none` attempt failed DNS, then bridge-network attempt resolved it).
  Logs `/tmp/pages-p5-browser-*.log`. Do not call all browsers green or promise
  portable hard CPU isolation. `e2e/pages-preview-smoke.mjs` now accepts an already
  compiled runtime harness and optional test hostnames for container execution.

Remaining release decisions/work: browser support/fallback policy given the actual
Firefox/WebKit loop result; runtime domain from user; concrete dev3 deployment
that preserves root credentials WIP and existing instance data; live Studio/chat
smoke after deployment. No commits, merges, service overrides or Caddy edits have
been made. No issue claimed; claim before any first commit. Root and worktree PRD
copies need syncing again after final results.

### P5 current checkpoint — 2026-09-09 09:37 UTC

Deployment status: **NOT deployed**. Read-only `./dev.sh status` confirmed dev3
still runs root `feat/credentials-complete-ux @005470ed` (Go PID 103845, Next PID
103996). No service or Caddy configuration has been changed. User has authorized
implementation and deployment; missing input is the second registrable runtime
domain. Do not ask again for generic deployment permission.

The first full P5 Go run completed with **one failing documentation guard**:
`scripts/docs-inventory/TestOpenAPIReferenceQuotesTheCurrentSpec` found the old
612/633 and 273/269 counters in `docs/api-reference/openapi.mdx`. All other test
packages passed, including API 713.647s and backup 102.579s. Corrected the prose to
615/636 and 274/270; the guard and strict inventory now pass. Do not describe that
first invocation as all-green.

One final production hardening fix followed: panel history counts the actual
JSON-encoded item size, including HTML escaping, toward its 1 MiB response limit.
Raw JSON byte count could undercount strings containing <, > or &. Regression
with 220,000 literal less-than characters passes, as do publication/history and
retention tests. Log `/tmp/pages-p5-history-bounds.log` and `...-final-fixes.log`.

**Final full Go rerun is in progress**, exec session **63380**, log
`/tmp/pages-p5-final-all-go.log`, started ~09:33 UTC. Final vet session **28704**
(`/tmp/pages-p5-final-vet.log`) and strict inventory session **10319**
(`/tmp/pages-p5-final-docs.log`) were also launched; inspect completion status
before reporting. No production changes after this final rerun began.

Confirmed final acceptance:

- `/tmp/pages-p5-cli-restart-final.log`: actual CLI + Docker build + publication,
  replay/rollback/action/status/withdrawal + 480 concurrent conditional reads +
  role revocation/reconnect + real MySQL/Ansible verdict delivery + **two actual
  daemon processes with SIGKILL between them**. First boot preserves withdrawal,
  then republishes with build worker disabled; second boot loads publication v3
  AND its JavaScript artifact. Passed 26.206s. The isolated daemon fixture uses
  `CREWSHIP_SKIP_SIDECAR=1` with `--no-docker`; it never touches live dev3.
- `/tmp/pages-p5-pilot-reproducible.log`: committed-path pilot runner (currently
  uncommitted file) reproduced MySQL and Ansible success/failure. Temporary
  containers, network and credentials cleaned up. Earlier ad-hoc runner is not
  the only reproduction anymore.
- `/tmp/pages-p5-browser-chromium-pilot.log`: freshly compiled operational example
  exercises actual SDK `runAction`, `getActionStatus`, **getPanelHistory** and
  snapshot updates in Chromium, plus sandbox checks and stop-loop. Passed.
  `/tmp/pages-p5-docker-pilot-artifact.log` verifies its compilation/typecheck.
- Firefox/WebKit stop-loop failures remain an explicit browser-support limit;
  no production workaround or browser block was silently introduced.

Prepared binaries (not installed): `/tmp/crewship-pages-p5-final` and
`/tmp/crewship-pages-p5-sidecar`. Build logs `...-final-binary.log` and
`...-final-sidecar.log`. Source/tools image remains the P5 digest above. Preserve
all WIP, root credentials branch and the recovery stash. Remaining work is the
final test result, concrete dev3 deployment once runtime domain is known, and
live Studio/chat acceptance. Customer routine activation/inventory verification
remains installation-specific; current pilot does not claim that customer setup.

## Runtime address clarification — 2026-09-09

The user's Operations screenshot shows the intended existing Studio route.
Custom applications keep that route and navigation. The separate runtime address
is an installation-level implementation detail, not a domain per Page. The v1
PRD now distinguishes browser-reachable DNS/TLS from server-local aliases and
records automated provisioning as missing product packaging, not implemented.
No requirement for a new application server or public cloud dependency was added.
No live DNS, Caddy, service or Page was changed during this clarification.

## Final P5 full Go verification — completed 2026-09-09

`GOMAXPROCS=4 go test ./... -p 2 -parallel 4 -count=1 -timeout 30m`
completed with exit 0. All 138 test packages pass; 11 have no tests. CLI 220.883s,
API 656.283s, backup 116.058s, database 635.327s. Log:
`/tmp/pages-p5-final-all-go.log`. This supersedes earlier pending-run notes and
the first P5 run's fixed documentation-count failure. No production code changed
after this invocation started. PRD/operator documentation clarification followed.
This does not change the Firefox/WebKit failure or undeployed dev3 status.

## Chronology note

Earlier sections below/above marked P3/P4/P5 are historical evidence. Their
"undeployed", missing-domain and pending statements are superseded by the current
delivery/deployment sections at the top, not retroactively rewritten as successes.

## Live demo final acceptance — 2026-09-09

Final Chromium rerun exited 0: login, rendered data, filter, SDK history,
stop/reopen and no page errors. The screenshot after reopening explicitly waits
for data rather than capturing the initial empty React render. Desktop and tablet
screenshots: `/tmp/pages-dev3-browser/custom-operations.png` and
`custom-operations-tablet.png`. Final log `/tmp/pages-dev3-live-browser-final.log`.
The real collector script was run successfully after deployment and produced
snapshot sequence 2; the live SDK history test read retained snapshots.
Final systemd MainPID 2567547, active; public HTTPS health returns 200.

Current all-Go background run: unified exec session 21583,
`/tmp/pages-dev3-demo-all-go.log`. It began before final API response-flag changes;
do not replace final affected-package evidence with this run or call it complete
without checking its exit. The preceding complete P5 run remains valid historical
evidence. No further code changes are needed merely to display this demo.

## Scheduled producer acceptance and final guard — 2026-09-09

Natural scheduled fires are verified, not just forced `schedule now` calls:
`run_cmtty4ko40003c41e6c5d` and `run_cmtty57tf000498c161f1` completed with
`triggered_via=schedule`, about 1.38s and $0. Later post-restart run
`run_cmtty8jbn0001f91e93cd` completed at 10:22:03 UTC. Schedule remains enabled,
zero consecutive failures. Evidence `/tmp/pages-dev3-schedule-proof.json`.

A final guard moved whole-object resolution after the executor's dry-run return:
dry-run must not need collector output or dispatch a write. Entire pipeline
package passes (12.550s), including that regression and payload-type tests.
Latest server binary was rebuilt with the verified embedded export and installed
without changing the publication. Final vet after this guard exited 0;
`/tmp/pages-dev3-final-vet-after-dry-run.log`. Strict docs inventory passed;
`/tmp/pages-dev3-container-docs.log`. Whole-repo test session 53849 remains pending
and began before this small dry-run guard; final pipeline evidence covers it.

Current portable Page export is publication-2 source/definition. It references the
routine, whose DSL and collector ship as separate examples. It does not export
running schedules or script installation authority. Legacy host collector is not
used by the current live Page. Credentials used for browser smoke were passed in
a temporary 0600 env file; it was removed after verification.
