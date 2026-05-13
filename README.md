<p align="center">
  <img src="crewship.svg" height="80" alt="Crewship" />
</p>

<h1 align="center">Crewship</h1>

<p align="center">
  <strong>Self-hosted runtime for AI coding agents.</strong><br/>
  Real Linux containers. Your hardware. Your keys. Your data.
</p>

<p align="center">
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI" /></a>
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/security.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/security.yml/badge.svg?branch=main" alt="Security" /></a>
  <a href="https://github.com/crewship-ai/crewship/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License" /></a>
  <a href="https://golang.org/doc/devel/release.html"><img src="https://img.shields.io/badge/go-1.26-00ADD8.svg?logo=go" alt="Go 1.26" /></a>
</p>

> **Status: v0.1 beta.** APIs and data models are still moving — pin a
> tag (or commit SHA) if you ship to production. See
> [CHANGELOG.md](CHANGELOG.md) and [RELEASING.md](RELEASING.md).

---

## What is Crewship?

Crewship turns a Claude Code / Codex / Gemini / OpenCode / Cursor / Factory
Droid session into a fleet of agents that share a workspace, talk to each
other, and keep state between runs.

Each crew gets its own Linux container — a real machine where its agents can
install services, run databases, mount volumes, and build a working system
together. The whole environment — code, data, conversations, audit trail —
runs on your hardware and packs into portable encrypted backups.

You bring the API keys. Crewship keeps them off disk, off the wire, and out
of agent processes.

## What's in the box

- **Real Linux containers** — one per crew, isolated network, non-root UID,
  read-only root, cap-drop ALL. Agents can install, build, and run anything
  Linux supports.
- **Six CLI adapters** — Claude Code, Codex CLI, Gemini CLI, OpenCode,
  Cursor CLI, Factory Droid. Same skill, same crew, swap the runtime.
- **Encrypted credential vault** — AES-256-GCM at rest, piped over a Unix
  socket to a sidecar (UID 1002) that injects per-request, never to the
  agent process directly.
- **Outbound scrubber** — 13+ credential patterns redacted from agent
  stdout before it leaves the container.
- **Skills as portable playbooks** — author or import a `SKILL.md`,
  attach to one agent or a whole crew. Works across every supported CLI.
- **Routines** — a JSON DSL for AI-authored workflows. Six step types
  (`agent_run`, `call_pipeline`, `http`, `code`, `wait`, `transform`),
  DAG parallelism via `needs[]`, cron + HMAC-signed webhooks,
  human-in-the-loop waitpoints, immutable version history, test-run
  gate before save. See [`docs/guides/routines.mdx`](docs/guides/routines.mdx).
- **Approvals (Harbormaster)** — risky tool calls pause for human
  sign-off; the agent waits.
- **Crew Journal** — append-only event stream: every LLM call, tool
  use, prompt, response, decision. Searchable (FTS5), exportable.
- **Backup & restore** — Age-encrypted bundles capture an entire
  workspace or crew (code, data, conversations, journal) so nothing
  agents create disappears.
- **Single binary** — Next.js static export embedded into the Go
  server. No Node.js at runtime, no separate services to deploy.

## Quick start (5 minutes to a running agent)

```bash
git clone https://github.com/crewship-ai/crewship.git
cd crewship

# 1. install frontend deps (pnpm only — npm/yarn will lock-step drift)
pnpm install

# 2. generate the two secrets you need
cp .env.example .env.local
# then put real values into .env.local for at least:
#   NEXTAUTH_SECRET=$(openssl rand -base64 32)
#   ENCRYPTION_KEY=$(openssl rand -hex 32)

# 3. start (SQLite is the default; no extra DB needed)
./dev.sh start
```

Then open:

- **UI:** http://localhost:3001
- **API + WebSocket:** http://localhost:8080

On first load you'll see a 6-step onboarding wizard (workspace → crew →
agent → credentials → done). Demo data: `./dev.sh seed` after the server
is up.

Other `./dev.sh` subcommands: `stop`, `restart`, `status`, `seed`,
`nuke`, `logs`, `logs:go`, `logs:next`.

### Single-binary production build

```bash
make build      # pnpm build → cp -r out web/out → go build -o crewship
./crewship start
```

`make build` is end-to-end. **Don't** run `pnpm build` followed
directly by `go build` — the `cp -r out web/out` step in between is
required, otherwise the embedded UI drifts out of sync with the
Next.js output.

Defaults: port `:8080`, SQLite at `~/.crewship/crewship.db`.
Override with `CREWSHIP_PORT` / `--db postgres://...` (PostgreSQL is
on the v0.2 roadmap).

## Stack

| Layer | Technology |
|---|---|
| UI | Next.js 16 (static export), React 19, Tailwind 4, shadcn/ui |
| Auth | NextAuth.js v5 (Auth.js), JWT + refresh tokens |
| Database | SQLite via `modernc.org/sqlite`, Go-side migrations (no Prisma at runtime) |
| Backend | Go 1.26 (`crewship`) — REST + WebSocket, Docker orchestration |
| Agent runtime | Docker containers, six CLI adapters |
| IPC | HTTP-over-Unix-socket on `/tmp/crewship.sock` (X-Internal-Token auth) |

> **Prisma is TypeScript-types only.** All schema changes go through
> `internal/database/migrate.go`. Never run `prisma migrate`.

## How the code is organised

```
app/                   Next.js frontend (static-export)
components/            UI components — shadcn/ui + feature folders
hooks/  lib/  stores/  Frontend hooks, utilities, Zustand stores

cmd/crewship/          Main binary (subcommands: start, seed, doctor,
                       skill, crew, routine, paymaster, …)
cmd/crewship-sidecar/  In-container sidecar (credentials, MCP, memory)

internal/api/          HTTP + WebSocket handlers (~50 REST endpoints)
internal/orchestrator/ Agent run loop — dispatch, exec, handoff
internal/sidecar/      Sidecar coordinator + bridges (keeper, MCP, memory)
internal/database/     SQLite schema + Go migrations
internal/journal/      Append-only event stream (canonical source of truth)
internal/keeper/       Credential gatekeeping (optional Ollama-backed)
internal/harbormaster/ Human-in-the-loop approval queue
internal/scrubber/     Outbound text secret redaction
internal/paymaster/    LLM cost ledger + budget enforcement primitives
internal/lookout/      Argument injection + prompt-injection guardrails
internal/backup/       Age-encrypted bundle export / restore
internal/provider/     Pluggable container / storage / state backends

dev.sh                 Local dev orchestration
prisma/                Prisma schema (TypeScript types only — do NOT migrate)
```

## Verify a change

```bash
go test ./... && go vet ./...        # backend
pnpm test && pnpm exec tsc --noEmit  # frontend
```

## Contributing

PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for workflow,
house rules, and commit conventions. Open an issue first to discuss
larger changes.

Security: see [SECURITY.md](SECURITY.md). Do not file public issues
for vulnerabilities.

## License

[Apache License 2.0](LICENSE) — free to use, modify, distribute.

Copyright 2025-2026 Unify Technology s.r.o.
