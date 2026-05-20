# kind: Hook

## What it is

`kind: Hook` is the **toggle-only** manifest kind. A `Hook` document
flips the `enabled` boolean on a hook that is already registered in
code; it can **never create a new hook**.

Hooks are part of the runtime control plane — they fire on lifecycle
events (`pre_run`, `post_run`, …) and can run shell commands, dispatch
sub-agents, or call HTTP endpoints. Because that surface is sensitive
(arbitrary shell, third-party network egress), hook *registration* is
deliberately a build-time concern: a developer wires the hook in
Go code (see `internal/hooks/store.go:Register`) and only then can an
operator decide whether to switch it on for a given environment.

The manifest therefore exposes exactly one verb: **toggle**. If the
hook does not exist server-side, `crewship apply` fails with
`hook "X" is not registered — register it in code first`, which is
the explicit prompt to add the registration in code rather than in
YAML.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: Hook
metadata:
  # human-readable name; surfaces in CLI plan output and the hook
  # journal entry.
  name: pre-run-cost-gate

  # MUST equal the hook id from GET /api/v1/hooks. Hooks have no
  # separate slug column in the DB, so the manifest uses the
  # code-registered id as the slug. Stable across environments.
  slug: pre-run-cost-gate

  # optional — purely descriptive; the server doesn't store this.
  description: "Aborts a mission run if estimated cost exceeds budget"

spec:
  # The only field. true → ensure the hook is enabled;
  # false → ensure the hook is disabled.
  enabled: true
```

There is no `event`, `matcher`, `handler_kind`, or `handler_config`
field. Those live in code and are immutable from the manifest's
perspective.

## Examples

### Minimal — enable a single hook

```yaml
apiVersion: crewship/v1
kind: Hook
metadata:
  name: pre-run-cost-gate
  slug: pre-run-cost-gate
spec:
  enabled: true
```

### Disable a hook (e.g. dev environment)

```yaml
apiVersion: crewship/v1
kind: Hook
metadata:
  name: prod-only-pager-duty
  slug: prod-only-pager-duty
spec:
  enabled: false
```

### Multi-doc bundle — toggle several hooks at once

```yaml
apiVersion: crewship/v1
kind: Hook
metadata: { name: pre-run-cost-gate,   slug: pre-run-cost-gate }
spec: { enabled: true }
---
apiVersion: crewship/v1
kind: Hook
metadata: { name: post-run-slack-notify, slug: post-run-slack-notify }
spec: { enabled: true }
---
apiVersion: crewship/v1
kind: Hook
metadata: { name: prod-only-pager-duty,  slug: prod-only-pager-duty }
spec: { enabled: false }
```

A single `crewship apply -f` over this file leaves the workspace's
hooks in exactly the declared state. Hooks already in the desired
state report as `unchanged`.

## CLI reference

Hooks have a pre-existing CLI surface — `crewship hooks ...` — for
listing and toggling outside the manifest. The manifest path is the
declarative complement; the imperative commands stay useful for
break-glass / one-off toggles in production.

| Command                              | Description                                   |
|--------------------------------------|-----------------------------------------------|
| `crewship hooks list`                | Print the workspace's hooks + their state.    |
| `crewship hooks enable <id>`         | Imperative enable (same endpoint as manifest).|
| `crewship hooks disable <id>`        | Imperative disable.                           |
| `crewship apply -f hooks.yaml`       | Toggle hooks declaratively.                   |
| `crewship export workspace`          | Includes `kind: Hook` docs for every hook.    |

There is no `crewship hooks create` because hooks are registered in
code, not over REST. Attempting to apply a `kind: Hook` document for
an unregistered hook is the error path — the CLI message tells the
operator to add the registration in Go and rebuild.

## REST endpoint mapping

| Manifest field   | Resolves to                                                  |
|------------------|--------------------------------------------------------------|
| `metadata.slug`  | Path segment `{id}` in `/api/v1/hooks/{id}/{enable\|disable}` |
| `spec.enabled`   | `true` → POST `.../enable`; `false` → POST `.../disable`      |

The manifest only consumes three REST routes:

- `GET  /api/v1/hooks`               — list every registered hook (used by Plan + Export)
- `POST /api/v1/hooks/{id}/enable`   — toggle on
- `POST /api/v1/hooks/{id}/disable`  — toggle off

The DB columns on `hooks_config` that the manifest actually touches:

| DB column   | Manifest equivalent           |
|-------------|-------------------------------|
| `id`        | `metadata.slug`               |
| `enabled`   | `spec.enabled`                |

Every other column (`event`, `matcher`, `handler_kind`,
`handler_config`, `blocking`, `crew_id`, `workspace_id`, `created_*`,
`updated_*`) is read-only from the manifest's perspective and set
when the developer registers the hook in code.

## Validation rules

Static, performed by `Validate` before any network round-trip:

- `apiVersion` must equal `crewship/v1`.
- `kind` must equal `"Hook"`.
- `metadata.slug` must be set (non-blank). It is the hook id.
- `metadata.name` must be set (non-blank).
- `spec.enabled` is a boolean; YAML defaults to `false` when omitted.

The "hook actually exists on the server" check happens at **Plan
time** (it requires a live HTTP call against `/api/v1/hooks`). A
missing hook surfaces as a PlanItem with `Action=Update` whose Exec
closure returns the registration error — that lets `--dry-run`
report every missing hook in one pass instead of stopping at the
first.

## Apply behavior

### Default mode (`ApplyUpsert`)

- Declared `enabled` matches remote → `Action=Unchanged` (no network call).
- Declared `enabled` differs from remote → `Action=Update`,
  POSTs to `/api/v1/hooks/{id}/enable` or `/disable`.
- Hook does not exist on the server → `Action=Update` with an
  erroring Exec closure (`hook "X" is not registered — register it
  in code first`). Apply fails on that hook but the dry-run plan
  shows every drifted/missing hook so the operator gets the full
  picture in one pass.

### `ApplyStrict`

No semantic difference for hooks — the strict-mode "fail if any slug
already exists" rule only fires for create actions, and hooks never
create. A declared hook that's missing in the registry produces the
same registration-error PlanItem in either mode.

### `ApplyReplace`

Same plan as `ApplyUpsert`. The "replace = delete + create" pattern
has no meaning for a kind the user cannot author, so `ApplyReplace`
collapses to the default toggle path. (Trying to delete a hook via
the manifest would silently un-register a code path; the design
intentionally refuses.)

## Round-trip via export

`crewship export workspace` emits one `kind: Hook` document per row
in `hooks_config`. The slug is the hook's `id`; the spec carries the
current `enabled` state. `metadata.description` is synthesised from
`event + handler_kind` (e.g. `"pre_run shell hook"`) — the
`hooks_config` table has no description column, so the export side
manufactures one for human readability.

The round-trip property is one-way:

- `apply` → server state matches manifest.
- `export → apply` → no-op (manifest matches server, every hook
  reports `unchanged`).

`export` is the way to capture the current toggle layout for source
control. Diffing two exports reveals which hooks drifted between
environments.

## Why this kind is special

Most manifest kinds have full Create/Update/Delete authority. Hooks
deliberately don't, because:

1. **Shell hooks execute arbitrary commands.** A hook registered in
   YAML would let any operator with manifest-apply rights smuggle
   shell commands into the supervisor — an obvious privilege
   escalation. The code-registration gate forces a code-review,
   build, and deploy cycle for new shell hooks.
2. **HTTP hooks egress sensitive workspace state.** Same reasoning —
   any new HTTP destination needs to go through the egress-allowlist
   review in code.
3. **Hook matchers are coupled to internal event names.** Letting
   the manifest define a matcher freezes the manifest schema to the
   internal event enum. Keeping matchers in code lets the event
   surface evolve without a breaking manifest version bump.

The manifest still owns the **policy** ("which hooks are on in this
environment?") which is the part operators actually need to control
declaratively.

## See also

- [`internal/hooks/store.go`](../../internal/hooks/store.go) — Go
  side of hook registration (`hooks.Register`).
- [`internal/api/hooks_handler.go`](../../internal/api/hooks_handler.go)
  — REST handler the manifest calls.
- [Hooks operator guide](../guides/hooks.mdx) — runtime semantics,
  matcher syntax, and handler kinds.
- `kind: TriageRule` — also "rules in YAML", but those *are*
  user-creatable because they only mutate workspace data, not the
  control plane.
