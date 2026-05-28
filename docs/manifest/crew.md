# kind: Crew

## What it is

`kind: Crew` is the full-CRUD authoring surface for one crew: Create on
first apply, Update on drift, Unchanged when the declared spec already
matches the server. Where [`CrewTemplate`](./crew_template.md) is a
one-shot *deploy* of a server-side blueprint (no in-place updates),
`kind: Crew` owns every field of the crew row directly — runtime image,
devcontainer overlay, mise toolchain, and sidecar services.

Both kinds write to the same `crews` table; the difference is
provenance and lifecycle. An exported workspace round-trips through
`kind: Crew` for crews whose slug doesn't match a known template (the
common case, since operators usually rename on deploy).

The kind is implemented in `internal/manifest/kinds/crew.go`.

> **Agents are separate documents.** A standalone `kind: Crew`
> document defines only the crew row (and its sidecars / container
> config). Its agents are authored either as standalone
> [`kind: Agent`](./agent.md) documents that reference this crew by
> `crew_slug`, or inline under a [`kind: Workspace`](./workspace.md)
> bundle's nested crew shape. The standalone Crew document does **not**
> carry an `agents:` list.

## YAML schema

```yaml
apiVersion: crewship/v1        # required — always crewship/v1 for now
kind: Crew                     # required — the literal string "Crew"
metadata:
  name: Code Review            # required — display name
  slug: code-review            # required — kebab-case, workspace-unique
  description: ""              # optional — mirrored to crews.description
spec:
  description: ""              # optional — alternative to metadata.description
  icon: git-pull-request       # optional — lucide-react slug
  color: "#3B82F6"             # optional — hex #RRGGBB or a palette token ("blue")
  runtime_image: mcr.microsoft.com/devcontainers/javascript-node:22-bookworm  # required
  devcontainer:                # optional — devcontainer.json overlay
    features:
      ghcr.io/devcontainers/features/common-utils:2: {}
    env:
      TZ: UTC
    memory_mb: 4096
    cpus: 2
    post_create_command: npm ci
    raw:                       # escape hatch: any unmodeled devcontainer.json key
      remoteUser: node
  mise:                        # optional — .mise.toml overlay
    tools:
      node: "22"
      python: "3.12"
  services:                    # optional — sidecar containers
    - name: postgres
      image: postgres:16
      env:
        POSTGRES_DB: app
      env_refs: [POSTGRES_PASSWORD]
      ports: ["5432"]
      volumes:
        - { name: pg-data, mount: /var/lib/postgresql/data }
      healthcheck:
        test: ["CMD-SHELL", "pg_isready -U postgres"]
        interval: 5s
        timeout: 3s
        retries: 5
```

### Top-level spec fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `metadata.name` | string | **yes** | Display name. |
| `metadata.slug` | string | **yes** | Kebab-case (`^[a-z0-9][a-z0-9_-]{0,49}$`), workspace-unique. |
| `metadata.description` / `spec.description` | string | no | Mirrored to `crews.description`. When both are set, `metadata.description` wins (the older convention); either lands in the same column. |
| `spec.icon` | string | no | lucide-react slug (e.g. `terminal`, `shield-check`). |
| `spec.color` | string | no | Hex `#RRGGBB` **or** a palette token (`blue`, `green`). A string that starts with `#` but isn't valid hex is rejected. |
| `spec.runtime_image` | string | **yes** | Base image the devcontainer build extends. No sane default exists, so the manifest requires it explicitly. |
| `spec.devcontainer` | object | no | devcontainer.json overlay (see below). |
| `spec.mise` | object | no | `.mise.toml` overlay (see below). |
| `spec.services` | []object | no | Sidecar containers (see below). `services: []` clears all sidecars; an absent key leaves them alone. |

### `spec.devcontainer`

Models the subset of `devcontainer.json` operators commonly tweak;
unmodeled keys pass through via `raw:` (typed fields win on collision).

| Field | Type | Notes |
|---|---|---|
| `features` | map | feature-id → feature-config (OCI ref → JSON). |
| `env` | map[string]string | static container env (emitted as `containerEnv`). |
| `memory_mb` | int | container memory; emitted as `hostRequirements.memory` and forwarded to the `container_memory_mb` column. Non-negative. |
| `cpus` | float | container CPUs; emitted as `hostRequirements.cpus` and forwarded to `container_cpus`. Non-negative. |
| `post_create_command` | string | shell snippet run once after first build. |
| `raw` | map | passthrough for any unmodeled key (e.g. `remoteUser`, `customizations`). |

> `spec.runtime_image` is the canonical image. The legacy
> `spec.devcontainer.image` is the same thing — set **one only**; a
> divergent pair is rejected as a paste mistake.

### `spec.mise`

| Field | Type | Notes |
|---|---|---|
| `tools` | map[string]string | tool → version pin (e.g. `node: "22"`). |
| `raw` | map | passthrough for unmodeled mise config (e.g. `env`, `tasks`). |

### `spec.services[]`

Each entry is one sidecar on the crew's private bridge network. The
wire shape mirrors the server's `serviceWire`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `name` | string | **yes** | DNS label (lowercase letters/digits/`-`, start with a letter), unique within the crew. Agents reach the service as `name:port`. |
| `image` | string | **yes** | Container image. |
| `command` | []string | no | Override the image entrypoint/cmd. |
| `env` | map[string]string | no | Literal env vars. |
| `env_refs` | []string | no | Credential env-var names the Keeper resolves at start. |
| `ports` | []string | no | Numeric ports (`"5432"` or `"5432/tcp"`). Host:container mappings are rejected — the crew network is private. |
| `volumes` | []object | no | Named volumes `{ name, mount }`. Bind mounts (path-like names) are rejected; mounts must be unique. |
| `healthcheck` | object | no | Docker healthcheck `{ test, interval, timeout, retries, start_period }`. Duration strings must parse via Go's `time.ParseDuration` ("5s", "1m"); `retries` non-negative. |

## Examples

### Minimal

```yaml
apiVersion: crewship/v1
kind: Crew
metadata:
  name: Code Review
  slug: code-review
spec:
  runtime_image: mcr.microsoft.com/devcontainers/javascript-node:22-bookworm
```

### With devcontainer sizing + mise toolchain

```yaml
apiVersion: crewship/v1
kind: Crew
metadata:
  name: Data Platform
  slug: data-platform
spec:
  icon: database
  color: "#10B981"
  runtime_image: mcr.microsoft.com/devcontainers/python:3.12
  devcontainer:
    memory_mb: 8192
    cpus: 4
    post_create_command: pip install -r requirements.txt
  mise:
    tools:
      python: "3.12"
      node: "22"
```

### With a Postgres sidecar

```yaml
apiVersion: crewship/v1
kind: Crew
metadata:
  name: Backend
  slug: backend
spec:
  runtime_image: mcr.microsoft.com/devcontainers/javascript-node:22-bookworm
  services:
    - name: postgres
      image: postgres:16
      env:
        POSTGRES_DB: app
        POSTGRES_USER: app
      env_refs: [POSTGRES_PASSWORD]
      ports: ["5432"]
      volumes:
        - { name: pg-data, mount: /var/lib/postgresql/data }
      healthcheck:
        test: ["CMD-SHELL", "pg_isready -U app"]
        interval: 5s
        retries: 5
```

## CLI reference

| Command | Description |
|---|---|
| `crewship crew delete <slug>` | Imperative delete (also used to free a slug before re-deploying a template). |
| `crewship apply --file crew.yaml` | Declarative upsert (Create / Update / Unchanged). |
| `crewship apply --dir ./manifests/` | Walk a directory; crews + agents run before projects/labels in topo order. |
| `crewship export workspace` | Round-trip — emits one `kind: Crew` document per crew (see "Round-trip via export"). |
| `crewship export crew <slug>` | Export just one crew + everything labelled `crew: <slug>`. |

## REST endpoint mapping

| Manifest field | POST/PATCH body field | Notes |
|---|---|---|
| `metadata.name` | `name` | |
| `metadata.slug` | `slug` | |
| `metadata`/`spec.description` | `description` | |
| `spec.color` | `color` | |
| `spec.icon` | `icon` | |
| `spec.runtime_image` | `runtime_image` | |
| `spec.devcontainer` | `devcontainer_config` | JSON string (validated by `devcontainer.ParseBytes` server-side). |
| `spec.devcontainer.memory_mb` | `container_memory_mb` | Also written to the row column. |
| `spec.devcontainer.cpus` | `container_cpus` | Also written to the row column. |
| `spec.mise` | `mise_config` | JSON string. |
| `spec.services` | `services_json` | JSON array of service objects. |

Endpoints used:

| Verb | Path | Action |
|---|---|---|
| `POST` | `/api/v1/crews` | Create |
| `PATCH` | `/api/v1/crews/{crewId}` | Update |
| `DELETE` | `/api/v1/crews/{crewId}` | Delete |
| `GET` | `/api/v1/crews` | List (drift + export) |

## Validation rules

`CrewDocument.Validate` (offline) enforces:

- `apiVersion` / `kind`, when set, equal `crewship/v1` / `Crew`.
- `metadata.name` and `metadata.slug` are non-empty; slug is
  kebab-case.
- `spec.color`, when it starts with `#`, is a valid 6-digit hex code.
- **`spec.runtime_image` is required** (no sane default; the server
  tolerates NULL but falls back to a wrong-for-you built-in image).
- `spec.devcontainer.image`, when set, must equal `spec.runtime_image`.
- `devcontainer.memory_mb` / `cpus` are non-negative.
- Each service: DNS-label name, unique within the crew, image present,
  numeric ports only, parseable healthcheck durations, named (not
  bind) volumes with unique mounts.

The assembled devcontainer / mise JSON is **not** round-tripped
through the server's parser at Validate time (Validate runs offline);
the server re-validates on every write, so any deeper schema error
surfaces at Apply.

## Apply behavior

### ApplyUpsert (default)

- Remote missing → `ActionCreate`: POST the full body.
- Remote present, fields drift → `ActionUpdate`: a **sparse PATCH** of
  only the drifted fields. Empty declared scalars and nil
  devcontainer/mise/services are skipped — so the manifest never
  overwrites a value the operator set via the UI.
- Remote present, no drift → `ActionUnchanged`.

`devcontainer_config` / `mise_config` / `services_json` are compared
after JSON normalisation, so server-side key reordering doesn't trigger
phantom drift. `services: []` (empty array) clears all sidecars; an
absent `services:` key leaves them alone.

## Round-trip via export

`crewship export workspace` calls `ExportCrews`, which renders one
`kind: Crew` per crew (sorted by slug). The devcontainer / mise JSON is
decoded back into the typed sub-fields where possible, with anything
unmodeled stashed under `raw:` so the round-trip stays byte-stable.
Columns the manifest doesn't model (cached_image, config_hash,
container_ttl_hours, network_mode) are dropped.

## See also

- [Agent](./agent.md) — references this crew via `spec.crew_slug`.
- [CrewTemplate](./crew_template.md) — one-shot deploy of a server blueprint.
- [Integration](./integration.md) — crew-scoped MCP servers.
- [Workspace](./workspace.md) — the top-level bundle that nests crews + agents.
- Backend: `internal/api/crews_create.go`, `internal/api/crews_update.go`, `internal/api/crew_services.go`.
- This kind's Go implementation: `internal/manifest/kinds/crew.go`.
