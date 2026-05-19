# Workspace Manifest Format

Crewship workspaces, crews, agents, skills, credentials, and MCP
servers can be expressed as a single declarative YAML manifest. The
manifest is the source of truth; `crewship apply -f` converges the
live workspace toward it through normal REST API calls — the same
ones the UI uses, so RBAC and audit logging behave identically.

`apiVersion`: `crewship/v1`

## Two top-level shapes

| `kind:`      | Use when                                                |
| ------------ | ------------------------------------------------------- |
| `Crew`       | Ship a single crew + its agents + its skills            |
| `Workspace`  | Multiple crews sharing workspace-scope creds and skills |

Both share the same nested types (`Agent`, `Skill`, `Credential`,
`MCPServer`, `Devcontainer`).

## Idempotency model

`crewship apply --file file.yaml` is **idempotent and convergent**.
The manifest is the source of truth: missing things are created,
drifted things updated, and **things in the workspace that the
manifest no longer declares are deleted** (Terraform-style).

| Existing state    | Default (`sync`)     | `--strict`  | `--replace`           |
| ----------------- | -------------------- | ----------- | --------------------- |
| Missing           | create               | create      | create                |
| Exists, identical | no-op                | error 409   | delete then create    |
| Exists, drift     | update in place      | error 409   | delete then create    |
| Workspace has X, manifest doesn't | delete (prompts) | keep | keep |

Destructive operations (delete, replace) **always prompt for
confirmation** unless `--yes` is passed. The plan is printed before
the prompt so you can see exactly what will be mutated.

Resources covered by sync delete:

- crews (per workspace bundle)
- agents within each declared crew
- agent skill bindings (agent_skills join)
- agent credential bindings (agent_credentials join)
- MCP servers on each declared crew

Resources **not** synced (additive only — manifest can add but never
remove):

- skills themselves at workspace scope (their delete is destructive
  across many crews; do it through `crewship skill rm`)
- credentials themselves (their delete drops a value that may be
  shared elsewhere; do it through the UI)

Identity key is the **slug** within its workspace:

- crews: `(workspace_id, slug)` UNIQUE
- agents: `(workspace_id, slug)` UNIQUE — even if `crew_id` differs
- skills: `slug` UNIQUE workspace-wide
- credentials: `(workspace_id, name)` UNIQUE — name = `env:` field

## Credential safety

Manifests **never** carry credential values. The `credentials:` block
declares slots; values are supplied at apply-time via flags:

```
crewship apply -f team.yaml --from-env          # read ANTHROPIC_API_KEY etc. from process env
crewship apply -f team.yaml --secrets-file s.env # KEY=VALUE file
crewship apply -f team.yaml                      # values omitted → PENDING slots
```

PENDING credentials show up in the UI as "Needs value" with a CTA to
set the value. Agent runs against a PENDING credential fail
gracefully with "credential not configured" — the placeholder string
is never injected into the agent's environment, so the LLM cannot
exfiltrate it.

## Cross-references

Inside a single manifest, agents reference skills and credentials by
slug/env name:

```yaml
agents:
  - slug: daniel
    skills: [house-style, security-review]   # must exist under spec.skills
    env_refs: [ANTHROPIC_API_KEY, GH_TOKEN]  # must exist under spec.credentials
```

Cross-refs are resolved at apply-time after the referenced object
has been created. Validation fails fast on dangling references — no
half-applied state.

For workspace bundles, agents can reference workspace-level skills
and credentials as well as their own crew's; the resolver merges
both scopes.

## Skill sources (pick one per skill)

```yaml
skills:
  - slug: house-style
    inline: |              # SKILL.md body in the YAML itself (≤ 8 KB)
      ---
      name: house-style
      description: ...
      ---
      ...

  - slug: security-review
    path: ./skills/security-review/SKILL.md   # multi-file, recommended for > 1 KB skills

  - slug: git-flow
    source: https://github.com/anthropics/skills/blob/main/git-flow/SKILL.md
    ref: v1.2.0                  # optional git tag/sha
    digest: sha256:abc123...     # optional content digest for reproducibility
```

The license-allow gate runs per-skill. To override for one entry,
add `allow_unsafe_license: true`.

## Devcontainer

```yaml
devcontainer:
  image: mcr.microsoft.com/devcontainers/javascript-node:22-bookworm
  memory_mb: 4096
  cpus: 2.0
  features:
    "ghcr.io/devcontainers/features/common-utils:2": {"username": "agent"}
  env:
    PATH: /home/agent/.local/bin:/usr/local/bin:/usr/bin:/bin
  mise: |
    [tools]
    node = "22"
  raw:                     # passthrough for fields not modelled above
    customizations:
      vscode:
        extensions: [golang.go]
```

`raw:` lets you ship arbitrary devcontainer.json keys; structured
fields take precedence on conflict.

## Adding databases / sidecar services

Two shapes exist; pick based on whether you want the database to
live inside the agent container or alongside it.

### Today: devcontainer features (in-container)

Postgres, Redis, MySQL, MongoDB and friends ship as devcontainer
features that install during image build. Use these when you want a
zero-configuration dev DB whose lifecycle matches the agent:

```yaml
devcontainer:
  image: mcr.microsoft.com/devcontainers/python:3.12
  features:
    "ghcr.io/itsmechlark/features/postgresql:1": { version: "16" }
    "ghcr.io/itsmechlark/features/redis-server:1": {}
```

Agent reaches the DB at `localhost:5432` / `localhost:6379`. Works
on `crewship apply` today with no extra flags. See
`python-with-features.crew.yaml`.

### Sidecar containers: `services:`

For production-shape separation (DB lifecycle decoupled from agent,
volumes persisted across container rebuilds, healthchecks gate the
agent start) declare sidecars under `spec.services`:

```yaml
spec:
  devcontainer:
    image: python:3.12
  services:
    - name: redis
      image: redis:7-alpine
      ports: ["6379"]
    - name: postgres
      image: postgres:16
      env: { POSTGRES_DB: app, POSTGRES_USER: postgres }
      env_refs: [POSTGRES_PASSWORD]   # value comes from credentials vault
      ports: ["5432"]
      volumes:
        - { name: pg-data, mount: /var/lib/postgresql/data }
      healthcheck:
        test: ["CMD-SHELL", "pg_isready -U postgres"]
        interval: 5s
```

Agents inside the crew reach services by name (`redis:6379`,
`postgres:5432`) on the crew-private bridge network. The docker
provider starts each sidecar before the agent runtime so the first
DB call lands on a healthy endpoint (healthchecks gate readiness).

Volumes are per-crew named volumes — two crews that declare the
same `pg-data` name get isolated stores. Bind mounts to host paths
are intentionally rejected for portability.

See `python-with-services.crew.yaml` for the full template.

## Round-trip with export

`crewship export crew <slug>` is the round-trip partner of apply:

```
crewship export crew code-review > code-review.crew.yaml
crewship apply -f code-review.crew.yaml --dry-run
```

The export omits computed fields (IDs, timestamps) and credential
values, so the output is safe to commit.

## CLI summary

```
crewship apply --file manifest.yaml [flags]
  --dry-run            Validate + show plan, no mutations
  --strict             Fail on any existing resource
  --replace            Delete & recreate existing (destructive, prompts)
  --from-env           Read credential values from process env
  --secrets-file FILE  Read credential values from KEY=VALUE file
  --yes                Skip confirmation prompts
  --provision          Hint to also build devcontainers after apply

crewship export crew <slug> [flags]
  --output FILE        Write to file (default: stdout)
  --no-credentials     Omit credential slots
  --no-skill-bodies    Omit skill bodies (slug refs only)
```

## What's NOT modelled in v1 (intentional)

- **Routines / schedules** — separate `kind: Routine` planned, will
  reference crew slug.
- **Integrations (Slack/Linear/etc.)** — separate `kind: Integration`
  planned; MCP servers ARE modelled.
- **Eval scenarios** — separate `kind: EvalScenario` planned.
- **Workspace members / invitations** — out of scope; these are
  per-user identity, not part of a shareable bundle.
- **Pipeline definitions** — pipelines already have their own
  import endpoint; manifest may grow `pipelines:` later.

A future `apiVersion: crewship/v2` will retain compatibility with v1
manifests; the server accepts every past version it has ever shipped.

## Examples

See `examples/manifests/`:

- `code-review.crew.yaml` — single crew with multi-file skills
  (`./code-review/skills/...`) and a multi-file prompt
  (`./code-review/prompts/daniel.md`)
- `triage.crew.yaml` — single crew with inline skills
- `full-team.workspace.yaml` — workspace bundle with two crews
  sharing creds and a workspace-level skill
