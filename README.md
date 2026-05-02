<p align="center">
  <img src="crewship.svg" height="80" alt="Crewship" />
</p>

<h1 align="center">Crewship</h1>

<p align="center">
  <strong>Self-hosted agent runtime</strong> — Linux containers your AI can build in, on your hardware, backed up by default.
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

Crewship is a self-hosted runtime for AI coding agents. Each agent gets its own Linux container — a real machine where it can install services, run databases, mount volumes, and build a complete working system. The whole environment — code, data, conversations, AI cost ledger — runs on your hardware and is packaged into portable, encrypted backups, so nothing your agents create ever disappears. Scale a Claude Code, Gemini, or OpenCode session into a fleet of governed agents with cost budgets, approval gates, and audit logs built in.

## Features

- **Real Linux containers** — every agent gets a full container; databases, queues, file stores, mounted volumes — anything your AI needs to build a complete working system
- **Self-hosted** — runs on your own hardware; your data, your perimeter, your control
- **Backup & restore** — portable, AGE-encrypted bundles capture an entire workspace or crew (code, data, conversations, AI cost ledger) so nothing your agents create ever disappears
- **Fleet mode for any AI coding CLI** — scale a Claude Code, Gemini, Codex, or OpenCode session into a coordinated fleet of governed agents
- **Cost budgets & audit log** — Crew Journal records every LLM call, tool use, and decision; budgets enforced hierarchically (workspace → crew → mission → agent)
- **Approval gates** — risky actions pause for human sign-off (sync or async)
- **Credential vault + Keeper** — AES-256-GCM encrypted keys; agent-side access guarded by a local LLM (Ollama)
- **Crew-based organization** — group agents into crews with shared filesystem and lead-mode coordination
- **Persistent agent identity** — agents have role, history, conversations, and a workspace they keep across runs
- **Single binary** — Next.js static export embedded into the Go server; no Node.js at runtime

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
