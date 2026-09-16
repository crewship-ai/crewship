# Routines operator console — implementation contract (PR for #2560)

Companion to [the proposal](routines-operator-console-2026-09-15.md) and the
prototype `public/design/routines-operator-console-20260915.html`. Three work
packages build in parallel against this contract; the integrator merges them
into `feat/routines-operator-console`, deploys dev1 and opens one PR.

Rules for every package: UI text English only; no new API routes, no DB
migration, no new query parameters (docs-inventory gate); additive JSON fields
only; every new field tolerated as absent by the frontend; tests first
(table-driven Go, Vitest); no `git stash`; commit on your own `wp/*` branch;
run tests in the foreground with explicit timeouts.

## API additions (WP-A, Go)

### Pipeline list item and detail (`GET …/pipelines`, `GET …/pipelines/{slug}`)

```json
"draft": { "id": "drf_…", "revision": 2, "updated_at": "2026-09-15T10:31:00Z", "updated_by": "usr_…" }
```

Present only when a row exists in `pipeline_drafts` for the slug. `updated_by`
is the stored value; the frontend renders it as "you" when it equals the
current user id, otherwise as the raw id (no lookup).

### Pipeline detail only

```json
"files": [
  { "path": "scripts/ledger-post.go", "language": "go", "interpreter": "go",
    "step_ids": ["post"], "description": "Posts one invoice to the ERP ledger",
    "size_bytes": 4120, "updated_at": "…", "present": true, "status": "present" }
]
```

Sources, in this order: every `steps[].script.path` (recursing into
`foreach.steps` and `hooks`), then paths that look like files in
`script.args`/`script.env` values under `/crew/shared`. `language` from the
extension (`go py ts js sh bash yaml yml json md sql`), `interpreter` from
`script.interpreter` or the extension. `present`, `size_bytes`, `updated_at`
come from the author crew's shared volume via the existing crew files
reader (the same path `routine export --scripts` uses); when the crew or the
volume is unavailable, `status` is `unverified` and the size/time are omitted.
`status` is authoritative: `present` means verified present, `missing` means
verified absent, and `unverified` means the share could not be checked. The
compatibility boolean `present` is false for both missing and unverified files;
never infer absence from it when `status` is available. A file lookup failure
does not fail the detail request. `description` = the first
comment line(s) of the file (`//`, `#`, `/* */`, `<!-- -->`, max 200 chars),
empty when unreadable. `files` is `[]` when nothing is declared.

### Run detail (`GET …/pipelines/runs/{id}`)

```json
"failure": {
  "kind": "checker_rejected",
  "step_id": "verify", "step_name": "Check the extraction",
  "summary": "The checker rejected the result after exhausting the allowed model tiers: total_equals_lines.",
  "kept_step_ids": ["extract"], "not_done_step_ids": ["decide", "post", "notify"]
}
```

Only on runs whose status is `failed`/`interrupted` or outcome `FAILED`.
`kind` ∈ `checker_rejected | validation_failed | transform_input |
timeout | cancelled | missing_credential | missing_integration | http_status |
script_exit | cost_cap | unknown`, classified from `error_message` and the
executed definition with plain string matching on the engine's own error
formats (find them in `internal/pipeline`); `unknown` keeps the raw message as
`summary`. `summary` is a template per kind with the concrete values
substituted — never model-generated. `step_name` is `Step.Name` or the id.
`kept_step_ids` = top-level steps with a recorded output; `not_done_step_ids`
= top-level steps declared after the failed one (recipe order) without an
execution record. Raw `error_message` and `failed_at_step` stay unchanged.

### Schedules (`GET …/pipelines/schedules`, each row)

```json
"effective_version": 3, "version_pinned": false
```

`version_pinned` = `target_pipeline_version != null`; `effective_version` =
the pinned version, else the target pipeline's current head version (null
when the pipeline is gone).

### Also

`cmd/gen-openapi` regenerated; `docs/` updated where these responses are
documented; CHANGELOG entry under Unreleased ("Routines: …"). Targeted tests
in `internal/api` (`go test ./internal/api -run 'Draft|Files|Failure|Schedule' -timeout 15m`),
full `go vet ./...`, `go run ./scripts/agents-invariants`.

## Frontend ownership

### WP-B — routine page, steps, files, Edit, Publish, New

Owns: `routines-layout.tsx`, `routines-detail-panel.tsx`,
`routine-card-detail.tsx`, `routine-identity-header.tsx`,
`routine-step-spine.tsx`, `routine-definition-canvas.tsx` (grouped nodes),
`routine-versions-tab.tsx`, `routine-publication-review.tsx`, new
`routine-files-card.tsx`, `routine-edit-dialog.tsx`, `routine-publish-dialog.tsx`,
`routine-new-dialog.tsx`, `lib/routine-steps-layout.ts` (levels/folding),
`lib/routine-files.ts`, and their tests. May delete `routine-create-dialog.tsx`
and its tests only if every importer (`routines-layout`, `routine-card-detail`,
`app/(dashboard)/chat/chat-client.tsx`, `components/features/pages/page-editor.tsx`)
is migrated; otherwise keep the file and stop using it in Routines.

### WP-C — list, explorer, calendar, run dialog, run page, plan

Owns: `routines-workspace.tsx`, `routines-explorer.tsx`,
`routine-calendar.tsx` (+ `routine-calendar-schedule.tsx`),
`routine-run-inputs-dialog.tsx`, `routine-run-effects.tsx`,
`routine-run-detail.tsx`, `routine-execution-insights.tsx`,
`routine-schedules-tab.tsx`, `routine-once-schedule.tsx`,
`routine-schedule-editor-dialog.tsx`, `lib/routine-run-presentation.ts`,
`lib/routine-calendar-groups.ts` (new), hooks `use-pipeline-schedules.ts`,
`use-trace.ts` (types only), and their tests.

Shared by both (read-only unless noted): `lib/routine-inputs.ts`,
`lib/routine-behavior.ts`, `hooks/use-pipelines.ts` (WP-B adds the `draft`
type), `components/ui/detail`, `lib/colors`.

## Vocabulary (UI)

Routine · Published vN · Draft rM · Run · Stop (never Cancel) · Waiting for a
person · Could not finish · Completed · Stopped. "Recipe" only inside
Technical details.
