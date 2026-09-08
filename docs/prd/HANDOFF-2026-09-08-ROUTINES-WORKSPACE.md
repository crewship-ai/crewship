# Routines workspace — implementation and dev1 verification

2026-09-08. Application commit `189fa6d98`, feature branch
`feat/routines-workspace-v2`, PR #2460, claim #2459. Dev1 preview remains on
`dev1/issues-preview`. The branch includes the unmerged Issues contract from
PR #2448. Neither PR is merged into main.

## Implemented

User correction after the initial preview: preserve the original application
visual design and left explorer. The searchable, filterable RoutinesExplorer
is restored across overview, definition and historical runs, including routine
icons/colors. The landing page reuses the original dashboard cards; calendar
and recent runs are additional tabs. The routine icon/color picker remains
available in its header. On mobile the explorer opens over the content.
The original standalone table wireframe is superseded by this decision. Handoff results lead with their readable summary; raw protocol text is
expandable, and the routine-level technical dock is hidden in run detail.
A routine has definition/history/settings views; the complete definition
graph is initially visible and selecting a step exposes its task and settings.
Routines and Activity share a run detail with progress, actual invocations,
outputs, activity, inputs, the existing approval and Stop/Run again controls.
Manual starts using `Prefer: respond-async` return the durable run identity before
work completes. Existing synchronous clients and Issue dispatch retain their
contracts. Run again creates a new manual run using the current recipe and asks
for its declared inputs, initially populated from the historical run.

`GetRun` resolves the archived recipe without falling back to HEAD. New runs
also capture the effective DSL, including runtime overrides, before performing
work. Old runs without an archive say so. Output-loading failures are distinct
from an empty result. Actual retry attempts, foreach items, agent invocations
and grader invocations get separate persistent identities and outputs. Requested
models are recorded only for agent work; they are not a claim about a provider's
actual resolved model. Lists are paginated; execution and artifact content is
loaded on demand. Existing legacy step-output snapshots remain in GetRun.

Declared file/text/JSON/external-record/action-receipt outputs use one registry.
Files are copied into the existing workspace content-addressed attachment store,
with download-time authorization and GC ownership shared with Issue attachments.
Only explicit JSON artifacts or the existing HANDOFF artifacts declaration are
recognized. Reading a file or mentioning a path does not create an output.
Multiple declarations can refer to the same immutable blob, retaining their
separate execution provenance. States are draft/available/unavailable; available
means stored content, not an assertion that every business criterion passed.

The calendar derives future occurrences from real schedules and past events
from actual runs. It does not invent dates for manual/event starts. Repeating
schedules have human day/time/timezone controls and concrete previews; cron is
advanced. Once-on-a-date uses the durable pending queue, including atomic save
with a one-time trigger and cancellation. New routine Describe/editor flows
carry source/output intent and start preferences. Scheduled editor creation asks
for declared inputs. Event wiring remains an explicit settings step after save.
Static validation is labeled as validation, not execution.

Required outcomes checks fail closed on grader failure or malformed verdict;
legacy advisory failure semantics remain compatible. Script Stop uses a distinct
process group per invocation and verifies termination of live group members.
`setsid --wait` preserves the actual child exit status. The private control
folder's umask is restored before executing the script. Cancelled invocations
retain cancellation even if a runner returns a descriptive process-stop error.

Migrations (append-only):
- `20260908104844_pipeline_step_executions.sql`: effective run DSL + execution rows.
- `20260908111500_pipeline_run_artifacts.sql`: declared output ownership.

Backup inclusion and shared attachment GC account for both tables. Production
build also required moving existing login/admin helper exports out of Next.js
page modules; their behavior and focused tests are unchanged.

## Live evidence on dev1

All fixtures have a `test-routines-` prefix, belong to the Quality crew and have
no external action. The script fixture lives under that crew's shared scripts
folder. Test outputs live under `work/routines-release-20260908`. No model,
credential, Issue acceptance or external integration was changed.

- History run `run_cmtsm2ctg000544f2d775`: two distinct foreach items, script exit
  7 on attempt 1, successful attempt 2, a draft and immutable file/JSON outputs,
  parked approval, then completion of the SAME run from the browser's Approve.
  Repeated opposite approval returned 409. Overwriting the source file did not
  change the downloaded historical bytes.
- Stop run `run_cmtsmbyn000014a100e39`: browser Stop cancelled both the run and
  execution row. After the child process's intended write time, its marker still
  did not exist. This tests an actual container process tree, not only mocks.
- Hybrid run `run_cmtsm56m300148215400d`: script → Casey → required Jordan review
  → delivery. Outcome SUCCEEDED; script and agent-created files were captured.
  Worker and review calls are separate execution rows. Earlier run
  `run_cmtsm2ds90008cb94e15e` had an invalid handoff delimiter and correctly retained
  technical completed + outcome FAILED; a passing rubric alone did not make the
  unrecognized delivery green. This was a recipe-format correction, not a parser
  relaxation. The two runs retain different historical recipe versions.
- `test-routines-calendar-20260908`: its one-time pending ID survived a server
  restart, cancellation from Settings removed it, and a new short one-time start
  produced exactly one completed run, `run_cmtsm6kxg000100a3694a`.
- Older waiting test runs were cancelled. No outstanding test approvals or future
  test schedules remain. QUA-12/QUA-13 were not rerun or accepted.

The live pass caught two defects that mocked runner tests did not: setsid could
report its fork's success instead of the script's exit, and a control-directory
umask leaked into generated files. Both were fixed and covered by OS-process
regressions before the final live retry/Stop pass.

## Checks and operational state

- Full frontend suite: 641 files / 8,000 tests passed. After later refinements,
  Routines/shared run/historical hook suites passed again: 28 files / 204 tests.
  Login/admin build-helper tests: 15 passed.
- Initial full Go run: 131 packages passed; API and database hit the default
  10-minute package timeout on disk, and two generated documentation checks
  exposed a misplaced route annotation and stale route counts. Both were fixed.
  All four packages passed on rerun: full API 103.364s, database 348.081s,
  `cmd/gen-openapi` and `scripts/docs-inventory`. Use `TMPDIR=/dev/shm` and
  `-timeout=40m` for large suites on this server. Full pipeline passed again after
  the final cancellation change (9.164s).
- Full Go vet, final pipeline vet, migration lint, agents invariants and diff
  whitespace checks passed. ESLint: 0 errors, 32 existing warnings.
- Worktree production export passed using `pnpm build --webpack` because its
  shared node_modules symlink lies outside Turbopack's root. Normal Turbopack
  builds, embedded Go/sidecar builds and service reloads passed in the actual
  dev1 clone. Last application deployment is `189fa6d98`.
- Browser checks cover catalog, calendar, definition, run, outputs and mobile
  width, with no page errors or document-width overflow. Creation date/repeat
  controls passed the final smoke pass. Live read-only checks confirmed QUA-12
  remains REVIEW and QUA-13 remains TODO. Screenshots: `/tmp/routines-final-*.png`.
- Fresh pre-migration SQLite backup (0600):
  `/tmp/crewship-dev1-before-routines-workspace-20260908/crewship.db`.
  Original AGENTS/CODEX/design/document WIP is preserved in the main clone.

PR #2460 still requires an actual current-head review before merge. CodeRabbit
reported a rate limit, not a completed review. Its green status is not approval.

## Explicit limits

This implements the workspace-v2 release slices, not every longer-term item in
the hybrid audit. Foreach still cannot park individual items durably; per-item
resume, integration-specific adapters/readiness, document pickers, cross-provider
model routing and generalized external exactly-once delivery are not introduced.
Existing limits on nested workflows, budgets and retries still apply. Script
history pins the effective DSL and outputs, not every executable/package/input
revision in the container. Process-group Stop is tested for ordinary children;
an intentionally daemonized process that creates another session is outside
that guarantee. Custom crew images must provide setsid, ps, awk and /bin/kill.
Historical records are not synthesized for older runs. A shared generic artifact
renderer is provided; specialized document previews and an independent business
acceptance workflow are not. Issues remains the source of client acceptance.


## Original navigation restored after user review

Application commit `187e9d32d` is deployed only on dev1. The user explicitly
rejected replacing the left explorer and original visual design. The PRD now
records the explorer as the primary catalog, with the original dashboard cards
and additive Overview / Calendar / Recent runs navigation. Icon/color editing
in the routine header is retained. Selecting the routine from its historical
run returns to the definition; explorer filters survive local selection.

Verification on the public dev1 URL: search with no matches, status filters,
opening a routine through the filtered explorer, history-to-run navigation,
returning through that same explorer, mobile collapse/expand, icon PATCH and
reload, calendar, once/repeat controls and output view. No page errors or mobile
width overflow. The fixture icon was restored after the check. QUA-12 remains
REVIEW, QUA-13 TODO; historical recipe versions remain distinct (1 and 2).
Screenshots and browser evidence: `/tmp/routines-sidebar-*.png`,
`/tmp/routines-sidebar-browser.log`, `/tmp/routines-sidebar-navigation-pass.log`.

The 28 focused frontend files passed (248 tests), followed by the added
failed-result/history-link regression (19 overview tests passed). TypeScript,
production builds in the worktree and actual dev1 clone, and go vet passed.
ESLint has zero errors and the existing 32 warnings. Full Go verification passed all 135 tested packages, recorded in
`/tmp/routines-sidebar-go.log`. CI was requested for application
commit 187e9d32d; CodeRabbit is still rate-limited without an actual review.
Do not merge on that green status.

CI on 187e9d32d exposed two unused helpers from the preceding implementation
and two Linux process tests exceeding the skip budget. The follow-up removes
only the unused helpers and makes the process tests Linux build-time tests,
requiring util-linux setsid instead of runtime skips. This preserves runtime
behavior and retains the real child-process cancellation/exit assertions.

## Unified recipe/run UX follow-up

The approved refinement adds a shared identity header and navigation across
recipe and run detail, gray calendar cards, separate version archives with
read-only graphs/diffs and explicit unsaved restoration drafts. Historical
manual starts accept pinned_version, retain HEAD, and preflight the selected
recipe. The input review dialog offers current/executed versions and preserves
matching historical inputs. Optional input labels also reach Chat forms; output
value_labels come only from the executed author's schema.

Catalog and detail summaries now resolve their latest durable pipeline_runs
record, including business outcome. Pending approval responses include the
existing Inbox item ID, scoped by workspace/token. Activity deep links use the
canonical pipeline/run parameters, and Activity still uses the same run surface.
No Issues dispatch, review or client acceptance behavior is changed. No new
migration is required for this follow-up; existing version, run and inbox rows
remain authoritative. Test fixture prefixes are hidden through a reversible
explorer filter and marked when directly opened.

Drafts are local unsaved editor buffers, not durable collaborative drafts.
Historical re-execution pins the recipe, not executable/container revisions or
external source data. The existing current governance and runtime override
contracts still apply. Native unsaved-change protection covers editor closing,
its recipe tabs, anchor navigation and browser unload; it is not a shared
cross-page draft store.

### Unified follow-up verification

Application head `c6bce70c3` (including `0fb27303b` and `fb949cdba`). The final
layout places Result beside Progress and removes duplicate version controls
from the trigger card. Inputs cannot dismiss an in-flight start through the
close button; the regression test covers that path.

- Full Go suite: all 135 test packages passed in `/tmp/routines-unified-go.log`;
  API 197.111s and database 492.436s, using TMPDIR=/dev/shm and -timeout=40m.
  Full go vet and the commit-time golangci gate passed.
- Full frontend: 642 files / 8,005 tests passed. The later version-archive test
  proves comparison/restoration never writes HEAD; 3 focused files / 8 tests
  passed. After final layout/dialog changes, 27 Routines files / 204 tests
  passed, including 11 input-dialog tests. TypeScript passed.
- Full ESLint: 0 errors, 32 existing warnings. Final changed-file lint is clean.
  Worktree webpack production export and actual dev1 production builds passed.
- Public browser evidence: `/tmp/routines-unified-live.log`, screenshots
  `/tmp/routines-unified-live-*.png`. Original sidebar, both user-provided URLs,
  calendar, versions, input review, Activity deep link and mobile width passed.
  No browser page errors or document overflow. Preparing v1 of the hybrid
  fixture left HEAD at v2. QUA-12 remained REVIEW; QUA-13 remained TODO.
- Live isolated manual fixture `test-routines-unified-20260908`: original
  `run_cmtsp6yx400011c8985e5`; browser Run again selected executed v1 and created
  `run_cmtsp705r0002de04422f`, retaining the historical input and leaving HEAD
  at v2. Then v3 introduced an approval: `run_cmtsp70lm0003c81f02b6` linked the
  exact Inbox item `ibx_waitpoint_2534407ffc90116688c9fc70108461d7` and browser
  Stop cancelled the run and removed its pending waitpoint. No future start
  was created. Evidence: `/tmp/routines-unified-actions.log`.
- Backup before the new live fixture:
  `/tmp/crewship-dev1-before-routines-unified-20260908/crewship.db` (0600).
- Concurrent uncommitted verification fixes in the main clone were preserved
  via path-scoped stashes during builds and reapplied afterwards. They are NOT
  part of this application deployment. A separate binary patch backup is at
  `/tmp/routines-unified-preserved-peer-wip.patch`; stash IDs are recorded in
  `/tmp/routines-unified-peer-stash-id` and `...-peer-final-stash-id`.

These results verify the listed paths, not every future integration or model.
PR #2460 remains unmerged and requires actual current-head review; CodeRabbit
has reported rate limiting rather than a completed review.

## Full calendar follow-up (2026-09-08)

Day, 3 days, Monday-based week, month and year views now share Today,
previous/next, date navigation, event-kind filters and routine icons. The URL
retains tab/calendar/date. Clicking a date or hour, or Add routine, selects an
active recipe and its typed inputs and creates a durable one-time start through
fire_at. Recurrence editing remains in Plan. Browser-local timezone is explicit;
past/nonexistent local hours are refused. Starts are not drawn as invented
activity durations. Year fetches twelve bounded months with concurrency three.

The existing calendar called ListPending with 1000; that helper rejects limits
above 200 and silently defaults to 50. Later dates could disappear. The calendar
now queries pending starts within its workspace/date interval before limiting,
and reports >1000 entries as truncated. No migration is needed.

Validation: full Go suite passed 135 packages, then full API passed again after
the pending-window fix (110.320s). Full go vet passed again. Full frontend:
646 files / 8,014 tests passed; final calendar regressions: 3 files / 8 tests.
TypeScript and production builds passed; ESLint 0 errors / 32 existing warnings.
The Prague timezone check rejected 2026-03-29 02:30 and retained the repeated hour
in October's 745-hour month. Tests cover leap years, year-crossing weeks,
interval partitioning, typed inputs, refusal/error persistence, month coverage,
workspace scoping and truncation after a dense pending queue.

Public dev1 browser verification (`/tmp/routines-calendar-live.log`): all five
views, routine icons, date-click scheduling, persistence after reload, planned
start cancellation and mobile width without page errors. Tomorrow's 09:30 start
`pnd_cmtsqe2wa00087f9828d3` persisted and was cancelled. The next start
`pnd_cmtsqe5p40009832722df`, scheduled through the UI for 13:56 UTC, produced
completed `run_cmtsqfyeg00010aac0ae7`. Both use the existing manual, agentless
fixture `test-routines-calendar-20260908`; no future test schedule was left.
Screenshots: `/tmp/routines-calendar-*.png`. Concurrent main-clone verification
WIP was stashed only for deployment and reapplied; it is not shipped here.

## English dates and calendar starts in Plan (2026-09-08)

Application commit `73e72884a` is deployed on dev1. Routines now formats visible
weekday/month/date labels with en-GB independently of the browser language;
timezone handling is unchanged. Native date-input controls remain browser-owned.
Plan groups One-time starts (including Calendar entries) and Repeating schedules
under Schedules. Pending one-time rows lead; the old misleading global "No
schedules yet" state is gone. Advanced overlap configuration is collapsed below
scheduling. The existing left explorer, icons and gray card design remain.

Frontend verification: 31 files / 247 Routines and overview tests passed,
including the regression with a pending calendar start and no recurring schedule.
TypeScript, full go vet, production export and actual dev1 build passed. ESLint:
0 errors / 32 existing warnings; changed-file lint is clean.

Read-only public browser verification used cs-CZ with Europe/Prague. Week and
month views showed English labels. Incident timeline's real user-created start
`pnd_cmtsr2st70024c9c50523` appears within Schedules as Wed, 9 Sept 2026, 10:00.
Its stored `2026-09-09T08:00:00Z` value was identical before and after; no run,
cancellation or schedule write was performed. No browser page errors occurred.
Evidence: `/tmp/routines-plan-live.log`, `/tmp/routines-plan-english-week.png`,
`/tmp/routines-plan-english-schedules.png`. Concurrent Go verification WIP in the
main clone was preserved through a path-scoped stash for deployment and restored
afterwards; it is not part of this build.

The full Go suite also passed all 135 packages (database 385.768s), using
TMPDIR=/dev/shm, -p 3 and -timeout=40m; log `/tmp/routines-plan-go.log`.
PR #2460 remains unmerged: CodeRabbit is rate-limited, not a completed review.

## Guided creation editor (2026-09-08)

Dev1 application commits `d67c6c0d0` and `212ecb0f1` introduce a wider manual
creation editor with the original gray styling and a left section navigator.
Overview separates name, description and crew from templates. Inputs/Outputs
read the live declarations; Steps shows readable summaries and the graph; Code
keeps full YAML/JSON editing; Schedule and Validate have their own sections.
Forks identify their source. Description edits in Code update the Overview.

The test-run bypass was removed from this creation UI. Validate definition
accurately names static test_run validation, and Validate & Save still spends
the minted save token. Real execution remains Run after saving. Configured
schedules activate on save, stated explicitly in Schedule. No database migration
or new draft lifecycle is claimed: the editor explicitly labels drafts Unsaved.
Visual input/step construction remains a later slice; the present cards are
readable views with direct navigation to Code for changes.

211 tests in 30 Routines frontend files passed after the final change, including
buffer preservation through sections/graph, incomplete output declarations,
source-copy isolation, discard guards and validation-token save ordering.
TypeScript, full go vet, full ESLint (0 errors, 32 existing warnings), changed-file
lint and production builds passed. The final dev1 build serves `212ecb0f1`.
Concurrent main-clone Go verification WIP was stashed only during builds and
reapplied after reload; it is not shipped with this change.

Public browser verification on the final build passed all sections, an invalid
recipe with a link back to Code, successful DRY_RUN_OK validation, preservation
of typed code through graph/section switches, and loading Morning briefing as a
separate copy with its source visible. Desktop 1440px and phone 390px screenshots
were inspected; the phone save action fits within the viewport. No browser page
errors, save requests, routine starts or schedule writes were made. Evidence:
`/tmp/routines-author-live-final.log`, `/tmp/routines-author-overview.png`,
`/tmp/routines-author-steps.png`, `/tmp/routines-author-validation.png`,
`/tmp/routines-author-mobile.png` and `/tmp/routines-author-fork.png`.

The full Go suite passed all 135 packages on this implementation as well
(`/tmp/routines-author-go.log`, TMPDIR=/dev/shm, -p 3, -timeout=40m).
PR #2460 remains unmerged and requires actual review; CodeRabbit has reported
rate limiting rather than a completed review.

## Prepared answers and Overview consolidation (2026-09-08)

Application commit `0b562689b` adds editable question definitions in Overview,
with short/long text, one/multiple choice, custom single-choice answers,
whole/decimal numbers, boolean and JSON object/list controls. Question labels,
variable names, help, required flags, typed defaults and numeric bounds are
editable; defaults can be cleared and the real start form previewed. Preview
answers remain local. Inputs/Outputs navigation was removed; expected outputs
are compact cards in Overview. Steps exposes declared script/code configuration.

Optional InputSpec widget/options/allow_custom/placeholder metadata survives the
stored definition/version JSON; no migration is required. Shared form mapping
and the Chat slash catalog carry it. Array-valued multiselects use JSON encoding,
preserving comma-containing choices without changing the older Ask-form encoding.
New form metadata opts into static/default/type/choice/required/bounds validation;
unannotated legacy inputs retain their historical contract. The Run API checks
before enqueue, and executor entry plus nested execution check before step work.
Answers remain literal data, referenced by recipe authors via inputs variables.

Focused verification: 42 frontend files / 386 tests passed, including question
creation, typed multiple-choice defaults, custom answers, comma preservation,
invalid choices and legacy form behavior. New Go choice/default/type/schema and
Chat-mapping tests passed; full API passed (177.294s). TypeScript, go vet, ESLint
(0 errors, 32 existing warnings), changed-file lint and production export passed.
The commit-time golangci gate passed after an unrelated parallel lint released
its lock. Backup before the isolated live fixture:
`/tmp/crewship-dev1-before-survey-20260908/crewship.db` (0600).

Full verification then passed 648 frontend files / 8,021 tests and all 135 Go
packages (database 523.724s), with go vet clean. Follow-up `8eda95134` makes long
run forms scroll within the viewport and names the one-time confirmation
Schedule rather than Run; its final 31 Routines files / 214 tests, TypeScript
and changed-file lint passed.

The live manual, agentless fixture `test-routines-survey-20260908` was authored
through the UI, including a question-label/help edit, and saved with typed
choices/defaults. Invalid single-choice input returned HTTP 400 before dispatch.
Run `run_cmtst5x73000387ce1e93` completed with output
`["Support","Research, Europe"]` and outcome SUCCEEDED. The browser verified
that its request carried count as number 3, confirm as boolean false, teams as a
JSON list and a permitted custom string. The first harness expected an uppercase
status but the endpoint correctly returns lowercase completed; that assertion
was a harness error, not a routine failure.

Final live verification (`/tmp/routines-survey-final-live.log`) confirmed the
completed output, the Chat slash catalog's options/default/data-type mapping,
and one-time scheduling through the same form. Chat exposure was temporarily
opted into for this fixture and restored afterwards; no Chat message was sent.
Scheduled inputs retained period Month, two teams (including Research, Europe),
custom text, numeric count 4 and boolean false. Pending
`pnd_cmtstebep000bae2ef19e` was cancelled after inspection. No future test start
remains, and the user's Incident timeline pending event was unchanged.
Phone 390px fits the complete form; no browser page errors. Screenshots:
`/tmp/routines-survey-builder.png`, `/tmp/routines-survey-run.png`,
`/tmp/routines-survey-schedule.png`, `/tmp/routines-survey-mobile.png`.
Final dev1 application is `8eda95134`; main-clone peer Go verification WIP was
restored after both deployments and was not shipped. PR #2460 remains unmerged
and still requires actual current-head review.

## Shared recipe builder for Edit

The routine and historical run headers now expose Edit. It opens the same
builder used by New routine, with saved identity, description, inputs, recipe
and author crew prefilled. Settings is removed from the navigation; access,
budget and metadata remain expandable in the builder. Old settings links open
the new editor. Existing schedule/webhook managers are available under Schedule
and explicitly apply changes immediately. Saving an edited recipe omits trigger
creation and cannot rename its slug. Historical restoration remains an unsaved
draft. The existing acting-agent ID is preserved for the unchanged author crew.

Creation and editing offer the shared icon/color picker and crew-scoped avatar
choices for each top-level agent_run step. Nested and review agent configuration
remains in Code. Appearance-save failures report partial success and retry only
the appearance write; they do not repeat recipe or trigger creation.

Verified on dev1 through the real browser: create a manual fixture with a violet
star and Casey, reopen prefilled, save version 2 under the same ID, discard edits
without changing the stored name, add a future one-time start and preserve its
exact pending record when saving version 3, cancel that start and delete the
fixture. No agent work was executed. Original Routine playground is unchanged.
Legacy edit URL and 390px mobile layout passed without horizontal overflow.
No browser page errors. Evidence: /tmp/routine-builder-live.log,
/tmp/routine-builder-schedule-live.log, /tmp/routine-builder-edit.png,
/tmp/routine-builder-mobile.png. Peer WIP was stashed only during service build
and restored afterwards. Only dev1 was deployed.

Checks: 32 Routines frontend files / 217 tests; full Go suite, 135 tested packages
(API 126.073s, database 458.178s); go vet; TypeScript; production export; ESLint
zero errors / 32 existing warnings. Changed-file lint has no warnings. PR #2460
remains unmerged and needs a review of its current application head.

Final application deployment: `cded2ee4d`, dev1 only. Follow-up coverage verifies
that reopening New routine after saving starts a clean recipe, while reopening
Edit loads saved values. Final focused suite: 32 files / 218 tests. Public browser
confirmed clean second creation, prefilled Edit and the version-save hint with
zero page errors; the test fixture was deleted. Evidence:
/tmp/routine-builder-final-live.log. Final TypeScript and changed-file lint passed,
as did the actual dev1 production build. CodeRabbit remains rate-limited with no
current-head review, so PR #2460 must not be merged on its green status.
