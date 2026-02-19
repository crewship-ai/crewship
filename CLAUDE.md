# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Crewship is an open-source AI agent orchestration platform. Single Go binary with embedded Next.js static export. Agents run in Docker containers with a sidecar proxy for credential injection.

## Build & Dev Commands

```bash
# Development (hot reload)
./dev.sh start                  # Go :8080 + Next.js :3001 (HMR)
make dev:go                     # Go only (uses air for hot reload)
make dev:next                   # Next.js only on :3001 (turbopack)

# Production build (Next.js static export -> embedded in Go binary)
make build                      # pnpm build + go build -> ./crewship

# Run
crewship start                  # SQLite on :8080
crewship start --port 9090      # Custom port
crewship start --no-docker      # Without Docker requirement
crewship doctor                 # Check Docker, data dirs, connectivity
crewship version                # Show version, commit, build date
```

## Workflow: Verify First, Then Implement

**Before starting any work**, describe your verification plan in a short summary:
- What tests will you write or update?
- What commands will you run to confirm correctness?
- What could go wrong and how will you catch it?

**Test-first development (TDD):** Always design and write tests BEFORE implementing a feature. Think through how to verify the entire solution, define the test cases, then write the implementation to make them pass. This applies to both Go and frontend code.

**After every change**, run the verification loop until green:

```bash
# Go — MUST pass before considering work done
go test ./... -count=1                  # All tests (no cache)
go vet ./...                            # Static analysis

# Frontend — MUST pass for UI/frontend changes
pnpm lint                               # ESLint
pnpm build                              # Static export builds

# Full build — for cross-cutting or release changes
make build                              # pnpm build + go build -> ./crewship
```

If any step fails, fix it and re-run. Do NOT move on with failures.

## Testing

```bash
# Go
go test ./...                           # All packages
go test ./internal/api/...              # Single package
go test -run TestRouteAuth ./internal/api/  # Single test by name
go test -v -count=1 ./internal/scrubber/    # Verbose, no cache
go vet ./...                            # Static analysis

# Frontend
pnpm test                               # Vitest (passWithNoTests)
pnpm lint                               # ESLint
pnpm test:e2e                           # Playwright
```

Docker tests auto-skip if the Docker daemon is unavailable (`t.Skip()`).

## Architecture

```
cmd/crewship/         -- Production entry point (single binary with embedded frontend)
cmd/crewshipd/        -- Daemon-only server (dev/legacy)
cmd/crewship-sidecar/ -- Security proxy binary (runs inside agent containers)
internal/api/         -- HTTP API (stdlib http.ServeMux, 50+ endpoints)
internal/auth/        -- JWE token validation (NextAuth v5 compatible)
internal/orchestrator/ -- Agent execution engine (container exec + streaming)
internal/sidecar/     -- Credential-injecting forward proxy
internal/memory/      -- Agent memory FTS5 engine (search, chunk, reindex)
internal/provider/    -- Infrastructure abstraction (Container, Storage, State)
internal/database/    -- SQLite via database/sql (pure Go, no CGO)
internal/ws/          -- WebSocket hub (pub/sub channels)
internal/scrubber/    -- Credential pattern scrubber (13+ patterns)
internal/config/      -- Config: code defaults -> YAML -> env vars (env wins)
app/                  -- Next.js App Router pages (static export for prod)
components/           -- React components (ui/ = shadcn, features/, layout/)
hooks/                -- React hooks (use-auth, use-chat, use-websocket)
lib/                  -- Utilities, Zod schemas, CASL permissions
.claude/context/      -- Authoritative project documentation (KEEP UP TO DATE)
.claude/context/prd/  -- PRD, API, DATABASE, SECURITY, SIDECAR, AGENT-RUNTIME, etc.
.claude/context/wireframes/ -- HTML wireframes for all screens
.factory/context/     -- Legacy source PRD (DO NOT edit, reference only)
.cloud/               -- Generated documentation
```

### Documentation Maintenance

**`.claude/context/`** is the authoritative documentation directory. It contains PRD specs, wireframes, architecture docs, and naming conventions.

- **After every significant PR:** update relevant docs in `.claude/context/` as the final step
- **When unsure about design or requirements:** check `.claude/context/prd/` first before making assumptions

### Key Architectural Decisions

**Single binary with embedded frontend:** `make build` runs Next.js static export (`out/`), copies to `web/out/`, which Go embeds via `//go:embed`. No Node.js server at runtime.

**One container per crew, not per agent:** Multiple agents in the same crew share a Docker container. `Exec` (not `Run`) is used, so concurrent agent processes share a container, isolated only at the process level. Container names: `crewship-team-{slug}`.

**Conversation history in system prompt:** Each CLI invocation is stateless. The orchestrator reads JSONL history files (last 10 messages, max 20k chars) and prepends them to the system prompt as a `[CONVERSATION HISTORY]` block.

**IPC is HTTP-over-Unix-socket:** A second `http.Server` listens on `/tmp/crewship.sock`. The API proxy routes (`/api/v1/agents/{id}/debug`, etc.) forward to this socket. Internal auth uses auto-generated `X-Internal-Token` header.

**No ORM, no query builder:** All database access is raw `database/sql` with `QueryRowContext`/`ExecContext`. Uses `modernc.org/sqlite` (pure Go, no CGO).

**Credential encryption format:** Versioned `"v1:{base64}"` format. The byte layout `IV(16) || AuthTag(16) || Ciphertext` is manually ordered to match the TypeScript frontend (`lib/encryption.ts`), since Go's GCM appends the tag after ciphertext.

### Request Flow for Agent Execution

```
WebSocket message -> Hub.handleSendMessage -> Bridge.HandleChatMessage
  -> Orchestrator.RunAgent:
    1. Select credential (priority-based, cooldown-aware)
    2. Persist run state to bbolt
    3. Inject conversation history into system prompt
    4. Build env vars (real keys if no sidecar, dummy keys + proxy if sidecar)
    5. Start sidecar process (UID 1002) if enabled, health-check it
    6. Build CLI command (adapter-specific: Claude, Codex, Gemini, OpenCode)
    7. container.Exec with timeout, stream output through scrubber
    8. Parse output (NDJSON for Claude Code, plain text for others)
    9. Update run status on completion/error
```

### API Route Structure

Router uses Go 1.22 `http.ServeMux` with `"METHOD /path"` patterns. Two middleware layers:
- `authed` = `RequireAuth` (validates JWE from cookie or Bearer token)
- `wsCtx` = `RequireWorkspace` (looks up workspace membership + role)

`RequireWorkspace` reads `workspace_id` from query param OR `{workspaceId}` path param. Routes under `/workspaces/{workspaceId}/` use the path; routes under crews/agents pass it as a query param.

WebSocket endpoint is `/ws` (registered in `server/routes.go`, separate from the API router).

### Sidecar Proxy

Forward HTTP proxy at `127.0.0.1:9119` inside the container. Credentials piped via base64-encoded stdin (never in env vars or on disk). Agent env vars point `ANTHROPIC_BASE_URL=http://127.0.0.1:9119` so requests go HTTP (not HTTPS) to allow credential injection without TLS termination.

- HTTP requests: allowlist check -> provider detection -> credential header injection -> forward
- HTTPS CONNECT: allowlist check only, no credential injection (bidirectional TCP pipe)
- Default allowlist: `api.anthropic.com`, `api.openai.com`, `generativelanguage.googleapis.com`, `api.factory.ai`

### CLI Adapters (`internal/orchestrator/exec.go`)

- **CLAUDE_CODE**: `claude --print --output-format stream-json --dangerously-skip-permissions` (NDJSON output)
- **CODEX_CLI**: `codex --quiet [--sandbox]` (plain text)
- **GEMINI_CLI**: `gemini [--system-instruction "..."] -p` (plain text)
- **OPENCODE**: `opencode run` + writes `AGENTS.md` for system prompt (plain text)

Credential type `AI_CLI_TOKEN` maps to `CLAUDE_CODE_OAUTH_TOKEN` (OAuth tokens, not API keys).

## Code Conventions

### Go Backend
- **No ORM**: `database/sql` with direct SQL queries
- **Provider pattern**: All infra through interfaces (`ContainerProvider`, `StorageProvider`, `StateProvider`)
- **Error format**: RFC 7807 Problem Details
- **Naming**: `snake_case` for DB tables/columns, plural table names, CUID IDs (see `internal/api/cuid.go`)
- **Imports**: stdlib first, then external, then internal
- **Tests**: Table-driven, `t.Skip()` for optional deps (Docker)

### Frontend (TypeScript/React)
- **Framework**: Next.js 16 with App Router, static export
- **Styling**: Tailwind CSS 4, Radix UI primitives via shadcn/ui
- **Auth**: Custom `useAuth` hook (`hooks/use-auth.tsx`), NOT the `next-auth` package
- **State**: React hooks for local state, custom hooks for shared state
- **Validation**: Zod schemas
- **RBAC**: CASL (`lib/permissions/abilities.ts`)
- **Runtime**: Node.js >= 22, pnpm only (not npm/yarn)
- **Path alias**: `@/*` maps to project root

### Auth Flow
JWE tokens are NextAuth v5 compatible. Key derivation: `HKDF-SHA256(NEXTAUTH_SECRET, salt="authjs.session-token")`. Token set as `authjs.session-token` HttpOnly cookie (30 days). WebSocket auth: client fetches short-lived token via `GET /api/v1/ws-token`, passes as `?token=` query param.

### Database Migrations

Migrations live in `internal/database/migrate.go` as numbered Go constants. To add a migration:
1. Add `const migrationXxx = \`SQL\`` at the bottom of `migrate.go`
2. Append `{N, "descriptive_name", migrationXxx}` to the `migrations` slice (N = previous + 1)
3. Runs automatically at startup; tracked in `_migrations` table

Timestamps stored as TEXT (RFC 3339). Soft deletes via `deleted_at` column.

## Key Environment Variables

```
CREWSHIP_PORT=8080
CREWSHIP_SIDECAR_ENABLED=true
NEXTAUTH_SECRET=<required>          # Auth token encryption key
ENCRYPTION_KEY=<required>           # AES-256-GCM for credential vault
CREWSHIP_CONTAINER_PROVIDER=docker  # docker (MVP) | k8s (enterprise)
CREWSHIP_STORAGE_PROVIDER=localfs   # localfs (MVP) | s3 (enterprise)
CREWSHIP_STATE_PROVIDER=bbolt       # bbolt (MVP) | postgres (enterprise)
CREWSHIP_LOG_LEVEL=info
DATABASE_URL=                       # Optional PostgreSQL connection string
```

## NEVER DO (learned from past mistakes)

- **Never skip the verification loop.** Every change must pass `go test` + `go vet` before it's done.
- **Never implement without tests.** Write tests first, then code. No exceptions.
- **Never edit `.factory/context/`** — it's legacy reference only. Edit `.claude/context/` instead.
- **Never add `Co-Authored-By`** lines to commit messages.
- **Never commit secrets** (`.env.local`, real API keys, credentials).
- **Never use `require()` / CommonJS** in frontend code — ES modules only.
- **Never guess URLs or API endpoints** — read the router (`internal/api/router.go`) or code.
- **Never add GitLab remotes** — this project uses GitHub only.
- **Never use `interface{}` slices** when a typed slice is expected in Go (causes compile errors).
- **Never amend commits** after pre-commit hook failure — create a new commit instead.
- **Never use `"sqlite3"` as driver name** — `modernc.org/sqlite` registers as `"sqlite"`.
- **Never add API routes to `app/`** — static export silently excludes them. They work in dev, break in prod.
- **Never run `prisma migrate`** — Prisma is for TS type generation only. Migrations are Go-only in `migrate.go`.
- **Never change GCM byte layout** in `internal/encryption/` — the custom `IV||AuthTag||Ciphertext` order is for Go/TS compatibility. Changing it makes all stored credentials undecryptable.
- **Never change sidecar UID (1002) or agent UID (1001)** — the UID separation is a security boundary preventing the agent from reading sidecar memory.
- **Never use `npm` or `yarn`** — `pnpm` only. pnpm-specific config will break with other package managers.

When you make a new mistake, add it here so it never happens again. This file is a living document — updating it is part of every significant PR.

Commit style: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:` (conventional commits). Branch from `main`.
