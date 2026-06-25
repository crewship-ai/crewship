# kind: Routine

## What it is

`kind: Routine` declares a workspace-scoped declarative AI workflow
— what Crewship has historically called a "pipeline" in the database
and a "routine" in the UI. A routine is a versioned, schedulable,
webhook-dispatchable DAG of steps (agent runs, HTTP calls, code
blocks, transforms, waits) that the workspace's agents can invoke and
that humans can trigger directly via cron or webhook.

This kind **subsumes the legacy `crewship routine save -f X.json`
flow** while remaining fully backward-compatible: the JSON body that
the old CLI sent under `--file` is exactly the shape that lives under
`spec:` (modulo the manifest-only `schedules` + `webhook` fields
described below). An operator migrating from JSON to YAML can copy
their existing `routine.json` body verbatim into `spec:`, add the
manifest envelope (`apiVersion`, `kind`, `metadata`), and have it
apply identically. No field renames, no semantic drift — the
`internal/pipeline.Parse` server-side validator is the same code path
in both paths.

Routine is the **biggest** kind in the manifest system because one
document atomically deploys three sibling rows:

1. one `pipelines` row (the routine definition itself)
2. zero or more `pipeline_schedules` rows (cron triggers)
3. zero or one `pipeline_webhooks` row (public dispatch token)

A single `crewship apply -f routine.yaml` creates / updates / prunes
all three in one transaction-like sequence — what used to take three
CLI calls (`routine save` + `routine schedule create` + `routine
webhook create`) is now one declarative file.

## YAML schema

```yaml
apiVersion: crewship/v1                 # required — always crewship/v1
kind: Routine                           # required — literal "Routine"
metadata:
  name: <human-readable label>          # required — UI display name
  slug: <kebab-case>                    # required — workspace-unique idempotency key
  description: <optional>               # optional — surfaces in routine list
  labels:
    crew: <crew-slug>                   # REQUIRED — parent crew the routine belongs to.
                                        # Every step.agent_slug must be an agent that
                                        # is a member of this crew (server enforces
                                        # at apply time).

spec:
  # ---- routine DSL (same as routine.v1.json) ----
  dsl_version: "1.0"                    # required — DSL schema version, currently "1.0"
  description: <optional>               # routine-level description (one-line)
  inputs:                               # optional — declared input parameters
    - name: <identifier>
      type: string|integer|number|boolean|array|object
      required: false
      default: <value>
      description: <optional>
  steps:                                # required — at least one step
    - id: <unique within routine>
      type: agent_run|call_pipeline|http|code|wait|transform
      # agent_run-specific:
      agent_slug: <agent-slug-in-parent-crew>
      prompt: <template-substitutable string>
      # ...other per-type fields documented in schemas/routine.v1.json
  credentials_required:                 # optional — typed credential references
    - { type: <cred-type>, scope: <optional-scope> }
  estimated_cost_usd: 0.05              # optional — author estimate
  estimated_duration_seconds: 180       # optional — author estimate
  max_cost_usd: 0.50                    # optional — runtime cost cap (aborts run)
  egress_targets:                       # optional — enforced for `http` steps
    - <hostname>

  # ---- manifest-only nested triggers ----
  schedules:                            # optional — 0..N cron triggers
    - name: <schedule label>            # required, unique within this routine
      cron: "0 * * * *"                 # required — 5-field cron expr (Descriptor allowed)
      timezone: Europe/Prague           # required — IANA timezone name
      enabled: true                     # optional, default true
      inputs:                           # optional — override routine defaults at trigger time
        <input-name>: <value>

  webhook:                              # optional — at most one per routine
    enabled: true                       # required
    require_token: true                 # optional, default true (false = open dispatch)
    token_env_ref: WEBHOOK_TOKEN_ENV    # optional — name of a workspace env var
                                        # carrying the public token. Surfaced as a
                                        # warning at Plan time if it does not resolve.
```

## Examples

### Minimal — single-step routine, no triggers

```yaml
apiVersion: crewship/v1
kind: Routine
metadata:
  name: Echo
  slug: echo
  labels:
    crew: my-crew
spec:
  dsl_version: "1.0"
  steps:
    - id: hello
      type: code
      code:
        runtime: bash
        code: "echo hi"
```

### Realistic — Discord hourly sync with cron + webhook

```yaml
apiVersion: crewship/v1
kind: Routine
metadata:
  name: Discord hourly sync
  slug: discord-sync
  description: Pull recent Discord channel activity and summarize via Claude
  labels:
    crew: uo-outlands       # the routine runs in the context of this crew

spec:
  dsl_version: "1.0"
  description: Hourly Discord pull + LLM summary

  inputs:
    - name: channels
      type: string
      required: false
      default: all
      description: Comma-separated channel ids, or "all" to pull every channel.

  steps:
    - id: pull
      type: code
      code:
        runtime: bash
        code: |
          dce sync --channels "{{ inputs.channels }}" --json > /tmp/raw.json
          cat /tmp/raw.json
      timeout_seconds: 120

    - id: summarize
      type: agent_run
      agent_slug: trapper             # MUST be a member of crew "uo-outlands"
      needs: [pull]
      prompt: |
        Summarize the following Discord activity in 5 bullets,
        flagging anything that requires moderator follow-up.

        Raw events:
        {{ steps.pull.output }}
      complexity: fast
      validation:
        min_length: 100
        must_not_contain:
          - "ANTHROPIC_API_KEY"
          - "DISCORD_BOT_TOKEN"

  credentials_required:
    - { type: GENERIC_SECRET, scope: discord-bot }
    - { type: API_KEY,        scope: anthropic   }

  estimated_cost_usd: 0.05
  estimated_duration_seconds: 180
  max_cost_usd: 0.50

  egress_targets:
    - discord.com
    - cdn.discordapp.com

  # Two schedules: production hourly + a low-frequency weekly digest.
  schedules:
    - name: Hourly
      cron: "0 * * * *"
      timezone: Europe/Prague
      enabled: true
      inputs:
        channels: all

    - name: Weekly digest
      cron: "0 9 * * MON"
      timezone: Europe/Prague
      enabled: true
      inputs:
        channels: announcements,community

  # One webhook for inbound Vendor invocations (e.g. third-party
  # alert sources). require_token: true (default) means the webhook
  # token must match the value of $DISCORD_WEBHOOK_TOKEN at dispatch
  # time; setting require_token: false would make the endpoint
  # publicly invokable (only safe behind a private network gateway).
  webhook:
    enabled: true
    require_token: true
    token_env_ref: DISCORD_WEBHOOK_TOKEN
```

## CLI reference

| Command | Purpose |
|---|---|
| `crewship apply -f routine.yaml` | Create / update the routine + its schedules + its webhook. |
| `crewship apply --dir ./manifests/` | Apply every routine (and every other kind) in a directory tree. |
| `crewship apply --dry-run -f routine.yaml` | Show the plan (per-row create/update/delete) without mutating. |
| `crewship export crew uo-outlands` | Round-trip: dump every routine labeled `crew: uo-outlands` back to YAML. |
| `crewship export workspace` | Dump every routine in the workspace (no crew filter). |
| `crewship routine list` | Pre-manifest CLI: still works, shows existing routines (read-only here). |
| `crewship routine save -f routine.json` | **Legacy** — equivalent to applying a Routine document with no schedules/webhook. Kept for back-compat. |

## REST endpoint mapping

How each manifest field lands on a REST call and ultimately a DB column:

| Manifest field | HTTP verb | Path | DB column / table |
|---|---|---|---|
| `metadata.slug` + `spec.*` (routine DSL) | `POST` | `/api/v1/workspaces/{ws}/pipelines/save` | `pipelines.slug` + `pipelines.definition_json` |
| `metadata.name` | (same as above) | (same as above) | `pipelines.name` |
| `metadata.labels.crew` | (resolved client-side to crew id) | (same as above; server validates) | `pipelines.author_crew_id` |
| `spec.schedules[]` | `POST` | `/api/v1/workspaces/{ws}/pipeline-schedules` | `pipeline_schedules.*` |
| `spec.schedules[].name` | (same as above) | (same as above) | `pipeline_schedules.name` |
| `spec.schedules[].cron` | (same as above) | (same as above) | `pipeline_schedules.cron_expr` |
| `spec.schedules[].timezone` | (same as above) | (same as above) | `pipeline_schedules.timezone` |
| `spec.schedules[].inputs` | (same as above; JSON-encoded) | (same as above) | `pipeline_schedules.inputs_json` |
| `spec.schedules[].enabled` | (same as above) | (same as above) | `pipeline_schedules.enabled` |
| `spec.webhook` | `POST` | `/api/v1/workspaces/{ws}/pipeline-webhooks` | `pipeline_webhooks.*` |
| `spec.webhook.enabled` | (same as above) | (same as above) | `pipeline_webhooks.enabled` |
| `spec.webhook.token_env_ref` | (resolved at Plan time; not persisted) | (n/a — the server mints its own token) | — |

Plan-then-Apply order is fixed: routine row first, then schedules,
then webhook. Schedules and the webhook foreign-key back to the
routine, so we cannot reverse the order.

## Validation rules

Validate (client-side, before any REST call fires):

- `metadata.name` and `metadata.slug` required.
- `metadata.labels.crew` required; must appear in the workspace's
  declared crews OR remote crews.
- `spec.dsl_version` required.
- `spec.steps` must contain at least one step.
- Every `step.agent_slug` (on `type: agent_run` steps) must appear
  in the workspace's declared agents OR remote agents. We DO NOT
  validate the agent-membership-in-parent-crew constraint here; the
  server enforces that at apply time.
- Every schedule must have a non-empty `name`; names unique within
  the document.
- Every schedule's `cron` must parse with
  `github.com/robfig/cron/v3`'s standard parser (5 fields + descriptor
  support).
- Every schedule's `timezone` must parse via `time.LoadLocation`.
- Webhook `token_env_ref` is NOT validated here — workspace creds
  aren't in `WorkspaceContext` today. A Plan-time advisory line on
  the report flags missing resolution; Validate stays purely
  structural so Export → re-apply round-trips cleanly.

The DSL itself (step shapes, validation blocks, outcomes, etc.) is
re-validated server-side by `internal/pipeline.Parse` and
`internal/pipeline.Validate`. We deliberately don't re-implement that
logic here — duplicating it would just create skew opportunities.

## Apply behavior

### `ApplyUpsert` (default)

1. **Routine row.** Look up `/pipelines/{slug}`:
   - missing → `Action=Create`, POST `/pipelines/save`
   - drifted (name, description, or canonical definition JSON
     differs) → `Action=Update`, POST `/pipelines/save` again (the
     save endpoint is idempotent on slug; it bumps the version)
   - identical → `Action=Unchanged`

2. **Schedules.** List `/pipeline-schedules` filtered by slug,
   match-by-name against the declared schedules. For each:
   - declared, not on remote → `Action=Create`, POST
     `/pipeline-schedules`
   - declared and drifted (cron, timezone, enabled, or inputs differ)
     → `Action=Update`, PATCH `/pipeline-schedules/{id}`
   - declared and identical → `Action=Unchanged`
   - on remote but no longer declared → `Action=Delete`, DELETE
     `/pipeline-schedules/{id}` (the manifest is the source of truth)

3. **Webhook.** GET the routine's webhook (if any):
   - declared `enabled: true`, no remote → `Action=Create`, POST
     `/pipeline-webhooks`
   - declared, remote drifted → `Action=Update`, which is
     delete-then-recreate (no PATCH endpoint exists)
   - declared and identical → `Action=Unchanged`
   - `webhook:` omitted (or `enabled: false`) but remote exists →
     `Action=Delete`

### `ApplyStrict`

Refuses to update or delete; if any routine in the manifest already
exists on the server, apply stops with an error. Use in CI when "this
manifest must create fresh resources" is the requirement.

### `ApplyReplace`

Destructive recreate: emits `Action=Delete` for every existing
routine + schedule + webhook that matches a manifest slug, then
creates everything fresh. The webhook token + signing secret change
on replace (server-minted on each create) — operators must
re-distribute the new public URL after a replace.

## Round-trip via export

`crewship export crew <crew-slug>` emits one `Routine` document per
routine where `pipelines.author_crew_id` resolves to `<crew-slug>`.
Each document includes the full DSL under `spec:` plus every nested
schedule and the (optional) webhook block.

What round-trips losslessly:

- `metadata.name`, `metadata.slug`, `metadata.labels.crew`
- The entire routine DSL (`spec.dsl_version`, `spec.description`,
  `spec.inputs`, `spec.steps`, `spec.credentials_required`,
  `spec.estimated_cost_usd`, `spec.estimated_duration_seconds`,
  `spec.max_cost_usd`, `spec.egress_targets`)
- `spec.schedules[]` (every field — name, cron, timezone, enabled,
  inputs)
- `spec.webhook.enabled` only

What does NOT round-trip (manifest-only fields):

- `spec.webhook.require_token` — the server stores
  `signing_secret_set` instead; export emits `require_token: true`
  implicitly via the default.
- `spec.webhook.token_env_ref` — purely a Plan-time hint to the CLI
  about which env var holds the public token; not persisted.

Operators editing an exported document should re-add `token_env_ref`
manually before re-applying, otherwise the Plan layer will print a
warning that the webhook's public URL won't be discoverable from the
env.

## Code steps: `runtime: expr` (wired) vs shell runtimes (not wired)

`type: code` supports two tiers of runtime.

### `runtime: expr` — wired, deterministic, token-zero

The `expr` runtime is the production runner for **agentless** probes
(`internal/pipeline/runner_code_expr.go`). It is a pure-Go, in-process
evaluator: it spins no container, calls no LLM, and touches no
filesystem or network — so it honours the token-zero guarantee and adds
no code-execution surface. It evaluates a single comparison and emits
`true` / `false`:

```yaml
steps:
  - id: probe
    type: code
    code:
      runtime: expr
      # body is rendered first ({{ inputs.x }} substituted), then
      # evaluated. Operators: >  >=  <  <=  ==  !=
      code: "{{ inputs.spend_usd }} > {{ inputs.threshold_usd }}"
```

Operands are numeric or string literals, or a `CREWSHIP_INPUT_<NAME>`
env reference. This is the wake-gate / cost-spike primitive — pair it
with a schedule whose `wake_gate` checks the probe output. Anything that
isn't a single comparison fails closed at validation/run time.

### `runtime: bash | python | go` — validated, but no sandbox wired

These runtimes pass the DSL validator but have **no sandboxed runner
wired** in this build. A routine that uses one **saves successfully**
and the schedule fires on cron, but the step fails at runtime with:

```text
code runtime "bash" not available in this build (no sandbox wired) —
use runtime: expr for agentless probes, or convert this step to
type: agent_run with an agent that has shell-tool access
```

`crewship apply` surfaces this at plan time as a yellow warning (only
for the unwired runtimes — `expr` steps do not warn) so you see the gap
before the cron fires the first time:

```text
Warnings:
  ! routine "seznam-check": step "probe" is type: code with runtime
    "bash", which has no wired runner — invocations will fail until it
    is converted to type: agent_run with a shell-tool-enabled agent, or
    to runtime: expr for agentless probes
```

### Conversion recipe (shell runtimes → agent_run)

Replace the code step with an `agent_run` against an agent whose
`tool_profile: FULL` (or any profile that includes shell). The agent
runs the same command from inside its container, which is already
wired end-to-end.

Before:

```yaml
steps:
  - id: probe
    type: code
    code:
      runtime: bash
      code: |
        curl -sS https://www.seznam.cz -o /tmp/page.html -w '%{http_code}'
```

After:

```yaml
steps:
  - id: probe
    type: agent_run
    agent_slug: sre-lead
    prompt: |
      Run exactly one shell command and report the http status code:

          curl -sS https://www.seznam.cz -o /tmp/page.html -w '%{http_code}'

      Reply on one line: `OK <code>` if status is 200, otherwise
      `BREACH <code>`.
```

Trade-off: an agent_run is ~30× more expensive than a raw shell
exec because it goes through the LLM. For pure shell probes that's
acceptable as a stopgap; the proper fix is to land a Docker-backed
CodeRunner — tracked separately from this doc.

## See also

- [Your First Crew](/guides/first-crew) — the parent concept; routines reference a crew via
  `metadata.labels.crew`. `step.agent_slug` resolves against agents-in-crew.
- [Connector](/manifest/connector) — `credentials_required` types are
  resolved against workspace credentials, which may come from
  installed connectors.
- [Hook](/manifest/hook) — `pre_run` / `post_run` hooks fire around every
  routine invocation.
- `schemas/routine.v1.json` — JSON Schema for the DSL portion of
  `spec:` (everything except `schedules` + `webhook`). Use with VSCode
  / JetBrains autocomplete: add `"$schema":
  "./schemas/routine.v1.json"` to a standalone `routine.json` file.
