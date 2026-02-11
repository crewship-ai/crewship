# Crewship -- Deployment (DEPLOYMENT.md)

**Verze:** 2.0
**Datum:** 2026-02-11

---

## 1. PREHLED

### Dva procesy, jedna databaze

| Komponenta | Technologie | Sit | Docker socket |
|---|---|---|---|
| **Next.js** | TypeScript, Prisma | `crewship-internal` | Ne |
| **crewshipd** | Go binary | `crewship-internal` | Ano (spravuje kontejnery) |
| **PostgreSQL** | PostgreSQL 16 | `crewship-internal` | Ne |
| **Agent kontejnery** | Docker (per team) | `crewship-agents` (--internal) | Ne |

> Next.js a crewshipd komunikuji pres Unix socket.
> Agent kontejnery NEMAJI pristup k platforme.

### Architektura

```
┌─────────────────────────── crewship-internal ────────────────────────────┐
│                                                                          │
│  ┌───────────────┐     Unix socket     ┌──────────────┐                  │
│  │   Next.js     │◄──────────────────►│  crewshipd   │                  │
│  │  (port 3000)  │                     │  (Go binary) │                  │
│  │  UI + API     │                     │  WS + Docker │                  │
│  └───────┬───────┘                     └──────┬───────┘                  │
│          │                                     │                          │
│          │ Prisma                               │ Docker SDK              │
│          ▼                                     ▼                          │
│  ┌──────────────┐                    ┌──────────────────┐                │
│  │  PostgreSQL   │                    │  Docker Socket   │                │
│  │  (port 5432)  │                    │  /var/run/docker │                │
│  └──────────────┘                    └────────┬─────────┘                │
│                                                │                          │
└────────────────────────────────────────────────┼──────────────────────────┘
                                                 │
                              ┌──────────────────┼────────── crewship-agents ──────┐
                              │                  │                                  │
                              │  ┌───────────┐  ┌───────────┐  ┌───────────┐       │
                              │  │ Team A    │  │ Team B    │  │ Team C    │       │
                              │  │ container │  │ container │  │ container │       │
                              │  └───────────┘  └───────────┘  └───────────┘       │
                              │  (--internal, no internet, LLM allowlist only)      │
                              └─────────────────────────────────────────────────────┘
```

### Komunikacni flow

```
1. Browser → HTTPS → Next.js (REST API, auth, CRUD)
2. Browser → WSS → crewshipd (real-time streaming)
3. Next.js → Unix socket → crewshipd (agent start/stop, status, files)
4. crewshipd → Docker SDK → Agent kontejner (exec, attach, logs)
5. Agent kontejner → HTTPS → LLM API (allowlisted endpoints only)
6. External → HTTPS → crewshipd (webhook trigger)
```

---

## 2. LOKALNI VYVOJ (Mac/Linux)

### 2.1 Prerekvizity

- Node.js 22+ (pnpm)
- Go 1.23+
- Docker Desktop (nebo colima/podman)
- PostgreSQL 16 (pres Docker Compose)

### 2.2 Setup

```bash
# 1. Spustit PostgreSQL
docker compose -f docker/docker-compose.yml up -d

# 2. Zkopirovat env
cp .env.example .env.local
# Vyplnit: NEXTAUTH_SECRET, ENCRYPTION_KEY

# 3. Nainstalovat dependencies
pnpm install
go mod download

# 4. Prisma
pnpm db:generate
pnpm db:push

# 5. Spustit (dva terminaly)
pnpm dev              # Next.js (localhost:3000)
go run ./cmd/crewshipd  # Go service (localhost:8080)
```

### 2.3 docker-compose.yml (lokalni dev)

```yaml
# docker/docker-compose.yml — JEN PostgreSQL
services:
  postgres:
    image: postgres:16-alpine
    container_name: crewship-postgres
    restart: unless-stopped
    ports:
      - "5432:5432"
    environment:
      POSTGRES_USER: crewship
      POSTGRES_PASSWORD: crewship
      POSTGRES_DB: crewship
    volumes:
      - crewship-pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U crewship"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  crewship-pgdata:
```

### 2.4 .env.local (lokalni dev)

```bash
DATABASE_URL=postgresql://crewship:crewship@localhost:5432/crewship
NEXTAUTH_SECRET=dev-secret-min-32-chars-openssl-rand
NEXTAUTH_URL=http://localhost:3000
ENCRYPTION_KEY=dev-key-64-hex-chars-openssl-rand-hex-32
CREWSHIPD_SOCKET=/tmp/crewship.sock
```

---

## 3. STAGING (Coolify na Proxmox)

### 3.1 Infrastruktura

```
Proxmox server (128GB RAM, i7-12700)
  └── VM/LXC s Coolify
      ├── crewship-nextjs (Coolify Docker service)
      ├── crewship-go (Coolify Docker service)
      ├── crewship-postgres (Coolify PostgreSQL service)
      └── Agent kontejnery (crewshipd vytvari dynamicky)
```

### 3.2 Coolify services

| Service | Image | Port | Docker socket |
|---|---|---|---|
| crewship-nextjs | `ghcr.io/crewship-ai/crewship:latest` | 3000 | Ne |
| crewship-go | `ghcr.io/crewship-ai/crewshipd:latest` | 8080 | Ano |
| crewship-postgres | `postgres:16-alpine` | 5432 | Ne |

### 3.3 Deployment postup

```bash
# 1. Build Next.js image
docker build -t ghcr.io/crewship-ai/crewship:latest .

# 2. Build Go image
docker build -t ghcr.io/crewship-ai/crewshipd:latest -f docker/crewshipd/Dockerfile .

# 3. Push to GHCR
docker push ghcr.io/crewship-ai/crewship:latest
docker push ghcr.io/crewship-ai/crewshipd:latest

# 4. Coolify auto-deploys (webhook trigger)
```

---

## 4. DOCKER IMAGES

### 4.1 Next.js Dockerfile

```dockerfile
# Dockerfile (root)
FROM node:22-alpine AS base
RUN corepack enable pnpm

FROM base AS deps
WORKDIR /app
COPY package.json pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

FROM base AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN pnpm build

FROM base AS runner
WORKDIR /app
ENV NODE_ENV=production
RUN addgroup --system --gid 1001 crewship && \
    adduser --system --uid 1001 crewship
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
COPY --from=builder /app/public ./public
USER crewship
EXPOSE 3000
CMD ["node", "server.js"]
```

### 4.2 crewshipd Dockerfile

```dockerfile
# docker/crewshipd/Dockerfile
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /crewshipd ./cmd/crewshipd

FROM alpine:3.19
RUN apk --no-cache add ca-certificates && \
    addgroup -S crewship && adduser -S crewship -G crewship
COPY --from=builder /crewshipd /usr/local/bin/crewshipd
USER crewship
EXPOSE 8080
ENTRYPOINT ["crewshipd"]
```

### 4.3 Agent Runtime Dockerfile

```dockerfile
# docker/agent-runtime/Dockerfile
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y \
    curl git jq openssh-client python3 \
    && rm -rf /var/lib/apt/lists/*

# CLI tools (pinned versions)
RUN npm install -g \
    @anthropic-ai/claude-code@1.x.x \
    @openai/codex@0.x.x

# Non-root user
RUN groupadd -g 1001 agent && useradd -u 1001 -g agent agent
USER agent
WORKDIR /workspace

# Healthcheck
HEALTHCHECK --interval=30s --timeout=5s CMD echo "alive"
```

---

## 5. DOCKER NETWORKING

```bash
# Vytvoreni siti (jednorazove)
docker network create crewship-internal
docker network create crewship-agents --internal  # bez internet access

# Next.js + crewshipd + PostgreSQL → crewship-internal
# Agent kontejnery → crewship-agents (--internal)

# LLM API allowlist (iptables na hostu)
iptables -A DOCKER-USER -s crewship-agents -d api.anthropic.com -p tcp --dport 443 -j ACCEPT
iptables -A DOCKER-USER -s crewship-agents -d api.openai.com -p tcp --dport 443 -j ACCEPT
iptables -A DOCKER-USER -s crewship-agents -d generativelanguage.googleapis.com -p tcp --dport 443 -j ACCEPT
iptables -A DOCKER-USER -s crewship-agents -j DROP
```

---

## 6. PERSISTENT STORAGE

```
/var/lib/crewship/
  ├── output/{org-id}/{team-name}/{agent-name}/   ← agent deliverables
  ├── conversations/{org-id}/{agent-id}/           ← JSONL session files
  └── bbolt/                                       ← Go service WAL

/var/log/crewship/
  └── teams/{team-id}/agents/{agent-id}/
      └── current.jsonl                            ← agent logs (logrotated)
```

### Logrotate konfigurace

```
# /etc/logrotate.d/crewship
/var/log/crewship/teams/*/agents/*/current.jsonl {
    hourly
    rotate 720        # 30 dni * 24h
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
}
```

---

## 7. HEALTH MONITORING

### 7.1 Health endpoint

```
GET /api/v1/health → Next.js kontroluje DB + crewshipd
GET /metrics       → crewshipd Prometheus metriky
```

### 7.2 Prometheus metriky (Go)

```
crewship_websocket_connections_total
crewship_websocket_messages_total
crewship_agent_runs_total{status="completed|failed|timeout"}
crewship_agent_run_duration_seconds
crewship_docker_containers_active
crewship_ipc_requests_total{method="start|stop|status"}
crewship_ipc_request_duration_seconds
```

### 7.3 cAdvisor (container metriky)

```yaml
# docker-compose.monitoring.yml (optional)
services:
  cadvisor:
    image: gcr.io/cadvisor/cadvisor:latest
    container_name: crewship-cadvisor
    ports:
      - "8081:8080"
    volumes:
      - /:/rootfs:ro
      - /var/run:/var/run:ro
      - /sys:/sys:ro
      - /var/lib/docker/:/var/lib/docker:ro
```

---

## 8. ENVIRONMENT VARIABLES

### 8.1 Next.js

| Promenna | Povinne | Popis |
|---|---|---|
| `DATABASE_URL` | Ano | PostgreSQL connection string |
| `NEXTAUTH_SECRET` | Ano | JWT signing secret |
| `NEXTAUTH_URL` | Ano | Application URL |
| `ENCRYPTION_KEY` | Ano | AES-256-GCM key (hex) |
| `CREWSHIPD_SOCKET` | Ano | Path k Unix socket |
| `NODE_ENV` | Ano | production / development |

### 8.2 crewshipd (Go)

| Promenna | Povinne | Popis |
|---|---|---|
| `CREWSHIPD_SOCKET` | Ano | Path k Unix socket |
| `CREWSHIPD_HTTP_PORT` | Ne | HTTP port pro WebSocket (default: 8080) |
| `CREWSHIPD_LOG_DIR` | Ne | Log directory (default: /var/log/crewship) |
| `CREWSHIPD_OUTPUT_DIR` | Ne | Output directory (default: /var/lib/crewship/output) |
| `CREWSHIPD_BBOLT_PATH` | Ne | bbolt DB path (default: /var/lib/crewship/bbolt/crewship.db) |
| `DATABASE_URL` | Ano | PostgreSQL (pro IPC validaci — Go cte agent/team data) |

### 8.3 Agent kontejner (injektovane)

| Promenna | Popis |
|---|---|
| `CREWSHIP_AGENT_ID` | Agent UUID |
| `CREWSHIP_TEAM_ID` | Team UUID |
| `CREWSHIP_SESSION_ID` | Current session UUID |
| `{USER_CREDENTIALS}` | Dynamicky dle AgentCredential (napr. OPENAI_API_KEY) |

---

## 9. BACKUP

| Co | Jak | Frekvence |
|---|---|---|
| PostgreSQL | `pg_dump` → S3/local | Denne |
| /output/ (agent deliverables) | rsync → backup server | Denne |
| JSONL konverzace | rsync → backup server | Denne |
| bbolt WAL | Snapshot pri graceful shutdown | Automaticky |

---

## 10. SCALING (Phase 2+)

### Single instance (MVP)

```
1x Next.js + 1x crewshipd + 1x PostgreSQL
Kapacita: ~50 soucasnych agentu, ~200 WebSocket connections
RAM: ~400 MB infrastruktura + agent kontejnery
```

### Horizontal (Phase 2+)

```
N x Next.js (stateless, load balancer)
1x crewshipd (single instance — drzi Docker state)
1x PostgreSQL (vertikalni scaling)

Phase 3 (K8s):
N x crewshipd (gRPC, shared state pres etcd)
PostgreSQL cluster (patroni)
```

> **crewshipd je bottleneck** pro horizontal scaling — drzi WebSocket connections
> a Docker state v pameti. Phase 3 resi gRPC koordinaci mezi instancemi.

---

## 11. OTEVRENE OTAZKY

1. **Zero-downtime deploy** — jak restartovat crewshipd bez ztraceni WebSocket connections? Graceful shutdown + client reconnect?
2. **Agent image updates** — jak rolling-update agent runtime image? Stop kontejner → pull new image → start?
3. **Log aggregation** — Loki/Grafana pro centralni log viewing? Nebo staci JSONL + file browser?
4. **SSL termination** — Coolify/Caddy/nginx pred Next.js + crewshipd? Nebo kazdy service vlastni TLS?
5. **Secrets management** — Coolify env vars staci? Nebo Vault/SOPS pro ENCRYPTION_KEY?
