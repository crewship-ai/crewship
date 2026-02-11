# Crewship -- AI Agent Orchestration Platform

> Open-source platform for managing AI "virtual employees" in teams.
> Brand: **Crewship** (Crew + Ship + "-ship" suffix -- triple meaning)
> Domain: crewship.ai | GitHub: github.com/crewship-ai | npm: @crewship/*

---

## Two-Language Project

```
TypeScript (Next.js):  UI, CRUD API, auth, Prisma ORM
Go (crewshipd):        WebSocket, Docker orchestration, logs, files, webhooks
```

Communication: Unix socket (local) or gRPC (K8s).

## Core Commands

```bash
# Frontend (TypeScript)
pnpm dev              # Next.js dev server (localhost:3000)
pnpm build            # Production build (MUST pass before commit)
pnpm test             # Vitest test suite
pnpm lint             # ESLint
pnpm db:generate      # Prisma generate (after schema changes)
pnpm db:push          # Push schema to DB
pnpm db:studio        # Prisma Studio (DB browser)

# Backend (Go)
go run ./cmd/crewshipd    # Go service dev (localhost:8080)
go build ./cmd/crewshipd  # Build binary
go test ./...             # Go tests

# Infrastructure
docker compose -f docker/docker-compose.yml up -d   # PostgreSQL
```

## Project Layout

```
app/                          → Next.js frontend (TypeScript)
  ├─ (auth)/                  → Login, signup, OAuth
  ├─ (dashboard)/             → Dashboard (teams, agents, files, settings)
  ├─ api/v1/                  → REST API for frontend
  ├─ globals.css              → Design tokens (Tailwind v4, @theme inline)
  └─ layout.tsx               → Root layout (Inter + JetBrains Mono)
components/
  ├─ ui/                      → shadcn/ui primitives (ONLY these)
  └─ features/                → Feature components (chat, file-browser, terminal)
lib/
  ├─ services/                → Business logic (*.service.ts)
  ├─ permissions/             → RBAC (CASL-based)
  ├─ encryption.ts            → Credentials vault (AES-256-GCM)
  ├─ types/                   → TypeScript types
  └─ utils/                   → Shared utilities
prisma/
  └─ schema.prisma            → DB schema (source of truth)
cmd/
  └─ crewshipd/               → Go service entrypoint
      └─ main.go
internal/                     → Go internal packages
  ├─ docker/                  → Docker container management
  ├─ ws/                      → WebSocket gateway
  ├─ orchestrator/            → Agent job orchestration
  ├─ logcollector/            → JSONL log collection
  ├─ fileserver/              → File serving + fsnotify
  ├─ webhook/                 → Webhook ingress handler
  └─ config/                  → YAML config parser
docker/
  ├─ agent-runtime/           → Agent container Dockerfile
  └─ docker-compose.yml       → PostgreSQL (local dev)
skills/
  ├─ registry/                → Built-in skills
  └─ templates/               → Skill YAML templates
.factory/
  └─ context/                 → AI knowledge base
```

## Tech Stack

| Layer | Technology |
|---|---|
| Frontend | Next.js, React, Tailwind CSS 4, shadcn/ui (new-york) |
| Icons | lucide-react (ONLY allowed) |
| Auth | NextAuth.js (Auth.js v5) |
| ORM | Prisma (ONLY DB access, from Next.js only) |
| Database | PostgreSQL (local Docker, structured data only) |
| Backend | Go (`crewshipd` binary) |
| WebSocket | Go native (goroutines) |
| Docker mgmt | Docker SDK for Go |
| Job state | bbolt (embedded KV, WAL) |
| File watch | fsnotify (inotify) |
| Metrics | Prometheus (Go native) |
| Logs | JSONL on filesystem + logrotate |
| Agent output | Persistent filesystem (/output/ bind mount) |
| State mgmt | Zustand (client) |
| Validation | Zod |
| RBAC | CASL |
| Design tokens | `app/globals.css` (oklch, tweakcn.com) |

## Architecture

```
User → Chat UI → WebSocket (Go) → Docker exec → CLI session → LLM API
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
CONVERSATIONS (host):      /var/lib/crewship/conversations/  ← JSONL per session
```

## Conventions

### TypeScript (frontend)
- Strict mode, no `any`, prefer interfaces for public APIs
- JSDoc for exported functions
- Zod for all validation

### Go (backend)
- Standard Go project layout (cmd/, internal/)
- No frameworks -- stdlib + minimal dependencies
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Context propagation on all functions
- Structured logging (JSON to stdout)

### UI
- **ONLY** shadcn/ui components (`npx shadcn@latest add [name]`)
- **ONLY** lucide-react for icons
- Tailwind CSS 4 only (no inline styles, no tailwind.config.ts)
- Design tokens in `app/globals.css` via `@theme inline`

### Database
- **Prisma schema** = source of truth
- PostgreSQL for structured data ONLY (users, teams, agents, credentials, session metadata)
- NO logs in PostgreSQL. NO conversation messages in PostgreSQL.
- Logs → JSONL files. Conversation messages → JSONL files (one file per session).
- Conversation session metadata (id, agent, title, status, timestamps) → PostgreSQL.

### Security (CRITICAL)
- Credentials ALWAYS encrypted with AES-256-GCM
- Agent containers: non-root (UID 1001), --internal network, no internet (except LLM allowlist)
- Agent CANNOT escape container, CANNOT escalate to root
- RBAC check on EVERY API endpoint
- Webhook auth via per-agent secret token
- Audit log: append-only (chattr +a)

### Git Workflow
- Feature branches: `feature/description` or `fix/issue-name`
- Conventional commits: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`
- PR to `develop` first, then `develop` → `main` for release

## Roles (RBAC)

| Role | See all teams | Create agents | Manage credentials | Audit access |
|------|:---:|:---:|:---:|:---:|
| OWNER | Yes | Yes | Yes | All |
| ADMIN | Yes | Yes | Yes | All |
| MANAGER | Assigned only | In assigned teams | Team-level | Team |
| MEMBER | Assigned only | No | No | Own actions |
| VIEWER | Assigned only | No | No | None |

## What NOT To Do

- Do NOT use any UI library other than shadcn/ui
- Do NOT use any icon library other than lucide-react
- Do NOT use `tailwind.config.ts` (Tailwind v4 = CSS-first)
- Do NOT store credentials in plain text
- Do NOT store logs or conversations in PostgreSQL
- Do NOT skip RBAC checks on API endpoints
- Do NOT give containers root access
- Do NOT create documentation files unless explicitly asked

## Environment Variables

```bash
DATABASE_URL=postgresql://crewship:crewship@localhost:5432/crewship
NEXTAUTH_SECRET=           # openssl rand -base64 32
NEXTAUTH_URL=http://localhost:3000
ENCRYPTION_KEY=            # openssl rand -hex 32
CREWSHIPD_SOCKET=/tmp/crewship.sock   # Unix socket path
```

## Development Environment

- **Local dev:** Mac Mini 16GB -- Docker Compose (PostgreSQL), Next.js + Go natively
- **Staging:** Coolify on Proxmox (128GB RAM, i7-12700)
- **Production:** TBD
