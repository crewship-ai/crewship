# kind: Milestone

## What it is

A `Milestone` is a named deliverable target inside a `Project`. Milestones group issues toward a single target date and progress state — the workspace UI uses them to render burn-down strips and "what's blocking v1.0" rollups. Every milestone is owned by exactly one project; deleting the parent project cascades to its milestones.

Milestones are workspace-scoped (via their project) and idempotent on `metadata.slug` within a workspace. The server table has no slug column, so on export the manifest synthesises a kebab-case slug from `metadata.name` for round-trip identity.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: Milestone
metadata:
  name: v1.0 launch              # required — server stores this verbatim
  slug: v1-launch                # required — workspace-unique idempotency key
spec:
  project_slug: q2-roadmap       # required — slug of a Project in the same bundle/workspace
  description: Public 1.0 release
  target_date: "2026-06-15"      # optional — YYYY-MM-DD
  status: planned                # optional — planned | active | completed (default: planned)
```

### Field reference

| Field | Required | Type | Notes |
|---|---|---|---|
| `apiVersion` | yes | string | Always `crewship/v1`. |
| `kind` | yes | string | Always `Milestone`. |
| `metadata.name` | yes | string | Server stores verbatim. Drives the UI label. |
| `metadata.slug` | yes | string | Workspace-unique. Used as the cross-kind reference key. |
| `metadata.description` | no | string | Informational only; not sent to the server. |
| `spec.project_slug` | **yes** | string | Must reference an existing `Project` (declared earlier in the bundle or already on the server). |
| `spec.description` | no | string | Free-form prose; rendered in the milestone detail panel. |
| `spec.target_date` | no | string | `YYYY-MM-DD`. Empty means no deadline. |
| `spec.status` | no | enum | `planned` \| `active` \| `completed`. Defaults to `planned` in the manifest. |

## Examples

### Minimal example

```yaml
apiVersion: crewship/v1
kind: Milestone
metadata:
  name: Beta
  slug: beta
spec:
  project_slug: q2-roadmap
```

### Realistic example with every common field

```yaml
apiVersion: crewship/v1
kind: Milestone
metadata:
  name: v1.0 launch
  slug: v1-launch
spec:
  project_slug: q2-roadmap
  description: |
    Public 1.0 launch. Blocked by:
      - billing migration
      - docs.crewship.ai cutover
  target_date: "2026-06-15"
  status: active
```

### FK reference — milestone alongside its parent project (same file)

```yaml
apiVersion: crewship/v1
kind: Project
metadata:
  name: Q2 Roadmap
  slug: q2-roadmap
spec:
  status: active
  priority: high
---
apiVersion: crewship/v1
kind: Milestone
metadata:
  name: v1.0 launch
  slug: v1-launch
spec:
  project_slug: q2-roadmap        # resolved at Plan time
  target_date: "2026-06-15"
  status: planned
```

The `Project` and `Milestone` documents may live in the same multi-doc YAML file. Apply runs `Project` (phase 3) before `Milestone` (phase 5) regardless of declaration order, so the parent `project_id` is always resolvable by the time the milestone Plan executes. (A `--dir` flag for walking a manifest directory is on the follow-up list but not yet shipped — for now collect every kind in one `---`-separated file.)

## CLI reference

The milestone CLI is nested under [`crewship project`](../cli/project) because milestones are children of a project. There is no standalone `crewship milestone` root command.

```bash
crewship project milestone list <project-id-or-slug>          # GET /api/v1/projects/{id}/milestones
crewship project milestone create <project-id-or-slug> \
  --name "Phase 1" --target-date 2026-06-15                   # POST /api/v1/projects/{id}/milestones
crewship project milestone update <milestone-id> --status completed
                                                              # PATCH /api/v1/milestones/{id}
crewship project milestone delete <milestone-id>              # DELETE /api/v1/milestones/{id}
crewship apply --file milestone.yaml                          # manifest pipeline (preferred for repeatable setups)
```

`crewship apply` is the only path that resolves `project_slug` → `project_id` for you. The flat `crewship project milestone create` takes the resolved project id (or slug) positionally and accepts individual `--name` / `--target-date` / `--status` / `--description` flags rather than a YAML file. For multi-milestone bundles, reach for `crewship apply`.

## REST endpoint mapping

| Manifest field | POST body field | DB column |
|---|---|---|
| `metadata.name` | `name` | `milestones.name` |
| `spec.description` | `description` | `milestones.description` |
| `spec.target_date` | `target_date` | `milestones.target_date` |
| `spec.status` | `status` | `milestones.status` |
| `spec.project_slug` | _path parameter_ `{projectId}` | `milestones.project_id` |
| _(server-assigned)_ | — | `milestones.id`, `position`, `created_at`, `updated_at` |

The REST surface is asymmetric:

| Operation | Method | Path |
|---|---|---|
| List | GET | `/api/v1/projects/{projectId}/milestones` |
| Create | POST | `/api/v1/projects/{projectId}/milestones` |
| Update | PATCH | `/api/v1/milestones/{milestoneId}` |
| Delete | DELETE | `/api/v1/milestones/{milestoneId}` |

The manifest layer hides the asymmetry — `Plan` resolves `project_slug` → `project_id` against `GET /api/v1/projects` before issuing the create, then switches to the flat path for updates.

## Validation rules

- `metadata.name` is required (server rejects empty `name` with HTTP 400).
- `metadata.slug` is required and must be unique within the workspace.
- `spec.project_slug` is required and must reference a Project that is either declared in the same bundle or already present on the server.
- `spec.target_date`, if set, must parse as `YYYY-MM-DD` (e.g. `2026-06-15`).
- `spec.status`, if set, must be one of `planned`, `active`, `completed`.

Failed validation is reported per-document with the offending milestone slug in the error message so multi-doc bundles surface every issue in one pass.

## Apply behavior

### `ApplyUpsert` (default)

1. Look up the parent project ID via `GET /api/v1/projects` (filter client-side by slug — the API has no `?slug=` parameter).
2. List the project's milestones via `GET /api/v1/projects/{projectId}/milestones`.
3. Match by `metadata.name`:
   - No match → `Action=Create`, `POST /api/v1/projects/{projectId}/milestones` with `{name, description, target_date, status}`.
   - Match with differing fields → `Action=Update`, sparse `PATCH /api/v1/milestones/{id}` covering only the drifted fields.
   - Match with identical fields → `Action=Unchanged`, no HTTP call issued.

### `ApplyStrict`

Fails with a `slug already exists` error if any declared milestone already exists by name in its parent project. Useful for new-environment bootstrapping where overwriting a same-named milestone would be a bug.

### `ApplyReplace`

For each declared milestone, emits `Action=Delete` followed by `Action=Create`. Use this only when the milestone identity should be reset (e.g. you've changed the name and want a fresh row rather than a rename). Apply also deletes any milestones in the project that the manifest no longer declares — be careful, this is the destructive mode.

## Round-trip via export

`crewship export workspace` walks every project, lists its milestones, and emits one `kind: Milestone` document per row. The export resolves `project_id` back to `project_slug` so the output is directly re-applyable into a different workspace or instance:

```yaml
apiVersion: crewship/v1
kind: Milestone
metadata:
  name: v1.0 launch
  slug: v1-0-launch                # synthesised from name on export
spec:
  project_slug: q2-roadmap         # resolved back from server-side project_id
  description: Public 1.0 release
  target_date: "2026-06-15"
  status: planned
```

The synthesised slug strips non-alphanumeric runs to single dashes (`v1.0 launch` → `v1-0-launch`). If two milestones in the same workspace share a name (legal at the DB level — uniqueness is per project), their slugs will collide and `crewship apply` will reject the bundle. Edit one of the slugs before re-applying.

`crewship export crew <slug>` does NOT include milestones — milestones are workspace-scoped, not crew-scoped. Use `crewship export workspace` to capture them.

## See also

- [kind: Project](/manifest/project) — parent record; must exist before any milestone.
- [kind: Label](/manifest/label) — applied to issues, not directly to milestones.
- [kind: SavedView](/manifest/saved_view) — `entity_type: issue` views can filter by milestone via the UI (no manifest field today).
- SPEC-2 section 3 — authoritative contract for this kind.
