# kind: Project

## What it is

`kind: Project` is the declarative representation of a Crewship project — the workspace-scoped container that groups missions, tracks delivery health, and assigns a lead agent. Use it in a manifest when you want `crewship apply` to create new projects, keep an existing roadmap aligned with version-controlled YAML, or round-trip workspace state through `crewship export` for backup and migration. One YAML document maps to exactly one row in the `projects` table.

## YAML schema

```yaml
apiVersion: crewship/v1            # only crewship/v1 is supported
kind: Project
metadata:
  name: Q2 Roadmap                 # required — human-facing label, also feeds the server's slug derivation
  slug: q2-roadmap                 # required — workspace-unique idempotency key
  description: All Q2 deliverables # optional — long-form description, stored in projects.description
spec:
  color: "#3B82F6"                 # optional — six-digit hex, default "blue" applied by server
  status: planned                  # optional — one of: planned, active, completed, archived
  priority: medium                 # optional — one of: low, medium, high, urgent
  health: on_track                 # optional — one of: on_track, at_risk, off_track
  target_date: "2026-06-30"        # optional — YYYY-MM-DD calendar date
  lead_agent_slug: pepa            # optional — slug of an Agent in the same workspace
```

Every field except `apiVersion`, `kind`, `metadata.name`, and `metadata.slug` is optional. Empty fields are treated as "leave server-side default in place" — they do not overwrite values an operator changed via the UI.

## Examples

### Minimal — slug + name only

```yaml
apiVersion: crewship/v1
kind: Project
metadata:
  name: Migration backlog
  slug: migration-backlog
spec: {}
```

`crewship apply` creates the project with server defaults: `color=blue`, `status=backlog`, `priority=none`, `health=on_track`.

### Realistic — all common fields

```yaml
apiVersion: crewship/v1
kind: Project
metadata:
  name: Q2 Roadmap
  slug: q2-roadmap
  description: |
    Every Q2 deliverable, from the auth rework through the
    sidecar V2 cutover. Owner: Pepa.
spec:
  color: "#3B82F6"
  status: active
  priority: high
  health: on_track
  target_date: "2026-06-30"
  lead_agent_slug: pepa
```

### Cross-kind FK — Milestone referencing a Project

```yaml
---
apiVersion: crewship/v1
kind: Project
metadata: { name: Q2 Roadmap, slug: q2-roadmap }
spec: { priority: high, target_date: "2026-06-30" }
---
apiVersion: crewship/v1
kind: Milestone
metadata: { name: v1.0 launch, slug: v1-launch }
spec:
  project_slug: q2-roadmap         # FK by slug, resolved at apply time
  target_date: "2026-06-15"
  status: planned
```

The Apply pipeline runs Projects (Phase 3) before Milestones (Phase 5), so the slug resolves cleanly even on a from-scratch deploy.

## CLI reference

The existing `crewship project ...` subcommands cover ad-hoc admin tasks; the manifest is the recommended path for repeatable deploys.

| Command | Description |
|---|---|
| `crewship apply --file projects.yaml` | Create/update projects to match the manifest |
| `crewship apply --file projects.yaml --dry-run` | Show the plan without mutating |
| `crewship apply --file projects.yaml --replace --yes` | Destructive recreate (Delete-then-Create per project) |
| `crewship export workspace` | Emit a multi-doc YAML containing every project (and other kinds) |
| `crewship project list` | Existing CLI surface — list projects in the workspace |
| `crewship project get <slug>` | Existing CLI surface — print one project's full row |
| `crewship project delete <slug>` | Existing CLI surface — delete by slug |

## REST endpoint mapping

| Manifest field | POST body field | DB column |
|---|---|---|
| `metadata.name` | `name` | `projects.name` |
| `metadata.slug` | `slug` | `projects.slug` (server re-derives from name) |
| `metadata.description` | `description` | `projects.description` |
| `spec.color` | `color` | `projects.color` |
| `spec.status` | `status` | `projects.status` |
| `spec.priority` | `priority` | `projects.priority` |
| `spec.health` | `health` | `projects.health` |
| `spec.target_date` | `target_date` | `projects.target_date` |
| `spec.lead_agent_slug` | `lead_id` (resolved from slug) + `lead_type: agent` | `projects.lead_id`, `projects.lead_type` |

Endpoints touched:

| Verb | Path | When |
|---|---|---|
| `GET` | `/api/v1/projects` | Plan phase — slug-based lookup against existing rows |
| `GET` | `/api/v1/agents` | Plan phase — resolve `lead_agent_slug` to `lead_id` |
| `POST` | `/api/v1/projects` | Apply phase, `Action=Create` |
| `PATCH` | `/api/v1/projects/{id}` | Apply phase, `Action=Update` |
| `DELETE` | `/api/v1/projects/{id}` | Apply phase, `Action=Delete` (only in ApplyReplace mode) |

## Validation rules

- `apiVersion` must equal `crewship/v1`.
- `kind` must equal `Project`.
- `metadata.name` is required and non-empty.
- `metadata.slug` is required and non-empty.
- `spec.status`, when set, must be one of `planned`, `active`, `completed`, `archived`.
- `spec.priority`, when set, must be one of `low`, `medium`, `high`, `urgent`.
- `spec.health`, when set, must be one of `on_track`, `at_risk`, `off_track`.
- `spec.color`, when set, must match `^#[0-9A-Fa-f]{6}$`.
- `spec.target_date`, when set, must parse as `YYYY-MM-DD`.
- `spec.lead_agent_slug`, when set, must reference an Agent present in either the declared manifest or the workspace's existing agents.

Validation is executed before any HTTP call, so a malformed document fails fast without partial mutations.

## Apply behavior

### Default — `ApplyUpsert`

1. Fetch `GET /api/v1/projects` and look up the slug client-side (the server has no `?slug=` filter).
2. If not found: emit `Action=Create` and `POST /api/v1/projects` with the full body.
3. If found and identical: emit `Action=Unchanged` (no HTTP call).
4. If found and drifted: emit `Action=Update` and `PATCH /api/v1/projects/{id}` with only the drifted fields. Fields the manifest leaves empty are skipped — they never overwrite a non-empty server value.

### `ApplyStrict`

Same as Upsert except step (2) is the only allowed outcome. If a project with the same slug already exists, apply fails with `already exists`. Use this in CI for "expect-fresh-deploy" pipelines.

### `ApplyReplace`

For every declared project that already exists remotely, emit `Action=Delete` (DELETE `/api/v1/projects/{id}`) before the Create. New projects skip the Delete and behave like Upsert. This mode also unlinks every mission whose `project_id` pointed at the old row, because the handler's DELETE path wraps the operation in a transaction that nulls the FK.

## Round-trip via export

`crewship export workspace` calls `ExportProjects`, which:

1. Issues `GET /api/v1/projects` for every project in the workspace.
2. Issues `GET /api/v1/agents` once and builds an `id → slug` map.
3. Maps each row into a `ProjectDocument`:
   - Server columns `id`, `workspace_id`, `created_at`, `updated_at`, `issue_count`, `done_count`, `progress`, and `icon` are dropped (manifest doesn't model server-managed state).
   - `lead_type=agent` + a known `lead_id` resolves back to `spec.lead_agent_slug`. User-leads (`lead_type=user`) and unknown agent ids are dropped silently — manifest only models agent leads.
   - Empty `description` / `target_date` collapse to omitted YAML keys via `omitempty`.
4. Sorts the result by slug so repeated exports produce identical files (clean git diffs).

`crewship export` emits the project as a standalone YAML doc separated by `---` from other kinds; `--split-dir <dir>` writes it as `project-<slug>.yaml` instead.

## See also

- [Milestone](./milestone.md) — references projects via `spec.project_slug`.
- [RecurringIssue](./recurring_issue.md) — pre-creates issues into a `template.project_slug`.
- [TriageRule](./triage_rule.md) — can route incoming issues into a project via `actions.assign_to_project_slug`.
- [SavedView](./saved_view.md) — filters its result set by `filter.project_slug`.
- SPEC-2 §1 Project (`.claude/context/specs/SPEC-2-manifest-complete.md`) — implementation contract.
