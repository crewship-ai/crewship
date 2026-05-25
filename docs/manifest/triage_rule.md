# kind: TriageRule

## What it is

`kind: TriageRule` declares a workspace-scoped automation that
inspects incoming issues — their title, body, originating agent, or
originating crew — and applies a fixed set of mutations
(`add_labels`, set priority, assign to a project, route to an
agent, change status) whenever the conditions match. Rules form an
ordered pipeline: each issue runs through every enabled rule, low
`priority` numbers first, and the first match wins. They are the
declarative form of "if a bug report mentions 'crash', label it
`bug` and assign it to the on-call agent."

Under the hood the server stores the structured match/action blocks
as opaque JSON TEXT columns (`match_json`, `actions_json`), but the
manifest exposes them as nested objects so YAML stays readable. The
apply pipeline marshals them on the way in, and `crewship export`
unmarshals them on the way out.

## YAML schema

```yaml
apiVersion: crewship/v1   # required — always crewship/v1 for now
kind: TriageRule          # required — the literal string "TriageRule"
metadata:
  name: Bug auto-label    # required — workspace-unique. Idempotency key.
  slug: bug-auto-label    # required — kebab-case; also the cross-doc handle.
  description: ""         # optional — advisory only.
spec:
  enabled: true           # default: true. Toggle without deleting.
  priority: 100           # rule evaluation order — LOWER runs FIRST.
                          # Default: 100 when omitted or zero.

  match:                  # all populated fields are AND-ed together.
                          # At least one field must be non-empty
                          # (Validate rejects an entirely empty match
                          # because such a rule would fire on every
                          # incoming issue — almost certainly a typo).

    title_contains: []    # []string — substring match (case-insensitive
                          # at evaluation time); empty list = no constraint.
    body_contains: []     # []string — same semantics for the issue body.
    from_agent_slug: ""   # only fire when the issue originated from
                          # the agent with this slug. Empty = any agent.
    from_crew_slug: ""    # only fire when the originating agent
                          # belongs to this crew. Empty = any crew.

  actions:                # mutations applied to the matched issue.
                          # Every field is optional individually; an
                          # actions block with no fields just bumps the
                          # rule's match counter (useful for monitoring).

    add_labels: []        # []string — slugs of Label kinds to attach.
                          # Validate fails the apply if any slug isn't
                          # declared in the manifest or already present
                          # on the server.
    set_priority: ""      # one of: "low" | "medium" | "high" | "urgent".
                          # Empty = leave priority unchanged.
    assign_to_project_slug: ""  # slug of a Project kind. Validate
                                # fails if the project doesn't resolve.
    assign_to_agent_slug: ""    # slug of an Agent in the workspace.
                                # Validate fails if the agent doesn't resolve.
    set_status: ""        # free-form status string (matches the
                          # workspace's WorkflowTemplate stages, e.g.
                          # "in_review", "backlog"). Empty = no change.
```

## Examples

### Auto-label bug reports

The canonical use-case: any issue whose title mentions a crash- or
error-related keyword gets tagged `bug` and bumped to high priority.

```yaml
apiVersion: crewship/v1
kind: TriageRule
metadata:
  name: Bug auto-label
  slug: bug-auto-label
spec:
  enabled: true
  priority: 100
  match:
    title_contains: ["error", "crash", "exception", "panic"]
  actions:
    add_labels: [bug]
    set_priority: high
```

The rule depends on a `Label` named `bug` existing in the same
workspace. If the manifest declares both, the topological apply
order in `internal/manifest/apply.go` (`Phase 4: Labels` runs before
`Phase 15: TriageRules`) guarantees the label exists by the time
the triage rule's POST body is built.

### Auto-assign Discord pulls to the trapper agent

A more elaborate rule: any issue raised by the `discord-puller`
agent inside the `uo-outlands` crew is automatically routed to the
`q2-roadmap` project, assigned to the `trapper` agent, and tagged
`discord` + `automation`.

```yaml
apiVersion: crewship/v1
kind: TriageRule
metadata:
  name: Discord auto-route
  slug: discord-auto-route
spec:
  enabled: true
  priority: 50            # runs BEFORE the bug-auto-label rule (priority 100)
  match:
    from_agent_slug: discord-puller
    from_crew_slug: uo-outlands
  actions:
    add_labels: [discord, automation]
    assign_to_project_slug: q2-roadmap
    assign_to_agent_slug: trapper
    set_status: in_review
```

Because `priority: 50` is lower than the bug-auto-label rule's
`priority: 100`, this Discord-specific rule fires first. If it
doesn't match (issue isn't from the discord-puller), evaluation
continues to the next rule.

### Cross-kind FK references

A complete deployable bundle: Project + Labels + the rule that
references both.

```yaml
---
apiVersion: crewship/v1
kind: Project
metadata: { name: Q2 Roadmap, slug: q2-roadmap }
spec: { status: active, priority: high }
---
apiVersion: crewship/v1
kind: Label
metadata: { name: bug, slug: bug }
spec: { color: "#EF4444" }
---
apiVersion: crewship/v1
kind: Label
metadata: { name: urgent, slug: urgent }
spec: { color: "#F59E0B" }
---
apiVersion: crewship/v1
kind: TriageRule
metadata: { name: Production crash, slug: prod-crash }
spec:
  enabled: true
  priority: 10            # runs first — destructive matches take precedence
  match:
    title_contains: ["production", "outage"]
    body_contains: ["error", "crash"]
  actions:
    add_labels: [bug, urgent]
    set_priority: urgent
    assign_to_project_slug: q2-roadmap
```

## CLI reference

The existing `crewship triage` surface covers per-rule admin and
the `process` endpoint (which actually evaluates rules against the
current backlog). The manifest pipeline uses the CRUD endpoints
under the hood; no new subcommands ship with this kind.

| Command                                  | Description                                          |
| ---------------------------------------- | ---------------------------------------------------- |
| `crewship triage list`                   | List every triage rule in the workspace.             |
| `crewship triage get <slug>`             | Fetch a single rule by name/slug.                    |
| `crewship triage create -f rule.yaml`    | Imperative create from a YAML file.                  |
| `crewship triage delete <slug>`          | Delete a rule (one-shot; bypasses manifest).         |
| `crewship triage process`                | Evaluate all enabled rules against the backlog.      |

For declarative management, use `crewship apply --file
triage.yaml` instead — manifest-managed rules round-trip via
`crewship export` and survive workspace migrations.

## REST endpoint mapping

The manifest's structured `match` / `actions` blocks marshal into
two JSON TEXT columns on the server. The mapping is one-way at
apply time and reversed at export time.

| Manifest field                      | POST body field        | DB column           |
| ----------------------------------- | ---------------------- | ------------------- |
| `metadata.name`                     | `name`                 | `triage_rules.name` |
| `metadata.slug`                     | `slug`                 | (advisory only — no slug column) |
| `spec.enabled`                      | `enabled`              | `triage_rules.enabled` |
| `spec.priority` (default 100)       | `priority`             | `triage_rules.priority` |
| `spec.match.*` (all nested fields)  | `match_json` (string)  | `triage_rules.match_json` |
| `spec.actions.*` (all nested fields)| `actions_json` (string)| `triage_rules.actions_json` |

`match_json` and `actions_json` are JSON-encoded strings whose
keys are the snake_case versions of the YAML field names
(`title_contains`, `from_agent_slug`, `add_labels`,
`assign_to_project_slug`, etc.) — the exact JSON tags on the
`TriageMatch` and `TriageActions` Go structs.

| Endpoint                               | Verb   | Used by             |
| -------------------------------------- | ------ | ------------------- |
| `/api/v1/triage-rules`                 | GET    | Plan (lookup), Export |
| `/api/v1/triage-rules`                 | POST   | Plan (create)       |
| `/api/v1/triage-rules/{id}`            | PATCH  | Plan (update)       |
| `/api/v1/triage-rules/{id}`            | DELETE | ApplyReplace        |

## Validation rules

`TriageRuleDocument.Validate(ctx)` enforces every rule in one pass
and returns all violations joined into a single error so the user
gets the full picture per apply attempt.

- `metadata.name` and `metadata.slug` are required (both must be
  non-empty after trim).
- `spec.match` must have at least one non-empty field
  (`title_contains`, `body_contains`, `from_agent_slug`, or
  `from_crew_slug`). A wholly empty match would fire on every
  issue — almost always a manifest authoring mistake.
- Every slug in `spec.actions.add_labels` must resolve via
  `ctx.HasLabel(slug)` — i.e. it appears in the manifest's
  `DeclaredLabels` or in the workspace's `RemoteLabels`.
- `spec.actions.assign_to_project_slug`, if set, must resolve via
  `ctx.HasProject(slug)`.
- `spec.actions.assign_to_agent_slug`, if set, must resolve via
  `ctx.HasAgent(slug)`.
- `spec.match.from_agent_slug`, if set, must resolve via
  `ctx.HasAgent(slug)`.
- `spec.match.from_crew_slug`, if set, must resolve via
  `ctx.HasCrew(slug)`.
- `spec.actions.add_labels` entries that are empty strings are
  rejected (use `[]` or omit the field instead).

`spec.priority` is an `int`. Zero or absent values are treated as
the default (100) at apply time — they're not a validation error.

## Apply behavior

### Default mode (`Upsert`)

For each declared `TriageRule`:

1. Fetch every existing rule via `GET /api/v1/triage-rules`.
2. Match by `metadata.name` against the server's `name` field
   (the natural key — the server has no `slug` column).
3. If no match: emit `Action=Create` → POST the rule with the
   structured match/actions blocks marshaled into
   `match_json`/`actions_json` strings.
4. If match found but any of {enabled, priority, match_json,
   actions_json} differ: emit `Action=Update` → PATCH the rule.
5. If match found and every field is byte-identical (after
   re-marshaling both sides through the same JSON encoder):
   emit `Action=Unchanged`.

### `ApplyStrict`

Strict mode is enforced at the parent `apply.go` level, not inside
`TriageRule.Plan`: if any declared rule's name collides with an
existing server-side rule, the parent aborts before invoking Plan.
The TriageRule kind itself has no extra strict behavior.

### `ApplyReplace`

The parent layer emits an `Action=Delete` for every server-side
rule whose name is in the manifest, then re-runs Plan with
`remote=nil` so each kind issues a fresh `Action=Create`. The
TriageRule kind doesn't need a special-case branch for replace —
its existing nil-remote path covers the recreation step.

## Round-trip via export

`crewship export workspace` invokes `ExportTriageRules`, which:

1. Fetches every rule via `GET /api/v1/triage-rules`.
2. For each row, unmarshals `match_json` and `actions_json` back
   into structured `TriageMatch` / `TriageActions` Go values so
   the YAML output is readable, not JSON-encoded strings.
3. Derives a `metadata.slug` from the row's `name` field
   (kebab-case) — the server doesn't store a slug column for
   triage rules, so we deterministically slugify on export. The
   operator can override by editing the exported YAML before
   re-apply.
4. Tolerates corrupt `match_json` / `actions_json` server-side:
   the export emits the rest of the document with the corrupted
   field at its zero value rather than failing outright. (A reapply
   of the exported YAML will re-write the JSON column.)

The resulting document is byte-stable: `apply → export → apply`
produces zero plan items the second time.

## See also

- [Label](/manifest/label) — `actions.add_labels` references Label slugs
- [Project](/manifest/project) — `actions.assign_to_project_slug` references Project slugs
- [RecurringIssue](/manifest/recurring_issue) — sibling kind for time-based
  (rather than match-based) issue creation
- [SavedView](/manifest/saved_view) — uses the same label/project FK
  conventions for filter expressions
- `internal/api/triage_handler.go` — backend handler that serves
  the REST endpoints this kind targets
