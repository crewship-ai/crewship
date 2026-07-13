<p align="center">
  <img src="crewship.svg" height="80" alt="Crewship" />
</p>

<h1 align="center">Crewship</h1>

<p align="center">
  <strong>Run AI coding agents on your own hardware — each in a real, isolated container,<br/>
  with a company-grade control plane around the whole fleet.</strong>
</p>

<p align="center">
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI" /></a>
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/security.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/security.yml/badge.svg?branch=main" alt="Security" /></a>
  <a href="https://github.com/crewship-ai/crewship/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License" /></a>
  <a href="https://golang.org/doc/devel/release.html"><img src="https://img.shields.io/badge/go-1.26-00ADD8.svg?logo=go" alt="Go 1.26" /></a>
</p>

> **Status: open beta.** APIs and data models are still moving — pin a tag
> or commit SHA if you ship to production. [What's ready vs. WIP](#whats-ready-vs-wip)
> spells out the exact state of every piece.

<!-- DEMO: replace with an animated GIF (VHS) or a YouTube thumbnail once recorded, e.g.
<p align="center"><a href="https://youtube.com/@crewshipai"><img src="docs/assets/demo.gif" alt="Crewship demo" width="760" /></a></p>
-->

---

## What is Crewship?

Crewship gives every crew of agents its own **real Linux container** — a fully
sandboxed machine where the agent runs and can install *literally anything*:
system packages, databases, build tools, whole workspaces. You choose what
drives it — a **local model via Ollama**, **OpenCode**, or **Claude Code** — so
you are never locked to one vendor or forced to push your code into someone
else's cloud.

Around that runtime, Crewship is the control plane a team actually needs to run
agents for real: **missions** where a lead plans a task breakdown and agents
work it, scheduled **routines**, human-filed **issues**, **role-based access
control** for a whole company, complete **audit logs** of every action, and a
governance layer (**Keeper**) that watches what agents do. Everything — code,
data, conversations, memory, the audit trail — stays on your hardware and packs
into **encrypted backups** that capture not just memory but the agents' whole
working state, so nothing an agent builds is ever lost.

Crewship models a crew like a small company, so the structure is obvious to
anyone: a **Lead** agent directs the work and can **hire** specialists on demand,
while member **Agents** do the tasks. Every participant — human and agent — has
their own **chat**, their own **inbox**, and a place in the org.

You bring the keys — or run fully local. Crewship keeps them off disk, off the
wire, and out of the agent process.

---

## Quickstart

```bash
brew install crewship-ai/tap/crewship   # macOS / Linux (other installs below)
crewship doctor                         # checks container runtime, ports, deps
crewship start                          # boots the daemon on :8080
open http://localhost:8080              # 3-step wizard: workspace → crew → key → launch
```

You need a container runtime (Docker, Podman, Colima, OrbStack, or Apple
Containers). `crewship doctor` autodetects one and tells you exactly what's
missing. Want demo data to poke at? `crewship seed`.

> Prefer to wire everything from the terminal instead of the wizard? Jump to
> [First crew — CLI walkthrough](#first-crew--cli-walkthrough). Full install
> options (signed installer, Docker Compose, air-gapped) are under
> [Install](#install).

### Install a crew in one click

A **recipe** is a curated crew bundled with the credentials and MCP servers it
needs — installed **atomically** in a single transaction, so a half-installed
crew never exists.

```bash
crewship recipe list                    # browse the bundled crews
crewship recipe install <name>          # crew + credentials + MCP servers, one shot
```

---

## What's in the box

Labels: ✅ **stable** · 🟡 **early** (works, contract may still shift) ·
🚧 **WIP** (scaffolded, not yet end-to-end). Each item links to its guide.

**1 · The runtime**

- ✅ **Real Linux containers** — one per crew: isolated network, non-root UID,
  read-only root, cap-drop ALL. Install, build, and run anything Linux
  supports. [→ devcontainers](docs/guides/devcontainers.mdx)
- ✅ **Pick your engine** — drive a crew with a **local model via Ollama**,
  **OpenCode**, or **Claude Code**. No API key required if you run local.
  [→ CLI adapters](docs/guides/cli-adapters.mdx)
- ✅ **Skills** — author or import a `SKILL.md` playbook and attach it to one
  agent or a whole crew. [→ skills](docs/guides/skills.mdx)
- ✅ **Manifests** — declare your whole org (workspace, crews, agents, skills,
  integrations, issues, projects) as files and `crewship apply` it. GitOps for
  your agent fleet. [→ manifests](docs/guides/manifests.mdx)

**2 · Working with agents (the "company")**

- ✅ **Crews with Lead/Agent roles + hiring** — a lead plans and delegates; hire
  ephemeral specialists on demand. [→ orchestration](docs/guides/orchestration.mdx)
  · [ephemeral agents](docs/guides/ephemeral-agents.mdx)
- ✅ **Missions** — a lead plans a task breakdown, agents work the tasks, and the
  mission moves through a tracked lifecycle you can start, resume, restart, or
  clone. [→ API: missions](docs/api-reference/missions.mdx)
- ✅ **Per-agent chat** — every agent has its own conversation, resumable across
  sessions. [→ chat sessions](docs/guides/chat-sessions.mdx)
- ✅ **Issue tracker + triage** — humans file issues; **triage rules** auto-route
  each one to the right crew, agent, and project. Full backlog with **projects,
  milestones, recurring issues, and saved views**. [→ API: issues](docs/api-reference/issues.mdx)
  · [triage](docs/api-reference/triage.mdx)
- 🟡 **Routines** — scheduled, AI-authored workflows: step DAGs, cron +
  HMAC-signed webhooks, human-in-the-loop waitpoints, immutable version history.
  [→ routines](docs/guides/routines.mdx)
- ✅ **Inbox + notifications** — messages, mentions, and events land in a
  per-user inbox with configurable notification channels. [→ inbox](docs/guides/inbox.mdx)
  · [notifications](docs/guides/notifications.mdx)

**3 · Control plane & governance**

- ✅ **Role-based access control** — OWNER › ADMIN › MANAGER › MEMBER › VIEWER,
  enforced on every route. [→ auth](docs/guides/auth.mdx)
- ✅ **Approvals** — risky tool calls pause for human sign-off; the agent waits.
  *(Harbormaster)* [→ harbormaster](docs/guides/harbormaster.mdx)
- ✅ **Keeper** — optional rule-based gate + watchdog on what agents pull and do,
  with snitch-to-admin alerts. [→ keeper](docs/guides/keeper.mdx)
- ✅ **Cost ledger** — every LLM call priced with token counts; per-workspace
  budgets enforced. *(Paymaster)* [→ paymaster](docs/guides/paymaster.mdx)
- ✅ **Input guard** — argument- and prompt-injection guardrails on LLM inputs.
  *(Lookout)* [→ lookout](docs/guides/lookout.mdx)
- ✅ **Audit journal** — append-only, searchable (FTS5), exportable stream of
  every LLM call, tool use, and decision. *(Crew Journal)* [→ crew journal](docs/guides/crew-journal.mdx)
- ✅ **Replay & regression** — replay a mission deterministically or diff two
  runs for regressions in tool success, cost, and step signature.
  *(Quartermaster)* [→ API: eval](docs/api-reference/eval.mdx)
- ✅ **Checkpoints & fork** — snapshot a mission's state, advisory-restore it, or
  fork a fresh mission from any point. *(Cartographer)* [→ API: checkpoints](docs/api-reference/checkpoints.mdx)

**4 · Your data, your keys**

- ✅ **Encrypted credential vault** — AES-256-GCM at rest, piped over a Unix
  socket to a sidecar that injects per-request, never to the agent process.
  [→ credentials](docs/guides/credentials.mdx) · [encryption at rest](docs/guides/encryption-at-rest.mdx)
- ✅ **Outbound scrubber** — credential patterns redacted from agent output
  before it leaves the container.
- ✅ **Agent memory** — file-first memory that recalls across sessions, plus
  crew-shared facts with cross-crew isolation. [→ agent memory](docs/guides/agent-memory.mdx)
- ✅ **Encrypted backups** — Age-encrypted bundles capture a whole workspace or
  crew — code, data, conversations, journal, memory — so nothing agents create
  disappears. [→ backup](docs/guides/backup.mdx)
- 🟡 **Integrations** — connect agents to external tools via MCP and Composio.
  [→ integrations](docs/guides/integrations.mdx)

**5 · Interfaces**

- ✅ **Web UI** — activity feed, per-crew dashboards, approvals queue,
  integrations page, and a bottom command dock. [→ activity](docs/guides/activity.mdx)
- ✅ **Full CLI** — every workflow, scriptable and headless. [→ CLI overview](docs/cli/overview.mdx)
- ✅ **Single binary** — the Next.js UI is embedded in the Go server. No Node.js
  at runtime, no separate services to deploy.

---

## Everything, from your terminal

Crewship exposes one versioned REST surface under `/api/v1/` (five auth methods,
RFC 7807 errors, WebSocket + webhooks for real-time and inbound triggers) — and
**every API resource has a matching `crewship` command.** API↔CLI parity is a
project rule, not an afterthought: anything the platform can do, you can do from
a shell script *or* hand to an agent to drive safely.

```bash
crewship crew list --format json | jq '.[].slug'
crewship ask --agent viktor "scaffold a Go HTTP service with a /health endpoint"
crewship mission run "ship the auth refactor" --crew eng
crewship approvals list                 # what's waiting on a human
crewship cost --workspace demo          # token + dollar ledger
```

Full reference: [docs/cli/overview.mdx](docs/cli/overview.mdx) and
[docs/api-reference/overview.mdx](docs/api-reference/overview.mdx). The docs are
large — **60+ guides and 40+ API pages** under [`docs/`](docs/), rendered at
[docs.crewship.ai](https://docs.crewship.ai) (coming soon).

---

## What's ready vs. WIP

This is an **open beta**. The pieces marked ✅ above have been used by the
maintainer in production-shaped workloads; 🟡 and 🚧 are still being shaped.

- **Claude Code is the production-tested adapter.** Ollama and OpenCode run
  today; Codex / Gemini / Cursor / Factory Droid have adapter scaffolds but not
  yet the integration tests and tuning to call production-ready.
- **SQLite for now.** Runs on `modernc.org/sqlite` (single binary, WAL, no extra
  services). PostgreSQL is on the roadmap.
- **Single host.** One instance manages many crews on its own host; multi-host
  clustering is future work.
- **APIs may break across minor bumps.** Patch bumps inside a minor are
  backwards-compatible. Pin a tag for production.
- **Telemetry is opt-in on stable builds.** Prerelease/dev builds send anonymous
  crash reports to help a small team fix bugs; the onboarding wizard asks
  explicitly and your answer sticks (`crewship telemetry on|off`).
  [→ telemetry](docs/guides/telemetry.mdx)

Found a beta-blocker? [Open an issue][issues] — the `beta-blocker` label gets
priority triage.

[issues]: https://github.com/crewship-ai/crewship/issues/new/choose

---

## Install

Three supported paths — full details in [docs/guides/install](docs/guides/install.mdx).

```bash
# macOS / Linux — Homebrew
brew install crewship-ai/tap/crewship

# Any Unix — signed installer (fetch the script direct from the repo)
curl -fsSL https://raw.githubusercontent.com/crewship-ai/crewship/main/scripts/install.sh | bash

# Self-hosted — Docker Compose (builds from source; brokers all Docker API
# access through a docker-socket-proxy sidecar)
git clone https://github.com/crewship-ai/crewship.git
cd crewship
cp .env.example .env   # then set NEXTAUTH_SECRET + ENCRYPTION_KEY (both required)
docker compose -f docker/docker-compose.prod.yml up -d
```

Homebrew / curl installs then start with `crewship start` (the Compose stack
already boots the server). Defaults: HTTP on `:8080`, SQLite at
`~/.crewship/crewship.db`; override with `CREWSHIP_PORT` / `--db file:/path`.
Platform gotchas (macOS Gatekeeper, Linux linger, Windows SmartScreen) are in
[troubleshooting](docs/guides/troubleshooting.mdx).

## First crew — CLI walkthrough

The web wizard is the easy path. To wire the same setup from your terminal — to
script it, dotfile it, or run headless — every step has a subcommand:

```bash
crewship init --email you@example.com --name "You"   # first admin on an empty DB; returns a CLI token
crewship login --token <token-from-init>             # persists to ~/.crewship/cli-config.yaml
crewship crew create --name "Engineering" --slug eng --icon code --color blue
read -rs -p "Anthropic API key: " KEY && \
  printf '%s' "$KEY" | crewship credential create \
    --name anthropic-key --type API_KEY --provider ANTHROPIC --value-stdin && \
  unset KEY
crewship agent create --name "Viktor" --slug viktor --crew eng --role LEAD \
  --cli-adapter CLAUDE_CODE --tool-profile CODING --system-prompt @prompts/lead.md
crewship credential assign anthropic-key viktor --env-var-name ANTHROPIC_API_KEY
crewship ask --agent viktor "scaffold a Go HTTP service with a /health endpoint"
```

Full CLI reference: [docs/cli/overview.mdx](docs/cli/overview.mdx). Pair an
already-running server with a fresh CLI install via
[cli-pairing](docs/guides/cli-pairing.mdx) — the same device-code flow Claude
Code itself uses.

## Build from source

```bash
git clone https://github.com/crewship-ai/crewship.git
cd crewship
pnpm install          # frontend deps (pnpm required)
./dev.sh start        # SQLite, hot-reload, Go :8080 + Next.js :3001
```

`./dev.sh start` auto-generates `NEXTAUTH_SECRET` and `ENCRYPTION_KEY` into
`~/.crewship/secrets.env` on first boot — no `.env.local` editing for the happy
path. Other subcommands: `stop`, `restart`, `status`, `seed`, `nuke`, `logs`.

Single-binary production build:

```bash
make build            # pnpm build → cp -r out web/out → go build -o crewship
./crewship start
```

`make build` is end-to-end. **Don't** run `pnpm build` then `go build` directly
— the `cp -r out web/out` step in between keeps the embedded UI in sync.

## Stack

| Layer | Technology |
|---|---|
| UI | Next.js 16 (static export), React 19, Tailwind 4, shadcn/ui |
| Auth | NextAuth.js v5 (Auth.js), JWT + refresh tokens |
| Database | SQLite via `modernc.org/sqlite`, Go-side migrations (no Prisma at runtime) |
| Backend | Go 1.26 (`crewship`) — REST + WebSocket, Docker orchestration |
| Agent runtime | Docker containers; Ollama / OpenCode / Claude Code adapters (plus scaffolds) |
| IPC | HTTP-over-Unix-socket on `/tmp/crewship.sock` (X-Internal-Token auth) |

> **Prisma is TypeScript-types only.** All schema changes go through
> `internal/database/migrate.go`. Never run `prisma migrate`.

## Verify a change

```bash
go test ./... && go vet ./...        # backend
pnpm test && pnpm exec tsc --noEmit  # frontend
```

## Contributing

PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for workflow and commit
conventions; open an issue first to discuss larger changes. Security: see
[SECURITY.md](SECURITY.md) — do not file public issues for vulnerabilities.
Community conduct: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) (Contributor
Covenant 2.1).

## Community & links

- **Docs:** [docs/](docs/) in this repo; hosted [docs.crewship.ai](https://docs.crewship.ai) coming soon
- **Discord:** community help + showcase (invite on [crewship.ai](https://crewship.ai))
- **Reddit:** [r/Crewship](https://reddit.com/r/Crewship)
- **X / Twitter:** [@crewshipai](https://twitter.com/crewshipai) · **Bluesky:** [@crewship.ai](https://bsky.app/profile/crewship.ai)
- **YouTube:** [@crewshipai](https://youtube.com/@crewshipai)
- **GitHub Discussions:** [crewship-ai/crewship/discussions](https://github.com/crewship-ai/crewship/discussions)

## License

[Apache License 2.0](LICENSE) — free to use, modify, distribute.

Copyright 2025-2026 Unify Technology s.r.o.
