# kind: RecurringIssue

## What it is

A `RecurringIssue` is a workspace-scoped, crew-owned schedule that
stamps out a fresh issue every time its cron expression fires. It is
the declarative equivalent of opening the same recurring task in
your project tracker every week — "weekly status review", "monthly
billing reconciliation", "daily on-call handoff" — except authored
in YAML, version-controlled, and applied through
`crewship apply --file recurring.yaml`.

Every recurring issue must belong to a specific crew (the `crew_slug`
field is required). The cron and timezone fields drive the
scheduler; the nested `template:` block describes the issue that
gets created on each fire.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: RecurringIssue
metadata:
  name: Weekly status review        # human-readable; surfaces in the UI
  slug: weekly-status               # workspace-unique idempotency key
  description: |                    # optional
    Posted every Monday at 09:00.
spec:
  enabled: true                     # optional, default true
  cron: "0 9 * * MON"               # required — 5-field cron (see syntax below)
  timezone: Europe/Prague           # required — IANA timezone

  template:                         # required — the issue template
    title: "Weekly status — {{.Date}}"
    description: |
      Status update for week of {{.WeekStart}}.

      ## Done last week
      - …

      ## Planned this week
      - …
    labels: [recurring, status]     # optional — list of Label slugs
    project_slug: q2-roadmap        # optional — Project slug
    priority: medium                # optional — none|low|medium|high|urgent (default: none)
    assignee_agent_slug: pepa       # optional — Agent slug
    crew_slug: my-crew              # REQUIRED — Crew slug (recurring issues are crew-scoped)
```

### Field reference

| Field | Type | Required | Notes |
|---|---|---|---|
| `spec.enabled` | bool | no | Defaults to `true`. Set `false` to register the schedule but skip firing. |
| `spec.cron` | string | yes | Standard 5-field cron expression. See syntax below. |
| `spec.timezone` | string | yes | IANA timezone name (e.g. `Europe/Prague`, `UTC`, `America/New_York`). |
| `spec.template.title` | string | yes | Issue title. Go template syntax (`{{.Date}}`) is interpreted at fire time. |
| `spec.template.description` | string | no | Issue body. Same template syntax. |
| `spec.template.labels` | string[] | no | List of Label slugs (or names) to attach. Each must exist in the workspace. |
| `spec.template.project_slug` | string | no | Project slug. Must exist in the workspace. |
| `spec.template.priority` | enum | no | One of `none`, `low`, `medium`, `high`, `urgent`. Default `none`. |
| `spec.template.assignee_agent_slug` | string | no | Agent slug to assign the new issue to. |
| `spec.template.crew_slug` | string | **yes** | Crew that owns the issue. Required — recurring issues are crew-scoped. |

## Cron syntax

`spec.cron` uses the standard 5-field cron expression parsed by
[`github.com/robfig/cron/v3`](https://pkg.go.dev/github.com/robfig/cron/v3),
which mirrors the dialect of crontab(5):

```
┌───────────── minute        (0-59)
│ ┌───────────── hour          (0-23)
│ │ ┌───────────── day of month (1-31)
│ │ │ ┌───────────── month      (1-12 or JAN-DEC)
│ │ │ │ ┌───────────── day of week (0-6 or SUN-SAT; 0 = Sunday)
│ │ │ │ │
* * * * *
```

Useful examples:

| Expression | Meaning |
|---|---|
| `* * * * *` | Every minute |
| `0 * * * *` | Every hour, on the hour |
| `0 9 * * MON` | Every Monday at 09:00 |
| `0 0 1 * *` | First of every month, midnight |
| `*/15 9-17 * * MON-FRI` | Every 15 minutes during business hours, Mon–Fri |
| `0 0 1 1 *` | Once a year (Jan 1, midnight) |

Special syntax:
- Lists: `1,15,30` — minutes 1, 15, 30
- Ranges: `1-5` — Monday through Friday (in DOW)
- Steps: `*/5` — every 5th unit
- Names: `JAN`, `FEB`, …, `MON`, `TUE`, … (case-insensitive)

The parser does **not** support the `@yearly`, `@monthly`, `@hourly`
descriptor shortcuts — write the equivalent cron string instead
(`0 0 1 1 *` for yearly, etc.). It also does not support seconds
(no 6-field form) because the existing server handler uses the same
5-field parser; staying in lockstep prevents a manifest from
validating client-side and then failing server-side.

The timezone is independent of the system's TZ — `0 9 * * MON` with
`timezone: Europe/Prague` fires at 09:00 Prague time regardless of
where the Crewship server runs.

## Examples

### Minimal — daily standup reminder

```yaml
apiVersion: crewship/v1
kind: RecurringIssue
metadata:
  name: Daily standup
  slug: daily-standup
spec:
  cron: "0 9 * * MON-FRI"
  timezone: Europe/Prague
  template:
    title: "Standup — {{.Date}}"
    crew_slug: eng-team
```

### Realistic — weekly review with labels and assignee

```yaml
apiVersion: crewship/v1
kind: RecurringIssue
metadata:
  name: Weekly status review
  slug: weekly-status
  description: Sets up the weekly status thread every Monday at 09:00 Prague.
spec:
  enabled: true
  cron: "0 9 * * MON"
  timezone: Europe/Prague
  template:
    title: "Weekly status — {{.Date}}"
    description: |
      Status update for week of {{.WeekStart}}.

      ## Highlights
      - …

      ## Blockers
      - …
    labels: [recurring, status]
    project_slug: q2-roadmap
    priority: medium
    assignee_agent_slug: pepa
    crew_slug: my-crew
```

### Cross-kind references in one apply

A single `crewship apply --file` can declare every dependency the
recurring issue needs:

```yaml
apiVersion: crewship/v1
kind: Crew
metadata:
  name: My Crew
  slug: my-crew
spec:
  agents:
    - { slug: pepa, name: Pepa, agent_role: LEAD, prompt: "…" }
---
apiVersion: crewship/v1
kind: Label
metadata:
  name: recurring
  slug: recurring
spec:
  color: "#3B82F6"
---
apiVersion: crewship/v1
kind: Project
metadata:
  name: Q2 Roadmap
  slug: q2-roadmap
spec:
  status: active
---
apiVersion: crewship/v1
kind: RecurringIssue
metadata:
  name: Weekly status review
  slug: weekly-status
spec:
  cron: "0 9 * * MON"
  timezone: Europe/Prague
  template:
    title: "Weekly status — {{.Date}}"
    labels: [recurring]
    project_slug: q2-roadmap
    assignee_agent_slug: pepa
    crew_slug: my-crew
```

Apply runs phases in topological order (crew → labels/projects →
recurring issues), so every cross-kind slug resolves cleanly even on
a brand-new workspace.

## CLI reference

| Command | Description |
|---|---|
| `crewship recurring list` | List recurring issues in the current workspace |
| `crewship recurring get <slug>` | Show one recurring issue |
| `crewship recurring create -f recurring.yaml` | Apply a single recurring-issue file |
| `crewship recurring delete <slug>` | Delete by slug |
| `crewship recurring enable <slug>` | Set `enabled: true` |
| `crewship recurring disable <slug>` | Set `enabled: false` |
| `crewship apply --file recurring.yaml` | Generic apply path; works for any kind |
| `crewship export workspace` | Includes every recurring issue the user can read |

(`crewship recurring …` subcommands are provided by `cmd/crewship/cmd_recurring.go`; the apply/export commands are generic across all kinds.)

## REST endpoint mapping

| Manifest field | POST body field | DB column |
|---|---|---|
| `metadata.name` | `name` | (not stored as a column today — sent for symmetry with other kinds) |
| `metadata.slug` | `slug` | (idempotency key; reads via list filter) |
| `metadata.description` | `description` | n/a (kept on manifest side only) |
| `spec.enabled` | `enabled` | `enabled` |
| `spec.cron` | `cron` | `cron_expression` |
| `spec.timezone` | `timezone` | `timezone` |
| `spec.template.title` | `template_json.title` | `title` (also mirrored into template_json blob) |
| `spec.template.description` | `template_json.description` | `description` |
| `spec.template.labels[]` | `template_json.label_ids[]` (slug → id resolved) | `labels_json` |
| `spec.template.project_slug` | `template_json.project_id` (slug → id) | `project_id` |
| `spec.template.priority` | `template_json.priority` | `priority` |
| `spec.template.assignee_agent_slug` | `template_json.assignee_agent_id` (slug → id) | `assignee_id` (with `assignee_type='agent'`) |
| `spec.template.crew_slug` | `template_json.crew_id` (slug → id) | `crew_id` |

The endpoint is:

```
POST   /api/v1/recurring-issues          Create
GET    /api/v1/recurring-issues          List (workspace-scoped)
PATCH  /api/v1/recurring-issues/{id}     Update
DELETE /api/v1/recurring-issues/{id}     Delete
```

The kind sends a single `template_json` string field carrying the
resolved template; the server unmarshals it into the per-column
fields (`title`, `description`, `labels_json`, etc.) defined by the
existing recurring-issues table. Keeping the manifest payload
shaped as a single blob means future template fields don't require
DB migrations or handler changes.

## Validation rules

- `metadata.slug` is required.
- `spec.cron` is required and must parse via
  `cron.NewParser(cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow)`.
- `spec.timezone` is required and must parse via
  `time.LoadLocation` (i.e. a valid IANA zone).
- `spec.template.title` is required (issues need a title).
- `spec.template.crew_slug` is required and must reference a crew
  that exists in the workspace (declared in this manifest or already
  on the server).
- `spec.template.project_slug`, if set, must reference an existing
  project.
- `spec.template.assignee_agent_slug`, if set, must reference an
  existing agent.
- Every entry in `spec.template.labels[]` must reference an
  existing label.
- `spec.template.priority`, if set, must be one of `none`, `low`,
  `medium`, `high`, `urgent`.

Validation runs client-side before any REST call. A failing
manifest is reported with every offending rule in one
ValidationError so the author can fix all of them in one pass.

## Apply behavior

**Default mode (`ApplyUpsert`):**
1. Look up the existing row by `metadata.slug` via `GET /api/v1/recurring-issues`.
2. If absent → `Action=Create` → `POST /api/v1/recurring-issues`.
3. If present and any field drifts → `Action=Update` → `PATCH /api/v1/recurring-issues/{id}`.
4. If present and identical (including the resolved `template_json`)
   → `Action=Unchanged`, no network call.

**`ApplyStrict`:** Same as Upsert but a pre-existing slug aborts
the apply with a clear error — useful in CI when the manifest must
create fresh resources.

**`ApplyReplace`:** Emits a `Delete` plan item followed by a
`Create`. Destructive; the apply path prompts for confirmation
unless `--yes` was passed.

Drift detection looks **inside** `template_json` — adding or
removing a single label surfaces as `Action=Update` even if every
top-level column (cron, timezone, enabled) matches. The diff
compares resolved IDs, not slugs, so reordering a labels list in
the YAML without semantic change produces an `Unchanged` plan
(label IDs are sorted before comparison).

## Round-trip via export

`crewship export workspace` calls
`ExportRecurringIssues(ctx, client)` once per workspace, which:

1. GETs `/api/v1/recurring-issues` for the row list.
2. GETs `/api/v1/crews`, `/api/v1/projects`, `/api/v1/agents`,
   `/api/v1/labels` once to build id → slug lookup tables.
3. For each row, unmarshals `template_json`, reverse-resolves each
   ID back to its slug, and emits a `RecurringIssueDocument`.

The exported YAML re-applies cleanly: every slug in the file
resolves to the same row on the next `crewship apply`, producing
zero diff. Labels are sorted alphabetically in the exported file
so successive exports of the same state produce byte-identical
output.

`crewship export crew <slug>` filters: only recurring issues whose
`template.crew_slug == <slug>` are included, so a per-crew bundle
ships exactly the schedules that crew owns.

## See also

- [`kind: Crew`](crew.md) — provides the `crew_slug` reference. Crew must exist before the recurring issue applies.
- [`kind: Label`](label.md) — provides the `labels[]` references.
- [`kind: Project`](project.md) — provides the `project_slug` reference.
- [`kind: Routine`](routine.md) — for cron-triggered **automation pipelines**, where the trigger runs code instead of opening an issue. Recurring issues are the lightweight cousin: they only create a tracked work item; routines run a full agent pipeline.
- [`kind: TriageRule`](triage_rule.md) — to auto-label or auto-route the issues a recurring schedule creates.
