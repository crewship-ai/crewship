# kind: Workspace

## What it is

`kind: Workspace` is the top-level bundle: a single document that
captures workspace-level **credentials** + **skills** plus a list of
**crews**, each crew nesting its own agents, MCP servers, sidecar
services, and per-crew credential/skill overrides. One
`crewship apply` of a Workspace document converges the whole
workspace; `crewship export workspace` round-trips it back.

It is the most convenient shape for shipping or sharing a complete
team setup as one file. The same underlying data can also be expressed
as a set of standalone documents — [`Crew`](./crew.md),
[`Agent`](./agent.md), [`Skill`](./skill.md),
[`Integration`](./integration.md) — but `kind: Workspace` keeps the
shared credentials and skills in one place and nests everything that
belongs to a crew under it.

This kind is modeled in `internal/manifest/schema.go`
(`WorkspaceDocument` / `WorkspaceSpec`) and validated by
`internal/manifest/validate.go`; unlike the per-record kinds it does
not live under `internal/manifest/kinds/`.

> **Credentials are slots, never values.** The manifest format has no
> `value:` field for credentials — a Workspace document is always safe
> to commit to git. Credential values are supplied at apply time via
> `crewship apply --from-env` / `--secrets-file`, or prompted; any slot
> left unfilled is created as `status=PENDING` and shows a "needs
> value" CTA in the UI.

## YAML schema

```yaml
apiVersion: crewship/v1        # required — always crewship/v1 for now
kind: Workspace                # required — the literal string "Workspace"
metadata:
  name: ACME Engineering       # required (display)
  slug: acme-engineering       # required — idempotency key
  description: ""              # optional
  author: ACME Corp            # optional — descriptive header
  version: 0.1.0               # optional
  preferred_language: en       # optional
spec:
  credentials:                 # workspace-scoped credential slots (shared by every crew)
    - env: ANTHROPIC_API_KEY   # required — env-var name == credential name
      provider: ANTHROPIC      # required
      type: API_KEY            # required
      label: Anthropic API key # optional
  skills:                      # workspace-scoped skills (referenced by crew agents)
    - slug: house-style        # required — idempotency key
      inline: |                # one of inline / path / source
        ---
        name: house-style
        description: ACME's internal code-style conventions
        ---
        # House Style
        One feature per PR; commits in imperative mood.
  crews:                       # required — at least one crew
    - slug: code-review
      name: Code Review
      icon: git-pull-request
      color: blue
      devcontainer:
        image: mcr.microsoft.com/devcontainers/javascript-node:22-bookworm
      mcp_servers:
        - name: github
          display_name: GitHub
          transport: stdio
          command: npx
          args: [-y, "@modelcontextprotocol/server-github"]
          env_mapping:
            GITHUB_PERSONAL_ACCESS_TOKEN: GH_TOKEN
      agents:
        - slug: daniel
          name: Daniel
          agent_role: LEAD
          cli_adapter: CLAUDE_CODE
          llm: { provider: ANTHROPIC, model: claude-haiku-4-5 }
          tool_profile: MINIMAL
          skills: [house-style]
          env_refs: [ANTHROPIC_API_KEY, GH_TOKEN]
          prompt: |
            You are Daniel, a senior code reviewer.
```

### Top-level fields

| Field | Type | Required | Notes |
|---|---|---|---|
| `metadata.name` | string | **yes** | Display name. |
| `metadata.slug` | string | **yes** | Idempotency key (`^[a-z0-9][a-z0-9_-]{0,49}$`). |
| `metadata.description` / `author` / `version` / `preferred_language` / `icon` / `color` | string | no | Descriptive header. |
| `spec.credentials` | []object | no | Workspace-scoped credential slots, shared by every crew. |
| `spec.skills` | []object | no | Workspace-scoped skills referenced by crew agents. |
| `spec.crews` | []object | **yes** | At least one crew. Each crew slug must be unique within the workspace. |

### `spec.credentials[]` (slot declaration)

| Field | Type | Required | Notes |
|---|---|---|---|
| `env` | string | **yes** | The env-var name agents bind against — doubles as the credential's workspace-unique name. |
| `provider` | string | **yes** | e.g. `ANTHROPIC`, `GITHUB`, `NONE`. |
| `type` | string | **yes** | e.g. `API_KEY`, `CLI_TOKEN`. |
| `label` / `help_url` / `description` | string | no | UI hints. |
| `required` | bool | no | Defaults to true; metadata hint today. |

### `spec.skills[]`

Workspace-scoped skill declarations. Each needs **exactly one** body
source (`inline` / `path` / `source`); see [Skill](./skill.md) for the
detailed source semantics.

| Field | Type | Required | Notes |
|---|---|---|---|
| `slug` | string | **yes** | Idempotency key; the `SKILL.md` front-matter `name:` should match. |
| `inline` / `path` / `source` | string | one-of | The `SKILL.md` body source. |
| `allow_unsafe_license` | bool | no | Bypass the SPDX gate. |

### `spec.crews[]`

Each entry is a crew. The nested shape carries `slug`, `name`, `icon`,
`color`, an optional `devcontainer` block, optional per-crew
`credentials` / `skills` / `mcp_servers` / `services`, and a required
`agents:` list. Workspace-scoped credentials and skills are visible to
every crew; per-crew entries add to (and can override) them.

- **agents** — see [Agent](./agent.md) for the field reference (the
  nested form omits `crew_slug` since the parent crew is implicit). At
  most one agent per crew may be `agent_role: LEAD`.
- **mcp_servers** — the inline form of [Integration](./integration.md),
  crew-scoped.
- **services** — sidecar containers; same shape as
  [Crew](./crew.md)'s `spec.services`.

## Examples

### Minimal — one crew, one agent

```yaml
apiVersion: crewship/v1
kind: Workspace
metadata:
  name: Solo
  slug: solo
spec:
  credentials:
    - env: ANTHROPIC_API_KEY
      provider: ANTHROPIC
      type: API_KEY
  crews:
    - slug: main
      name: Main
      devcontainer:
        image: mcr.microsoft.com/devcontainers/javascript-node:22-bookworm
      agents:
        - slug: lead
          name: Lead
          agent_role: LEAD
          cli_adapter: CLAUDE_CODE
          env_refs: [ANTHROPIC_API_KEY]
          prompt: You are the crew lead.
```

### Multi-crew with shared skill + credentials

See
[`examples/manifests/full-team.workspace.yaml`](../../examples/manifests/full-team.workspace.yaml)
for a complete two-crew bundle that shares workspace-level credentials
and a `house-style` skill, then gives each crew its own agents and MCP
servers.

## CLI reference

| Command | Description |
|---|---|
| `crewship apply --file workspace.yaml --from-env` | Apply the bundle, sourcing credential values from the environment. |
| `crewship apply --file workspace.yaml --secrets-file secrets.env` | Apply, sourcing credential values from a file. |
| `crewship apply --file workspace.yaml --dry-run` | Plan only — no mutations. |
| `crewship apply --file workspace.yaml --replace --yes` | Destructive: delete existing, recreate fresh. |
| `crewship export workspace` | Emit the entire workspace as a single `kind: Workspace` document. |
| `crewship export workspace --split-dir ./out/` | Export, one file per kind. |

## Apply behavior

Applying a Workspace document expands it into the underlying create /
update calls for each nested entity, in dependency order: workspace
credentials + skills first, then crews and their agents, then the
crews' MCP servers and sidecar services. Every mutation goes through
the same REST endpoints the UI uses (`/api/v1/credentials`,
`/api/v1/skills/import`, `/api/v1/crews`, `/api/v1/agents`,
`/api/v1/integrations`, …), so RBAC, audit logging, and WebSocket
notifications fire exactly as they would for an interactive user — no
direct DB writes.

Idempotency is by slug within the workspace: re-applying converges
existing rows toward the declared state.

- **Upsert (default)** — create missing rows, update drifted ones.
- **`--strict`** — fail if any slug already exists (fresh-bootstrap
  guard).
- **`--replace --yes`** — destructive recreate.

## Validation rules

`checkWorkspaceDoc` (in `internal/manifest/validate.go`) aggregates
every failure into one message so you fix them in a single pass:

- `metadata.slug` is required and slug-formatted.
- Workspace credentials: each has a non-empty `env`, `provider`, and
  `type`; no duplicate `env` within the scope.
- Workspace skills: each has a non-empty `slug` and **exactly one** of
  `path` / `source` / `inline`.
- Crews: each crew has a slug (slug-formatted, unique within the
  workspace) and at least one agent.
- Agents: slug + name required; `agent_role` (AGENT \| LEAD),
  `cli_adapter`, and `tool_profile` enum-checked; at most one LEAD per
  crew; `skills` and `env_refs` resolve against the crew-scope then
  workspace-scope declarations.
- MCP servers and services validate their own shapes (env-mapping
  values resolve to known credentials; service names are DNS labels;
  named volumes only).

## Round-trip via export

`crewship export workspace` (`ExportWorkspace`) renders every crew in
the workspace as one `kind: Workspace` bundle: workspace-level
credentials are emitted as slots (values never travel in the file),
workspace-level skills as slug-only references, and each crew with its
agents and integrations nested under `spec.crews`. Use
`--split-dir ./out/` to emit one file per kind instead of a single
bundle.

## See also

- [Crew](./crew.md) / [Agent](./agent.md) / [Skill](./skill.md) / [Integration](./integration.md) — the standalone equivalents of the nested blocks.
- [`examples/manifests/full-team.workspace.yaml`](../../examples/manifests/full-team.workspace.yaml) — complete multi-crew bundle.
- Schema source: `internal/manifest/schema.go` (`WorkspaceDocument`, `WorkspaceSpec`).
- Validation: `internal/manifest/validate.go` (`checkWorkspaceDoc`).
- Export: `internal/manifest/export.go` (`ExportWorkspace`).
