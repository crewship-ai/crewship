# R8 — restore reachable decision authoring

Issue #2637. Base main `8a1ca5fc3`. The old builder and its tests remained,
but `RoutineRecipeSteps` had no production caller. The active edit dialog
made step configuration read-only. R8 was therefore an implementation gap,
not merely missing live validation.

Edit → Decisions now exposes existing approval steps, including nested
foreach and hook paths. Authors can edit the prompt/title, typed questions
and named decision actions with the existing builder and reviewer preview.
Saving uses the existing draft revision comparison; publication is explicit.
Unknown forms are preserved and link to actual CLI editing instructions.
The publish summary also detects changed questions/actions under People.
Adding arbitrary steps/branches remains outside this editor's scope.

## Evidence

- 55 targeted frontend files / 362 tests passed, including active-dialog
  authoring, unchanged published input object, draft envelope/version checks,
  unsupported-form preservation, nested paths and publication summary.
- Production export passed; ESLint: 0 errors / 30 existing warnings.
- Test type gate: clean.
- Chromium at 1440px served the new export through a temporary local proxy
  to DEV1's real API. Authentication, draft save and publication used real
  endpoints without response substitution.
- Browser created a numeric `amount` question (minimum 0) and a `Pay invoice`
  action, saved the draft and verified that v1 still lacked the form, then
  explicitly published v2.
- Run `run_cmub2y1ry00138c80749b` reached the approval. The browser clicked
  Decide, filled 42.5 and submitted the authored action. The run completed.
  A separate authenticated execution-output read confirmed numeric
  `data.amount = 42.5` and `action_id = continue` in the completed attempt.
  The earlier parked attempt remains in audit history.
- The disposable routine was deleted (204); run audit history is retained.
- Evidence: `/srv/crewship/backups/crewship_1/routines-r8-20260921/`.

Two harness assumptions were corrected during this exercise: a select label
needed a non-exact query, and the overview's Decide button must be opened
before the form appears. The extra output read initially used the wrong API
path, then selected the parked attempt instead of the completed attempt.
These failed harness steps are not counted as passing validations.

This verifies top-level form authoring and response on a local production
preview with the DEV1 backend. Nested authoring has component/path tests;
this report does not claim a separate live nested-loop walkthrough or a
public frontend deployment. It does not satisfy the five-user human gate.
