# kind: SavedView

## What it is

`kind: SavedView` declares a workspace-scoped filter + sort preset for
the issue, mission, or run lists. A saved view is the data form of
"my open bugs sorted by newest first" or "team-wide active missions
this sprint" — the structured filter clause + a sort order, given a
human-readable name.

Two visibility modes:

- **Per-user** (`shared: false`, default) — the view shows up only in
  the creating user's saved-view list. Useful for personal filters
  that don't belong in a shared manifest.
- **Workspace-shared** (`shared: true`) — every workspace member sees
  the view. The common case for manifest-committed views: the whole
  team should see the "Open P0 bugs" board the same way.

Under the hood the server stores the filter and sort as opaque JSON
TEXT columns (`filter_json`, `sort_json`). The manifest exposes them
as nested structured objects so YAML stays readable; the apply
pipeline marshals them into JSON on the way in, and `crewship export`
unmarshals them on the way out.

## YAML schema

```yaml
apiVersion: crewship/v1   # required — always crewship/v1 for now
kind: SavedView           # required — the literal string "SavedView"
metadata:
  name: My open bugs      # required — human-facing label. Idempotency key
                          # on the saved_views table because the schema has
                          # no slug column.
  slug: my-open-bugs      # required — kebab-case; the cross-doc handle
                          # used by `crewship export` to map an exported
                          # row back to its manifest file. The slug is
                          # advisory on the server (key is `name`); apply
                          # diffs by name internally.
  description: ""         # optional — free-form description shown in
                          # tooltips and the saved-view picker.
spec:
  shared: false           # true = visible to every workspace member.
                          # Default false = personal view (creator only).

  entity_type: issue      # required — one of: "issue" | "mission" | "run".
                          # Picks the underlying record kind the filter and
                          # sort operate over.

  filter:                 # structured WHERE clause. All fields are optional;
                          # an empty filter object matches everything (useful
                          # as a pure sort).

    status: []            # []string — entity-type-specific status values
                          # (e.g. "todo", "in_progress", "done" for issues).
                          # Server validates the per-type enum; manifest layer
                          # only enforces the field exists.

    label_slugs: []       # []string — slugs of Label kinds. ALL labels must
                          # be present on the matched issue (AND semantics).
                          # Validate rejects unknown slugs at parse time.

    assignee_agent_slug: ""  # slug of an Agent. Empty = no assignee filter.
                             # NB: the manifest layer does NOT resolve this
                             # against WorkspaceContext; the server matches
                             # by slug at evaluation time.

    project_slug: ""      # slug of a Project. Empty = all projects.
                          # Validate fails if set but unknown.

  sort:                   # required — structured ORDER BY clause.

    field: created_at     # column name to sort by. Server-validated;
                          # an unknown column comes back as a 400 from
                          # the apply.

    direction: desc       # one of: "asc" | "desc". Validate enforces
                          # the enum.
```

## Examples

### Per-user view: my open bugs

The canonical personal-workspace example. A developer adds this view
to their personal manifest (or `crewship apply -f` of a one-off file)
so their bug-triage board persists across machines.

```yaml
apiVersion: crewship/v1
kind: SavedView
metadata:
  name: My open bugs
  slug: my-open-bugs
spec:
  shared: false
  entity_type: issue
  filter:
    status: [todo, in_progress]
    label_slugs: [bug]
  sort:
    field: created_at
    direction: desc
```

The view depends on a `Label` named `bug` existing in the same
workspace. The topological apply order
(`internal/manifest/apply.go` runs `Phase 4: Labels` before
`Phase 16: SavedViews`) guarantees the label exists by the time the
saved-view POST body is built.

### Shared team view: active P0 missions across the roadmap

A workspace-shared view that the whole team uses. Committing this to
the workspace manifest means every onboarded engineer sees it on
their dashboard the moment they join — no per-user setup.

```yaml
apiVersion: crewship/v1
kind: SavedView
metadata:
  name: Active P0 missions (Q2)
  slug: active-p0-missions-q2
  description: |
    Every in-flight P0 mission scoped to the Q2 roadmap.
    Used by leadership review on Mondays.
spec:
  shared: true
  entity_type: mission
  filter:
    status: [in_progress, blocked]
    label_slugs: [p0, critical]
    project_slug: q2-roadmap
  sort:
    field: updated_at
    direction: desc
```

This view references three other kinds via slug FKs:

- `Project` `q2-roadmap` (Phase 3)
- `Label` `p0` and `Label` `critical` (Phase 4)

…all of which apply before `Phase 16: SavedViews`, so a single
`crewship apply --file workspace.yaml` carrying all four documents
succeeds in one command.

## CLI reference

| Command | Description |
|---|---|
| `crewship saved-view list` | List every saved view visible to the current user (own + shared) |
| `crewship saved-view get <slug>` | Read one saved view by slug |
| `crewship saved-view delete <slug>` | Delete a saved view (owner-only) |
| `crewship apply -f saved-view.yaml` | Create or update from a manifest |
| `crewship export workspace` | Include saved views in the workspace export |

## REST endpoint mapping

| Manifest field | POST body field | DB column |
|---|---|---|
| `metadata.name` | `name` | `name` |
| `spec.shared` | `shared` | `shared` |
| `spec.entity_type` | `entity_type` | `entity_type` (logical — see note below) |
| `spec.filter` (struct) | `filter_json` (string) | `filters_json` |
| `spec.sort` (struct) | `sort_json` (string) | `sort_json` |

> The current `saved_views` schema uses a `view_type` column with the
> values `('board','list')` — a UI presentation hint. The manifest's
> `entity_type` is a logical field tracking which record kind the
> filter operates over; the SPEC-2 wiring step adds the server-side
> column. Until then, the manifest layer round-trips `entity_type`
> via the POST body and tolerates a missing field on read.

Endpoints:

- `GET /api/v1/saved-views` — list (own + workspace-shared)
- `POST /api/v1/saved-views` — create
- `PATCH /api/v1/saved-views/{viewId}` — update (owner-only)
- `DELETE /api/v1/saved-views/{viewId}` — delete (owner-only)

## Validation rules

- `metadata.slug` is required (and must be unique within the manifest).
- `spec.entity_type` must be one of `{issue, mission, run}`.
- `spec.sort.field` must be non-empty.
- `spec.sort.direction` must be one of `{asc, desc}`.
- Every entry in `spec.filter.label_slugs` must resolve via
  `WorkspaceContext.HasLabel` (declared in the same manifest or
  already present on the server). Unknown slugs surface as a
  validate-time error that names the missing slug.
- `spec.filter.project_slug`, when non-empty, must resolve via
  `WorkspaceContext.HasProject`.

Status strings and the `assignee_agent_slug` are passed through
unchanged — the server validates them at apply time because the valid
set differs per `entity_type` and per workspace.

## Apply behavior

| Mode | Behavior |
|---|---|
| `ApplyUpsert` (default) | Lookup by `name`. Create if missing, PATCH if drifted, no-op if identical. Label-order drift in `label_slugs` does NOT trigger an update — the diff sorts slices before comparing. |
| `ApplyStrict` | Fail with `already exists` if a row with the same `name` already exists in the workspace. |
| `ApplyReplace` | Delete the existing row first (if any), then create. Destructive: requires `--yes` or interactive confirmation. |

`crewship apply --dry-run` reports per-view planned action without
issuing any mutating call.

## Round-trip via export

`crewship export workspace` runs `ExportSavedViews(ctx, client)`,
which:

1. `GET /api/v1/saved-views` (the user's own + workspace-shared).
2. For each row, unmarshals `filters_json` and `sort_json` back into
   structured `SavedViewFilter` / `SavedViewSort`.
3. Derives `metadata.slug` from the row's `name`
   (`savedViewSlugify` — lowercase ASCII, non-alnum → `-`, trimmed).
   Use an explicit slug in the manifest if you need stability across
   `name` edits — the server keys on `name` regardless.
4. Emits one `SavedViewDocument` per row in the same `apiVersion:
   crewship/v1` shape the manifest accepts.

A round-trip export → wipe → re-apply lands in an identical state,
modulo the slug if you renamed a view between exports.

## See also

- [`kind: Label`](label.md) — referenced via `spec.filter.label_slugs`.
- [`kind: Project`](project.md) — referenced via
  `spec.filter.project_slug`.
- [`kind: TriageRule`](triage_rule.md) — sibling read-list automation;
  triage rules tag issues, saved views surface the tagged subset.
- [Apply order in SPEC-2](../../.claude/context/specs/SPEC-2-manifest-complete.md)
  — Phase 16 (SavedViews) depends on Phase 4 (Labels) and
  Phase 3 (Projects).
