<p align="center">
  <img src="crewship.svg" height="80" alt="Crewship" />
</p>

<h1 align="center">Crewship</h1>

<p align="center">
  Open-source platform for orchestrating AI agents as virtual employees.
</p>

<p align="center">
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI" /></a>
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/security.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/security.yml/badge.svg?branch=main" alt="Security" /></a>
  <a href="https://github.com/crewship-ai/crewship/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License" /></a>
  <a href="https://golang.org/doc/devel/release.html"><img src="https://img.shields.io/badge/go-1.26-00ADD8.svg?logo=go" alt="Go 1.26" /></a>
  <a href="https://pkg.go.dev/github.com/crewship-ai/crewship"><img src="https://pkg.go.dev/badge/github.com/crewship-ai/crewship.svg" alt="Go Reference" /></a>
</p>

> **Project status:** active development. Public APIs and data models may
> change before the first tagged release. Self-host at your own risk and
> pin a commit SHA if you ship to production.

---

Crewship lets you organize AI agents into crews, assign them roles, credentials, and skills, and run them in isolated containers. Think of it as an HR platform — but for AI workers.

## Features

- **Agents as colleagues** — each agent has a role, crew, credentials, and persistent memory
- **Crew-based organization** — group agents into crews with shared filesystem and lead-mode coordination
- **Credential vault** — AES-256-GCM encrypted keys with priority-based failover
- **Keeper** — agent-side credential access guarded by a local LLM (Ollama)
- **Isolated execution** — every crew runs in its own Docker container; agents share via Unix socket IPC
- **Crew Journal** — append-only event stream is the canonical source of truth (cost, guardrails, approvals, checkpoints)
- **Single binary** — Next.js static export embedded into the Go server; no Node.js at runtime
- **Self-hosted** — run on your own infrastructure, keep your data

## Tech Stack

| Layer | Technology |
|-------|------------|
| UI | Next.js 16 (static export), React 19, Tailwind CSS 4, shadcn/ui |
| Auth | NextAuth.js v5 (Auth.js) |
| Database | SQLite via `modernc.org/sqlite` (PostgreSQL opt-in) |
| Backend | Go (`crewshipd`) — WebSocket, Docker orchestration, Crew Journal, embedded UI |
| Agent runtime | Docker containers with CLI adapters (Claude Code, OpenCode, Codex, …) |
| IPC | HTTP-over-Unix-socket on `/tmp/crewship.sock` (X-Internal-Token auth) |

> **Prisma is TypeScript-types only** — all DB migrations are Go-side in `internal/database/migrate.go`. Never run `prisma migrate`.

## Quick Start

```bash
# Clone
git clone https://github.com/crewship-ai/crewship.git
cd crewship

# Install frontend dependencies (pnpm only — never npm/yarn)
pnpm install

# Set up environment (SQLite is default; no DB container needed)
cp .env.example .env.local
# Edit .env.local — at minimum set NEXTAUTH_SECRET and ENCRYPTION_KEY:
#   openssl rand -base64 32   # NEXTAUTH_SECRET
#   openssl rand -hex 32      # ENCRYPTION_KEY

# Start backend + frontend (and PostgreSQL only if DATABASE_URL points to it)
./dev.sh start
```

- Frontend: <http://localhost:3001>
- Backend API + WebSocket: <http://localhost:8080>

Other `./dev.sh` subcommands: `stop`, `restart`, `status`, `seed`, `nuke`, `logs`, `logs:go`, `logs:next`.

### Production build (single binary)

```bash
make build           # pnpm build → cp -r out web/out → go build
./crewshipd          # serves embedded UI + API on $CREWSHIP_PORT (default 8080)
```

`make build` is end-to-end. Don't run `pnpm build` followed directly by `go build` — the `cp -r out web/out` step is required, otherwise the embedded FS drifts out of sync with Next.js output.

## Project Structure

```
app/                   Next.js frontend (TypeScript, static-export)
components/            UI components (shadcn/ui + feature folders)
hooks/  stores/  lib/  Frontend hooks, Zustand stores, shared utilities

cmd/crewship/          Go CLI binary (subcommands: run, seed, doctor, …)
cmd/crewship-sidecar/  Sidecar process that runs inside crew containers
internal/api/          HTTP / WebSocket handlers
internal/database/     SQLite schema + Go-side migrations (DO NOT use Prisma)
internal/orchestrator/ Agent run loop, lead-mode coordination
internal/sidecar/      Per-agent in-container HTTP server (UID 1002 boundary)
internal/journal/      Append-only event stream (the canonical source of truth)
internal/paymaster/    LLM cost tracking + budget enforcement
internal/lookout/      Guardrails (prompt-injection, schema validation, redaction)
internal/harbormaster/ Human-in-the-loop approval queue
internal/keeper/       Credential gatekeeping (Ollama-backed)
internal/scrubber/     Outbound-text secret redaction
internal/provider/     Pluggable container / storage / state providers

dev.sh                 Local dev orchestration (SQLite + go run + next dev)
prisma/                Prisma schema for TypeScript type generation only
```

## Verify any change

```bash
go test ./... -count=1 && go vet ./...    # Go — must pass
pnpm lint && pnpm build                    # Frontend — must pass for UI changes
```

## Contributing

Contributions are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow and [CLAUDE.md](CLAUDE.md) for project rules and anti-patterns. Please open an issue first to discuss what you'd like to change.

Security issues: see [SECURITY.md](SECURITY.md).

## License

[Apache License 2.0](LICENSE) — free to use, modify, and distribute. The `ee/` directory (when present) is governed by a separate enterprise license.

Copyright 2025-2026 Unify Technology s.r.o.
