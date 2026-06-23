# AGENTS.md

Concise entrypoint for AI agents and new contributors working in this repo.
For deep specs see [`.claude/context/prd/`](.claude/context/prd/); for contributor
process see [`CONTRIBUTING.md`](CONTRIBUTING.md).

## What Crewship is

Open-source AI agent orchestration platform. A single Go binary embeds a Next.js
static export; agents run inside Docker containers behind a credential-injecting
sidecar proxy. Orchestration is **queue + trigger** based (cron schedulers,
webhooks, an autonomous assignment queue, durable pipeline waitpoints), not
infinite loops.

## Build, run, test

```bash
./dev.sh start          # dev: Go :8080 + Next.js :3001 (hot reload). `make up` is an alias.
make build              # prod: pnpm build → static export → embedded → ./crewship
make build:go           # Go binary only (+ sidecar)

# Verification loop — run until green before considering work done:
go test ./... -count=1  # all Go tests (Docker tests self-skip if daemon absent)
go vet ./...            # static analysis
pnpm lint               # ESLint (UI/frontend changes)
pnpm build              # confirm static export still builds (cross-cutting changes)
```

Multi-instance: a clone named `crewship_N` auto-offsets all ports/data/sockets,
so parallel agents/worktrees run conflict-free (see `dev.sh`).

## Architecture map (`internal/`)

```
api/            HTTP API (stdlib http.ServeMux, "METHOD /path" patterns)
orchestrator/   agent execution engine — container exec + streaming + env build
sidecar/        credential-injecting forward proxy (127.0.0.1:9119)
keeper/         credential gating + gatekeeper + Phase-2 self-improvement routines
scheduler/      robfig/cron engine — fires scheduled agent runs
pipeline/       pipeline runner: schedules, webhooks, durable waitpoints (HITL)
harbormaster/   approval gate for dangerous actions (approvals_queue)
policy/         per-crew autonomy matrix (strict|guided|trusted|full × warn|block)
paymaster/      cost ledger + budgets + per-model pricing
journal/        durable run/event journal (the shared audit + activity spine)
episodic/ consolidate/ memory/   agent memory: recall before runs, consolidate after
manifest/       declarative `crewship apply` (plan/validate, ~20 resource kinds)
skills/         SKILL.md parser + importer + bundled skills
auth/           JWE (NextAuth v5 compatible) validation
database/       SQLite via database/sql (modernc.org/sqlite, pure Go, no CGO)
provider/       infra interfaces: Container (Docker/Apple), Storage, State
scrubber/ httpsafe/ safepath/   credential scrubbing, SSRF + path-traversal guards
ws/             WebSocket hub (pub/sub channels)
config/         defaults → YAML → env vars (env wins)
```

Frontend: `app/` (Next.js App Router), `components/` (`ui/` = shadcn), `hooks/`,
`lib/` (Zod schemas, CASL permissions).

## Conventions

- **Go**: no ORM — raw `database/sql` with `QueryRowContext`/`ExecContext`. Provider
  interfaces for all infra. RFC 7807 Problem Details for errors. Imports stdlib →
  external → internal. Table-driven tests; `t.Skip()` for optional deps (Docker).
- **DB**: `snake_case`, plural tables; CUID ids (`internal/api/cuid.go`); soft delete
  via `deleted_at`; timestamps TEXT (RFC 3339).
- **Frontend**: Next.js 16 App Router (static export), Tailwind 4 + Radix/shadcn,
  Zod, CASL (`lib/permissions/`). ES modules only. `@/*` path alias.
- **Commits**: conventional (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`,
  `chore:`). Branch from `main`.

## Migrations

Go-only, in `internal/database/migrate.go`. Add `const migrationXxx = ...`, append
`{N, "name", migrationXxx}` to the `migrations` slice (N = previous + 1). Runs at
startup, tracked in `_migrations`. **Prisma is for TypeScript types only — never run
`prisma migrate`.**

## NEVER DO

- Never skip the verification loop (`go test` + `go vet`) — or ship without tests.
- Never add API routes under `app/` — static export silently drops them.
- Never use `"sqlite3"` as the driver name — `modernc.org/sqlite` registers `"sqlite"`.
- Never use `npm`/`yarn` — `pnpm` only.
- Never change the GCM byte layout in `internal/encryption/` — breaks all stored creds.
- Never change sidecar UID (1002) or agent UID (1001) — it's a security boundary.
- Never discard WIP — `git stash` before switching branches, never `git checkout .`.
- Never commit secrets (`.env.local`, real keys).

## When unsure

Check `.claude/context/prd/` before assuming design or requirements. Keep these docs
current after significant changes.
