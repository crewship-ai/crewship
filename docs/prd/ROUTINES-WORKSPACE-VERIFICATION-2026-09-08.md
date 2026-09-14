# Routines workspace — independent claim verification

> **Archiv k 2026-09-10.** Historická evidence, nikoli aktuální stav ani potvrzení UX.
> Rozsah, otevřené body a přejímku udržuje [živý PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md).

2026-09-08. Verifies `docs/prd/HANDOFF-2026-09-08-ROUTINES-WORKSPACE.md`
(application commit `187e9d32d`, feature branch `feat/routines-workspace-v2`,
PR #2460) by turning its sentences into tests that fail when the sentence stops
being true. Written from the code, not from the handoff: every claim below was
checked against the implementation, and the three that did not hold are fixed
here rather than reported as caveats.

The existing suite proves the happy path of each new surface and the
OS-process behaviour of the script wrapper. What it could not distinguish is a
claim that is *described* from one that is *enforced* — a calendar that does
not invent dates, an output that is not listed twice, a workspace argument that
cannot be chosen by the adapter. Those are negatives, and a green happy-path
suite reads identically either way.

## New tests

| File | Covers |
|---|---|
| `internal/pipeline/routines_claims_test.go` | executor→ExecutionStore wiring: which rows a real run leaves, model provenance, tier-escalation attempts, foreach identities, when the effective recipe becomes durable, history pagination cursors |
| `internal/api/routines_claims_test.go` | calendar negatives and window bounds, artifact registry identity/provenance/scope, handoff capture, interrupted executions, unreadable-vs-empty step outputs, one-time start lifecycle, run-history cursor |
| `components/features/routines/__tests__/routine-run-detail-claims.test.tsx` | handoff summary leads and raw protocol is collapsed, missing archive says so, unreadable outputs vs none, Run again asks the CURRENT recipe prefilled from the historical run |
| `components/features/routines/__tests__/routine-execution-outputs.test.tsx` | executions and artifacts list without downloading content, fetch on demand, paging with the server cursor, download only for stored bytes |

`routine-run-detail.tsx`, `routine-run-artifacts.tsx` and
`routine-execution-history.tsx` had no component tests of their own before this.

## Claims that hold as written

- `GetRun` resolves the archived recipe and never falls back to HEAD; a run with
  no archive reports `unavailable`, and the run detail says so instead of
  drawing a graph.
- The effective DSL is durable **before the first step runs**, and it is the
  effective one: a step override present at run start is inside the snapshot,
  and the snapshot is not rewritten afterwards.
- Requested models are recorded only for agent work. A transform step and a
  skipped step both record an empty model; the agent step and its nested
  invocation record the requested one.
- A tier escalation leaves two durable attempts (`failed` then `completed`),
  numbered 1 and 2; the successful attempt does not overwrite the failed one.
- Foreach items get distinct identities through the real executor
  (`/each/items/0/handle`, `/each/items/1/handle`), not only in the store.
- A conditional that did not fire is recorded as `skipped`, not omitted.
- The calendar plans nothing for a routine with no schedule, for a switched-off
  schedule, or for a disabled/proposed routine, and it refuses a missing,
  unparsable, reversed, zero-width or >32-day window.
- Two unrelated steps declaring the same file keep separate provenance rows over
  one stored blob.
- A mismatched workspace argument is refused and writes nothing.
- A handoff captures only `/crew/shared/` paths — `/etc/passwd`, a relative
  escape and a bare filename are all ignored.
- A step execution still marked `running` under a terminal run reads
  `interrupted`; under a live run it still reads `running`.
- `step_outputs_available` distinguishes an unreadable output store from a run
  that produced none, and the UI renders the two differently.
- Executions and artifacts list without their content and fetch it on demand.

## Defects found and fixed

**1. One declared output was stored twice, in two states.**
`internal/api/pipeline_artifacts.go`. The executor publishes an agent step's
output twice: as a draft from the nested invocation that produced it, then as
available from the step that accepted it. `ON CONFLICT(step_execution_id,…)`
does not dedupe across two different executions, so a single declared file
landed as two rows — one `draft`, one `available` — and run detail listed the
same deliverable twice. The enclosing step now promotes the row its own subtree
already owns and only inserts when there is nothing to promote. Two *unrelated*
steps naming the same file still keep separate rows, which is the property the
handoff actually claims.
Pinned by `TestRoutineArtifacts_OneDeclarationIsOneRow` and
`TestRoutineArtifacts_SeparateStepsKeepSeparateProvenance`.

**2. A routine could have exactly one one-time start for its entire life.**
`internal/pipeline/store.go`. The pending row's authoring identity is
`pnd_once_<pipelineID>` and the upsert only applied `WHERE status='pending'`.
Once the start fired the row was no longer pending, so every later one-time
start on that routine was refused with "this one-time start has already been
consumed" — including a genuinely new future date. The upsert now also applies
when the requested `fire_at` differs from the stored one: re-saving the *same*
consumed date is still refused (a recipe edit must not re-arm a spent start),
picking a *new* date is a new start.
Pinned by `TestOneTimeStart_CanBeArmedAgainAfterTheFirstOneFired`; the existing
`TestOneTimeAuthoring_AtomicAndNeverRearmsConsumedStart` still passes unchanged.

**3. A stale history cursor silently truncated a routine's history.**
`internal/pipeline/runs.go`, `internal/api/pipelines_exec.go`. `ListByPipeline`
compared `(started_at,id) < (SELECT …)`; when the `before` run was gone the
subquery was NULL, the comparison was NULL, and the page came back empty. A UI
paging older runs would read that as "no older runs" for a routine whose
history was merely cut at a removed run. The cursor is now resolved first and
`ErrUnknownRunCursor` is mapped to 400.
Pinned by `TestRoutineClaims_UnknownHistoryCursorIsRefused` and
`TestListRunRecords_RefusesAnUnresolvableCursor`.

## Notes, not defects

- `GET /pipelines/calendar` shadows a routine whose slug is `calendar`: Go's
  mux gives the literal precedence over `{slug}`. This follows the existing
  house pattern (`/pipelines/runs/...` already shadows a routine slugged
  `runs`, and the router comments discuss exactly this) and is not introduced
  by the new route. Nothing reserves either word at save time, so a routine
  named "Calendar" would be created and then be unreachable on its detail
  endpoint. Worth a reserved-slug list, separately from this PR.
- `orchestrator.ParseHandoff` sets `Parsed` only when **both** `summary:` and
  `confidence:` are present. A handoff block carrying an `artifacts:` line but
  no confidence therefore declares nothing — correct, but it is the first thing
  a fixture gets wrong.

## Checks run

- `go test ./internal/pipeline/` — ok (69.2s)
- `go test ./internal/api/` — ok (152.4s)
- `go test ./internal/backup/` — ok (54.2s)
- `go vet ./internal/api/ ./internal/pipeline/` — clean
- `vitest` on both new frontend files — 13 tests, all passing
- Use `TMPDIR=/dev/shm` for the Go suites on this box. The run above needed
  `go clean -cache` first: the build cache had reached 100 GB and `/` was at
  100%, which surfaces as `no space left on device` from the compiler, not as a
  test failure.

## Not covered here

Live container behaviour (script Stop against a real process tree, the setsid
exit-status path, the umask fix) is left to the handoff's own OS-process
regressions and its dev1 evidence; these tests deliberately do not re-mock it.
Calendar cron generation itself, the schedule editor's preview text, mobile
layout and the routines explorer chrome are unchanged surfaces with existing
coverage. The `Prefer: respond-async` path degrades to a synchronous 200 when
no run store is wired (`OnStarted` never fires); that is graceful and unwired
only in tests, so it is documented rather than pinned.
