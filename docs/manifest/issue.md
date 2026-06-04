# kind: Issue

## What it is

`kind: Issue` is the per-row CRUD entry point for a single issue
(a tracker ticket) declared in source control. Its cousin
[`RecurringIssue`](/manifest/recurring_issue) mints the same shape on a
cron; `kind: Issue` is for human-authored, one-shot tickets that
should be tracked declaratively rather than clicked into the UI.

Issues are **crew-scoped** — the create endpoint embeds the crew id in
the URL — so every Issue references its parent crew by
`spec.crew_slug`, resolved to a crew id at Plan time. Optional
references to a project, an assignee agent, and labels are likewise
resolved slug → id before the POST.

The kind is implemented in `internal/manifest/kinds/issue.go`.

### The slug is manifest-side only

The `missions` table that backs issues has **no slug column** and no
predictable identifier (the server-generated `ENG-7`-style identifier
is assigned by an atomic counter on POST). So `metadata.slug` is a
*manifest-side* idempotency key: it makes plan output and the apply
journal grep-able and lets cross-document references resolve, but it is
**not persisted server-side**.

Because there's no server slug, drift detection matches a declared
issue to a remote row by the pair **(crew, title)**. The consequence:
**renaming an issue's title in the manifest creates a new row instead
of updating the old one.** This mirrors how [Milestone](/manifest/milestone)
behaves (also no slug column). Keep titles stable if you want updates
to land on the same row.

## YAML schema

```yaml
apiVersion: crewship/v1        # required — always crewship/v1 for now
kind: Issue                    # required — the literal string "Issue"
metadata:
  name: Fix flaky login test   # required-ish — falls back to spec.title
  slug: fix-flaky-login-test   # required — manifest-side idempotency key
spec:
  crew_slug: code-review       # required — parent crew (resolved to crew_id)
  title: Fix flaky login test  # optional — falls back to metadata.name
  description: |               # optional — markdown body
    The login e2e flakes ~1 in 5 runs on CI.
  priority: high               # optional — none | low | medium | high | urgent
  status: todo                 # optional — backlog | todo | in_progress | review | done | failed | cancelled
  assignee_slug: daniel        # optional — agent slug → assignee
  project_slug: q2-roadmap     # optional — attach to a project
  labels: [bug, ci]            # optional — label slugs (== names)
```

### Field reference

| Field | Type | Required | Notes |
|---|---|---|---|
| `metadata.slug` | string | **yes** | Manifest-side idempotency key. NOT persisted server-side. |
| `metadata.name` | string | * | Display name; used as the title fallback when `spec.title` is empty. At least one of `metadata.name` / `spec.title` must be non-empty. |
| `spec.crew_slug` | string | **yes** | Parent crew slug. Resolved to `crew_id` at Plan time. |
| `spec.title` | string | * | The on-the-row title. Falls back to `metadata.name`. |
| `spec.description` | string | no | Free-form markdown body. |
| `spec.priority` | enum | no | One of `none` \| `low` \| `medium` \| `high` \| `urgent`. Empty → server default `none`. |
| `spec.status` | enum | no | One of `backlog` \| `todo` \| `in_progress` \| `review` \| `done` \| `failed` \| `cancelled` (uppercase also accepted; up-cased before sending). See the create-status quirk below. |
| `spec.assignee_slug` | string | no | Agent slug → `assignee_type=agent` + `assignee_id`. |
| `spec.project_slug` | string | no | Project slug → `project_id`. |
| `spec.labels` | []string | no | Label slugs (the [Label](/manifest/label) kind enforces slug == name). No duplicates, no empty entries. |

> **Create-status quirk.** The create handler hard-codes the new row
> to `BACKLOG` and ignores `spec.status` on POST. If you declare
> `status: done` on a brand-new Issue, the row first lands in
> `BACKLOG`; the **next** apply detects the drift and PATCHes it to
> `DONE`. Reaching a non-default starting status is therefore a
> two-apply operation today.

## Examples

### Minimal

```yaml
apiVersion: crewship/v1
kind: Issue
metadata:
  name: Fix flaky login test
  slug: fix-flaky-login-test
spec:
  crew_slug: code-review
```

### Full

```yaml
apiVersion: crewship/v1
kind: Issue
metadata:
  name: Fix flaky login test
  slug: fix-flaky-login-test
spec:
  crew_slug: code-review
  title: Fix flaky login test
  description: |
    The login e2e flakes ~1 in 5 runs on CI. Suspect a race in the
    session-cookie write.
  priority: high
  status: todo
  assignee_slug: daniel
  project_slug: q2-roadmap
  labels: [bug, ci]
```

### Cross-kind references

The Issue's FK targets (crew, project, agent, labels) must be declared
earlier in the same bundle or already exist on the server. The apply
phase order guarantees those kinds are created before the Issue:

```yaml
---
apiVersion: crewship/v1
kind: Label
metadata: { name: bug, slug: bug }
spec: { color: "#EF4444" }
---
apiVersion: crewship/v1
kind: Issue
metadata: { name: Fix flaky login test, slug: fix-flaky-login-test }
spec:
  crew_slug: code-review
  labels: [bug]       # ← resolved to the Label's id at apply time
```

## CLI reference

There is no dedicated `crewship issue` per-kind admin command for
declarative authoring — issues are managed through the manifest
pipeline (or the UI / runtime mission flow). The relevant CLI surface
is the global apply/export flow:

| Command | Description |
|---|---|
| `crewship apply --file issue.yaml` | Declarative create/update from manifest. |
| `crewship apply --dir ./manifests/` | Walk a directory; RecurringIssues/TriageRules and Issue FKs (crews, projects, labels) resolve in topo order. |
| `crewship apply --file issue.yaml --dry-run` | Plan only — surfaces dangling crew/project/assignee/label slugs before any mutation. |
| `crewship export workspace` | Round-trip — emits one `kind: Issue` document per row (see "Round-trip via export"). |

## REST endpoint mapping

| Manifest field | POST/PATCH body field | Notes |
|---|---|---|
| `spec.title` / `metadata.name` | `title` | Required server-side; the fallback decides which wins. |
| `spec.description` | `description` | |
| `spec.priority` | `priority` | Defaults to `none`. |
| `spec.status` | `status` | Ignored on POST (always BACKLOG); honored on PATCH. |
| `spec.assignee_slug` | `assignee_type=agent` + `assignee_id` | Resolved slug → id. |
| `spec.project_slug` | `project_id` | Resolved slug → id. |
| `spec.labels[]` | `labels` (id array) | On PATCH the handler treats a non-nil `labels` as a full set replacement. |

Endpoints used:

| Verb | Path | Action |
|---|---|---|
| `POST` | `/api/v1/crews/{crewId}/issues` | Create |
| `PATCH` | `/api/v1/crews/{crewId}/issues/{identifier}` | Update |
| `GET` | `/api/v1/issues?crew_id=…` | List (paginated; drift + export) |
| `GET` | `/api/v1/crews` / `/projects` / `/agents` / `/labels` | Resolve slugs → ids |

## Validation rules

`IssueDocument.Validate` enforces:

- `apiVersion` / `kind`, when set, equal `crewship/v1` / `Issue`.
- `metadata.slug` is non-empty.
- `spec.crew_slug` is non-empty.
- A non-empty resolved title (`spec.title` OR `metadata.name`).
- `priority` / `status`, when set, are members of their allow-lists.
- `spec.labels` has no empty entries and no duplicates.
- When `WorkspaceContext` carries the relevant data, `crew_slug`,
  `project_slug`, `assignee_slug`, and each label must reference a
  declared or remote entity (degrades gracefully when the context is
  empty — Plan catches dangling FKs at resolution).

## Apply behavior

### ApplyUpsert (default)

- No remote row with the same (crew, title) → `ActionCreate`: resolve
  every FK, then POST to the crew-scoped endpoint.
- Matching remote, fields/labels drift → `ActionUpdate`: a **sparse
  PATCH** of only the drifted fields. Labels ride in the same PATCH —
  a non-nil `labels` key is a full replacement; an empty list clears
  all labels.
- Matching remote, no drift → `ActionUnchanged`.

**Clearing an assignee** via manifest is not supported today (an empty
`assignee_slug` is treated as "leave alone", not "clear") — clearing is
a UI operation.

## Round-trip via export

`crewship export workspace` calls `ExportIssues`, which walks every
crew, lists its issues (paginated), and renders each as a `kind: Issue`
document. To avoid duplicate `metadata.slug` values across crews, the
export namespaces the slug as `<crew-slug>--<title-slug>` (falling back
to the server identifier when the title yields no slug-safe
characters). Project, assignee, and label references are folded back to
slugs. Fields the manifest doesn't model (identifier, timestamps,
comment counts) are dropped.

## See also

- [RecurringIssue](/manifest/recurring_issue) — the cron-driven sibling that mints the same shape.
- [TriageRule](/manifest/triage_rule) — auto-routes incoming issues.
- [Crew](/manifest/crew) — the parent crew (`spec.crew_slug`).
- [Project](/manifest/project) / [Label](/manifest/label) / [Agent](/manifest/agent) — FK targets.
- This kind's Go implementation: `internal/manifest/kinds/issue.go`.
