# kind: WorkflowTemplate

## What it is

A `WorkflowTemplate` defines the **state machine** an issue, run, or other tracked item moves through inside a workspace. It's the Linear-style equivalent of "status options on a board": you declare an ordered list of stages, each tagged with a `type` (open, started, completed, or cancelled), and the workspace UI uses that as the column layout for kanban boards plus the legal transition graph for status changes.

WorkflowTemplates are workspace-scoped and idempotent on `metadata.slug` within a workspace. The `workflow_templates` DB table has no slug column, so on export the manifest synthesises a kebab-case slug from `metadata.name` to keep round-trips stable.

Built-in templates seeded by the server (`sequential`, `parallel`, `dev-test-loop`, `pipeline`) are owned by the server and explicitly **excluded** from export — re-applying an exported workspace will never overwrite them and never produce drift against them.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Engineering Standard          # required — stored verbatim in workflow_templates.name
  slug: engineering-standard          # required — workspace-unique idempotency key
spec:
  description: "Default engineering issue lifecycle"   # optional
  icon: ":hammer_and_wrench:"          # optional — emoji shortcode or icon slug
  color: "#3B82F6"                     # optional — hex RGB, applies to the template badge
  stages:                              # required — at least one entry
    - { name: backlog,     type: open,        position: 1, color: "#9CA3AF" }
    - { name: in_progress, type: started,     position: 2, color: "#3B82F6" }
    - { name: in_review,   type: started,     position: 3, color: "#F59E0B" }
    - { name: done,        type: completed,   position: 4, color: "#10B981" }
    - { name: cancelled,   type: cancelled,   position: 5, color: "#EF4444" }
```

### Field reference

| Field | Required | Type | Notes |
|---|---|---|---|
| `apiVersion` | yes | string | Always `crewship/v1`. |
| `kind` | yes | string | Always `WorkflowTemplate`. |
| `metadata.name` | yes | string | Stored verbatim. Drives the UI label and is the server-side uniqueness key (per workspace). |
| `metadata.slug` | yes | string | Workspace-unique manifest identifier. |
| `spec.description` | no | string | Free-form text shown on the template detail panel. |
| `spec.icon` | no | string | Emoji shortcode (`:hammer_and_wrench:`) or icon slug — the UI looks it up against its icon set. |
| `spec.color` | no | string | `#RRGGBB`. Three-digit shorthand is rejected. |
| `spec.stages` | **yes** | array | Ordered list of stage objects. Must be non-empty. |
| `spec.stages[].name` | yes | string | Unique within the template. Lowercase + underscores conventionally; the UI displays them with that casing. |
| `spec.stages[].type` | yes | enum | One of `open`, `started`, `completed`, `cancelled` — see [Stage types](#stage-types) below. |
| `spec.stages[].position` | yes | int | Unique within the template. Drives the UI column order. |
| `spec.stages[].color` | no | string | `#RRGGBB`. Same constraint as `spec.color`. |

### Stage types

Every stage carries one of four `type` tags. The tag is what the orchestrator and UI key on for behaviour — not the stage name, which is purely human-facing.

| Type | Semantics | Cardinality |
|---|---|---|
| `open` | Entry state. Newly-created items land here. **Exactly one** stage per template must be `open`; that stage is the implicit default when an item is created without a status. | Exactly 1 |
| `started` | In-progress states. The item is being worked on but is not yet terminal. Multiple `started` stages are common (e.g. `in_progress`, `in_review`, `blocked`). | 0..n |
| `completed` | Terminal success states. The item is done and counts toward "completed" in burn-down / velocity rollups. **At least one** is required so an item can ever finish. | 1..n |
| `cancelled` | Terminal failure / abandon states. The item is done but is **excluded** from completion metrics. Use this for "won't fix", "duplicate", "out of scope" — anything that closes the item without counting as progress. | 0..n |

Transitions are unrestricted by default: from any stage to any other stage. The state machine is structural ("which columns exist?"), not behavioural ("which arrows are legal?").

## Examples

### Minimal example

```yaml
apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Simple
  slug: simple
spec:
  stages:
    - { name: todo,  type: open,      position: 1 }
    - { name: done,  type: completed, position: 2 }
```

The minimum legal template: one `open` stage and one `completed` stage. No `started` and no `cancelled` are fine.

### Realistic example with all common fields

```yaml
apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Engineering Standard
  slug: engineering-standard
spec:
  description: |
    Default engineering issue lifecycle. Used by all backend crews.
    Map your repo's PR labels to these stages 1-to-1.
  icon: ":hammer_and_wrench:"
  color: "#3B82F6"
  stages:
    - { name: backlog,        type: open,      position: 1, color: "#9CA3AF" }
    - { name: ready,          type: open,      position: 2, color: "#6B7280" }    # ⚠ invalid — only one open allowed
    - { name: in_progress,    type: started,   position: 3, color: "#3B82F6" }
    - { name: in_review,      type: started,   position: 4, color: "#F59E0B" }
    - { name: blocked,        type: started,   position: 5, color: "#EF4444" }
    - { name: done,           type: completed, position: 6, color: "#10B981" }
    - { name: wont_fix,       type: cancelled, position: 7, color: "#6B7280" }
    - { name: duplicate,      type: cancelled, position: 8, color: "#6B7280" }
```

The `ready` row above intentionally illustrates a **rejected** shape — Validate refuses two `open` stages. Pick one of `backlog`/`ready` as the entry state and tag the other `started`.

### Multi-template bundle

```yaml
apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Support Triage
  slug: support-triage
spec:
  description: "Customer support intake → resolution"
  stages:
    - { name: new,         type: open,      position: 1 }
    - { name: triaged,     type: started,   position: 2 }
    - { name: with_eng,    type: started,   position: 3 }
    - { name: resolved,    type: completed, position: 4 }
    - { name: not_a_bug,   type: cancelled, position: 5 }
---
apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Marketing Sprint
  slug: marketing-sprint
spec:
  stages:
    - { name: idea,       type: open,      position: 1 }
    - { name: drafting,   type: started,   position: 2 }
    - { name: shipping,   type: started,   position: 3 }
    - { name: live,       type: completed, position: 4 }
```

Multiple templates may be declared in one file; each is applied independently. There are no cross-template references.

## CLI reference

```bash
crewship workflow list                          # GET /api/v1/workflow-templates
crewship workflow get <slug>                    # GET /api/v1/workflow-templates/{id}, slug→id resolved
crewship workflow create -f workflow.yaml       # POST /api/v1/workflow-templates
crewship workflow delete <slug>                 # DELETE /api/v1/workflow-templates/{id}
crewship apply --file workflow.yaml             # manifest pipeline (preferred for repeatable setups)
crewship export workspace                       # emits one document per non-builtin template
```

The `crewship apply` path is the only one that resolves `metadata.slug` → server ID for you. The flat `crewship workflow get/delete` accept a slug for convenience and do the lookup themselves.

## REST endpoint mapping

| Manifest field | POST body field | DB column |
|---|---|---|
| `metadata.name` | `name` | `workflow_templates.name` |
| `spec.description` | `description` | `workflow_templates.description` |
| `spec.stages` | `template_json` (serialised) | `workflow_templates.template_json` (TEXT) |
| `spec.icon` | `icon` | `workflow_templates.icon` |
| `spec.color` | `color` | `workflow_templates.color` |
| _(server-set, always `false` for user-created)_ | — | `workflow_templates.is_builtin` |
| _(server-assigned)_ | — | `workflow_templates.id`, `workspace_id`, `created_at`, `updated_at` |

The `template_json` column stores the entire `stages` array as a JSON string (the column is `TEXT`, not `JSON`). The handler does not re-parse user input — it passes the marshalled string straight through to the DB, so any future stage-shape extension is forward-compatible.

| Operation | Method | Path |
|---|---|---|
| List | GET | `/api/v1/workflow-templates` |
| Get | GET | `/api/v1/workflow-templates/{id}` |
| Create | POST | `/api/v1/workflow-templates` |
| Update | PATCH | `/api/v1/workflow-templates/{id}` |
| Delete | DELETE | `/api/v1/workflow-templates/{id}` |

All paths are workspace-scoped via the JWT/workspace context. RBAC: OWNER, ADMIN, and MANAGER can create / update / delete; every authenticated role can read.

## Validation rules

- `metadata.name` is required (server rejects empty `name` with HTTP 400).
- `metadata.slug` is required and must be unique within the workspace.
- `spec.stages` must be a non-empty array.
- Each `spec.stages[].name` must be non-empty and unique within the template.
- Each `spec.stages[].position` must be unique within the template.
- Each `spec.stages[].type` must be one of `open`, `started`, `completed`, `cancelled`.
- **Exactly one** stage must have `type=open`.
- **At least one** stage must have `type=completed`.
- `spec.color` and `spec.stages[].color`, if set, must match `^#[0-9A-Fa-f]{6}$`. Three-digit shorthand is not accepted.

Failed validation is reported per-document with the offending template slug in the error message so multi-doc bundles surface every issue in one pass.

## Apply behavior

### `ApplyUpsert` (default)

1. List the workspace's templates via `GET /api/v1/workflow-templates`.
2. Match by `metadata.name`:
   - No match → `Action=Create`, `POST /api/v1/workflow-templates` with `{name, description, template_json, icon, color}`.
   - Match with differing fields → `Action=Update`, `PATCH /api/v1/workflow-templates/{id}` carrying the same body.
   - Match with identical fields → `Action=Unchanged`, no HTTP call issued.

Stages are compared after sorting by `position`, so reordering the `stages` array in the YAML file without changing positions is a no-op (Unchanged), not a drift.

### `ApplyStrict`

Fails with a `slug already exists` error if any declared template already exists by name. Useful for new-workspace bootstrapping where overwriting a same-named template would be a bug.

### `ApplyReplace`

For each declared template, emits `Action=Delete` followed by `Action=Create`. Use this only when you want a fresh row (e.g. you've renamed a stage and want the underlying record reset rather than mutated in place). Apply also deletes any templates in the workspace that the manifest no longer declares — but **never** the built-in templates, which are protected server-side.

## Round-trip via export

`crewship export workspace` lists every non-builtin template and emits one `kind: WorkflowTemplate` document per row. The export decodes `template_json` back into the structured `stages` array so the output is directly re-applyable:

```yaml
apiVersion: crewship/v1
kind: WorkflowTemplate
metadata:
  name: Engineering Standard
  slug: engineering-standard         # synthesised from name on export
spec:
  description: Default engineering issue lifecycle
  icon: ":hammer_and_wrench:"
  color: "#3B82F6"
  stages:
    - { name: backlog,     type: open,      position: 1, color: "#9CA3AF" }
    - { name: in_progress, type: started,   position: 2, color: "#3B82F6" }
    - { name: done,        type: completed, position: 4, color: "#10B981" }
```

The synthesised slug strips non-alphanumeric runs to single dashes (`Engineering Standard` → `engineering-standard`). If two templates in the same workspace share a name (rejected by the DB's `UNIQUE(workspace_id, name)` index — so this should never happen in practice), their slugs would collide and `crewship apply` would reject the bundle.

Built-in templates (`sequential`, `parallel`, `dev-test-loop`, `pipeline`, plus any others the server seeds in the future) are **filtered out**. The intent is that re-applying an exported workspace bundle never produces drift against server-owned rows.

## See also

- [kind: Project](/manifest/project) — projects don't reference workflow templates directly today, but a future `default_workflow_slug` field is planned.
- [kind: TriageRule](/manifest/triage_rule) — `actions.set_status` references a stage name; if you wire one up, make sure the stage exists in whichever template the matched issue lives under.
- [kind: SavedView](/manifest/saved_view) — views can filter by stage type (`type=open`/`type=started`) to render "active work" boards.
- SPEC-2 section 8 — authoritative contract for this kind.
