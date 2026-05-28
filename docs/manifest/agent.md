# kind: Agent

## What it is

`kind: Agent` is the per-record CRUD entry point for a single agent —
the standalone counterpart to the `agents:` list nested under a
`kind: Crew` or `kind: Workspace` document. Operators reach for the
standalone form when they want to author or patch one agent in
isolation (e.g. add a skill, repoint the model) without re-shipping
the whole crew bundle.

Every agent belongs to exactly one crew. The manifest references its
parent crew by **slug** (`spec.crew_slug`); Plan resolves that slug to
the crew's id at apply time — the Create handler only accepts a
crew_id, never a slug.

The kind is implemented in `internal/manifest/kinds/agent.go`.

### Bindings are separate POSTs

The Create handler does **not** accept inline skills or credential
references in its body. Each binding is its own POST. So the Create
Exec runs three phases in order:

1. `POST /api/v1/agents` — create the agent, returns the new id.
2. `POST /api/v1/agents/{id}/skills` — one call per `spec.skills` entry.
3. `POST /api/v1/agents/{id}/credentials` — one call per `spec.env_refs` entry.

Binding POSTs are idempotent on the server (a re-bind returns "already
assigned"), so re-applying is safe. A skill/env-ref typo fails loud at
apply with the offending slug named.

## YAML schema

```yaml
apiVersion: crewship/v1      # required — always crewship/v1 for now
kind: Agent                  # required — the literal string "Agent"
metadata:
  name: Daniel               # required — display name
  slug: daniel               # required — workspace-unique idempotency key
  description: ""            # optional — mapped to agents.description
spec:
  crew_slug: code-review     # required — parent crew (resolved to crew_id)
  role_title: Code Reviewer  # optional — human-facing UI title
  agent_role: LEAD           # optional — LEAD | AGENT | COORDINATOR (server default: AGENT)
  cli_adapter: CLAUDE_CODE   # optional — runtime adapter (see enum below)
  llm:
    provider: ANTHROPIC      # optional — ANTHROPIC | OPENAI | GOOGLE | NONE
    model: claude-haiku-4-5  # optional — adapter-specific model id (free-form)
  tool_profile: MINIMAL      # optional — FULL | CODING | MINIMAL
  timeout_seconds: 1800      # optional — single-turn cap (default 1800)
  memory_enabled: true       # optional — per-agent memory tier (default true)
  prompt: |                  # one of prompt / prompt_file (exactly one)
    You are Daniel, a senior code reviewer.
  skills: [house-style]      # optional — skill slugs to bind
  env_refs: [ANTHROPIC_API_KEY, GH_TOKEN]  # optional — credential names to bind
```

### Field reference

| Field | Type | Required | Notes |
|---|---|---|---|
| `metadata.name` | string | **yes** | Display name. |
| `metadata.slug` | string | **yes** | Workspace-unique idempotency key. |
| `metadata.description` | string | no | Mapped to `agents.description`. |
| `spec.crew_slug` | string | **yes** | Parent crew slug. Resolved to `crew_id` at Plan time. The manifest requires it for every agent even though the server allows crewless agents — so cross-document references stay unambiguous. |
| `spec.role_title` | string | no | Human-facing UI title (e.g. "Technical Architect"). |
| `spec.agent_role` | enum | no | One of `LEAD` \| `AGENT` \| `COORDINATOR`. Empty → server default `AGENT`. `LEAD` requires a `crew_slug`. |
| `spec.cli_adapter` | enum | no | One of `CLAUDE_CODE` \| `OPENCODE` \| `CODEX_CLI` \| `GEMINI_CLI` \| `CURSOR_CLI` \| `FACTORY_DROID`. |
| `spec.llm.provider` | enum | no | One of `ANTHROPIC` \| `OPENAI` \| `GOOGLE` \| `NONE`. `NONE` = explicitly no LLM (for adapters that pin their own). |
| `spec.llm.model` | string | no | Adapter-specific model id. Free-form by design. |
| `spec.tool_profile` | enum | no | One of `FULL` \| `CODING` \| `MINIMAL`. |
| `spec.timeout_seconds` | int | no | Single-turn execution cap. Defaults to **1800**; must be non-negative. |
| `spec.memory_enabled` | bool | no | Per-agent memory tier. Defaults to **true**. Pointer-typed internally so an absent field is distinguishable from `false`. |
| `spec.prompt` | string | one-of | Inline system prompt body. |
| `spec.prompt_file` | string | one-of | Manifest-relative path to a prompt body. Folded into `prompt` at parse time. |
| `spec.skills` | []string | no | Skill slugs to bind (one POST each). |
| `spec.env_refs` | []string | no | Credential names to bind as env vars (one POST each). The credential's `name` must equal the env-var (e.g. `ANTHROPIC_API_KEY`). |

> **Exactly one** of `prompt` / `prompt_file` must be set. `prompt_file`
> is resolved relative to the manifest file and folded into `prompt`
> before Validate runs, so a hand-built document that never went
> through the loader must set `prompt` directly.

## Examples

### Minimal

```yaml
apiVersion: crewship/v1
kind: Agent
metadata:
  name: Daniel
  slug: daniel
spec:
  crew_slug: code-review
  prompt: You are a senior code reviewer.
```

### Lead with model, tools, and bindings

```yaml
apiVersion: crewship/v1
kind: Agent
metadata:
  name: Daniel
  slug: daniel
spec:
  crew_slug: code-review
  role_title: Code Reviewer
  agent_role: LEAD
  cli_adapter: CLAUDE_CODE
  llm:
    provider: ANTHROPIC
    model: claude-haiku-4-5
  tool_profile: MINIMAL
  memory_enabled: true
  skills: [house-style]
  env_refs: [ANTHROPIC_API_KEY, GH_TOKEN]
  prompt: |
    You are Daniel, a senior code reviewer. Apply the house-style
    skill, then run a security and correctness pass.
```

### Prompt from a sibling file

```yaml
apiVersion: crewship/v1
kind: Agent
metadata:
  name: Alice
  slug: alice
spec:
  crew_slug: triage
  agent_role: LEAD
  prompt_file: ./prompts/alice.md
```

## CLI reference

There is no dedicated `crewship agent` per-kind admin command — agents
are authored through the manifest pipeline (or the UI). The relevant
CLI surface is the global apply/export flow:

| Command | Description |
|---|---|
| `crewship apply --file agent.yaml` | Declarative upsert (Create + bind skills/credentials, or Update). |
| `crewship apply --dir ./manifests/` | Walk a directory; crews + agents run before projects/labels in topo order. |
| `crewship apply --file agent.yaml --dry-run` | Plan only — surfaces a dangling `crew_slug` before any mutation. |
| `crewship export workspace` | Round-trip — emits one `kind: Agent` document per agent, with bindings folded back in. |

## REST endpoint mapping

| Manifest field | POST/PATCH body field | Notes |
|---|---|---|
| `metadata.name` | `name` | |
| `metadata.slug` | `slug` | |
| `spec.crew_slug` | `crew_id` | Resolved slug → id before the call. |
| `spec.role_title` | `role_title` | |
| `spec.agent_role` | `agent_role` | |
| `spec.cli_adapter` | `cli_adapter` | |
| `spec.llm.provider` | `llm_provider` | Omitted when `NONE`. |
| `spec.llm.model` | `llm_model` | |
| `spec.tool_profile` | `tool_profile` | |
| `spec.timeout_seconds` | `timeout_seconds` | Sent explicitly (default 1800) so the round-trip diff is stable. |
| `spec.memory_enabled` | `memory_enabled` | Sent explicitly (default true). |
| `spec.prompt` | `system_prompt` | |
| `spec.skills[]` | `skill_id` | One `POST .../skills` per entry. |
| `spec.env_refs[]` | `credential_id` + `env_var_name` | One `POST .../credentials` per entry. |

Endpoints used:

| Verb | Path | Action |
|---|---|---|
| `POST` | `/api/v1/agents` | Create |
| `PATCH` | `/api/v1/agents/{agentId}` | Update |
| `GET` | `/api/v1/agents` | List (drift + export) |
| `GET` | `/api/v1/crews` | Resolve `crew_slug` → `crew_id` |
| `POST` | `/api/v1/agents/{id}/skills` | Bind a skill |
| `POST` | `/api/v1/agents/{id}/credentials` | Bind a credential |

## Validation rules

`AgentDocument.Validate` enforces:

- `apiVersion` / `kind`, when set, equal `crewship/v1` / `Agent`.
- `metadata.name` and `metadata.slug` are non-empty.
- `spec.crew_slug` is non-empty (and required when `agent_role: LEAD`).
- `agent_role`, `cli_adapter`, `llm.provider`, `tool_profile`, when
  set, are members of their allow-lists (errors spell out the legal
  values).
- `timeout_seconds` is non-negative.
- **Exactly one** of `prompt` / `prompt_file` is set.
- `skills[]` and `env_refs[]` have no empty entries.
- When `WorkspaceContext` carries crew data, `crew_slug` must
  reference a declared or remote crew. Skill/credential FK checks run
  at Plan time (the live client is available there).

## Apply behavior

### ApplyUpsert (default)

- Remote missing → `ActionCreate`: POST the agent, then bind each
  skill + env-ref in sequence. A partial binding failure leaves the
  agent created; re-applying converges (the bindings are idempotent).
- Remote present, fields drift → `ActionUpdate`: a **sparse PATCH**
  carrying only the fields whose declared value differs from the
  remote. Empty declared fields are skipped — so omitting `role_title`
  won't blank out a title set via the UI. Declared bindings are
  re-asserted (idempotent).
- Remote present, no field drift, no declared bindings →
  `ActionUnchanged`.

The diff is intentionally narrow: `system_prompt`, `description`, and
`memory_enabled` are not touched unless explicitly declared, so the
manifest never silently clobbers a prompt grown via the UI.

### Crew reassignment

Declaring a different `crew_slug` on an existing agent emits a
`crew_id` patch — supported but rare.

## Round-trip via export

`crewship export workspace` calls `ExportAgents`, which renders one
`kind: Agent` document per agent (sorted by slug), folding `crew_id`
back to `crew_slug` and pulling each agent's bound skill slugs +
credential env-names back into `spec.skills` / `spec.env_refs`.
`memory_enabled` is emitted explicitly so the round-trip diff doesn't
fall into the "use server default" branch. Fields the manifest doesn't
model (runtime status, run counts, timestamps) are dropped.

## See also

- [Crew](./crew.md) — the parent crew; can also declare agents inline under `spec.agents`.
- [Skill](./skill.md) — bound via `spec.skills` (slug list).
- [Workspace](./workspace.md) — the top-level bundle that nests crews + agents.
- [Issue](./issue.md) — references an agent via `spec.assignee_slug`.
- This kind's Go implementation: `internal/manifest/kinds/agent.go`.
