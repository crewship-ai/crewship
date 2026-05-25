# kind: Connector

## What it is

A `Connector` installs an entry from the server-side connector catalog
(Linear, GitHub, Slack, …) into the current workspace. The manifest
**cannot define new connector types** — connector authoring lives in
`internal/connectors/fixtures/*.yaml`, baked into the binary. What the
manifest CAN do is:

- Mark a catalog entry as **installed** in this workspace
  (`spec.install: true`).
- **Bind** the connector's expected runtime env-vars to the names of
  workspace credentials that satisfy them (`spec.credentials`).
- Drift back to **uninstalled** (`spec.install: false`) where the server
  endpoint exposes a delete verb — best-effort; if the catalog entry has
  no DELETE handler the planner degrades to `Unchanged` with a warning.

This makes `Connector` an **install-only reference kind**, alongside
`Recipe` and `CrewTemplate`. The distinction matters when authoring a
manifest:

- Full-CRUD kinds (`Project`, `Label`, `Milestone`, …) let the manifest
  describe shape *and* trigger create/update/delete.
- Reference kinds express **intent only** ("this connector should be
  installed and wired to these credentials"); the underlying schema is
  owned by the server catalog.

The catalog connector id (e.g. `linear`, `github`, `slack`) is the
`metadata.slug`. Apply matches it against `GET /api/v1/connectors`.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: Connector
metadata:
  name: Linear                # informational
  slug: linear                # required — must match a catalog connector id
  description: Issue tracker  # optional
spec:
  install: true               # required — true=install, false=uninstall intent
  credentials:                # required when the catalog declares required_credentials
    # ENV_VAR_NAME the connector consumes : workspace credential `name`
    LINEAR_API_KEY: LINEAR_PROD_KEY
```

### Field reference

| Field | Required | Type | Notes |
|---|---|---|---|
| `apiVersion` | yes | string | Always `crewship/v1`. |
| `kind` | yes | string | Always `Connector`. |
| `metadata.name` | yes | string | Human-facing label. Used in plan output, not by the server. |
| `metadata.slug` | **yes** | string | Must match a connector id from `GET /api/v1/connectors`. |
| `metadata.description` | no | string | Informational only; not forwarded to the install endpoint. |
| `spec.install` | **yes** | bool | `true` = ensure installed. `false` = ensure uninstalled (best-effort). |
| `spec.credentials` | conditional | map[string]string | Required when the catalog entry declares `required_credentials`. Each key is an env-var name the connector reads at runtime; each value is the `name` of a workspace credential whose decrypted value should be wired into that env var. |

## Credential mapping — how to think about it

The mapping is **directional, but both sides are env-var names**.

```yaml
credentials:
  LINEAR_API_KEY: LINEAR_PROD_KEY
  #     ^                ^
  #     |                └── name of a workspace credential (kind: Credential)
  #     |                    declared elsewhere in the manifest or already
  #     |                    present in the workspace credentials table
  #     └── env var the connector's MCP/HTTP runtime reads at request time
```

The **left-hand side** is fixed by the connector — the catalog manifest
declares which env vars the connector binary expects. You can see them
on `GET /api/v1/connectors/{slug}` under `required_credentials`.

The **right-hand side** is yours. Each value names a workspace credential
(a `Credential` kind, or an existing row in `credentials.name`). At
install time the server resolves that name → decrypted value → env-var
injection into the connector's MCP runtime.

**Both halves must be valid POSIX env-var names**
(`^[A-Za-z_][A-Za-z0-9_]*$`). Validate enforces this at parse time so a
typo on either side fails fast rather than blowing up at runtime with a
mysterious "credential not found".

## Examples

### Minimal example — connector with no required credentials

```yaml
apiVersion: crewship/v1
kind: Connector
metadata:
  name: Public Calendar
  slug: public-cal
spec:
  install: true
```

### Realistic example — Linear with an API-key binding

```yaml
apiVersion: crewship/v1
kind: Connector
metadata:
  name: Linear
  slug: linear
  description: Issue tracker for engineering crews
spec:
  install: true
  credentials:
    LINEAR_API_KEY: LINEAR_PROD_KEY
```

`LINEAR_PROD_KEY` here refers to a workspace credential row whose
`name` column equals `LINEAR_PROD_KEY`. You either declare it earlier
in the same manifest (kind: Credential) or it already exists in the
workspace from a previous apply / UI action.

### Cross-kind reference — credential declared in the same bundle

Apply orders Phase 1 (Workspace credentials) before Phase 11
(Connectors), so a credential declared in the same multi-doc manifest
will be on the server by the time the connector install runs.

```yaml
---
apiVersion: crewship/v1
kind: Crew
metadata:
  name: Engineering
  slug: eng
spec:
  credentials:
    - { env: LINEAR_PROD_KEY, provider: NONE, type: API_KEY }
---
apiVersion: crewship/v1
kind: Connector
metadata:
  name: Linear
  slug: linear
spec:
  install: true
  credentials:
    LINEAR_API_KEY: LINEAR_PROD_KEY
```

### Uninstall intent

```yaml
apiVersion: crewship/v1
kind: Connector
metadata:
  name: Linear
  slug: linear
spec:
  install: false
```

The planner issues `DELETE /api/v1/connectors/linear/install`. A
404/405/501 from that endpoint is treated as "catalog entry doesn't
support uninstall" and the plan item is reported as `Unchanged` with a
description flagging the manual cleanup — Apply does not fail on the
remainder of the bundle.

## CLI reference

`Connector` is driven by the generic `crewship apply` / `crewship export`
commands; there is no per-kind `crewship connector …` subcommand because
the install verb is a single-shot operation that the standard manifest
flow already covers end-to-end.

| Command | Purpose |
|---|---|
| `crewship apply --file connectors.yaml` | Install/uninstall connectors per the manifest. |
| `crewship apply --file connectors.yaml --dry-run` | Show the install plan without mutating. |
| `crewship export workspace` | Includes a `kind: Connector` document for every connector currently installed in the workspace. |

For interactive exploration use the existing connectors API directly:

```
GET /api/v1/connectors                  # catalog list
GET /api/v1/connectors/{slug}           # catalog detail + installed state
POST /api/v1/connectors/{slug}/verify   # pre-install credential probe
```

The UI page `/connectors` wraps the same endpoints.

## REST endpoint mapping

| Manifest field | POST body field | Server-side effect |
|---|---|---|
| `metadata.slug` | path param `{connectorId}` | Selects the catalog entry. |
| `spec.credentials` (map) | `{credentials: {ENV_NAME: workspace_cred_name}}` | Server resolves each `workspace_cred_name` to a credential row, decrypts its value, and binds the result to the runtime env var `ENV_NAME` on the connector's MCP/HTTP runtime. |
| `spec.install: true` (and not installed) | — | Apply issues `POST /api/v1/connectors/{slug}/install`. |
| `spec.install: false` (and installed) | — | Apply issues `DELETE /api/v1/connectors/{slug}/install` (best-effort). |

The credential map is wire-equivalent to what the
`POST /api/v1/connectors/{id}/install` handler reads off the request
body. See `internal/api/connectors_handler.go` for the canonical Go
shape.

## Validation rules

These run at parse time, **before any HTTP traffic**:

- `metadata.name` is non-empty.
- `metadata.slug` is non-empty.
- For every entry in `spec.credentials`:
  - The **key** matches `^[A-Za-z_][A-Za-z0-9_]*$` (POSIX env-var name).
  - The **value** matches the same env-var regex.
  - The value is not empty / whitespace-only.

These run at Plan time (one HTTP round-trip):

- `metadata.slug` exists in `GET /api/v1/connectors`. A 404 surfaces as
  `connector slug "X" is not in the catalog` so the user can spot the
  typo immediately.
- Every entry in the catalog entry's `required_credentials` array has
  a corresponding key in `spec.credentials`. Missing entries produce an
  `Action=Create` plan item whose `Exec` returns an error before any
  HTTP call (so dry-run sees the failure too).

These run inside the install `Exec` (defer-failure semantics so
credentials declared earlier in the same bundle have a chance to land
in Phase 1):

- Every workspace credential name on the right-hand side of
  `spec.credentials` exists in `GET /api/v1/credentials`. Missing names
  abort the install before the POST goes out, with an error that names
  the missing credential.

## Apply behavior

The kind plans against the catalog detail and the workspace credential
list to decide one of four outcomes:

| `spec.install` | Remote `installed` | Action | What runs |
|---|---|---|---|
| `true` | `false` | `Create` | `POST /api/v1/connectors/{slug}/install` with `{credentials: spec.credentials}`. |
| `true` | `true` | `Unchanged` | Nothing. The plan item carries a "already installed" description. |
| `false` | `true` | `Delete` | `DELETE /api/v1/connectors/{slug}/install`. Endpoint 404/405/501 degrades to `Unchanged` with a warning. |
| `false` | `false` | `Unchanged` | Nothing. |

**ApplyUpsert** (default): runs the table above.

**ApplyStrict**: same as upsert. Connector installs are idempotent at
the catalog level; `Strict` doesn't add an "already-installed = error"
arm because the install endpoint itself is idempotent and connector
catalog entries are not workspace-unique resources.

**ApplyReplace**: connectors do not participate in the
delete-then-recreate pass. The install is already a "force overwrite"
verb at the server level, so a `Replace` on a connector entry just
re-runs the `Create` branch.

## Round-trip via export

`crewship export workspace` walks `GET /api/v1/connectors` and emits
one `kind: Connector` document for every entry whose `installed=true`.
Uninstalled catalog entries are dropped — emitting them as
`install: false` would explode the bundle by one doc per global
catalog connector with no useful state preserved.

The credential mapping is exported **empty** today (`credentials: {}`)
because the current server does not echo the per-install mapping back
in the GET response. The exported document round-trips structurally
(same slug, same install flag); re-applying it without re-filling the
credential map is a no-op when the connector is already installed and
an error when it is not. Future server work that surfaces the binding
back through the API will make the round-trip lossless without a YAML
schema change.

## See also

- [kind: Recipe](/manifest/recipe) — sister install-only reference kind.
- [kind: CrewTemplate](/manifest/crew_template) — deploys a packaged crew
  rather than wiring a single integration.
- [Your First Crew](/guides/first-crew) — defines workspace credentials a
  connector can bind to.
- Backend handler: `internal/api/connectors_handler.go`.
- Catalog fixtures: `internal/connectors/fixtures/*.yaml`.
