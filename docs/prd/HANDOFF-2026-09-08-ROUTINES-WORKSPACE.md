# Routines workspace — implementation and dev1 verification

2026-09-08. Application commit `189fa6d98`, feature branch
`feat/routines-workspace-v2`, PR #2460, claim #2459. Dev1 preview remains on
`dev1/issues-preview`. The branch includes the unmerged Issues contract from
PR #2448. Neither PR is merged into main.

## Implemented

The catalog appears once, with attention, in-progress work, calendar and recent
runs. Handoff results lead with their readable summary; raw protocol text is
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
