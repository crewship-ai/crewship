# kind: FeatureFlag

## What it is

`kind: FeatureFlag` declares a runtime toggle that Crewship code
checks at request time. A flag has two layers and the manifest can
write to both in a single document:

1. The **instance-global definition** — name (`key`), description,
   the default `enabled` state, and an optional `default_percentage`
   for gradual-rollout flags. This row lives in the `feature_flags`
   table and is shared by every workspace on the instance. Only
   ADMIN may create, update, or delete it.

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
DELETE any stale override the workspace happened to have — that's
how operators clear an override and return to the instance default.

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
| `spec.default_enabled` | bool | yes | Instance-wide default. Only honored on create; runtime toggles via the admin UI win on re-apply (see "Apply behavior"). |
| `spec.default_percentage` | int 0..100 | yes | Gradual rollout knob. Validation rejects out-of-range values. **Not** diffed on update — see "Apply behavior". |
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
feature ON regardless of the instance default. Two PlanItems will
be emitted on first apply: POST the definition, then PUT the
override.

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

The instance default is `true`, but this workspace explicitly opts
out — useful for a customer who isn't ready for the change.

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

Drop the `workspace_override:` line entirely and re-apply. The plan
emits one `ActionDelete` against
`DELETE /api/v1/feature-flags/<key>/override`. The flag definition
itself is untouched.

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
  # workspace_override removed → existing override row will be deleted
```

## CLI reference

```
crewship feature-flag list
    List every flag + this workspace's effective override state.

crewship feature-flag enable <key>
    Shortcut: PUT override with enabled: true for the current workspace.

crewship feature-flag disable <key>
    Shortcut: PUT override with enabled: false.

crewship feature-flag override <key> --workspace <ws-slug> --enabled <bool>
    Admin form: target an arbitrary workspace by slug instead of the current one.
```

For full CRUD (creating new flag definitions, deleting flags) use
`crewship apply --file flag.yaml`. The admin CLI intentionally
does NOT expose `feature-flag create` / `delete` — flag definitions
are a versioned codebase concern and should land via a reviewed
manifest, not an ad-hoc shell command.

## REST endpoint mapping

| Concern | Endpoint | Manifest field → body field |
|---|---|---|
| List | `GET /api/v1/feature-flags` | (response) `key`, `description`, `default_enabled`, `default_percentage`, `workspace_override` |
| Create definition | `POST /api/v1/feature-flags` | `metadata.slug` → `key`; `spec.description` → `description`; `spec.default_enabled` → `default_enabled`; `spec.default_percentage` → `default_percentage` |
| Update definition | `PATCH /api/v1/feature-flags/{key}` | `spec.description` → `description`; `spec.default_enabled` → `default_enabled` (NOT `default_percentage`) |
| Delete definition | `DELETE /api/v1/feature-flags/{key}` | — (only used by `ApplyReplace`) |
| Upsert override | `PUT /api/v1/feature-flags/{key}/override` | `spec.workspace_override` → `enabled` |
| Remove override | `DELETE /api/v1/feature-flags/{key}/override` | — (emitted when `workspace_override` is absent but an override row exists) |

DB columns: `feature_flags(key, description, enabled, percentage)` +
`feature_flag_overrides(flag_id, workspace_id, enabled)`. See
`internal/database/migrate_consts_v01_init.go` for the schema.

## Validation rules

- `metadata.slug` is required (used as the server-side `key`).
- `spec.default_percentage` MUST be in the closed range `[0, 100]`.
  Out-of-range values fail validation at parse time, before any
  REST call.
- `spec.workspace_override`, when present, must be a YAML boolean
  (`true` / `false`). Pointer typing means "field omitted" is a
  third valid state and not a validation failure.
- No cross-kind FK references — FeatureFlag is a leaf kind in the
  dependency graph. The `WorkspaceContext` argument to `Validate`
  is accepted for signature uniformity but ignored.

## Apply behavior

### Default mode (Upsert)

Plan() emits one PlanItem per drifted concern, with a maximum of
two items per flag:

- Flag definition missing remotely → `ActionCreate` (POST).
- Flag definition present but `description` or `default_enabled`
  differ → `ActionUpdate` (PATCH).
- Flag definition present and identical → no item.
- `workspace_override` set in YAML and differs from remote (or
  remote has no override) → `ActionUpdate` (PUT override).
- `workspace_override` absent in YAML and remote has an override
  row → `ActionDelete` (DELETE override).
- `workspace_override` absent both sides → no item.

When the flag is being created AND the manifest sets an override,
the POST runs first and the PUT second (declaration order); the
override endpoint requires the flag to already exist.

**`default_percentage` is deliberately excluded from the update
diff.** Percentage rollouts are typically tuned at runtime by ops
("bump to 25%"); re-applying a manifest that hard-codes the
original `0` would fight the live system on every CI run. The
field is still honored on POST (for first-time bootstrap) and
round-trips via export, but `crewship apply` won't reset it once
the row exists.

### ApplyStrict

Fails with a clear error if the flag definition already exists. Use
this in fresh-instance bootstrap pipelines where any pre-existing
flag is a sign of a previous half-baked apply.

### ApplyReplace

Emits `ActionDelete` for the flag definition first, then
`ActionCreate` to recreate it from the manifest. Workspace
override rows are cascaded-deleted by the foreign key on the
`feature_flags` table, then recreated by the same Plan logic if
the manifest declares an override. This mode is destructive — use
`--yes` to bypass the interactive confirmation only after the dry
run looks right.

## Round-trip via export

`crewship export workspace` includes one `kind: FeatureFlag`
document per flag definition the user can read. The
`workspace_override` field is emitted ONLY when the server reports
an override row for the current workspace (the JSON pointer
`workspace_override` field is non-nil on the response). Pointer
semantics carry through to the YAML: an absent field means
"inherit the default."

Round-trip is lossless for `description`, `default_enabled`,
`default_percentage`, and `workspace_override`. The
`feature_flags.id` column (CUID) is regenerated on a fresh apply
and is not preserved — references between kinds are always by
slug, never by id.

## See also

- [InstanceSetting](/manifest/instance_setting) — the other admin-only
  kind in the manifest; same dual-layer pattern but for
  configuration key/value pairs instead of feature toggles.
- SPEC-2 §9 (`.claude/context/specs/SPEC-2-manifest-complete.md`) —
  the authoritative implementation contract.
