# kind: FeatureFlag

## What it is

`kind: FeatureFlag` declares a runtime toggle that Crewship code
checks at request time. A flag has two layers and the manifest is
designed to write to both in a single document:

1. The **instance-global definition** — name (`key`), description,
   the default `enabled` state, and an optional `default_percentage`
   for gradual-rollout flags. This row lives in the `feature_flags`
   table and is shared by every workspace on the instance. Only an
   instance administrator who is also OWNER or ADMIN of the target
   workspace may create, update, or delete it
   ([details](/api-reference/feature-flags#who-may-change-a-definition)).

2. The **workspace override** — an optional per-workspace
   force-enable / force-disable that takes precedence over the
   instance default. The override row lives in
   `feature_flag_overrides` keyed by `(flag_id, workspace_id)`.
   OWNER and ADMIN of the target workspace may write it.

### Why two layers in one document

Most operational uses look like: "ship the flag definition with the
codebase, then let individual workspaces opt-in (or stay opted-out)
without round-tripping through the admin console." Splitting the
two concerns into separate kinds would force two YAML files and
two PRs for every flag rollout. Keeping them in one document with
an optional `workspace_override` keeps the surface tight and lets
`crewship apply` reconcile both layers in one phase.

### Instance default vs workspace override — concretely

| `default_enabled` (instance) | `workspace_override` (workspace) | Net runtime check |
|---|---|---|
| `false` | unset (absent in YAML) | `false` — inherit default |
| `true`  | unset | `true` — inherit default |
| `false` | `true`  | `true` — override wins |
| `true`  | `false` | `false` — override wins |

The `workspace_override` field is **pointer-typed** in the Go
struct (`*bool`). That is deliberate: an *absent* field means
"inherit", which is structurally different from "force OFF
(`false`)". Re-applying a manifest that omits the field will
is meant to DELETE any stale override the workspace happened to have.
On `main` it does not (see the limitations above): clear an override
with `crewship feature-flag inherit <key>`.

> **Current limitations on `main`.** The `FeatureFlag` kind parses and
> validates, but `crewship apply` only partly reconciles it today:
>
> - Apply always sends a **create** (`POST`) for the definition. It does
>   not read existing flags first, so applying a manifest for a flag that
>   already exists fails that item with `unexpected status 409`. Use
>   [`crewship feature-flag update`](/cli/feature-flag#crewship-feature-flag-update)
>   to change an existing definition.
> - The create body names the fields `default_enabled` and
>   `default_percentage`, but the server reads `enabled` and `percentage`.
>   A flag created by apply therefore starts with `enabled: false` and
>   `percentage: 0` whatever the manifest says; only `key` and
>   `description` are taken from it. Set the default afterwards with
>   `crewship feature-flag update <key> --enabled`.
> - `workspace_override` set to `true` or `false` is applied (`PUT`
>   override). Leaving it out does **not** remove an existing override; use
>   `crewship feature-flag inherit <key>`.
> - `crewship export` does not emit `FeatureFlag` documents.
> - `percentage` is stored but not evaluated by the server; see
>   [Effective value](/api-reference/feature-flags#effective-value).
>
> The sections below describe the intended behaviour and note where `main`
> differs.

## YAML schema

```yaml
apiVersion: crewship/v1               # required — always crewship/v1 for now
kind: FeatureFlag                     # required — the literal string "FeatureFlag"
metadata:
  name: experimental-llm-cache        # required — human-readable; mirrors slug for readability
  slug: experimental-llm-cache        # required — used as the server's `key` column
  description: ""                     # optional — advisory only; spec.description is the real one
spec:
  description: "Enable LLM cache"     # optional — stored in feature_flags.description
  default_enabled: false              # required — instance-default state when no override
  default_percentage: 0               # required — 0..100, for gradual rollout
  workspace_override: true            # optional — when present, an override row is upserted
                                      # for the current workspace; when absent, any stale
                                      # override is removed.
```

### Field reference

| Field | Type | Required | Notes |
|---|---|---|---|
| `metadata.slug` | string | yes | Becomes the `feature_flags.key` column. Used as the path parameter on every per-flag endpoint. |
| `spec.description` | string | no | Free-form, shown in admin UI / `feature-flag list`. |
| `spec.default_enabled` | bool | yes | Intended instance-wide default. On `main` it is not applied: the server ignores the field name and creates the flag with `enabled: false`. |
| `spec.default_percentage` | int 0..100 | yes | Validation rejects out-of-range values. On `main` it is not applied (created as `0`) and the server does not evaluate percentages anyway. |
| `spec.workspace_override` | *bool | no | Force-enable (`true`) or force-disable (`false`) for the current workspace. Omit to inherit the instance default. |

## Examples

### Minimal — definition only

Defines an instance-global flag with no workspace-specific override.
Every workspace inherits `default_enabled`.

```yaml
apiVersion: crewship/v1
kind: FeatureFlag
metadata:
  name: experimental-llm-cache
  slug: experimental-llm-cache
spec:
  description: "Experimental cross-request LLM response cache."
  default_enabled: false
  default_percentage: 0
```

### Realistic — definition + workspace opt-in

Same flag, but the workspace this manifest applies to wants the
feature ON regardless of the instance default. Two PlanItems are
emitted: POST the definition, then PUT the override. On a re-apply
the POST fails with `409` because the flag exists.

```yaml
apiVersion: crewship/v1
kind: FeatureFlag
metadata:
  name: experimental-llm-cache
  slug: experimental-llm-cache
spec:
  description: "Experimental cross-request LLM response cache."
  default_enabled: false
  default_percentage: 0
  workspace_override: true
```

### Force-disable for a specific workspace

This workspace explicitly opts out — useful for a customer who isn't
ready for the change. The override is applied; `default_enabled: true`
is not (see the limitations above), so turn the instance default on
with `crewship feature-flag update new-issue-search --enabled`.

```yaml
apiVersion: crewship/v1
kind: FeatureFlag
metadata:
  name: new-issue-search
  slug: new-issue-search
spec:
  description: "FTS5-backed issue search."
  default_enabled: true
  default_percentage: 100
  workspace_override: false
```

### Clear an existing override

Intended: drop the `workspace_override:` line and re-apply, and the
plan removes the override. On `main` this emits nothing for the
override (and the definition POST fails with `409`), so run
`crewship feature-flag inherit experimental-llm-cache` instead.

```yaml
apiVersion: crewship/v1
kind: FeatureFlag
metadata:
  name: experimental-llm-cache
  slug: experimental-llm-cache
spec:
  description: "Experimental cross-request LLM response cache."
  default_enabled: false
  default_percentage: 0
  # workspace_override removed — on main this does NOT delete the override
```

## CLI reference

```
crewship feature-flag list                List every flag, its default, this workspace's override and the effective value.
crewship feature-flag create --key <key>  Create a definition (instance administrator).
crewship feature-flag update <key>        Change description / --enabled / --percentage of a definition.
crewship feature-flag delete <key>        Delete a definition and every workspace's override.
crewship feature-flag enable <key>        PUT override enabled: true for the current workspace.
crewship feature-flag disable <key>       PUT override enabled: false.
crewship feature-flag inherit <key>       Drop this workspace's override and revert to the instance default.
```

The alias `crewship flag` works for every subcommand. The override
subcommands act on the selected workspace: pass the global
`--workspace` / `-w` flag, set `CREWSHIP_WORKSPACE`, or run
`crewship workspace use`. Full reference: [`crewship feature-flag`](/cli/feature-flag).

## REST endpoint mapping

| Concern | Endpoint | What apply sends on `main` |
|---|---|---|
| Create definition | `POST /api/v1/feature-flags` | `key` ← `metadata.slug`, `description` ← `spec.description`, plus `default_enabled` / `default_percentage`, which the server ignores (it reads `enabled` / `percentage`) |
| Upsert override | `PUT /api/v1/feature-flags/{key}/override` | `enabled` ← `spec.workspace_override`, only when the field is set |

The kind also contains update (`PATCH`) and remove-override (`DELETE
…/override`) steps, but they only run when apply knows the remote
state, which it does not fetch on `main`. The server's response fields
are `enabled`, `percentage` and `override_enabled`; see the
[API reference](/api-reference/feature-flags).

DB columns: `feature_flags(key, description, enabled, percentage)` +
`feature_flag_overrides(flag_id, workspace_id, enabled)`. See
`internal/database/migrate_consts_v01_init.go` for the schema.

## Validation rules

- `metadata.slug` is required (used as the server-side `key`). It may be
  1-256 characters and must not contain whitespace, control characters,
  `/`, `?` or `#`.
- `spec.default_percentage` MUST be in the closed range `[0, 100]`.
  Out-of-range values fail validation at parse time, before any
  REST call.
- `spec.workspace_override`, when present, must be a YAML boolean
  (`true` / `false`). Pointer typing means "field omitted" is a
  third valid state and not a validation failure.
- Two `FeatureFlag` documents with the same slug in one bundle are
  rejected.
- No cross-kind FK references — FeatureFlag is a leaf kind in the
  dependency graph.

## Apply behavior

On `main`, every apply mode plans the same items for a `FeatureFlag`
document:

- one `POST /api/v1/feature-flags` (create) for the definition, and
- one `PUT …/override` when `workspace_override` is set.

The create runs before the override, so a first apply of a new flag
with an override succeeds. When the flag already exists, the create
item fails with `unexpected status 409`. `--strict` and `--replace`
do not change this: replace mode does not delete and recreate flag
definitions.

`default_percentage` is deliberately left out of the definition diff
the kind computes when it knows the remote state, so that runtime
tuning is not reset by a re-apply. Because apply does not fetch the
remote state on `main`, that diff does not run today.

## Round-trip via export

`crewship export` does not include `FeatureFlag` documents on `main`.
The kind has an export helper, but no export command calls it. To
capture flags, use `crewship feature-flag list --format yaml`.

## See also

- [InstanceSetting](/manifest/instance_setting) — the other admin-only
  kind in the manifest; same dual-layer pattern but for
  configuration key/value pairs instead of feature toggles.
- SPEC-2 §9 (`.claude/context/specs/SPEC-2-manifest-complete.md`) —
  the authoritative implementation contract.
