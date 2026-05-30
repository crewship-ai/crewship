# kind: Recipe

## What it is

A `Recipe` is a server-curated 1-click bundle (crew + MCP servers + credentials) that ships baked into the Crewship binary under `internal/recipes`. The manifest can request that a recipe be **installed** into the current workspace, but it **cannot create, edit, or define new recipes** — authoring belongs in server source code, not in user YAML.

This makes `Recipe` an **install-only reference kind**, in contrast to the full-CRUD kinds (`Project`, `Label`, `Milestone`, etc.). The distinction matters when you're writing manifests:

- Full-CRUD kinds let the manifest describe shape *and* trigger creation/updates/deletion of the entity itself.
- Reference kinds only express **intent** ("this recipe should be installed in this workspace"); the underlying entity is owned by the server.

Recipes are identified by `metadata.slug`, which must match a recipe slug from the server catalog (`GET /api/v1/recipes`). Installs are not declarative in the same way as Projects — the server may also create credentials, crews, and MCP servers as side-effects of `POST /api/v1/recipes/{slug}/install`, and those side-effects appear as their own rows once installed.

> **Heads-up on uninstall.** The backend currently exposes **no DELETE endpoint** for installed recipes. The manifest still accepts `install: false` so the YAML shape stays symmetrical, but on encountering an already-installed recipe with `install: false` the planner emits an `Unchanged` plan item carrying a warning description (`recipe uninstall not supported by server`) rather than a `Delete`. Future server versions may add a DELETE verb, at which point this kind upgrades without a YAML schema change.

## YAML schema

```yaml
apiVersion: crewship/v1
kind: Recipe
metadata:
  name: Code review pipeline       # informational; the slug is authoritative
  slug: code-review                # required — must match an entry in GET /api/v1/recipes
spec:
  install: true                    # required — true=install, false=uninstall intent
  inputs:                          # optional — passthrough body forwarded to the install endpoint
    credential_values:             # canonical key consumed by the backend installer today
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
      GH_TOKEN: ${GH_TOKEN}
    account_labels:
      ANTHROPIC_API_KEY: "Workspace default"
    crew_slug: my-crew             # optional — recipe-specific knob; validated against declared crews
```

### Field reference

| Field | Required | Type | Notes |
|---|---|---|---|
| `apiVersion` | yes | string | Always `crewship/v1`. |
| `kind` | yes | string | Always `Recipe`. |
| `metadata.name` | no | string | Informational. The server identifies recipes by slug, not by name. |
| `metadata.slug` | **yes** | string | Must match a recipe in the server catalog (`GET /api/v1/recipes`). |
| `metadata.description` | no | string | Informational only; not forwarded to the server. |
| `spec.install` | **yes** | bool | `true` = ensure installed. `false` = ensure uninstalled (best-effort; see Apply behavior). |
| `spec.inputs` | no | map[string]any | Passthrough body for the install endpoint. Keys depend on the backend's `installRecipeRequest` shape; the manifest does not validate them beyond `crew_slug`. |
| `spec.inputs.crew_slug` | no | string | If present, the slug must reference a declared or remote `Crew`. |

## Examples

### Minimal example — install with no extra inputs

```yaml
apiVersion: crewship/v1
kind: Recipe
metadata:
  slug: code-review
spec:
  install: true
```

### Realistic example — install with credential values and a target crew

```yaml
apiVersion: crewship/v1
kind: Recipe
metadata:
  name: Code review pipeline
  slug: code-review
spec:
  install: true
  inputs:
    credential_values:
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
      GH_TOKEN: ${GH_TOKEN}
    account_labels:
      ANTHROPIC_API_KEY: "Engineering default"
      GH_TOKEN: "Crewship CI"
    crew_slug: my-crew
```

### Uninstall intent — recorded but not executed today

```yaml
apiVersion: crewship/v1
kind: Recipe
metadata:
  slug: code-review
spec:
  install: false
```

The planner records intent and emits a warning ("recipe uninstall not supported by server") on dry-run and apply summaries. No mutation is performed — the recipe and its installed entities (crew, MCP servers, credentials) remain in the workspace.

### Cross-kind references

A recipe install often pairs with a crew defined elsewhere in the same bundle. Apply orders Phase 2 (Crews + agents) before Phase 9 (Recipes), so the crew slug referenced in `inputs.crew_slug` will exist on the server by the time the recipe plan runs.

```yaml
---
apiVersion: crewship/v1
kind: Crew
metadata: { name: My Crew, slug: my-crew }
spec:
  agents:
    - { slug: alice, name: Alice, agent_role: LEAD, prompt: "..." }
---
apiVersion: crewship/v1
kind: Recipe
metadata: { slug: code-review }
spec:
  install: true
  inputs:
    crew_slug: my-crew
```

## CLI reference

`Recipe` is driven by the generic `crewship apply` / `crewship export` commands; there is no per-kind `crewship recipe …` subcommand today. The endpoints below back the manifest plan/apply machinery; raw HTTP callers can hit them directly.

| Command | One-liner |
|---|---|
| `crewship apply --file <file>` | Declarative apply — installs every `Recipe` doc whose `spec.install: true` is not yet realised. |
| `crewship apply --file <file> --dry-run` | Show the install plan without mutating. |
| `crewship export workspace` | Emits one `Recipe` doc per installed recipe (see "Round-trip via export"). |

## REST endpoint mapping

| Manifest field | HTTP verb + path | Request body |
|---|---|---|
| `spec.install: true` (not installed) | `POST /api/v1/recipes/{slug}/install` | `spec.inputs` forwarded verbatim |
| `spec.install: true` (already installed) | — | No request issued; plan is `Unchanged` |
| `spec.install: false` (installed) | — | No request issued; plan is `Unchanged` with warning |
| `spec.install: false` (not installed) | — | No request issued; plan is `Unchanged` |
| Drift detection | `GET /api/v1/recipes/{slug}` | The `installed` JSON key on the response controls Plan branch |
| Export | `GET /api/v1/recipes` | Only entries with `installed: true` are emitted |

There is no DB column that uniquely maps to `spec.install`; "installed" is a derived state on the server (typically: "a crew matching the recipe's `crew_slug` exists in the workspace and has the recipe's MCP servers"). The manifest treats the boolean as opaque and trusts the server's answer.

## Validation rules

`Validate` (offline, no HTTP) enforces:

- `metadata.slug` is non-empty.
- `spec.inputs.crew_slug`, when present, must be a string.
- `spec.inputs.crew_slug`, when present and non-empty, must reference a crew that's either declared earlier in the bundle or already on the server.

Catalog membership (`metadata.slug` actually exists in `GET /api/v1/recipes`) is **not** checked at validate time — the `WorkspaceContext` doesn't carry remote recipes today, so the check is deferred to `Plan`, which has live HTTP access. A missing slug surfaces as a 404 from `LookupRecipeRemote` → a transparent "recipe not found" plan error.

## Apply behavior

### Default (`ApplyUpsert`)

Plan decision table:

| `spec.install` | remote installed? | Action | Notes |
|---|---|---|---|
| `true` | no | `Create` | POST `/api/v1/recipes/{slug}/install` with `spec.inputs` |
| `true` | yes | `Unchanged` | No HTTP traffic |
| `false` | yes | `Unchanged` | Warning surfaced via description (no DELETE endpoint) |
| `false` | no | `Unchanged` | Benign no-op |

### `ApplyStrict`

Strict mode adds no extra constraints for this kind — a recipe already installed is not an error (recipes are workspace-scoped catalog entries, not user-named entities, so re-declaring an installed one is a normal `Unchanged` outcome).

### `ApplyReplace`

Replace mode is also a no-op for this kind today: the server has no DELETE endpoint, so even "destructively recreate" cannot uninstall the existing bundle. The planner emits `Unchanged` with the same warning as `install: false` against an installed recipe. When the backend grows a DELETE verb this kind will start emitting `Delete` + `Create` pairs in Replace mode without any YAML schema change.

## Round-trip via export

`crewship export workspace` walks `GET /api/v1/recipes` and emits one `RecipeDocument` per row whose `installed` field is true. The emitted document carries:

- `metadata.name` and `metadata.slug` = the catalog slug
- `spec.install: true`
- No `spec.inputs` (the install body is not persisted server-side; only the resulting crew/credentials/MCP rows are)

Re-applying an exported recipe doc is a no-op (`Unchanged`) because the recipe is already installed.

> **Limitation.** At time of writing the catalog list endpoint does not surface the `installed` flag for every row; the helper decodes whatever JSON the server returns and filters by `installed == true`. Until the backend exposes per-row installed state, `ExportRecipes` returns an empty slice in practice. This is correct behaviour ("nothing to round-trip yet") and upgrades silently once the backend ships the field.

## See also

- [`docs/manifest/crew_template.md`](./crew_template.md) — the deploy-only sibling for crew templates.
- [`docs/manifest/connector.md`](./connector.md) — the install-only reference kind for integration connectors.
- `internal/recipes/recipes.go` — the in-binary catalog source of truth.
- `internal/api/recipes.go` — backend handler for `GET /api/v1/recipes`, `GET /api/v1/recipes/{slug}`, `POST /api/v1/recipes/{slug}/install`.
