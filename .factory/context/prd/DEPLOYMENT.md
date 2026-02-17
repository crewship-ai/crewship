# Crewship -- Deployment (DEPLOYMENT.md)

**Verze:** 3.0
**Datum:** 2026-02-17

> Aktualizovano 2026-02-17: Pridana single binary distribuce (Mode 1), SQLite default, GoReleaser.

---

## 1. DISTRIBUCNI MODELY

Crewship podporuje tri distribucni mody. **Mode 1 (Single Binary)** je primarni a doporuceny
pro vetsinu uzivatelu. Mody 2 a 3 jsou urceny pro serverovy provoz a enterprise.

| Mode | Nazev | Cilovy uzivatel | Databaze | Docker nutny pro platformu? |
|---|---|---|---|---|
| **1** | **Single Binary** (PRIMARY) | Solo dev, maly tym | SQLite (default) | **Ne** (jen pro agent kontejnery) |
| **2** | Docker Compose | Server, staging, vetsi tym | PostgreSQL | Ano |
| **3** | Kubernetes | Enterprise (Phase 3) | PostgreSQL cluster | Ano (K8s) |

---

### 1.1 Mode 1: Single Binary (PRIMARY -- doporuceny)

Jediny soubor ke stazeni. Inspirovano modelem Ollama, Gitea, k9s.

#### Instalace

```bash
# macOS
brew install crewship

# Linux (Debian/Ubuntu)
curl -fsSL https://get.crewship.ai | sh

# Linux (RPM)
dnf install crewship

# Windows
winget install crewship
# nebo
scoop install crewship

# Docker (fallback -- single binary v kontejneru)
docker run -d -p 3001:3001 --name crewship ghcr.io/crewship-ai/crewship:latest
```

#### Co `crewship start` udela

```
1. Zkontroluje Docker (pro agent kontejnery -- `crewship doctor`)
2. Inicializuje data directory (~/.crewship/)
3. Inicializuje SQLite databazi (~/.crewship/crewship.db)
4. Spusti embedded web server (Next.js static build pres Go HTTP server)
5. Spusti crewshipd engine (WebSocket, Docker orchestrace)
6. Otevre http://localhost:3001 v prohlizeci
7. Uzivatel vidi onboarding wizard
```

#### CLI prikazy

```
crewship                      # help
crewship start                # spusti vse (SQLite, localhost:3001)
crewship start --port 8080    # custom port
crewship start --db postgres://user:pass@host/db  # PostgreSQL misto SQLite
crewship stop                 # zastavi vse
crewship status               # stav sluzeb
crewship logs                 # tail logy
crewship logs --follow        # stream logy
crewship doctor               # diagnostika (Docker check, port check, DB check)
crewship update               # aktualizace na nejnovejsi verzi (go-selfupdate)
crewship version              # verze
crewship skill install <name> # nainstaluje skill z marketplace
crewship skill list           # seznam nainstalovanych skills
crewship skill search <query> # hledani v marketplace
```

#### Architektura single binary

```
crewship (Go binary, ~50-80 MB)
  ├── Embedded: Next.js static build (embed.FS)
  │     └── HTML/CSS/JS/assets -- servovane pres Go HTTP server
  ├── crewshipd engine:
  │     ├── WebSocket gateway (goroutines)
  │     ├── Docker SDK (kontejnerova orchestrace)
  │     ├── Log collector (JSONL)
  │     ├── File server (fsnotify)
  │     ├── Webhook ingress
  │     └── Skill sandbox enforcement
  ├── Database:
  │     ├── SQLite (default, zero deps) -- ~/.crewship/crewship.db
  │     └── PostgreSQL (opt-in: crewship start --db postgres://...)
  ├── CLI: viz tabulka CLI prikazu vyse
  └── Auto-update: go-selfupdate
```

> **Docker je nutny JEN pro agent kontejnery** (izolace agentu), NE pro samotnou platformu.
> Uzivatel nepotrebuje Docker Compose, PostgreSQL, ani zadnou dalsi infrastrukturu.

#### Auto-update

Single binary podporuje automaticke aktualizace pres `go-selfupdate`:
- `crewship update` -- manualni aktualizace
- Kontrola nových verzi pri kazdem `crewship start` (opt-out pres config)
- Release artefakty stahovane z GitHub Releases (podepsane checksumy)

---

### 1.2 Mode 2: Docker Compose (server, staging, tymy)

Pro serverovy provoz, staging prostredi, nebo vetsi tymy kde je potreba PostgreSQL,
centralni logovani a trvalé bezici sluzby. Toto je puvodni dokumentovany pristup.

Viz sekce 3 (Staging), 4 (Docker Images) a 5 (Docker Networking) nize.

---

### 1.3 Mode 3: Kubernetes (Enterprise, Phase 3)

Helm chart pro nasazeni na GKE, EKS, AKS. K8s provider nahrazuje Docker provider
pro kontejnerovou orchestraci.

Detaily viz `.factory/context/K8S-READINESS.md`.

- gRPC komunikace mezi instancemi crewshipd
- PostgreSQL cluster (Patroni)
- Horizontalni skalovani
- SSO/SAML (Okta, Azure AD)

---

## 2. BUILD PIPELINE

### 2.1 Single binary build

```
┌──────────────────┐     ┌──────────────────┐     ┌──────────────────────┐
│  pnpm build      │ ──► │  next export     │ ──► │  go build            │
│  (Next.js)       │     │  → web/out/      │     │  s embed.FS          │
│                  │     │  (static HTML/   │     │  → crewship binary   │
│                  │     │   CSS/JS/assets) │     │  (~50-80 MB)         │
└──────────────────┘     └──────────────────┘     └──────────┬───────────┘
                                                              │
                                                              ▼
                                                   ┌──────────────────────┐
                                                   │  GoReleaser          │
                                                   │  cross-compile:      │
                                                   │  linux/amd64         │
                                                   │  linux/arm64         │
                                                   │  darwin/amd64        │
                                                   │  darwin/arm64        │
                                                   │  windows/amd64      │
                                                   │  windows/arm64      │
                                                   └──────────────────────┘
```

### 2.2 Embedded Next.js v Go

```go
// cmd/crewship/main.go
//go:embed web/out/*
var webFS embed.FS

func main() {
    // Serve static Next.js build
    http.Handle("/", http.FileServer(http.FS(webFS)))
    // API routes zpracovava crewshipd engine primo
    http.Handle("/api/", crewshipdEngine.Handler())
}
```

**Build postup:**
1. `pnpm build` → `next export` → `web/out/` (static SPA)
2. `go build -o crewship ./cmd/crewship` → Go embeduje `web/out/` pres `embed.FS`
3. GoReleaser cross-compile pro vsechny platformy

### 2.3 GoReleaser konfigurace

```yaml
# .goreleaser.yml
builds:
  - binary: crewship
    main: ./cmd/crewship
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.ShortCommit}}
      - -X main.date={{.Date}}

archives:
  - format: tar.gz
    format_overrides:
      - goos: windows
        format: zip

brews:
  - tap:
      owner: crewship-ai
      name: homebrew-tap
    formula: crewship
    homepage: https://crewship.ai
    description: "AI agent orchestration platform with container isolation"

scoops:
  - bucket:
      owner: crewship-ai
      name: scoop-bucket
    homepage: https://crewship.ai

winget:
  - name: crewship
    publisher: crewship-ai
    license: MIT

checksum:
  name_template: 'checksums.txt'

release:
  github:
    owner: crewship-ai
    name: crewship
```

### 2.4 Docker image build (Mode 2)

Pro Docker Compose deploy se Next.js a crewshipd stale buildi jako oddelene Docker images:
- `ghcr.io/crewship-ai/crewship:latest` (Next.js)
- `ghcr.io/crewship-ai/crewshipd:latest` (Go)

Viz sekce 4 nize.

---

## 3. DATA DIRECTORY (~/.crewship/)

Single binary (Mode 1) uklada veskera data do `~/.crewship/`:

```
~/.crewship/
  ├── crewship.db           # SQLite databaze (pokud SQLite mode)
  ├── config.yaml           # uzivatelska konfigurace (port, DB, auto-update)
  ├── crewship.pid          # PID soubor bezici instance
  ├── skills/               # nainstalovane skills
  │   ├── git-operations/
  │   ├── web-research/
  │   └── ...
  ├── output/               # agent vystupy (persistent)
  │   └── {workspace-id}/{crew-name}/{agent-name}/
  ├── conversations/        # JSONL konverzace
  │   └── {workspace-id}/{agent-id}/{session-id}.jsonl
  └── logs/                 # JSONL logy
      └── crews/{crew-id}/agents/{agent-id}/current.jsonl
```

> **Docker Compose (Mode 2)** pouziva systemove cesty:
> `/var/lib/crewship/` (data) a `/var/log/crewship/` (logy).
> Viz sekce 7 (Persistent Storage).

---

## 4. SQLite vs PostgreSQL

| Aspekt | SQLite (Mode 1 default) | PostgreSQL (Mode 2, opt-in v Mode 1) |
|---|---|---|
| Setup | Zero deps, instant | Docker container nebo external server |
| Vhodne pro | Solo dev, maly tym (1-10 lidi) | Vetsi tym, enterprise, high availability |
| Prisma podpora | Ano (`prisma/sqlite`) | Ano (`prisma/postgresql`) |
| Concurrent writes | Limitovane (WAL mode pomaha) | Plne |
| Backup | Kopie souboru (`~/.crewship/crewship.db`) | `pg_dump`, replikace |
| Prepnuti | `crewship start` (default) | `crewship start --db postgres://user:pass@host/db` |
| Data location | `~/.crewship/crewship.db` | Externi PostgreSQL server |

**Prisma multi-provider:**

```prisma
// prisma/schema.prisma
datasource db {
  provider = env("DB_PROVIDER")  // "sqlite" nebo "postgresql"
  url      = env("DATABASE_URL") // "file:./crewship.db" nebo "postgresql://..."
}
```

**Doporuceni:**
- **Jednotlivec / maly tym (< 10 lidi):** SQLite (Mode 1). Nulova konfigurace.
- **Vetsi tym / server (10+ lidi):** PostgreSQL (Mode 2 nebo Mode 1 s `--db`).
- **Enterprise / K8s (100+ lidi):** PostgreSQL cluster (Mode 3).

**Migrace SQLite → PostgreSQL:** `crewship migrate --from sqlite --to postgres://...` (planovano).

---

## 5. PREHLED ARCHITEKTURY

### Dva procesy, jedna databaze (Mode 2 / Docker Compose)

| Komponenta | Technologie | Sit | Docker socket |
|---|---|---|---|
| **Next.js** | TypeScript, Prisma | `crewship-internal` | Ne |
| **crewshipd** | Go binary | `crewship-internal` | Ano (spravuje kontejnery) |
| **PostgreSQL** | PostgreSQL 16 | `crewship-internal` | Ne |
| **Agent kontejnery** | Docker (per crew) | `crewship-agents` (--internal) | Ne |

### Jeden proces (Mode 1 / Single Binary)

| Komponenta | Technologie | Sit | Docker socket |
|---|---|---|---|
| **crewship** | Go binary + embedded Next.js | localhost | Ano (spravuje kontejnery) |
| **SQLite** | Embedded v binary | N/A (file) | Ne |
| **Agent kontejnery** | Docker (per crew) | `crewship-agents` (--internal) | Ne |

> V obou modech: Next.js a crewshipd komunikuji pres Unix socket (Mode 2)
> nebo primo v procesu (Mode 1).
> Agent kontejnery NEMAJI pristup k platforme.

### Architektura (Mode 2)

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
                              │  │ Crew A    │  │ Crew B    │  │ Crew C    │       │
                              │  │ container │  │ container │  │ container │       │
                              │  └───────────┘  └───────────┘  └───────────┘       │
                              │  (--internal, no internet, LLM allowlist only)      │
                              └─────────────────────────────────────────────────────┘
```

### Komunikacni flow

```
1. Browser → HTTPS → Next.js / crewship (REST API, auth, CRUD)
2. Browser → WSS → crewshipd (real-time streaming)
3. Next.js → Unix socket / in-process → crewshipd (agent start/stop, status, files)
4. crewshipd → Docker SDK → Agent kontejner (exec, attach, logs)
5. Agent kontejner → HTTPS → LLM API (allowlisted endpoints only)
6. External → HTTPS → crewshipd (webhook trigger)
```

---

## 6. LOKALNI VYVOJ (Mac/Linux)

### 6.1 Prerekvizity

- Node.js 22+ (pnpm)
- Go 1.23+
- Docker Desktop (nebo colima/podman)
- PostgreSQL 16 (pres Docker Compose)

### 6.2 Setup

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

### 6.3 docker-compose.yml (lokalni dev)

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

### 6.4 .env.local (lokalni dev)

```bash
DATABASE_URL=postgresql://crewship:crewship@localhost:5432/crewship
NEXTAUTH_SECRET=dev-secret-min-32-chars-openssl-rand
NEXTAUTH_URL=http://localhost:3000
ENCRYPTION_KEY=dev-key-64-hex-chars-openssl-rand-hex-32
CREWSHIPD_SOCKET=/tmp/crewship.sock
```

---

## 7. STAGING (Coolify na Proxmox)

### 7.1 Infrastruktura

```
Proxmox server (128GB RAM, i7-12700)
  └── VM/LXC s Coolify
      ├── crewship-nextjs (Coolify Docker service)
      ├── crewship-go (Coolify Docker service)
      ├── crewship-postgres (Coolify PostgreSQL service)
      └── Agent kontejnery (crewshipd vytvari dynamicky)
```

### 7.2 Coolify services

| Service | Image | Port | Docker socket |
|---|---|---|---|
| crewship-nextjs | `ghcr.io/crewship-ai/crewship:latest` | 3000 | Ne |
| crewship-go | `ghcr.io/crewship-ai/crewshipd:latest` | 8080 | Ano |
| crewship-postgres | `postgres:16-alpine` | 5432 | Ne |

### 7.3 Deployment postup

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

## 8. DOCKER IMAGES

### 8.1 Next.js Dockerfile

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

### 8.2 crewshipd Dockerfile

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

### 8.3 Agent Runtime Dockerfile

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

## 9. DOCKER NETWORKING

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

## 10. PERSISTENT STORAGE

### Mode 1 (Single Binary)

```
~/.crewship/
  ├── crewship.db                                  ← SQLite databaze
  ├── config.yaml                                  ← konfigurace
  ├── crewship.pid                                 ← PID soubor
  ├── skills/                                      ← nainstalovane skills
  ├── output/{workspace-id}/{crew-name}/{agent-name}/    ← agent deliverables
  ├── conversations/{workspace-id}/{agent-id}/           ← JSONL session files
  └── logs/crews/{crew-id}/agents/{agent-id}/      ← JSONL logy
```

### Mode 2 (Docker Compose)

```
/var/lib/crewship/
  ├── output/{workspace-id}/{crew-name}/{agent-name}/   ← agent deliverables
  ├── conversations/{workspace-id}/{agent-id}/           ← JSONL session files
  └── bbolt/                                       ← Go service WAL

/var/log/crewship/
  └── crews/{crew-id}/agents/{agent-id}/
      └── current.jsonl                            ← agent logs (logrotated)
```

### Logrotate konfigurace (Mode 2)

```
# /etc/logrotate.d/crewship
/var/log/crewship/crews/*/agents/*/current.jsonl {
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

## 11. HEALTH MONITORING

### 11.1 Health endpoint

```
GET /api/v1/health → Next.js kontroluje DB + crewshipd
GET /metrics       → crewshipd Prometheus metriky
```

### 11.2 Prometheus metriky (Go)

```
crewship_websocket_connections_total
crewship_websocket_messages_total
crewship_agent_runs_total{status="completed|failed|timeout"}
crewship_agent_run_duration_seconds
crewship_docker_containers_active
crewship_ipc_requests_total{method="start|stop|status"}
crewship_ipc_request_duration_seconds
```

### 11.3 cAdvisor (container metriky)

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

### 11.4 `crewship doctor` (Mode 1)

```
$ crewship doctor
✅ Docker:         Running (v27.5.1)
✅ Port 3001:      Available
✅ Database:       SQLite (~/.crewship/crewship.db, 2.4 MB)
✅ Data directory:  ~/.crewship/ (142 MB)
✅ Agent network:   crewship-agents (--internal)
✅ Version:        v0.3.2 (latest)
⚠️  Disk space:    12 GB free (recommend 20+ GB for agent containers)
```

---

## 12. ENVIRONMENT VARIABLES

### 12.1 Next.js

| Promenna | Povinne | Popis |
|---|---|---|
| `DATABASE_URL` | Ano | PostgreSQL connection string |
| `NEXTAUTH_SECRET` | Ano | JWT signing secret |
| `NEXTAUTH_URL` | Ano | Application URL |
| `ENCRYPTION_KEY` | Ano | AES-256-GCM key (hex) |
| `CREWSHIPD_SOCKET` | Ano | Path k Unix socket |
| `NODE_ENV` | Ano | production / development |

### 12.2 crewshipd (Go)

| Promenna | Povinne | Popis |
|---|---|---|
| `CREWSHIPD_SOCKET` | Ano | Path k Unix socket |
| `CREWSHIPD_HTTP_PORT` | Ne | HTTP port pro WebSocket (default: 8080) |
| `CREWSHIPD_LOG_DIR` | Ne | Log directory (default: /var/log/crewship) |
| `CREWSHIPD_OUTPUT_DIR` | Ne | Output directory (default: /var/lib/crewship/output) |
| `CREWSHIPD_BBOLT_PATH` | Ne | bbolt DB path (default: /var/lib/crewship/bbolt/crewship.db) |
| `DATABASE_URL` | Ano | PostgreSQL (pro IPC validaci — Go cte agent/crew data) |

### 12.3 Single binary (Mode 1)

| Promenna | Povinne | Popis |
|---|---|---|
| `CREWSHIP_PORT` | Ne | HTTP port (default: 3001) |
| `CREWSHIP_DATA_DIR` | Ne | Data directory (default: ~/.crewship) |
| `DB_PROVIDER` | Ne | `sqlite` (default) nebo `postgresql` |
| `DATABASE_URL` | Ne | `file:~/.crewship/crewship.db` (default) nebo `postgresql://...` |
| `ENCRYPTION_KEY` | Ne | Auto-generated pri prvnim spusteni, ulozeno v config.yaml |

### 12.4 Agent kontejner (injektovane)

| Promenna | Popis |
|---|---|
| `CREWSHIP_AGENT_ID` | Agent UUID |
| `CREWSHIP_TEAM_ID` | Crew UUID |
| `CREWSHIP_SESSION_ID` | Current session UUID |
| `{USER_CREDENTIALS}` | Dynamicky dle AgentCredential (napr. OPENAI_API_KEY) |

---

## 13. BACKUP

| Co | Jak | Frekvence |
|---|---|---|
| PostgreSQL (Mode 2) | `pg_dump` → S3/local | Denne |
| SQLite (Mode 1) | Kopie `~/.crewship/crewship.db` | Denne (uzivatel) |
| /output/ (agent deliverables) | rsync → backup server | Denne |
| JSONL konverzace | rsync → backup server | Denne |
| bbolt WAL | Snapshot pri graceful shutdown | Automaticky |

---

## 14. SCALING (Phase 2+)

### Single instance (Mode 1 -- MVP)

```
1x crewship binary (SQLite)
Kapacita: ~20 soucasnych agentu, ~50 WebSocket connections
RAM: ~200 MB infrastruktura + agent kontejnery
```

### Single instance (Mode 2)

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

## 15. OTEVRENE OTAZKY

1. **Zero-downtime deploy** — jak restartovat crewshipd bez ztraceni WebSocket connections? Graceful shutdown + client reconnect?
2. **Agent image updates** — jak rolling-update agent runtime image? Stop kontejner → pull new image → start?
3. **Log aggregation** — Loki/Grafana pro centralni log viewing? Nebo staci JSONL + file browser?
4. **SSL termination** — Coolify/Caddy/nginx pred Next.js + crewshipd? Nebo kazdy service vlastni TLS?
5. **Secrets management** — Coolify env vars staci? Nebo Vault/SOPS pro ENCRYPTION_KEY?
6. **SQLite → PostgreSQL migrace** — automaticky CLI tool (`crewship migrate`)? Nebo manualni dump/import?
7. **Auto-update UX** — notifikace v UI kdyz je nova verze? Nebo silent background update?
