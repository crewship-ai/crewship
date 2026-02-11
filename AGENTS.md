# Crewship -- AI Agent Orchestration Platform

> Open-source platform for managing AI "virtual employees" in teams.
> Brand: **Crewship** (Crew + Ship + "-ship" suffix -- triple meaning)
> Domain: crewship.ai | GitHub: github.com/crewship-ai | npm: @crewship/*
> GitLab: `ssh://git@gitlab.unifylab.cz:2222/development/crewship.git`
> Company: Unify Technology s.r.o.

---

## Status

**Phase:** Pre-scaffolding (no `package.json` yet, no `node_modules`).
`pnpm dev` will NOT work until Phase 1 scaffolding is done.

## Two-Language Project

```
TypeScript (Next.js):  UI, CRUD API, auth, Prisma ORM
Go (crewshipd):        WebSocket, Docker orchestration, logs, files, webhooks
```

Communication: Unix socket (local) or gRPC (K8s).

## Runtime Versions

```
Node.js:  25.x (engines >=22)
pnpm:     10.x
Go:       1.25
```

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

> Legend: ✅ exists now | 📋 create when implementing that feature (do NOT pre-create empty dirs)

```
app/                          → Next.js frontend (TypeScript)
  ├─ (auth)/                  📋 Login, signup, OAuth
  ├─ (dashboard)/             ✅ Dashboard route group
  │   └─ page.tsx             ✅ Dashboard root page
  ├─ api/v1/                  📋 REST API routes
  ├─ globals.css              ✅ Design tokens (Tailwind v4, @theme inline, oklch)
  └─ layout.tsx               ✅ Root layout (Inter + JetBrains Mono)
components/
  ├─ ui/                      📋 shadcn/ui primitives (regenerate: npx shadcn@latest add)
  └─ features/                📋 Feature components (chat, file-browser, terminal)
lib/
  ├─ services/                📋 Business logic (*.service.ts)
  ├─ permissions/             📋 RBAC (CASL-based)
  ├─ encryption.ts            ✅ Credentials vault (AES-256-GCM, key versioning v1:)
  ├─ types/                   📋 TypeScript types
  ├─ utils.ts                 ✅ Re-exports cn()
  └─ utils/cn.ts              ✅ clsx + tailwind-merge
prisma/
  └─ schema.prisma            ✅ DB schema (placeholder — populate from DATABASE.md)
cmd/
  └─ crewshipd/main.go        ✅ Go service entrypoint (signal handling placeholder)
internal/                     📋 Go internal packages
  ├─ provider/                📋 Provider interfaces (Container, Storage, State)
  │   ├─ container.go         📋 ContainerProvider interface
  │   ├─ storage.go           📋 StorageProvider interface
  │   ├─ state.go             📋 StateProvider interface
  │   ├─ docker/              📋 Docker implementation (MVP)
  │   ├─ k8s/                 📋 Kubernetes implementation (Enterprise)
  │   ├─ localfs/             📋 Local filesystem implementation (MVP)
  │   ├─ s3/                  📋 S3/MinIO implementation (Enterprise)
  │   ├─ bbolt/               📋 bbolt WAL implementation (MVP)
  │   └─ pgstate/             📋 PostgreSQL state implementation (Enterprise)
  ├─ ws/                      📋 WebSocket gateway
  ├─ orchestrator/            📋 Agent job orchestration + credential pool
  ├─ webhook/                 📋 Webhook ingress handler
  └─ config/                  📋 YAML config parser
docker/
  ├─ agent-runtime/           📋 Agent container Dockerfile
  └─ docker-compose.yml       ✅ PostgreSQL 16 (local dev)
skills/                       📋 Created when skill system is implemented
config/
  └─ rate-limits.yml          ✅ Rate limiting rules per endpoint
.factory/
  └─ context/                 ✅ AI knowledge base (PRD, architecture, security, etc.)
```

## Tech Stack

| Layer | Technology |
|---|---|
| Frontend | Next.js, React, Tailwind CSS 4, shadcn/ui (new-york) |
| Icons | lucide-react (ONLY allowed icon library) |
| Auth | NextAuth.js (Auth.js v5) with Prisma adapter |
| ORM | Prisma (ONLY DB access, from Next.js only) |
| Database | PostgreSQL 16 (local Docker, structured data only) |
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
- Named exports (no default exports except pages/layouts)

### Go (backend)
- Standard Go project layout (cmd/, internal/)
- No frameworks -- stdlib + minimal dependencies
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Context propagation on all functions
- Structured logging (JSON to stdout)
- **Provider pattern**: NEVER access Docker/filesystem/bbolt directly — use provider interfaces
- Provider selection via env var: `CREWSHIP_CONTAINER_PROVIDER=docker|k8s`

### UI
- **ONLY** shadcn/ui components (`npx shadcn@latest add [name]`)
- **ONLY** lucide-react for icons
- Tailwind CSS 4 only (no inline styles, no tailwind.config.ts)
- Design tokens in `app/globals.css` via `@theme inline`
- Responsive: mobile-first, use `md:` and `lg:` breakpoints

### Database
- **Prisma schema** = source of truth (19 tables defined in `.factory/context/prd/DATABASE.md`)
- PostgreSQL for structured data ONLY (users, teams, agents, credentials, session metadata)
- NO logs in PostgreSQL. NO conversation messages in PostgreSQL.
- Logs → JSONL files. Conversation messages → JSONL files (one file per session).
- Conversation session metadata (id, agent, title, status, timestamps) → PostgreSQL.
- Credential pool: agent can have MULTIPLE credentials for same env var (priority-based failover).

### Security (CRITICAL)
- Credentials ALWAYS encrypted with AES-256-GCM (key versioning: `v1:base64data`)
- Agent containers: non-root (UID 1001), --internal network, no internet (except LLM allowlist)
- Agent CANNOT escape container, CANNOT escalate to root
- RBAC check on EVERY API endpoint
- Webhook auth via per-agent secret token
- Audit log: append-only (chattr +a in production)
- Never log plaintext credentials, API keys, or secrets

### Git Workflow
- Main branch: `main` (direct push for now, PRs later)
- Feature branches: `feature/description` or `fix/issue-name`
- Conventional commits: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`
- Remote: `origin` → `ssh://git@gitlab.unifylab.cz:2222/development/crewship.git`

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
- Do NOT create empty placeholder directories (create when needed)
- Do NOT create documentation files unless explicitly asked
- Do NOT pre-create empty files for "future use"

## Environment Variables

```bash
DATABASE_URL=postgresql://crewship:crewship@localhost:5432/crewship
NEXTAUTH_SECRET=           # openssl rand -base64 32
NEXTAUTH_URL=http://localhost:3000
ENCRYPTION_KEY=            # openssl rand -hex 32
CREWSHIPD_URL=unix:///tmp/crewship.sock   # MVP: unix socket, K8s: http://crewshipd:8080

# Provider selection (MVP defaults)
CREWSHIP_CONTAINER_PROVIDER=docker    # docker | k8s
CREWSHIP_STORAGE_PROVIDER=localfs     # localfs | s3
CREWSHIP_STATE_PROVIDER=bbolt         # bbolt | postgres
```

## Development Environment

- **Local dev:** Mac Mini 16GB (macOS) -- Docker Compose (PostgreSQL), Next.js + Go natively
- **Staging:** Coolify on Proxmox (128GB RAM, i7-12700)
- **Production:** TBD

## Key Documentation (in .factory/context/)

| Document | What's in it |
|---|---|
| `prd/DATABASE.md` | Full Prisma schema (19 tables), credential pool pattern, JSONL format |
| `prd/SECURITY.md` | Threat model, isolation layers, OWASP, credential encryption |
| `prd/AGENT-RUNTIME.md` | Container lifecycle, Docker exec, key failover, loop modes |
| `prd/API.md` | REST API, IPC protocol, WebSocket, webhook API |
| `prd/DEPLOYMENT.md` | Coolify deployment, Docker images, networking |
| `architecture.md` | Two-process arch, data flows, container model, RBAC |
| `business.md` | Positioning, competition (vs OpenClaw, n8n), examples |
| `TODO.md` | Product summary, OpenClaw comparison, phased task list |
| `K8S-READINESS.md` | Provider interfaces, K8s manifests, migration path Docker→K8s |
