# Connector manifests

This package owns the **catalog** — the curated set of integrations a
Crewship workspace can install. A connector manifest is a YAML file
describing how to authenticate against a third-party service and how
to spawn the matching MCP server once credentials are in hand.

The package is split deliberately:

| File | Responsibility |
|---|---|
| `manifest.go` | Types, sentinel errors, `ParseManifest`, `Validate`, `Resolve`, `MaterializeMCP` |
| `loader.go` | `Catalog`, `LoadAll`, `LoadByID` — walks an `fs.FS` of manifests |
| `fixtures/*.yaml` | The shipped catalog (also embedded via `embed.FS`) |

Frontend forms render directly from a manifest; backend handlers
dispatch on `auth_mode`. There is **no per-connector code** — adding
a new integration is one YAML file.

---

## Auth modes

Five mutually exclusive modes. The frontend renders a different shape
for each; the backend installs each through a different code path.

| Mode | Used by | UX | Backend flow |
|---|---|---|---|
| `mcp_oauth` | MCP servers that implement RFC 7591 DCR (Linear, Anthropic Connectors) | One Connect button; provider runs the consent | Discover server metadata → DCR → consent flow |
| `pat` | Most APIs (GitHub, OpenAI, Slack bot, Brave, Sentry, Stripe, …) | Paste a token | Verify with the manifest's `verify.http` block, encrypt, store |
| `conn_string` | Databases (Postgres, MySQL, Mongo, Snowflake) | Form: host/port/db/user/password/ssl | Combine via `derived` template, pass as MCP arg |
| `byo_oauth` | OAuth providers without DCR when no broker exists | Customer registers their own OAuth app, pastes client_id/secret | Local OAuth dance, redirect via `${instance_url}/oauth/callback` |
| `none` | Public/demo MCP servers (`server-everything`, `server-filesystem`) | Single Connect button | No credential stored |

Choose `pat` over `byo_oauth` whenever the MCP server supports a token
on env (most do via the bot-token pattern). `byo_oauth` is reserved
for cases where the provider's OAuth dance must happen in-product.

---

## Manifest schema

```yaml
id: github                      # required, must match ^[a-z][a-z0-9_-]*$
name: GitHub                    # required, user-facing
description: Code repos & PRs   # one-line tile copy
category: dev_tools             # free-form bucket; UI groups by category
brand:
  logo: github                  # key into components/icons/mcp-logos
  color: "#181717"              # 6-digit hex; empty = neutral chrome
auth_mode: pat                  # one of the five modes above

# Form fields the UI renders. Required for pat / conn_string /
# byo_oauth; empty/omitted for mcp_oauth and (typically) none.
fields:
  - key: pat                    # used in ${field.<key>} placeholders
    label: Personal Access Token
    type: password              # text|password|number|select|bool
    required: true
    default: ""
    placeholder: ghp_...
    help: |
      Markdown allowed.
    choices: []                 # required when type=select

# Only when auth_mode=byo_oauth.
oauth:
  authorization_url: https://provider.example/oauth/authorize
  token_url: https://provider.example/oauth/token
  scopes: [read, write]
  pkce: true

# How to launch the MCP server. Strings may contain
# ${field.X}, ${derived.Y}, or ${instance_url} placeholders;
# they are resolved at materialize-time.
mcp:
  transport: stdio              # stdio | streamable-http
  command: npx                  # stdio
  args:
    - "-y"
    - "@modelcontextprotocol/server-github"
  endpoint: ""                  # streamable-http only
  env:
    GITHUB_PERSONAL_ACCESS_TOKEN: "${field.pat}"

# Optional: derived values built from fields, referenced via
# ${derived.<key>} elsewhere in mcp/verify. Resolved transitively.
derived:
  dsn: "postgres://${field.user}:${field.password}@${field.host}:${field.port}/${field.database}"

# Optional: pre-install probe. Either http (PAT-style auth check) or
# mcp_method (call after spawn to confirm the server reachable).
verify:
  http:
    method: GET
    url: https://api.github.com/user
    headers:
      Authorization: "Bearer ${field.pat}"
    expect_status: 200
  # OR:
  # mcp_method: tools/list

# Optional: markdown rendered in the connect sheet. Useful for
# byo_oauth setup instructions; the frontend resolves
# ${instance_url} for you.
docs:
  setup_md: |
    ## Connect GitHub
    1. Open https://github.com/settings/personal-access-tokens/new
    2. Add `repo` and `read:org` scopes
    3. Paste the token below.
```

### Placeholder grammar

Three sources, resolved by `Manifest.Resolve` and `Manifest.MaterializeMCP`:

| Form | Source | Example |
|---|---|---|
| `${field.X}` | User-submitted form value for field key X | `Bearer ${field.pat}` |
| `${derived.Y}` | Computed from `derived` map; can reference field/derived/instance_url | `connect ${derived.dsn}` |
| `${instance_url}` | Customer's Crewship base URL (e.g. `https://acme.example.com:8080`) | `${instance_url}/oauth/callback` |

Unknown keys, malformed placeholders, or missing required field values
return `ErrManifestPlaceholder` / `ErrManifestMissingFieldVal` — the
caller surfaces them rather than leaking literal `${…}` strings into
spawned processes.

---

## Validation rules (per `Validate()`)

Universal:

- `id` matches `IDPattern` (`^[a-z][a-z0-9_-]*$`)
- `name` non-empty
- `auth_mode` is one of the five const values
- `mcp.transport` is `stdio` or `streamable-http`
- `brand.color` is empty OR a 6-digit hex (`#RRGGBB`)
- Each `fields[].key` declared at most once
- Each `fields[].type` recognized; `type: select` requires non-empty `choices`

Per-mode:

- **mcp_oauth**: `transport == streamable-http`, `endpoint` set, no fields.
- **pat**: at least one field with `type: password`.
- **conn_string**: at least one field of any type.
- **byo_oauth**: `oauth` block present (with `authorization_url` + `token_url`); fields include both `client_id` and `client_secret`.
- **none**: no extra constraints.

Sentinel errors (use `errors.Is`, not message text):

```
ErrManifestEmpty             ErrManifestMissingID
ErrManifestInvalidID         ErrManifestMissingName
ErrManifestUnknownAuthMode   ErrManifestMissingField
ErrManifestInvalidTransport  ErrManifestMissingOAuth
ErrManifestPlaceholder       ErrManifestMissingFieldVal
ErrManifestDuplicateField    ErrManifestInvalidColor
ErrManifestEmptyChoices      ErrManifestMissingType
ErrManifestUnknownFieldType
```

---

## Authoring a new connector

1. Pick the mode (table at top).
2. Pick a real, shipping MCP server. The Crewship policy is
   "no dead bodies" — fixtures must reference packages or endpoints
   that actually exist. Common sources:
   - `@modelcontextprotocol/server-*` (official reference impls)
   - Provider-hosted remote MCP (e.g. `https://mcp.linear.app/mcp`)
3. Drop the YAML into `fixtures/`.
4. Run `crewship connector validate fixtures/<your>.yaml` (or
   `crewship connector lint fixtures/`) to confirm the schema is
   correct without booting the server.
5. Add a fixture-shape unit test in `manifest_test.go` if the
   connector exercises a previously-untested path (e.g. first user
   of a new auth_mode flavor).

`crewship connector lint` walks every `*.yaml` in a directory and
runs Validate; ship it in CI for community-contributed manifests.

---

## Why catalog ≠ installed instance

The catalog (this package) is read-only: manifests are embedded at
build time and live in code, not the database. Editing the catalog
requires a binary release.

Installing one creates a row in `workspace_mcp_servers` (or
`crew_mcp_servers`) with `connector_id` pointing at the manifest.
That row carries instance state — credential, agent bindings,
last-tested timestamp — and is the long-lived object users see in
the dashboard.

Migrating an instance forward when the underlying manifest changes
(rename, new required field, etc.) is an application-layer concern
and intentionally out of scope for the manifest format.
