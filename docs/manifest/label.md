# kind: Label

## What it is

`kind: Label` declares a workspace-scoped tag that other entities
(issues, missions, triage rules, recurring issues, saved views)
reference to classify, route, and filter work. Labels are the
universal cross-cutting taxonomy of a Crewship workspace — they sit
above projects and crews and apply equally to any of them.

**Load-bearing invariant: `metadata.slug` MUST equal `metadata.name`.**
The labels table has no slug column — the backend keys label
uniqueness on `name` within a workspace. Every other manifest kind
references labels by `slug` (TriageRule.actions.add_labels,
SavedView.filter.label_slugs, RecurringIssue.template.labels) so we
preserve a single FK convention across the whole manifest by
forcing the slug to mirror the name. Validate rejects the document
if the two diverge.

## YAML schema

```yaml
apiVersion: crewship/v1   # required — always crewship/v1 for now
kind: Label               # required — the literal string "Label"
metadata:
  name: bug               # required — workspace-unique. Also the cross-kind FK target.
  slug: bug               # required — MUST equal metadata.name (see invariant above).
  description: ""         # optional — purely advisory; not stored server-side.
spec:
  color: "#EF4444"        # required for create — hex `#RRGGBB`, case-insensitive.
  description: ""         # optional — sent in POST body for forward-compat once
                          # the labels table grows a description column. Today
                          # the backend silently ignores it.
```

## Examples

### Minimal

```yaml
apiVersion: crewship/v1
kind: Label
metadata:
  name: bug
  slug: bug
spec:
  color: "#EF4444"
```

### Realistic with description

```yaml
apiVersion: crewship/v1
kind: Label
metadata:
  name: urgent
  slug: urgent
  description: Production-impacting; pull into the next standup
spec:
  color: "#F59E0B"
  description: Production-impacting; pull into the next standup
```

### Cross-kind FK reference

Once `Label` is declared, other kinds reference it by slug:

```yaml
---
apiVersion: crewship/v1
kind: Label
metadata: { name: bug, slug: bug }
spec: { color: "#EF4444" }
---
apiVersion: crewship/v1
kind: TriageRule
metadata: { name: Bug auto-label, slug: bug-auto-label }
spec:
  enabled: true
  priority: 100
  match:
    title_contains: ["error", "crash"]
  actions:
    add_labels: [bug]      # ← references the Label above by slug
```

The apply phase resolves `bug` → the freshly created label's ID
before POSTing the TriageRule body, so authoring order never
matters: the topological sort in `internal/manifest/apply.go`
guarantees `Phase 4: Labels` runs before `Phase 15: TriageRules`.

## CLI reference

The existing `crewship label` surface covers the per-kind admin
flow. The manifest pipeline uses these same endpoints under the
hood; no new subcommands ship with this kind.

| Command                                | Description                          |
| -------------------------------------- | ------------------------------------ |
| `crewship label list`                                    | List every label in the workspace.   |
| `crewship label create --name <name> --color <hex>`      | Create one label inline. `--name` and `--color` are required flags; `--group` is optional. |
| `crewship label update <id> --name <name> --color <hex>` | Patch fields on one label by id. |
| `crewship label delete <id>`                             | Remove a label (by id, not name).    |
| `crewship apply --file labels.yaml`                      | Declarative upsert from manifest.    |
| `crewship export workspace`                              | Round-trip — emits one doc per row.  |

## REST endpoint mapping

| Manifest field          | POST/PATCH body field | DB column         | Notes                                               |
| ----------------------- | --------------------- | ----------------- | --------------------------------------------------- |
| `metadata.name`         | `name`                | `labels.name`     | Idempotency key (workspace-unique).                 |
| `metadata.slug`         | (not sent)            | (none)            | Manifest-only; enforced to equal `metadata.name`.   |
| `metadata.description`  | (not sent)            | (none)            | Advisory text in the YAML; ignored by backend.      |
| `spec.color`            | `color`               | `labels.color`    | Required on create. Hex `#RRGGBB`.                  |
| `spec.description`      | `description`         | (none today)      | Sent in POST/PATCH body; backend currently ignores. |

Endpoints used:

| Verb     | Path                          | Action  |
| -------- | ----------------------------- | ------- |
| `GET`    | `/api/v1/labels`              | List    |
| `POST`   | `/api/v1/labels`              | Create  |
| `PATCH`  | `/api/v1/labels/{labelId}`    | Update  |
| `DELETE` | `/api/v1/labels/{labelId}`    | Delete  |

## Validation rules

`LabelDocument.Validate` enforces:

- `metadata.name` is non-empty.
- `metadata.slug` is non-empty.
- **`metadata.slug == metadata.name`** — the load-bearing invariant
  that keeps cross-kind slug references resolvable against a
  backend keyed on name. Surface the error verbatim:
  `label "X": metadata.slug must equal metadata.name (got slug="Y", name="X")`.
- `spec.color`, when set, matches `^#[0-9A-Fa-f]{6}$`. Empty color
  is allowed at Validate time so the backend's `color is required`
  400 reaches the user with the original handler context — Validate
  doesn't duplicate server-side rules unless the manifest would
  otherwise silently produce a malformed apply.

Validate does **not** consult `WorkspaceContext` — labels have no
FK dependencies, so the parameter exists only to keep the dispatcher
signature uniform across kinds.

## Apply behavior

### ApplyUpsert (default)

For each declared label:

1. List `GET /api/v1/labels`, filter client-side by `name == metadata.name`.
2. Not found → `POST /api/v1/labels` with `{name, color, description}`.
3. Found, fields drift (color / description / name) → `PATCH /api/v1/labels/{id}` with only the changed fields. The PATCH body is pointer-style on the backend, so unspecified keys stay untouched.
4. Found and identical → `Action=Unchanged`, no REST call.

### ApplyStrict

A label whose `metadata.name` already exists in the workspace is a
hard error — apply aborts with `already exists` before touching any
other resource.

### ApplyReplace

Destructive recreate: emit `DELETE /api/v1/labels/{id}` first, then
`POST /api/v1/labels` for every declared label. Labels not declared
in the manifest are also deleted in this mode. Be aware that
`DELETE` cascades through `mission_labels` (the join table) and
removes the label from every mission that carried it — there is no
soft-delete on the labels table.

## Round-trip via export

`crewship export workspace` calls `ExportLabels` which:

1. `GET /api/v1/labels` once.
2. Emits one `LabelDocument` per row.
3. Sets `metadata.slug = row.name` so the export survives Validate
   on the next apply (the slug==name invariant is honored on
   both sides of the round-trip).
4. Output order matches the API response (today: name ASC).

`crewship export crew <slug>` includes the workspace's labels by
default because triage rules and recurring issues scoped to that
crew can reference any label. Pass `--crew-only` to exclude them.

## See also

- [Project](/manifest/project) — usually labeled alongside (e.g. `bug` + `q2-roadmap`).
- [TriageRule](/manifest/triage_rule) — references labels via `actions.add_labels`.
- [RecurringIssue](/manifest/recurring_issue) — references labels via `template.labels`.
- [SavedView](/manifest/saved_view) — references labels via `filter.label_slugs`.
- Backend handler: `internal/api/issue_handler_labels.go`.
