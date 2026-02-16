# Architecture

> Crewship -- Open-source AI agent orchestration platform
> "Linux machine where AI employees work. You give them instructions, credentials
> and skills. They work 24/7 and you download the results in the morning."

---

## Two-Process Architecture

```
+-------------------+   Unix socket    +--------------------+
|   Next.js         | ---------------> |   Go service       |
|   (TypeScript)    |   (or gRPC)      |   (crewshipd)      |
|                   |                  |                    |
|   - React UI      |                  |   - WebSocket GW   |
|   - shadcn/ui     |                  |   - Docker mgmt    |
|   - NextAuth      |                  |   - Agent orchestr. |
|   - Prisma CRUD   |                  |   - Log collector   |
|   - File browser  |                  |   - File server     |
|   - Web terminal  |                  |   - fsnotify watch  |
|   - Port 3000     |                  |   - WAL (bbolt)     |
|   ~300 MB RAM     |                  |   - Prometheus      |
|                   |                  |   - Webhook ingress |
+--------+----------+                  |   - Rate limiter    |
         |                             |   ~50 MB RAM        |
         v                             +--------+-----------+
+--------+----------+                           |
| PostgreSQL        |              +------------+------------+
| (structured       |              |            |            |
|  data ONLY)       |              v            v            v
|  - users, auth    |     +----------+   +----------+   +--------+
|  - orgs, teams    |     | Team A   |   | Team B   |   | Output |
|  - agents, skills |     | container|   | container|   | storage|
|  - credentials    |     |          |   |          |   | (persi-|
+-------------------+     | /workspace   | /workspace   | stent) |
                          | (ephemeral)  | (ephemeral)  |        |
+-------------------+     +----------+   +----------+   +--------+
| Filesystem        |
|  /var/log/crewship/  ← JSONL logs (logrotate)
|  /var/lib/crewship/  ← WAL, config, output storage
+-------------------+
```

## Core Concepts

- **Organization** = company (multi-tenant root)
- **Team** = department (isolation boundary, maps to Docker container or K8s Pod)
- **Agent** = virtual employee (CLI session inside container, has LLM, skills, credentials)
- **Skill** = MCP Server wrapper (tools + resources + system prompt + credential requirements)
- **Credential** = encrypted secret (AES-256-GCM, injected by sidecar into MCP servers at runtime)
- **Delegation** = leader/director deleguje ukol na podrizeneho agenta (auditovano)
- **Sidecar** = crewship-sidecar Go binary running inside team container (localhost:9119)
- **Runtime** = CLI mode (Claude Code, OpenCode) or API-direct mode (crewship-agent binary)

### Agent Hierarchy (3-level orchestration, Phase 2)

```
VIRTUAL DIRECTOR (1 per org, optional)
  │  - Koordinuje cross-team ukoly
  │  - Deleguje na Crew Leadery (nikdy primo na workery)
  │  - Bezi jako lightweight LLM call (bez Docker kontejneru)
  │
  ├── CREW LEADER (1 per team)
  │     │  - Primarni kontaktni bod pro uzivatel ↔ team komunikaci
  │     │  - Rozdeluje prace, agreguje vysledky, kontroluje kvalitu
  │     │  - Bezi v kontejneru sveho tymu (Docker exec)
  │     │
  │     ├── WORKER (default role)
  │     │     - Specializovany na konkretni ukoly
  │     │     - Komunikuje primarne se svym leaderem
  │     │     - Bezi v kontejneru sveho tymu (Docker exec)
  │     ├── WORKER ...
  │     └── WORKER ...
  │
  ├── CREW LEADER (jiny tym)
  │     ├── WORKER ...
  │     └── WORKER ...
  └── ...
```

Uzivatel chatuje primarne s Crew Leaderem (90 % interakci).
Pro cross-team otazky chatuje s Virtual Directorem.
Muze take chatovat primo s worker agentem (bypass leadera).

> Plna specifikace: `.factory/context/prd/ORCHESTRATION.md`

## Provider Pattern (K8s Readiness)

crewshipd NEVER accesses Docker, filesystem, or bbolt directly.
Everything goes through provider interfaces — swap implementation, not rewrite.

```
                        ┌─────────────────────────────┐
                        │        crewshipd             │
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
| IPC transport | HTTP over Unix socket | HTTP over TCP (K8s Service) |
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

### Next.js (TypeScript) -- UI + CRUD

What it does:
- React UI (dashboard, chat, file browser, web terminal, settings)
- NextAuth.js authentication (email+password, OAuth)
- Prisma ORM for CRUD operations on PostgreSQL
- REST API for frontend consumption (`/api/v1/`)
- Communicates with Go service via Unix socket (local) or gRPC (remote)

What it does NOT do:
- No WebSocket server (Go handles that)
- No Docker management
- No log collection
- No file serving from agent workspace
- No job queue

### Go service (crewshipd) -- Brain + Hands

What it does:
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

What it does NOT do:
- No HTML rendering
- No database access (Next.js owns PostgreSQL via Prisma)
- No authentication (validates JWT tokens from NextAuth)

## IPC: Next.js ↔ Go service

```
Local (same host):
  Dev: Unix domain socket /tmp/crewship.sock
  Prod: Unix domain socket /run/crewship/crewship.sock (chmod 0660)
  = zero TCP overhead, secure (not exposed on port)

Remote (K8s, multi-node):
  gRPC over HTTP/2 on port 8080
  = typed protobuf messages, auto-generated clients
  = mTLS for security
```

## Data Flow

### User sends message to agent

```
1. User types in chat UI (React)
2. Next.js sends to Go service via Unix socket
3. Go service delivers to agent container via Docker exec
   (crewship-sidecar already running in container on localhost:9119)
4. Agent process starts (CLI tool OR crewship-agent API-direct)
5. Agent writes response to stdout (user-facing, clean)
6. Agent delegates via HTTP to sidecar: POST localhost:9119/delegate
   (CLI mode: curl; API-direct: native HTTP call)
7. Sidecar validates (RBAC, circuit breaker) → forwards to crewshipd
8. Go service reads stdout → WebSocket + JSONL log
9. User sees response in real-time
```

### External webhook triggers agent

```
1. Grafana/n8n/Make sends POST to /api/v1/webhooks/{team}/{agent}/trigger
2. Go service validates webhook secret
3. Go service wakes agent (start container if stopped)
4. Agent processes the event
5. Agent writes results to /output/ (persistent)
6. Go service notifies via WebSocket (if user is online)
7. Agent optionally calls external service (Slack, Jira, email via skill)
```

### Agent creates file

```
1. Agent writes file to /output/reports/q1-report.pdf
2. fsnotify (inotify) detects new file
3. Go service indexes file metadata
4. Go service sends WebSocket notification to frontend
5. User sees "Agent created q1-report.pdf" in UI
6. User clicks Download → Go service serves file via HTTP
```

### Credential pool selection + failover

```
Agent start request arrives at crewshipd:
  1. For each env_var_name (e.g. ANTHROPIC_API_KEY):
     → Query credentials pool (sorted by priority ASC)
     → Skip credentials in cooldown (recent 429)
     → Select first available credential
  2. Inject selected credentials as ENV vars into Docker exec
  3. Agent runs with selected API key

If agent fails with 429 (rate limit):
  1. crewshipd detects rate limit error in stderr
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
/var/lib/crewship/output/        ← bind mount, on host filesystem
  ├─ {org-id}/
  │   ├─ {team-name}/
  │   │   ├─ {agent-name}/
  │   │   │   ├─ reports/
  │   │   │   │   ├─ q1-report.pdf
  │   │   │   │   └─ q1-report.pdf.meta.json
  │   │   │   └─ code/
  │   │   │       └─ scraper.py
  │   │   └─ shared/             ← shared across agents in team
  │   └─ _archived/              ← moved here when team is deleted
  │       └─ marketing-2026-02-11/
  └─ ...
```

Agent output -- reports, code, data, exports. This is what the business cares about.
When team is deleted: container gone, but files moved to `_archived/` (not deleted).
Admin can purge archives (GDPR).

### Logs

```
/var/log/crewship/
  ├─ service.jsonl               ← Go service logs
  ├─ teams/
  │   ├─ {team-id}/
  │   │   ├─ agents/
  │   │   │   ├─ {agent-id}/
  │   │   │   │   ├─ current.jsonl        ← active log
  │   │   │   │   ├─ 2026-02-11T13.jsonl.gz  ← rotated (hourly)
  │   │   │   │   └─ ...
  │   │   └─ audit.jsonl         ← team audit trail
  │   └─ ...
  └─ audit.jsonl                 ← global audit (append-only: chattr +a)
```

Managed by Linux logrotate. Hourly rotation, gzip compression, 30-day retention.
Zero custom code -- Linux has done this for 30 years.

## Container Model

```
Every team gets ONE Docker container:
  - Base image: ghcr.io/crewship-ai/agent-runtime:latest (Ubuntu 24.04)
  - Runtime: runc (default) or runsc/gVisor (optional, ADR-003)
  - Non-root user: agent (UID 1001) -- NEVER root
  - Network: crewship-agents (--internal, no internet by default)
  - Explicit allowlist for LLM API endpoints only
  - Contains:
    - crewship-sidecar (Go binary, localhost:9119, MCP Gateway + delegation proxy)
    - crewship-agent (Go binary, API-direct runtime, Phase 2)
    - CLI tools (Claude Code, OpenCode, Codex -- for CLI mode)
    - landrun (Landlock wrapper, per-agent filesystem isolation, Phase 2)
    - MCP servers (stdio processes, started by sidecar, 1 per skill)
  - Mounts:
    - /workspace (ephemeral Docker volume)
    - /output (persistent bind mount to host, includes .memory/ and .skills/)
  - Resource limits: configurable per team (default 1GB RAM, 0.5 CPU)
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

1. **Auth**: NextAuth.js (email+password, OAuth)
2. **JWT validation**: Go service verifies NextAuth JWT on every WS/webhook request
3. **RBAC**: CASL abilities (Owner/Admin/Manager/Member/Viewer)
4. **Encryption**: AES-256-GCM for all credentials at rest
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
- Delegation commands: named pipe → goroutine reads → Go channel → DelegationEngine
- WebSocket broadcast: in-process (single crewshipd instance)
- No external message broker dependency

**Phase 3 (multi-node cluster):** NATS JetStream
- When crewshipd needs to scale horizontally (multiple instances)
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
  1. Container starts → sidecar reads skill list from crewshipd
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
│ Team Container                                               │
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
team container. Double sandboxing: Docker (container) + srt (process).

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
                          Delegation via curl to sidecar

API-direct mode (Phase 2): Docker exec → crewship-agent (Go binary, ~5MB)
                          Calls LLM API directly (Anthropic/OpenAI/Google SDK)
                          Native tool use, native delegation via HTTP
                          Precise token tracking from API response
```

Both modes communicate with crewshipd through the same crewship-sidecar
(localhost:9119). The sidecar provides a unified delegation API regardless
of agent runtime type. See ORCHESTRATION.md section 5.9.

## Conversation Search (Phase 2, ADR-011)

```
JSONL append (real-time) → crewshipd → async indexer → Meilisearch
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
| cAdvisor | Container metrics (CPU, RAM, disk, network per team) | Separate container, zero config |
| Prometheus | Go service metrics (connections, agent runs, errors) | Native Go, /metrics endpoint |
| fsnotify | Real-time file change detection | inotify via Go, push to frontend via WS |
| Web terminal | SSH-like access to agent container from browser | xterm.js (frontend) + Docker exec API (Go) |
| Activity stream | Real-time feed of agent actions | Go captures stdout, pushes via WS |
| Health checks | Is container alive? Is agent responding? | Docker healthcheck + Go service ping |

## Deployment

- **Local dev**: Mac Mini 16GB -- Docker Compose (PostgreSQL + crewshipd), Next.js native
- **Staging**: Coolify on Proxmox (128GB RAM, i7-12700)
- **Production**: TBD (Coolify, K8s, or bare metal)
- **Container registry**: ghcr.io/crewship-ai/*

## Graceful Shutdown (Go service)

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
| MANAGER | Assigned | Create/Edit in assigned | Team-level | Team | Team |
| MEMBER | Assigned | Interact with assigned | None | Team (read) | Own |
| VIEWER | Assigned | Read-only | None | Team (read) | None |
