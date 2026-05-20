# Crewship manifest reference

The manifest is Crewship's declarative deployment surface: a YAML (or JSON, since JSON is YAML 1.2) file that captures every user-creatable entity as data. One `crewship apply --file foo.yaml` (or `crewship apply --dir ./manifests/`) converges the workspace to match. `crewship export workspace` round-trips back.

This page is the index. Per-kind details live in `docs/manifest/<kind>.md`.

## Why manifests

- **GitOps for Crewship.** Commit a manifest to git, deploy to dev / staging / prod from CI. No clickops drift.
- **Sharing without screenshots.** Send a workspace YAML to a teammate; they apply it and have an identical setup.
- **Reproducible disasters.** Backups + export = a complete restore path.
- **Less surface for human error.** YAML linting + manifest validation catch typos before they reach the server.

## Kinds at a glance

Every document has the shape:

```yaml
apiVersion: crewship/v1
kind: <Kind>
metadata:
  name: <human-readable>
  slug: <kebab-case, unique within workspace>
spec:
  ...kind-specific...
```

### Workspace structure

| Kind | What it is | Lifecycle | Doc |
|---|---|---|---|
| [`Workspace`](workspace.md) | Top-level bundle: credentials + skills + crews | full CRUD | (existing) |
| [`Crew`](crew.md) | One crew: agents + sidecars + MCP servers + credentials | full CRUD | (existing) |
| [`Project`](project.md) | Container for missions and milestones | full CRUD | [project.md](project.md) |
| [`Label`](label.md) | Tag for issues and missions | full CRUD | [label.md](label.md) |
| [`Milestone`](milestone.md) | Time-boxed project goal | full CRUD | [milestone.md](milestone.md) |

### Workflow + automation

| Kind | What it is | Lifecycle | Doc |
|---|---|---|---|
| [`Routine`](routine.md) | Workflow DSL (steps + schedules + webhook) | full CRUD | [routine.md](routine.md) |
| [`RecurringIssue`](recurring_issue.md) | Issue template + cron → periodic issue creation | full CRUD | [recurring_issue.md](recurring_issue.md) |
| [`TriageRule`](triage_rule.md) | Match + actions for auto-routing incoming issues | full CRUD | [triage_rule.md](triage_rule.md) |
| [`WorkflowTemplate`](workflow_template.md) | Custom issue status flow (Kanban-style stages) | full CRUD | [workflow_template.md](workflow_template.md) |

### Views + ops

| Kind | What it is | Lifecycle | Doc |
|---|---|---|---|
| [`SavedView`](saved_view.md) | Shared filtered list (issues / missions / runs) | full CRUD | [saved_view.md](saved_view.md) |
| [`Hook`](hook.md) | Toggle for code-registered lifecycle hooks | enable/disable only | [hook.md](hook.md) |

### Catalog references (install / deploy)

| Kind | What it is | Lifecycle | Doc |
|---|---|---|---|
| [`Recipe`](recipe.md) | Install a recipe from the server catalog | install only | [recipe.md](recipe.md) |
| [`CrewTemplate`](crew_template.md) | Deploy a crew from a template blueprint | deploy only (one-shot) | [crew_template.md](crew_template.md) |
| [`Connector`](connector.md) | Install an OAuth connector (Linear, GitHub, …) | install only | [connector.md](connector.md) |

### Instance / org config

| Kind | What it is | Lifecycle | Doc |
|---|---|---|---|
| [`FeatureFlag`](feature_flag.md) | Toggle experimental features (instance-default + per-workspace override) | full CRUD | [feature_flag.md](feature_flag.md) |
| [`InstanceSetting`](instance_setting.md) | Key/value config (SMTP, branding, …) — admin-only | full CRUD | [instance_setting.md](instance_setting.md) |

## Foreign-key references

Cross-kind references always use the **slug** of the referenced entity. The apply pipeline resolves slug → id after the dependency is created.

```yaml
# Milestone references Project by slug
kind: Milestone
spec:
  project_slug: q2-roadmap   # ← resolves to projects.id

# RecurringIssue references Project + Labels + Crew by slug
kind: RecurringIssue
spec:
  template:
    project_slug: q2-roadmap
    labels: [recurring, status]
    crew_slug: my-crew
```

## Apply order (topological)

`crewship apply` runs kinds in this order so dependencies are created before dependents:

1. Workspace credentials + skills (existing)
2. Crews + agents (existing)
3. **Projects, Labels** (no deps)
4. **Milestones** (deps: Projects)
5. **WorkflowTemplates**
6. **FeatureFlags, InstanceSettings**
7. **Recipes, CrewTemplates, Connectors** (catalog installs)
8. **Routines** (deps: Crews, Agents)
9. **Schedules + Webhooks** (nested in Routines)
10. **RecurringIssues** (deps: Projects, Labels, Crews)
11. **TriageRules** (deps: Projects, Labels, Crews)
12. **SavedViews** (deps: Labels, Projects)
13. **Hooks** (toggles only)

## Apply modes

```bash
crewship apply --file foo.yaml                # ApplyUpsert (default): create/update/delete to match
crewship apply --file foo.yaml --strict       # Fail if any slug already exists
crewship apply --file foo.yaml --replace --yes # Destructive: delete existing, recreate fresh
crewship apply --file foo.yaml --dry-run      # Plan only — no mutations
crewship apply --dir ./manifests/             # Walk a directory; apply every YAML/JSON in topo order
```

## Round-trip via export

```bash
crewship export workspace                     # everything in the workspace (multi-doc YAML)
crewship export workspace --split-dir ./out/  # one file per kind
crewship export crew uo-outlands              # the crew + everything labelled `crew: uo-outlands`
crewship export crew uo-outlands --crew-only  # JUST the crew document, no labels/projects/routines
```

## CLI per-kind admin commands

Every kind also has a per-entity CLI surface for one-off operations (no manifest needed):

| Kind | Command |
|---|---|
| Project | `crewship project list/get/create/update/delete` |
| Label | `crewship label list/create/update/delete` |
| Milestone | `crewship milestone list/create/update/delete` |
| Routine | `crewship pipeline list/save/run/schedules/webhooks/...` |
| RecurringIssue | `crewship recurring list/create/update/delete` |
| TriageRule | `crewship triage list/create/update/delete/process` |
| SavedView | `crewship saved-view list/create/update/delete` |
| WorkflowTemplate | `crewship workflow list/get/create/delete` |
| FeatureFlag | `crewship feature-flag list/enable/disable/inherit` |
| InstanceSetting | `crewship instance settings list/get/set/delete` |
| Hook | `crewship hooks list/enable/disable` |
| Recipe | `crewship recipe list/get/install` |
| CrewTemplate | `crewship template list/get/deploy` |
| Connector | `crewship connector list/get/install` |

## Examples

- [examples/manifests/full-complete.yaml](../../examples/manifests/full-complete.yaml) — one document per kind in one file
- [examples/manifests/full-team.workspace.yaml](../../examples/manifests/full-team.workspace.yaml) — multi-crew workspace (legacy shape)
- [examples/manifests/code-review.crew.yaml](../../examples/manifests/code-review.crew.yaml) — single-crew (legacy shape)

## What's NOT in the manifest (and why)

Some entities are deliberately out of scope:

| Entity | Why not |
|---|---|
| `missions`, `agent_runs`, `pipeline_runs`, `chats` | Runtime instances, not declarative state |
| `audit_logs`, `notifications`, `cost_ledger` | Telemetry, not config |
| `workspace_members`, `crew_members` | IAM lives in SSO / UI / CSV import, not YAML |
| `sessions`, `cli_pairings`, `oauth_states` | Ephemeral auth state |
| `backup_catalog`, `scheduled_jobs` | System runtime |
| `keeper_requests`, `approvals_queue` | Runtime instances of policies (policies themselves may land in a future `kind: ApprovalPolicy`) |

## See also

- [SPEC-2-manifest-complete.md](../../.claude/context/specs/SPEC-2-manifest-complete.md) — implementation contract / full schema
- [PRD: API](../../.claude/context/prd/API.md)
- [PRD: ORCHESTRATION](../../.claude/context/prd/ORCHESTRATION.md)
