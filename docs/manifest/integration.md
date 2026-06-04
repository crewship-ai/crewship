# kind: Integration

## What it is

`kind: Integration` declares one connected **MCP server** — either a
remote streamable-http endpoint (e.g. Linear's hosted MCP) or a
locally spawned stdio process (e.g. `npx -y @some/mcp-server`). It is
the standalone authoring surface for the case where you want one
integration declared once and shared across many crews
(`scope: workspace`), or want to declare a crew-scoped integration
outside the bulkier `kind: Crew` document.

The legacy inline `mcp_servers:` block nested under a crew (see
[Crew](/manifest/crew) and [Workspace](/manifest/workspace)) still works and
remains the most ergonomic shape for bundling integrations with a crew
definition. `kind: Integration` is the inverse: declare once, scope
explicitly.

The kind is implemented in `internal/manifest/kinds/integration.go`.

### Scope picks the table

A single `scope:` discriminator chooses where the row lands:

| `scope` | Table | Who sees it |
|---|---|---|
| `workspace` (default) | `workspace_mcp_servers` | every crew in the workspace |
| `crew` | `crew_mcp_servers` | only agents of the named crew |

`crew` scope **requires** `crew_slug`; `workspace` scope **rejects**
it (a `crew_slug` under workspace scope is treated as an authoring
mistake and fails loudly).

### slug == name

`metadata.slug` MUST equal `metadata.name` — the server keys MCP-server
uniqueness on `name` within the (workspace, crew) tuple, but every
cross-kind reference uses slug, so the manifest forces the two to
match (the same convention [Label](/manifest/label) uses).

### `env` vs `env_mapping`

Two env-related maps both land in the same `env_json` column:

- **`env`** — plain static environment variables (e.g.
  `NODE_ENV: production`). The value is the literal string the MCP
  process sees.
- **`env_mapping`** — the credential-indirection layer. Keys are the
  env-var the MCP server expects; values are the workspace
  credential's `name` (conventionally identical, but can differ — e.g.
  `{GITHUB_PERSONAL_ACCESS_TOKEN: GH_TOKEN}`). At agent run time the
  resolver looks up each credential by name and substitutes the value
  before the MCP process starts.

On key collision, **`env` wins** (a literal value beats a credential
reference for the same key).

## YAML schema

```yaml
apiVersion: crewship/v1        # required — always crewship/v1 for now
kind: Integration              # required — the literal string "Integration"
metadata:
  name: linear                 # required — MCP server name
  slug: linear                 # required — MUST equal metadata.name
spec:
  scope: workspace             # optional — workspace (default) | crew
  crew_slug: code-review       # required iff scope == crew; rejected otherwise
  display_name: Linear         # optional — defaults to metadata.name
  transport: streamable-http   # required — streamable-http | stdio
  endpoint: https://mcp.linear.app/sse   # required for streamable-http
  command: npx                 # required for stdio
  args: [-y, "@some/mcp-server"]         # stdio-only positional args
  env:                         # optional — literal env vars (win on collision)
    NODE_ENV: production
  env_mapping:                 # optional — credential indirection (key → cred name)
    LINEAR_API_KEY: LINEAR_API_KEY
  icon: linear                 # optional — lucide-react slug
  enabled: true                # optional — runtime connect toggle (default true)
```

### Field reference

| Field | Type | Required | Notes |
|---|---|---|---|
| `metadata.name` | string | **yes** | MCP server name; uniqueness key within (workspace, crew). |
| `metadata.slug` | string | **yes** | MUST equal `metadata.name`. |
| `spec.scope` | enum | no | `workspace` (default) \| `crew`. |
| `spec.crew_slug` | string | cond. | Required when `scope: crew`; must be empty when `scope: workspace`. |
| `spec.display_name` | string | no | UI label. Defaults to `metadata.name` server-side (and the manifest mirrors that default so the round-trip diff is stable). |
| `spec.transport` | enum | **yes** | `streamable-http` \| `stdio`. The server has no sensible default for "what kind of MCP server is this". |
| `spec.endpoint` | string | cond. | Required for `streamable-http`; ignored otherwise. |
| `spec.command` | string | cond. | Required for `stdio`; ignored otherwise. |
| `spec.args` | []string | no | stdio positional args. Marshaled into `args_json`. No empty entries. |
| `spec.env` | map[string]string | no | Literal env vars. No empty keys. |
| `spec.env_mapping` | map[string]string | no | Credential references (env-var → credential name). No empty keys or values. |
| `spec.icon` | string | no | lucide-react slug. |
| `spec.enabled` | bool | no | Runtime connect toggle. Defaults to **true**; pointer-typed so an absent field is distinguishable from `false`. |

## Examples

### Remote (streamable-http), workspace scope

```yaml
apiVersion: crewship/v1
kind: Integration
metadata:
  name: linear
  slug: linear
spec:
  transport: streamable-http
  endpoint: https://mcp.linear.app/sse
  env_mapping:
    LINEAR_API_KEY: LINEAR_API_KEY
  icon: linear
```

### Local (stdio), crew scope

```yaml
apiVersion: crewship/v1
kind: Integration
metadata:
  name: github
  slug: github
spec:
  scope: crew
  crew_slug: code-review
  display_name: GitHub
  transport: stdio
  command: npx
  args: [-y, "@modelcontextprotocol/server-github"]
  env_mapping:
    GITHUB_PERSONAL_ACCESS_TOKEN: GH_TOKEN
```

### Static env plus a credential reference

```yaml
apiVersion: crewship/v1
kind: Integration
metadata:
  name: custom-tool
  slug: custom-tool
spec:
  transport: stdio
  command: node
  args: [./mcp/server.js]
  env:
    NODE_ENV: production       # literal — wins on key collision
  env_mapping:
    API_TOKEN: SERVICE_TOKEN   # resolved from the SERVICE_TOKEN credential
  enabled: true
```

## CLI reference

There is no dedicated `crewship integration` per-kind admin command —
integrations are authored through the manifest pipeline (or installed
as part of a [Connector](/manifest/connector) / [Recipe](/manifest/recipe)). The
relevant CLI surface is the global apply/export flow:

| Command | Description |
|---|---|
| `crewship apply --file integration.yaml` | Declarative create/update (workspace or crew scope). |
| `crewship apply --dir ./manifests/` | Walk a directory; crews resolve before crew-scoped integrations. |
| `crewship apply --file integration.yaml --dry-run` | Plan only — surfaces a dangling `crew_slug` and shows scope-change replaces. |
| `crewship export workspace` | Round-trip — emits one document per integration across both scopes. |

## REST endpoint mapping

| Manifest field | POST/PATCH body field | Notes |
|---|---|---|
| `metadata.name` | `name` | |
| `spec.display_name` | `display_name` | Defaults to name. |
| `spec.transport` | `transport` | |
| `spec.endpoint` | `endpoint` | streamable-http only. |
| `spec.command` | `command` | stdio only. |
| `spec.args` | `args_json` | JSON-encoded string on the wire. |
| `spec.env` + `spec.env_mapping` | `env_json` | Merged map, JSON-encoded string. |
| `spec.icon` | `icon` | |
| `spec.enabled` | `enabled` | |

Endpoints used:

| Verb | Workspace scope | Crew scope |
|---|---|---|
| `POST` | `/api/v1/integrations` | `/api/v1/crews/{crewId}/integrations` |
| `GET` | `/api/v1/integrations` | `/api/v1/crews/{crewId}/integrations` |
| `PATCH` | `/api/v1/integrations/{id}` | `/api/v1/crews/{crewId}/integrations/{id}` |
| `DELETE` | `/api/v1/integrations/{id}` | `/api/v1/crews/{crewId}/integrations/{id}` |

## Validation rules

`IntegrationDocument.Validate` enforces:

- `apiVersion` / `kind`, when set, equal `crewship/v1` / `Integration`.
- `metadata.name` and `metadata.slug` are non-empty, and
  **`slug == name`**.
- `transport` is required and one of `streamable-http` \| `stdio`.
- `streamable-http` requires a non-empty `endpoint`; `stdio` requires a
  non-empty `command`.
- `scope`, when set, is `workspace` \| `crew`.
- `crew_slug` is required iff `scope: crew` and rejected under
  `scope: workspace`.
- `env` / `env_mapping` have no empty keys; `env_mapping` has no empty
  values; `args` has no empty entries.
- When `WorkspaceContext` carries crew data, a crew-scoped `crew_slug`
  must reference a declared or remote crew.

## Apply behavior

### ApplyUpsert (default)

- No remote on this scope → `ActionCreate`: POST to the workspace or
  crew endpoint.
- Remote on the matching scope, fields drift → `ActionUpdate`: a
  **sparse PATCH**. `args_json` / `env_json` are compared after JSON
  normalisation so key-reordering doesn't produce phantom drift.
- Remote matches exactly → `ActionUnchanged`.

### Scope change (workspace ↔ crew)

The two scopes are different tables and there is no "move" endpoint, so
a changed scope emits a **`Delete` + `Create` pair**, both visible in
the dry-run with a "scope change" note. The delete cascades any agent
bindings on the old row — review the plan before re-running with
`--yes`.

## Round-trip via export

`crewship export workspace` calls `ExportIntegrations`, which walks
both the workspace scope and every crew's crew scope, decoding
`args_json` back into `spec.args` and `env_json` back into `spec.env`.

**Env lossiness:** the server has no column distinguishing literal
`env` from credential-reference `env_mapping` (both live in
`env_json`), so **every** entry comes back under `spec.env` on export.
If you need the `env_mapping` shape preserved, keep your source YAML as
the source of truth and re-export to a side file rather than
overwriting the original. Output is sorted by scope, then crew, then
slug for stable diffs.

## See also

- [Crew](/manifest/crew) — can declare integrations inline via `mcp_servers:`.
- [Connector](/manifest/connector) — install-only OAuth connectors (Linear, GitHub, …).
- [Recipe](/manifest/recipe) — catalog installs that may bundle integrations.
- Backend: `internal/api/workspace_integrations.go`, `internal/api/crew_integrations.go`.
- This kind's Go implementation: `internal/manifest/kinds/integration.go`.
