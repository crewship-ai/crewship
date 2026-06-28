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
        runtime: cel          # wired, token-zero (expr | cel only; bash/python/go are rejected)
        code: '"hi"'
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
    # Shell work runs via an agent with shell-tool access — `type: code`
    # only wires the token-zero expr/cel runtimes (bash/python/go are
    # rejected at author time; see "Code steps" below).
    - id: pull
      type: agent_run
      agent_slug: trapper             # MUST be a member of crew "uo-outlands"
      prompt: |
        Run exactly one shell command and return its raw JSON stdout:

            dce sync --channels "{{ inputs.channels }}" --json
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

## Code steps: wired runtimes (`expr`, `cel`) vs shell runtimes (rejected)

`type: code` supports two **wired**, deterministic, token-zero runtimes.
Shell runtimes (`bash | python | go`) are **rejected at author time**
because no sandbox runner is wired.

### `runtime: expr` — single comparison, token-zero

The `expr` runtime (`internal/pipeline/runner_code_expr.go`) is a
pure-Go, in-process evaluator: no container, no LLM, no filesystem or
network — it honours the token-zero guarantee and adds no
code-execution surface. It evaluates a single comparison and emits
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
with a schedule whose `wake_gate` checks the probe output.

### `runtime: cel` — general agentless logic, token-zero

The `cel` runtime (`internal/pipeline/runner_code_cel.go`) evaluates a
[Google CEL](https://github.com/google/cel-go) expression. CEL is
**non-Turing-complete** (every expression provably terminates), pure-Go,
and sandboxed by construction — no loops, no I/O — so it keeps the
token-zero / no-RCE guarantees while giving you real logic: boolean
operators (`&&`, `||`, `!`), arithmetic, string ops, list/map
membership, ternaries, and field access. It is the primitive to reach
for when `expr`'s single comparison is not enough.

Inputs are exposed as the typed `inputs` map variable (numbers stay
numbers), so reference them directly — no `{{ }}` needed:

```yaml
steps:
  - id: spike
    type: code
    code:
      runtime: cel
      # Real logic in one deterministic, token-zero step:
      code: 'inputs.spend_usd > inputs.threshold_usd && inputs.region in ["eu", "us"]'
```

A `bool` result emits `true` / `false`; numeric and string results emit
their canonical string form. Compile/eval errors (unknown variable, bad
syntax) fail closed.

### `runtime: bash | python | go` — rejected at author time

These runtimes are schema-legal names but have **no sandboxed runner
wired** in this build. As of PR #710 a routine that uses one is
**rejected when you save / apply / test_run it** — it can no longer
save-cleanly-then-fail-at-3am:

```text
pipeline: step "probe" (code) runtime "bash" has no wired runner in this
build — use runtime: expr or cel for agentless logic, or convert to
type: agent_run with a shell-tool-enabled agent
```

`crewship apply` also surfaces the same gap as a plan-time warning for
any routine that bypassed the validator (legacy import bundles, direct
API writes). If you need real shell, use the conversion recipe below.

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

## Approval gates (`type: wait`, kind `approval`)

A `wait` step with `kind: approval` pauses the run for a human decision. The run
does NOT block the caller: when a foreground `routine run` reaches the gate it
returns promptly with status **`WAITING`** and a waitpoint token, and the run
row is parked (`status=waiting`) — it has released its execution slot.

```text
$ crewship routine run release-with-approval
Run run_abc: WAITING (12ms, $0.0021)
  paused at approval step: approve
  approve: crewship routine waitpoints approve <token> --comment "LGTM"
  reject:  crewship routine waitpoints reject <token>
```

Resolve it from the inbox UI or the CLI:

```text
crewship routine waitpoints list                 # find the pending token
crewship routine waitpoints approve <token>      # → run resumes to COMPLETED
crewship routine waitpoints reject  <token>      # → run resumes and FAILS (denied)
```

Approving (or rejecting) resumes the run from the gate: completed steps are
restored and skipped, the wait step resolves from the recorded decision, and
the rest of the routine runs. A parked run also survives a server restart — the
boot-time resume scan re-enters `waiting` runs. (Timeouts on a parked approval
are reconciled at the next boot scan rather than live.)

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

## Run observability: tags, metadata, replay, errors

Runs carry trigger.dev-style observability surface for filtering and
post-failure recovery.

### Tags + metadata at invoke

```
crewship routine run cost-spike-probe \
  --tag prod --tag billing \
  --metadata '{"source":"manual","ticket":"OPS-42"}'
```

- **Tags** — workspace-scoped labels (max 10/run, lowercased). Surfaced
  on the run detail; group related runs (incl. replays, which inherit
  the source run's tags).
- **Metadata** — a JSON object stored on the run and returned by
  `GET /pipeline-runs/{id}`. Set at invoke today; mid-run mutation +
  `{{ run.metadata.X }}` templating is a follow-up.

### Replay a failed run

```
crewship routine replay <run_id>
```

Re-invokes the routine with the run's **captured inputs**. The new run
is stamped `is_replay=true` + `replay_of=<run_id>`; a step can skip side
effects on replay by gating on `{{ env.is_replay }}`:

```yaml
steps:
  - id: notify
    if: "{{ env.is_replay }}"   # only on replays — or invert to skip on replay
    type: agent_run
    ...
```

### Errors view + bulk replay

Failed runs are bucketed by a stable **error fingerprint** (failing step
+ normalized message), so like failures group together:

```
crewship routine errors                       # list fingerprint groups
crewship routine bulk-replay --fingerprint <fp>   # replay the whole group after a fix
```

| Endpoint | CLI |
|---|---|
| `POST /pipelines/runs/{runId}/replay` | `routine replay <run_id>` |
| `GET /pipelines/runs/errors` | `routine errors` |
| `POST /pipelines/runs/bulk_replay` | `routine bulk-replay --fingerprint <fp>` |

## Waitpoint callback tokens (external completion)

A `wait` step parks the run on a high-entropy **token**. Beyond the
inbox approve/reject flow, an **external system** can complete the wait
via a public callback URL — no workspace JWT, the token is the auth
(same model as webhook dispatch). Surface the URL from the pending
waitpoint:

```
crewship routine waitpoints <token>     # prints "Callback URL: …"
```

The external task then completes (or denies) the wait:

```
curl -X POST <callback_url> -d '{"approved":true,"payload":{"result":"ok"}}'
```

`approved` defaults to `true` (bare POST = "task done, continue").
`payload` is stored on the waitpoint for the resumed step. Endpoint:
`POST /api/v1/waitpoint-tokens/{token}`.

## Batch trigger

Fan out N runs of one routine from an array of input sets. Every run is
tagged `batch:<id>` so the set is retrievable.

```
crewship routine run my-routine --batch inputs.jsonl --tag nightly
crewship routine runs my-routine --tag batch:<id>
```

`inputs.jsonl` is one inputs object per line (or a JSON array). Endpoint:
`POST /pipelines/{slug}/run_batch` (max 50 items/batch). The run-level
`--tag` / `--metadata` apply to every run in the batch.

## Per-step prompt/model override (no version bump)

Tweak a single step's prompt or model **without** bumping the routine
version — the override is applied at run start over the versioned DSL.
The durable, versioned routine stays the source of truth; the override
is a thin live patch an operator can set and clear.

```
crewship routine step-override set my-routine summarize \
  --prompt "Summarize in 3 bullets, lead with the risk." --model smart
crewship routine step-override list my-routine
crewship routine step-override clear my-routine summarize
```

Only non-empty fields win, so a prompt-only override leaves the authored
model. Endpoints: `PUT|DELETE /pipelines/{slug}/steps/{stepId}/override`,
`GET /pipelines/{slug}/overrides`.

## Deferred dispatch: delay, ttl, debounce, priority

A trigger that carries a delay or a debounce key is parked in
`pending_runs` instead of running immediately; an in-process dispatcher
(5s tick) fires due rows **highest-priority-first** and expires rows past
their ttl. Immediate runs (no delay/debounce) are unchanged.

```
# fire 60s from now, expire if not dispatched within 5 min, high priority
crewship routine run my-routine --delay 60 --ttl 300 --priority 9

# coalesce a burst: repeated triggers with the same key collapse into one
crewship routine run my-routine --debounce-key vendor-42 --debounce-window 30 --debounce-max 300

crewship routine pending list            # not-yet-fired deferred triggers
crewship routine pending cancel <id>     # cancel before it fires
```

- `--delay N` — fire N seconds out (returns `SCHEDULED` with a pending id).
- `--ttl N` — expire the deferred run if not dispatched within N seconds.
- `--debounce-key K` — repeat triggers sharing K extend the window +
  replace inputs (one run fires); `--debounce-window` (default 30) sets
  the window, `--debounce-max` caps total extension so a hot key still fires.
- `--priority N` — higher fires first among due deferred runs.

API: `POST /pipelines/{slug}/run` accepts `delay_seconds`, `ttl_seconds`,
`debounce_key`, `debounce_window_seconds`, `debounce_max_seconds`,
`priority`, `idempotency_key_ttl_seconds`. `GET /pipelines/pending`,
`POST /pipelines/pending/{id}/cancel`.

> Note: `priority` orders the **deferred** dispatch queue. Immediate runs
> execute on arrival, so priority there is recorded but not consumed
> until the per-crew admission queue (QUEUE-MECHANISM) lands.
