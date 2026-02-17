# Architecture

> Crewship -- Open-source AI agent orchestration platform
> "Linux machine where AI employees work. You give them instructions, credentials
> and skills. They work 24/7 and you download the results in the morning."

---

## Single Binary Architecture

Crewship bezi jako **jeden Go binary** (`crewship`). Next.js je staticky
exportovany (HTML/CSS/JS) a embedded pres `go:embed`. Zadny separatni
Node.js server, zadne API routes v Next.js, zadny Unix socket IPC.

```
+------------------------------------------------------------------+
|                     crewship (Go binary)                         |
|                                                                  |
|  ┌─────────────────────────────────────────────────────────────┐ |
|  │  Embedded static UI (Next.js static export via embed.FS)   │ |
|  │  React, shadcn/ui, Tailwind CSS 4, client components only  │ |
|  └─────────────────────────────────────────────────────────────┘ |
|                                                                  |
|  ┌──────────────┐  ┌──────────────┐  ┌───────────────────────┐  |
|  │ REST API     │  │ Auth         │  │ WebSocket Gateway     │  |
|  │ /api/v1/*    │  │ /api/auth/*  │  │ (native goroutines)   │  |
|  │ (50+ routes) │  │ NextAuth JWE │  │                       │  |
|  └──────┬───────┘  └──────┬───────┘  └───────────┬───────────┘  |
|         │                 │                       │              |
|  ┌──────┴─────────────────┴───────────────────────┴───────────┐  |
|  │  SQLite (default, embedded) or PostgreSQL (opt-in)         │  |
|  │  Go database/sql (direct queries, NO ORM)                  │  |
|  └────────────────────────────────────────────────────────────┘  |
|                                                                  |
|  ┌────────────┐  ┌────────────┐  ┌─────────────┐  ┌──────────┐ |
|  │ Docker     │  │ bbolt      │  │ LocalFS     │  │ Log      │ |
|  │ orchestr.  │  │ WAL state  │  │ storage     │  │ collect. │ |
|  └─────┬──────┘  └────────────┘  └─────────────┘  └──────────┘ |
|        │                                                         |
+--------+---------------------------------------------------------+
         │
         │  Docker SDK (agent containers only)
         │
    ┌────┴──────────────────────────────────────────┐
    │  crewship-agents network (--internal)         │
    │                                               │
    │  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
    │  │ Crew A   │  │ Crew B   │  │ Output     │  │
    │  │ container│  │ container│  │ storage    │  │
    │  │          │  │          │  │ (persist)  │  │
    │  │/workspace│  │/workspace│  │            │  │
    │  │(ephemer.)│  │(ephemer.)│  │            │  │
    │  └──────────┘  └──────────┘  └────────────┘  │
    └───────────────────────────────────────────────┘
```

### How it works

```
User → Browser → Go HTTP server (port 8080)
                   ├─ GET / → Embedded static UI (Next.js build via embed.FS)
                   ├─ REST API (/api/v1/*) → SQLite (database/sql)
                   ├─ Auth (/api/auth/*) → JWT (NextAuth-compatible JWE)
                   └─ WebSocket → Docker exec → CLI session → LLM API
                                        ↓
                                stdout → JSONL logs
                                        ↓
                                /output/ → persistent files

External → Webhook (Go) → Agent trigger → same flow as above
```

## Core Concepts

- **Workspace** = company (multi-tenant root)
- **Crew** = department (isolation boundary, maps to Docker container or K8s Pod)
- **Agent** = virtual employee (CLI session inside container, has LLM, skills, credentials)
- **Skill** = MCP Server wrapper (tools + resources + system prompt + credential requirements)
- **Credential** = encrypted secret (AES-256-GCM, injected by sidecar into MCP servers at runtime)
- **Assignment** = lead/coordinator deleguje ukol na podrizeneho agenta (auditovano)
- **Sidecar** = crewship-sidecar Go binary running inside crew container (localhost:9119)
- **Runtime** = CLI mode (Claude Code, OpenCode) or API-direct mode (crewship-agent binary)

### Agent Hierarchy (3-level orchestration, Phase 2)

```
VIRTUAL COORDINATOR (1 per workspace, optional)
  │  - Koordinuje cross-crew ukoly
  │  - Deleguje na Crew Leady (nikdy primo na agenty)
  │  - Bezi jako lightweight LLM call (bez Docker kontejneru)
  │
  ├── CREW LEAD (1 per crew)
  │     │  - Primarni kontaktni bod pro uzivatel ↔ crew komunikaci
  │     │  - Rozdeluje prace, agreguje vysledky, kontroluje kvalitu
  │     │  - Bezi v kontejneru sveho tymu (Docker exec)
  │     │
  │     ├── AGENT (default role)
  │     │     - Specializovany na konkretni ukoly
  │     │     - Komunikuje primarne se svym leadem
  │     │     - Bezi v kontejneru sveho tymu (Docker exec)
  │     ├── AGENT ...
  │     └── AGENT ...
  │
  ├── CREW LEAD (jiny tym)
  │     ├── AGENT ...
  │     └── AGENT ...
  └── ...
```

Uzivatel chatuje primarne s Crew Leadem (90 % interakci).
Pro cross-crew otazky chatuje s Virtual Coordinatorem.
Muze take chatovat primo s agentem (bypass leada).

> Plna specifikace: `.factory/context/prd/ORCHESTRATION.md`

## Provider Pattern (K8s Readiness)

Crewship NEVER accesses Docker, filesystem, or bbolt directly.
Everything goes through provider interfaces — swap implementation, not rewrite.

```
                        ┌─────────────────────────────┐
                        │         crewship             │
                        │   (business logic only)      │
                        └──┬──────────┬──────────┬─────┘
                           │          │          │
                    ┌──────┴──┐  ┌────┴────┐  ┌──┴──────┐
                    │Container│  │ Storage │  │  State  │
                    │Provider │  │Provider │  │Provider │
                    └──┬───┬──┘  └──┬───┬──┘  └──┬───┬──┘
                       │   │       │   │        │   │
                    Docker K8s  Local  S3    bbolt  PG
                    (MVP) (Ent) (MVP) (Ent)  (MVP) (Ent)
```

| Provider | MVP (single-node) | Enterprise (K8s) |
|---|---|---|
| ContainerProvider | Docker SDK (exec) | client-go (Pods/Jobs) |
| StorageProvider | Local filesystem + fsnotify | S3/MinIO + event notifications |
| StateProvider | bbolt (embedded WAL) | PostgreSQL table (shared) |
| WS broadcast | In-process (single instance) | PostgreSQL LISTEN/NOTIFY |
| Rate limiting | In-memory token bucket | PostgreSQL-backed counters |

Configuration: single env var per provider.
```bash
CREWSHIP_CONTAINER_PROVIDER=docker    # docker | k8s
CREWSHIP_STORAGE_PROVIDER=localfs     # localfs | s3
CREWSHIP_STATE_PROVIDER=bbolt         # bbolt | postgres
```

Full details: `.factory/context/K8S-READINESS.md`

## Process Responsibilities

### Single Go Binary (crewship)

Everything runs in one process:

**UI Serving:**
- Embedded static UI via `embed.FS` (Next.js static export: HTML/CSS/JS)
- SPA-aware file server (`internal/api/static.go`)

**API (internal/api/):**
- REST API for all CRUD operations (`/api/v1/*`, 50+ routes)
- NextAuth-compatible auth endpoints (`/api/auth/*` -- csrf, session, login, signout)
- Signup with bcrypt, JWT signing/validation (JWE compatible)
- RBAC middleware on every endpoint
- CSRF token validation

**Orchestration:**
- WebSocket gateway (native goroutines, handles thousands of connections)
- Docker container lifecycle (create, start, stop, exec, logs)
- Agent orchestration (dispatch commands, collect results)
- Webhook ingress (external triggers from Grafana, n8n, Make, other Crewship instances)
- Log collection (Docker stdout → JSONL files)
- File server (serve agent output files via HTTP)
- fsnotify (inotify) file watcher (notify frontend of new files)
- WAL via bbolt (durable job state, survives crashes)
- Prometheus metrics endpoint (/metrics)
- Rate limiting (token bucket, in-memory)
- Health checks for containers
- Graceful shutdown (SIGTERM handling)
- Credential encryption/decryption (AES-256-GCM, `internal/encryption/`)

**Database:**
- SQLite (default, embedded, zero deps) or PostgreSQL (opt-in)
- Go `database/sql` direct queries (NO ORM, NO Prisma at runtime)
- Migration system (`internal/database/migrate.go`, 20 tables)

Phase 2 additions:
- **Channel Gateway** (opt-in module) -- persistent messaging sessions
  - Discord (discordgo), Telegram (go-telegram-bot-api), Slack (slack-go)
  - WhatsApp (whatsmeow, Phase 2B)
  - ChannelProvider interface (adapter pattern)
  - Routes incoming messages to correct agent/crew
- **Cron scheduler** (github.com/robfig/cron) -- scheduled missions
- **Approval engine** -- trust levels per agent, multi-channel approval flow

## Data Flow

### User sends message to agent

```
1. User types in chat UI (React, client component)
2. Browser sends fetch/WS to Go server (port 8080)
3. Go server delivers to agent container via Docker exec
   (crewship-sidecar already running in container on localhost:9119)
4. Agent process starts (CLI tool OR crewship-agent API-direct)
5. Agent writes response to stdout (user-facing, clean)
6. Agent delegates via HTTP to sidecar: POST localhost:9119/assign
   (CLI mode: curl; API-direct: native HTTP call)
7. Sidecar validates (RBAC, circuit breaker) → forwards to crewship
8. Go server reads stdout → WebSocket + JSONL log
9. User sees response in real-time
```

### External webhook triggers agent

```
1. Grafana/n8n/Make sends POST to /api/v1/webhooks/{crew}/{agent}/trigger
2. Go server validates webhook secret
3. Go server wakes agent (start container if stopped)
4. Agent processes the event
5. Agent writes results to /output/ (persistent)
6. Go server notifies via WebSocket (if user is online)
7. Agent optionally calls external service (Slack, Jira, email via skill)
```

### Agent creates file

```
1. Agent writes file to /output/reports/q1-report.pdf
2. fsnotify (inotify) detects new file
3. Go server indexes file metadata
4. Go server sends WebSocket notification to frontend
5. User sees "Agent created q1-report.pdf" in UI
6. User clicks Download → Go server serves file via HTTP
```

### Credential pool selection + failover

```
Agent start request arrives at crewship:
  1. For each env_var_name (e.g. ANTHROPIC_API_KEY):
     → Query credentials pool (sorted by priority ASC)
     → Skip credentials in cooldown (recent 429)
     → Select first available credential
  2. Inject selected credentials as ENV vars into Docker exec
  3. Agent runs with selected API key

If agent fails with 429 (rate limit):
  1. crewship detects rate limit error in stderr
  2. Mark current credential as cooldown (5 min)
  3. Select next credential from pool (priority order)
  4. Context preservation:
     - Claude Code: --resume flag (native, reads /workspace/.claude/)
     - Other CLIs: JSONL catch-up prompt (last 10 messages from session)
  5. Restart agent with new credential + preserved context
  6. User sees seamless transition in chat UI

Pool exhausted (all keys in cooldown):
  → Agent run fails with "All API keys exhausted"
  → User notified via WebSocket
  → Keys auto-recover after cooldown period (5 min)
```

## Storage Model

### Data directories

> **Single binary mode (primary):** Vsechna data v `~/.crewship/`:
> `~/.crewship/crewship.db` (SQLite), `~/.crewship/output/`, `~/.crewship/logs/`,
> `~/.crewship/config.yaml`. Viz `prd/DEPLOYMENT.md`.
>
> **Docker Compose mode (legacy/enterprise):** pouziva `/var/lib/crewship/`
> a `/var/log/crewship/`.

### Ephemeral (dies with container)

```
/workspace/              ← Docker volume, inside container
  ├─ .cache/             ← pip/npm cache
  ├─ .local/             ← agent local state
  ├─ tmp/                ← temp files
  └─ ...                 ← working directory for agent
```

Agent's scratch space. Installed packages, temp files, CLI state.
Destroyed when container is removed. Cheap, disposable -- agent is cattle.

### Persistent (survives everything)

```
~/.crewship/output/              ← host filesystem (single binary)
/var/lib/crewship/output/        ← host filesystem (Docker Compose)
  ├─ {workspace-id}/
  │   ├─ {crew-name}/
  │   │   ├─ {agent-name}/
  │   │   │   ├─ reports/
  │   │   │   │   ├─ q1-report.pdf
  │   │   │   │   └─ q1-report.pdf.meta.json
  │   │   │   └─ code/
  │   │   │       └─ scraper.py
  │   │   └─ shared/             ← shared across agents in crew
  │   └─ _archived/              ← moved here when crew is deleted
  │       └─ marketing-2026-02-11/
  └─ ...
```

Agent output -- reports, code, data, exports. This is what the business cares about.
When crew is deleted: container gone, but files moved to `_archived/` (not deleted).
Admin can purge archives (GDPR).

**Who manages directories:**
- **crewship** (Go) = creates directories, bind-mounts into containers, archives on crew deletion
- **Agent** = writes to `/output/` (inside container), bind mount to host `~/.crewship/output/{crew}/{agent}/`
- **Lead** (orchestration) = does NOT have special FS access -- reads results via sidecar (HTTP GET /results)
- **UI** = File browser displays content via crewship HTTP API (GET /api/v1/crews/{id}/files/)

**Per-agent vs per-crew isolation:**
- `/output/{crew}/{agent}/` = per-agent (default, isolated)
- `/output/{crew}/_shared/` = shared across agents in crew (for collaboration)
- Landlock (Phase 2) = agent "bob" sees only `/output/{crew}/bob/` and `/output/{crew}/_shared/`, NOT `/output/{crew}/alice/`

### Logs

```
~/.crewship/logs/                    ← single binary mode
/var/log/crewship/                   ← Docker Compose mode
  ├─ service.jsonl                   ← Go service logs
  ├─ teams/
  │   ├─ {crew-id}/
  │   │   ├─ agents/
  │   │   │   ├─ {agent-id}/
  │   │   │   │   ├─ current.jsonl        ← active log
  │   │   │   │   ├─ 2026-02-11T13.jsonl.gz  ← rotated (hourly)
  │   │   │   │   └─ ...
  │   │   └─ audit.jsonl             ← crew audit trail
  │   └─ ...
  └─ audit.jsonl                     ← global audit (append-only: chattr +a)
```

Managed by Linux logrotate. Hourly rotation, gzip compression, 30-day retention.
Zero custom code -- Linux has done this for 30 years.

## Container Model

```
Every crew gets ONE Docker container:
  - Base image: ghcr.io/crewship-ai/agent-runtime:latest (Ubuntu 24.04)
  - Runtime: runc (default) or runsc/gVisor (optional, ADR-003)
  - Non-root user: agent (UID 1001) -- NEVER root
  - Network: crewship-agents (--internal, no internet by default)
  - Explicit allowlist for LLM API endpoints only
  - Contains:
    - crewship-sidecar (Go binary, localhost:9119, MCP Gateway + assignment proxy)
    - crewship-agent (Go binary, API-direct runtime, Phase 2)
    - CLI tools (Claude Code, OpenCode, Codex -- for CLI mode)
    - landrun (Landlock wrapper, per-agent filesystem isolation, Phase 2)
    - MCP servers (stdio processes, started by sidecar, 1 per skill)
  - Mounts:
    - /workspace (ephemeral Docker volume)
    - /output (persistent bind mount to host, includes .memory/ and .skills/)
  - Resource limits: configurable per crew (default 1GB RAM, 0.5 CPU)
  - Always-on by default, configurable TTL for auto-shutdown
```

Container = jail. Agent cannot:
- Access host filesystem (only mounted volumes)
- Access other containers (network isolation)
- Escalate to root (no sudo, no setuid)
- Access internet (except allowlisted LLM endpoints)
- See other teams' data
- Access other agents' workspace (Landlock per-agent isolation, Phase 2)

## Security Layers

1. **Auth**: Go (NextAuth-compatible JWE endpoints in `internal/api/`)
2. **JWT validation**: Go validates JWT on every API/WS/webhook request
3. **RBAC**: Go middleware (`internal/api/middleware.go`) -- Owner/Admin/Manager/Member/Viewer
4. **Encryption**: AES-256-GCM for all credentials at rest (`internal/encryption/`)
5. **Container isolation**: non-root, --internal network, resource limits
6. **Network allowlist**: only LLM API endpoints reachable from containers
7. **Audit trail**: append-only JSONL (chattr +a), immutable
8. **Webhook auth**: per-agent secret token for external triggers
9. **Control channel**: loopback HTTP sidecar (localhost:9119, session token auth)
10. **Per-agent isolation**: Landlock LSM (filesystem isolation within shared container)
11. **Credential isolation**: Agent has NO tool credentials; sidecar injects into MCP servers
12. **MCP RBAC**: Sidecar validates per-tool permissions before proxying MCP calls
13. **Tool call audit**: Every MCP tool call logged with agent, tool, credential_id, timestamp
14. **srt sandbox per-MCP-server**: Each MCP server wrapped in Anthropic Sandbox Runtime — FS deny + network allowlist per-skill (ADR-017)
15. **Skill security pipeline**: 6-step automated audit before VERIFIED status — source verification, static analysis, CVE scan, sandbox test (ADR-020)
16. **OCI digest verification**: Marketplace skill images verified by SHA256 digest at pull time (ADR-021)
17. **Runtime (optional)**: gVisor/runsc for syscall interception (multi-tenant SaaS)

## Messaging Architecture (ADR-002)

**MVP (single-node):** Go channels + goroutines (in-process messaging)
- Assignment commands: goroutine reads → Go channel → AssignmentEngine
- WebSocket broadcast: in-process (single crewship instance)
- No external message broker dependency

**Phase 3 (multi-node cluster):** NATS JetStream
- When crewship needs to scale horizontally (multiple instances)
- NATS JetStream for exactly-once delivery, persistence, replay
- External NATS service (docker-compose for staging, Helm chart for K8s)

**Decision:** No NATS for MVP. Go channels are sufficient for single-node deployment
with dozens of agents. NATS adds complexity (extra service, failure modes, latency)
that is not justified until multi-node scaling is needed.

## Skills + MCP Architecture (ADR-014, ADR-015, ADR-016)

Skill = MCP Server wrapper. crewship-sidecar = MCP Gateway inside container.

```
Skill Definition (DB):
  ├── MCP Server command (e.g. "npx @modelcontextprotocol/server-github")
  ├── MCP transport (stdio | sse | streamable-http)
  ├── System prompt fragment (instructions for LLM)
  ├── Credential requirements (e.g. GITHUB_TOKEN)
  ├── Dependencies (apt/pip/npm packages)
  └── defer_loading (true = on-demand via tool search)

Runtime Flow:
  1. Container starts → sidecar reads skill list from crewship
  2. Sidecar starts MCP servers (stdio) with injected credentials
  3. Agent starts → gets only search_tools meta-tool + critical tools
  4. Agent needs a tool → calls search_tools("create github issue")
  5. Sidecar returns matching tool definitions on-demand
  6. Agent calls tool → sidecar proxies to MCP server (with RBAC + audit)
  7. Agent NEVER sees credentials — sidecar injects into MCP servers
```

Credential flow comparison:
```
OLD: Agent gets GITHUB_TOKEN as env var → calls API directly
     Risk: prompt injection can extract token

NEW: Agent has NO tool credentials
     Agent → MCP tool call → sidecar (RBAC + credential inject) → MCP server → API
     Agent sees only tool result, never the credential
```

Full specification: `AGENT-RUNTIME.md` section 6A.

## Skill Hub — MCP Marketplace (ADR-019, ADR-020, ADR-021)

Curated marketplace of verified MCP servers with security audit pipeline.
Addresses the MCP ecosystem trust problem (20,000+ unaudited servers, OWASP
MCP Top 10, 53% plaintext credential storage in community servers).

### 3-tier model

```
┌──────────────────────────────────────────────────────────────┐
│                    CREWSHIP SKILL HUB                        │
│                                                              │
│  Official Skills        Community Skills     Private Skills  │
│  (Crewship-maintained)  (anyone + review)    (org-internal)  │
│  Badge: ✓ Official      Badge: ✓ Verified    Badge: Private  │
│                                                              │
│                    ┌──────────────────┐                      │
│                    │ Security Pipeline│                      │
│                    │ (6 steps)        │                      │
│                    │ → security_score │                      │
│                    │ → VERIFIED/      │                      │
│                    │   REJECTED       │                      │
│                    └────────┬─────────┘                      │
│                             ▼                                │
│                    Skill Registry (DB + OCI images)           │
│                    ghcr.io/crewship-ai/skills/{name}:{ver}   │
└──────────────────────────────────────────────────────────────┘
                             │
                             ▼  install to agent
┌──────────────────────────────────────────────────────────────┐
│ Crew Container                                               │
│ Agent ◄─MCP─► Sidecar ──srt sandbox──► MCP Server (OCI)     │
│                 ↑ credentials injected, RBAC, audit          │
│                 ↑ network: only Skill.allowed_domains        │
└──────────────────────────────────────────────────────────────┘
```

### Security Pipeline (ADR-020)

6 steps before VERIFIED status:
1. **Source verification** — Sigstore / GitHub Attestations
2. **Static analysis** — tool definition scan for dangerous operations
3. **Dependency scan** — SBOM + CVE scan (Trivy/Grype) + license check
4. **Sandbox test run** — MCP server in srt isolation, network/FS audit
5. **Manual review** — mandatory for community submissions
6. **Continuous monitoring** — re-scan on version updates

Result: `security_score` (0-100) + `verification` status.

### MCP Server Sandboxing (ADR-017)

Each MCP server runs inside Anthropic Sandbox Runtime (`srt`) within the
crew container. Double sandboxing: Docker (container) + srt (process).

```
Sidecar generates per-skill srt-settings.json from Skill.allowed_domains:
  GitHub MCP → network: only api.github.com, *.github.com
  Slack MCP  → network: only slack.com, *.slack.com
  Both       → filesystem: deny /output, deny /workspace writes
```

### Distribution (ADR-021)

Marketplace skills as OCI images: `ghcr.io/crewship-ai/skills/{name}:{version}`
Digest-verified (SHA256). Pre-pulled at install time. Cached in container.
Fallback: `mcp_server_command` for Phase 1 / custom skills.

### Docker MCP Catalog as upstream source

Docker MCP Catalog (270+ servers) used as **import source** — re-scanned
through Crewship security pipeline before publishing to Skill Hub.

### Monetization

Free + Premium tiers. Premium skills: revenue share with author (default 70%).
Gated by Plan tier (FREE plan: limited premium skills).

Full specification: `AGENT-RUNTIME.md` section 6A.10.

## Dual Runtime Architecture (ADR-009)

Agents can run in two modes -- CLI-first or API-direct:

```
CLI mode (Phase 1):       Docker exec → CLI tool (Claude Code, OpenCode, Codex)
                          CLI tool calls LLM API, has own tool use
                          Assignment via curl to sidecar

API-direct mode (Phase 2): Docker exec → crewship-agent (Go binary, ~5MB)
                          Calls LLM API directly (Anthropic/OpenAI/Google SDK)
                          Native tool use, native assignment via HTTP
                          Precise token tracking from API response
```

Both modes communicate with crewship through the same crewship-sidecar
(localhost:9119). The sidecar provides a unified assignment API regardless
of agent runtime type. See ORCHESTRATION.md section 5.9.

## Conversation Search (Phase 2, ADR-011)

```
JSONL append (real-time) → crewship → async indexer → Meilisearch
                                                          ↓
                                              UI search: instant results
                                              across all conversations,
                                              teams, agents
```

- Meilisearch: Rust-based, <10ms latency, JSONL import, typo tolerance
- Runs as separate service (docker-compose for dev, K8s Deployment for prod)
- Trace ID in every indexed document for cross-agent correlation

## Monitoring (built-in from day 1)

| Tool | What | How |
|---|---|---|
| cAdvisor | Container metrics (CPU, RAM, disk, network per crew) | Separate container, zero config |
| Prometheus | Go service metrics (connections, agent runs, errors) | Native Go, /metrics endpoint |
| fsnotify | Real-time file change detection | inotify via Go, push to frontend via WS |
| Web terminal | SSH-like access to agent container from browser | xterm.js (frontend) + Docker exec API (Go) |
| Activity stream | Real-time feed of agent actions | Go captures stdout, pushes via WS |
| Health checks | Is container alive? Is agent responding? | Docker healthcheck + Go service ping |

## Deployment

### Single binary (Mode 1 -- PRIMARY)

V primarnim distribucnim modu bezi Crewship jako jeden Go binary:
- Next.js static build embedded pres `embed.FS` (`web/embed.go`)
- Vsechny API routes v Go (`internal/api/router.go`)
- Auth v Go (`internal/api/auth.go`, `internal/api/nextauth.go`)
- SQLite jako default DB (`~/.crewship/crewship.db`)
- Docker pouze pro agent kontejnery (ne pro Crewship samotny)
- Default port: **8080**
- CLI: `crewship start/version/doctor`

Data: `~/.crewship/` (konfigurace, DB, logy, output).

```bash
# Production
crewship start                # Start (SQLite, localhost:8080)
crewship start --port 9090    # Custom port

# Dev (hot-reload -- dva procesy)
./dev.sh start                # Go :8080 + Next.js :3001 (HMR, proxies API)
./dev.sh stop
```

### Docker Compose (Mode 2 -- legacy/enterprise)

Pro enterprise deploy s PostgreSQL a externima sluzbama:
- Docker image (`Dockerfile`)
- PostgreSQL 16 (optional, misto SQLite)
- Data v `/var/lib/crewship/`, logy v `/var/log/crewship/`
- Viz `docker/docker-compose.prod.yml`

### Environment

- **Local dev**: Mac Mini 16GB (macOS) -- `./dev.sh start` (Go + Next.js hot-reload)
- **Staging**: Coolify on Proxmox (128GB RAM, i7-12700) -- Docker image
- **Production**: Single binary or Docker image
- **Container registry**: ghcr.io/crewship-ai/*

## Graceful Shutdown

```
1. Receives SIGTERM
2. Stops accepting new WebSocket connections
3. Sends "reconnect" to all connected clients
4. Waits for active agent runs to finish (30s timeout)
5. Flushes WAL and logs
6. Stops Docker containers gracefully (SIGTERM → SIGKILL after 10s)
7. Exit 0
```

## RBAC Model

| Role | Teams | Agents | Credentials | Files | Audit |
|------|-------|--------|-------------|-------|-------|
| OWNER | All | All | All | All | All |
| ADMIN | All | All | All | All | All |
| MANAGER | Assigned | Create/Edit in assigned | Crew-level | Crew | Crew |
| MEMBER | Assigned | Interact with assigned | None | Crew (read) | Own |
| VIEWER | Assigned | Read-only | None | Crew (read) | None |
