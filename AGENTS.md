<coding_guidelines>
# Crewship -- AI Agent Orchestration Platform

> Open-source platform for managing AI "virtual employees" in crews.
> Brand: **Crewship** (Crew + Ship + "-ship" suffix -- triple meaning)
> Domain: crewship.ai | GitHub: github.com/crewship-ai | npm: @crewship/*
> GitLab: `git@github.com:crewship-ai/crewship.git`


---

## Status

**Phase:** MVP (UI + backend wired, agent runtime operational).
**Architecture:** Single Go binary with embedded static UI (Next.js static export).

## Single Binary Architecture

```
Go (crewship binary):  HTTP API, auth, DB, WebSocket, Docker orchestration, embedded UI
TypeScript (Next.js):  UI only (static export -- HTML/CSS/JS, NO server, NO API routes)
```

The Go binary embeds the Next.js static build via `embed.FS` and serves everything
from a single process. There is NO separate Next.js server at runtime.

## Runtime Versions

```
Go:       1.25          (production runtime)
Node.js:  25.x          (build-time only -- Next.js static export, Prisma type generation)
pnpm:     10.x          (build-time only)
```

## Core Commands

```bash
# Production (single binary)
crewship start            # Start everything (SQLite, localhost:8080)
crewship start --port 9090  # Custom port
crewship version          # Version info
crewship doctor           # Diagnostics (Docker, ports, DB)

# Dev environment (two processes for hot-reload)
./dev.sh start        # Start Go + Next.js dev servers in background
./dev.sh stop         # Stop both
./dev.sh restart      # Stop then start
./dev.sh status       # Show status
./dev.sh logs         # Tail combined logs
make up               # Alias for ./dev.sh start
make down             # Alias for ./dev.sh stop

# Frontend (build-time only)
pnpm dev --port 3001  # Next.js dev server (HMR, localhost:3001)
pnpm build            # Static export to out/ (MUST pass before commit)
pnpm test             # Vitest test suite
pnpm lint             # ESLint
pnpm db:generate      # Prisma generate (type generation only)

# Backend (Go)
go run ./cmd/crewship     # Single binary dev
go build ./cmd/crewship   # Build binary
go test ./...             # Go tests

# Build single binary (production)
make build            # pnpm build → cp out web/out → go build
```

## Dev Environment

In development, two processes run for hot-reload:
- **Go server** (port 8080): API, auth, WebSocket, Docker orchestration
- **Next.js dev server** (port 3001): UI with HMR (proxies API to Go)

```bash
./dev.sh start     # Starts both in background
./dev.sh status    # Verify services
./dev.sh logs      # Watch output
./dev.sh stop      # Clean shutdown
```

| Service | Port | Purpose |
|---|---|---|
| crewship (Go) | 8080 | API, auth, WebSocket, Docker orchestration, embedded UI |
| Next.js dev | 3001 | UI hot-reload (dev only, proxies /api/ to :8080) |

> In production, only the single binary runs. Next.js is embedded as static files.

PID files: `/tmp/crewship-{next,go}.pid` -- Logs: `/tmp/crewship-{next,go}.log`

## Project Layout

> Legend: ✅ exists now | 📋 create when implementing that feature (do NOT pre-create empty dirs)

```
cmd/
  └─ crewship/main.go         ✅ Single binary entry point (start/version/doctor)
internal/                      ✅ Go internal packages
  ├─ api/                      ✅ HTTP API layer (50+ routes)
  │   ├─ router.go             ✅ Route registration with auth middleware
  │   ├─ middleware.go          ✅ JWT auth (NextAuth JWE), workspace context, RBAC
  │   ├─ nextauth.go           ✅ NextAuth-compatible endpoints (csrf/session/login/signout)
  │   ├─ auth.go               ✅ Signup (bcrypt), ws-token
  │   ├─ workspaces.go         ✅ Full CRUD + members + invitations
  │   ├─ crews.go              ✅ Full CRUD + members
  │   ├─ agents.go             ✅ Full CRUD + skills/credentials/chats/runs sub-resources
  │   ├─ credentials.go        ✅ Full CRUD with AES-256-GCM encryption
  │   ├─ skills.go             ✅ List with filters
  │   ├─ runs.go               ✅ List with pagination + stats
  │   ├─ audit.go              ✅ List with filters
  │   ├─ admin.go              ✅ Stats, users, workspaces (OWNER only)
  │   ├─ proxy.go              ✅ Crewshipd IPC proxy (agent debug/files/logs/stop)
  │   ├─ internal.go           ✅ Internal routes for crewshipd (decrypted credentials)
  │   └─ static.go             ✅ SPA-aware static file server (embedded Next.js)
  ├─ database/                 ✅ Pure-Go SQLite (modernc.org/sqlite, no CGO)
  │   ├─ database.go           ✅ Open, WAL mode, pragmas
  │   └─ migrate.go            ✅ Migration system (20 tables)
  ├─ auth/                     ✅ JWT validation + creation (NextAuth JWE compatible)
  ├─ encryption/               ✅ AES-256-GCM (cross-compatible with TS format v1:)
  ├─ server/                   ✅ HTTP + IPC server, combined SPA + API handler
  ├─ config/                   ✅ YAML config parser
  ├─ provider/                 ✅ Provider interfaces (Container, Storage, State)
  │   ├─ docker/               ✅ Docker implementation (MVP)
  │   ├─ localfs/              ✅ Local filesystem implementation
  │   └─ bbolt/                ✅ bbolt WAL implementation
  ├─ ws/                       ✅ WebSocket gateway
  ├─ orchestrator/             ✅ Agent job orchestration
  ├─ webhook/                  ✅ Webhook ingress handler
  └─ logcollector/             ✅ JSONL log collection
web/
  └─ embed.go                  ✅ go:embed for web/out/ static files
app/                           → Next.js frontend (static export only)
  ├─ (auth)/                   ✅ Login, signup pages (client components)
  ├─ (dashboard)/              ✅ Dashboard route group (client components)
  ├─ globals.css               ✅ Design tokens (Tailwind v4, @theme inline, oklch)
  └─ layout.tsx                ✅ Root layout (Inter + JetBrains Mono)
components/
  ├─ ui/                       ✅ shadcn/ui primitives
  └─ features/                 📋 Feature components (chat, file-browser, terminal)
lib/
  ├─ utils.ts                  ✅ Re-exports cn()
  └─ utils/cn.ts               ✅ clsx + tailwind-merge
prisma/
  └─ schema.prisma             ✅ DB schema (type generation only, NOT runtime ORM)
docker/
  ├─ agent-runtime/            📋 Agent container Dockerfile
  ├─ docker-compose.yml        ✅ PostgreSQL 16 (optional, for PG mode)
  └─ docker-compose.prod.yml   ✅ Production single binary deployment
config/
  └─ rate-limits.yml           ✅ Rate limiting rules per endpoint
.factory/
  └─ context/                  ✅ AI knowledge base (PRD, architecture, security, etc.)
```

## Tech Stack

| Layer | Technology |
|---|---|
| **Runtime** | Go single binary (`crewship`) |
| Frontend | Next.js (static export), React, Tailwind CSS 4, shadcn/ui (new-york) |
| Icons | lucide-react (ONLY allowed icon library) |
| Auth | Go (NextAuth-compatible JWE endpoints in `internal/api/`) |
| Database | SQLite (default, embedded) or PostgreSQL (opt-in) |
| DB access | Go `database/sql` (direct queries, NO ORM) |
| WebSocket | Go native (goroutines) |
| Docker mgmt | Docker SDK for Go |
| Job state | bbolt (embedded KV, WAL) |
| File watch | fsnotify (inotify) |
| Metrics | Prometheus (Go native) |
| Logs | JSONL on filesystem + logrotate |
| Agent output | Persistent filesystem (/output/ bind mount) |
| State mgmt | Zustand (client-side only) |
| Validation | Zod (client-side), Go validation (server-side) |
| RBAC | Go middleware (`internal/api/middleware.go`) |
| Encryption | AES-256-GCM (Go `internal/encryption/`) |
| Design tokens | `app/globals.css` (oklch, tweakcn.com) |
| Linting | ESLint 9 (pinned -- v10 awaiting @typescript-eslint support) |

## Architecture

```
User → Browser → Go HTTP server (port 8080)
                   ├─ Static UI (embedded Next.js build via embed.FS)
                   ├─ REST API (/api/v1/*) → SQLite/PostgreSQL
                   ├─ Auth (/api/auth/*) → JWT (NextAuth-compatible JWE)
                   └─ WebSocket → Docker exec → CLI session → LLM API
                                        ↓
                                stdout → JSONL logs
                                        ↓
                                /output/ → persistent files

External → Webhook (Go) → Agent trigger → same flow as above
```

## Storage Model

```
EPHEMERAL (container):     /workspace/  ← agent scratch space, disposable
PERSISTENT (host):         /output/     ← agent deliverables, survives everything
LOGS (host + logrotate):   /var/log/crewship/  ← JSONL, rotated hourly
CHATS (host):              /var/lib/crewship/chats/  ← JSONL per session
DATABASE:                  ~/.crewship/crewship.db  ← SQLite (default)
```

## Conventions

### TypeScript (frontend -- static export only)
- Strict mode, no `any`, prefer interfaces for public APIs
- JSDoc for exported functions
- Zod for client-side validation
- Named exports (no default exports except pages/layouts)
- All pages are client components (no server components, no server actions)
- API calls via fetch to `/api/v1/*` (handled by Go server)

### Go (backend -- single binary)
- Standard Go project layout (cmd/, internal/)
- No frameworks -- stdlib + minimal dependencies
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Context propagation on all functions
- Structured logging (JSON to stdout)
- **Provider pattern**: NEVER access Docker/filesystem/bbolt directly -- use provider interfaces
- Provider selection via env var: `CREWSHIP_CONTAINER_PROVIDER=docker|k8s`
- Always check `sql.ErrNoRows` vs real DB errors
- Always check `rows.Err()` after row iteration loops

### UI
- **ONLY** shadcn/ui components (`npx shadcn@latest add [name]`)
- **ONLY** lucide-react for icons
- Tailwind CSS 4 only (no inline styles, no tailwind.config.ts)
- Design tokens in `app/globals.css` via `@theme inline`
- Responsive: mobile-first, use `md:` and `lg:` breakpoints
- **Layout:** 3-layer -- Top Toolbar (dark, full-width, h-12) + Sidebar (256px) + Main
- **Top Toolbar:** Logo, Workspace Switcher, Search (Cmd+K), Docs, Notifications (bell+badge), Settings, User avatar
- **Sidebar:** Navigation only (no logo, no user footer -- both moved to toolbar)
- **Workspace Switcher:** Multi-workspace support, dropdown in toolbar, changes session `currentWorkspaceId`

### Database
- **SQLite** is the default database (embedded, zero deps)
- Go accesses DB directly via `database/sql` (NO Prisma at runtime)
- Prisma schema is used ONLY for TypeScript type generation (`pnpm db:generate`)
- Go migration system manages the actual schema (`internal/database/migrate.go`)
- NO logs in database. NO chat messages in database.
- Logs -> JSONL files. Chat messages -> JSONL files (one file per session).
- Chat metadata (id, agent, title, status, timestamps) -> database.
- Credential pool: agent can have MULTIPLE credentials for same env var (priority-based failover).

### Security (CRITICAL)
- Credentials ALWAYS encrypted with AES-256-GCM (key versioning: `v1:base64data`)
- Encryption/decryption in Go (`internal/encryption/`)
- Agent containers: non-root (UID 1001), --internal network, no internet (except LLM allowlist)
- Agent CANNOT escape container, CANNOT escalate to root
- RBAC check on EVERY API endpoint (Go middleware)
- CSRF token validation with cookie (login flow)
- Constant-time token comparison for internal auth
- Path traversal validation on file download routes
- Webhook auth via per-agent secret token
- Audit log: append-only (chattr +a in production)
- Never log plaintext credentials, API keys, or secrets

### Git Workflow (GitHub Flow)
- **`main` branch:** Production-ready, protected. NEVER push directly.
- **Feature branches:** `feature/description` or `fix/issue-name`, always from `main`
- **All changes via PR:** feature branch -> PR -> CodeRabbit review -> merge to main
- **Releases:** Tagged from main (`v0.1.0`, `v0.2.0`, etc.)
- **CI runs on every PR:** lint, typecheck, build, test, Go check
- **Conventional commits:** `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`
- **Remote:** `GitHub` -> `https://github.com/crewship-ai/crewship.git`
- **Working branch:** Always work on feature branch, never on main

## Roles (RBAC)

| Role | See all crews | Create agents | Manage credentials | Audit access |
|------|:---:|:---:|:---:|:---:|
| OWNER | Yes | Yes | Yes | All |
| ADMIN | Yes | Yes | Yes | All |
| MANAGER | Assigned only | In assigned crews | Crew-level | Crew |
| MEMBER | Assigned only | No | No | Own actions |
| VIEWER | Assigned only | No | No | None |

## CLI Tools & Workflows

- **Always use `gh` CLI** for GitHub operations (PRs, issues, reviews, comments)
- **Always use `git` CLI** for version control (never edit `.git/` directly)
- Prefer CLI tools over web UI when possible (faster, scriptable, auditable)
- Use `gh pr view`, `gh pr checks`, `gh api` to inspect PR status and reviews
- Use `gh pr comment` to respond to CodeRabbit or reviewers
- Use `pnpm` (not npm/yarn) for all Node.js package management

## Change Documentation (MANDATORY)

Every code change MUST be documented. No exceptions.

- **Before starting:** Read relevant docs in `.factory/context/` to understand current state.
- **After every code change:** Update ALL affected documentation files immediately.
  This includes: `AGENTS.md`, `.factory/context/architecture.md`, `.factory/context/prd/*.md`,
  `.factory/context/TODO.md`, `.factory/context/STRATEGY-2026.md`, and any other relevant docs.
- **What to update:** API changes → `prd/API.md`. DB changes → `prd/DATABASE.md`.
  Security changes → `prd/SECURITY.md`. New features → `TODO.md` + `STRATEGY-2026.md` (Section 0).
  Architecture changes → `architecture.md` + `AGENTS.md`.
- **AGENTS.md is the most critical file** -- it is read by every AI session. Keep it accurate.
- **STRATEGY-2026.md Section 0** ("Implementation Status") must reflect reality at all times.
  Move items from "PLANOVANO" to "IMPLEMENTOVANO" only when fully working and tested.
- **Commit docs together with code** -- never leave docs out of sync, even for one commit.

## What NOT To Do

- Do NOT use any UI library other than shadcn/ui
- Do NOT use any icon library other than lucide-react
- Do NOT use `tailwind.config.ts` (Tailwind v4 = CSS-first)
- Do NOT store credentials in plain text
- Do NOT store logs or chats in database
- Do NOT skip RBAC checks on API endpoints
- Do NOT give containers root access
- Do NOT create empty placeholder directories (create when needed)
- Do NOT create documentation files unless explicitly asked
- Do NOT pre-create empty files for "future use"
- Do NOT use Prisma at runtime (type generation only)
- Do NOT add Next.js API routes (all API is in Go)
- Do NOT use server components or server actions (static export)

## Environment Variables

```bash
# Required
NEXTAUTH_SECRET=           # openssl rand -base64 32 (JWT signing)
ENCRYPTION_KEY=            # openssl rand -hex 32 (credential encryption)

# Optional (defaults shown)
CREWSHIP_PORT=8080         # HTTP port
DATABASE_URL=              # Default: ~/.crewship/crewship.db (SQLite)
                           # PostgreSQL: postgresql://user:pass@host/db

# Provider selection (defaults)
CREWSHIP_CONTAINER_PROVIDER=docker    # docker | k8s
CREWSHIP_STORAGE_PROVIDER=localfs     # localfs | s3
CREWSHIP_STATE_PROVIDER=bbolt         # bbolt | postgres
```

## Development Environment

- **Local dev:** Mac Mini 16GB (macOS) -- `./dev.sh start` (Go + Next.js hot-reload)
- **Staging:** Coolify on Proxmox (128GB RAM, i7-12700) -- Docker image
- **Production:** Single binary or Docker image

## Key Documentation (in .factory/context/)

| Document | What's in it |
|---|---|
| `prd/DATABASE.md` | Full schema (20 tables), credential pool pattern, JSONL format |
| `prd/SECURITY.md` | Threat model, isolation layers, OWASP, credential encryption |
| `prd/AGENT-RUNTIME.md` | Container lifecycle, Docker exec, key failover, loop modes, mission runtime |
| `prd/ORCHESTRATION.md` | **Lead + Coordinator**: 3-level hierarchy, assignment protocol, industry context |
| `prd/API.md` | REST API (Go routes), WebSocket, webhook API |
| `prd/DEPLOYMENT.md` | Single binary distribution, Docker image, GoReleaser |
| `architecture.md` | Single binary arch, data flows, container model, RBAC |
| `business.md` | Positioning, competition (vs OpenClaw, n8n, CrewAI), mission differentiator |
| `TODO.md` | Product summary, OpenClaw comparison, phased task list |
| `K8S-READINESS.md` | Provider interfaces, K8s manifests, migration path Docker->K8s |
</coding_guidelines>
