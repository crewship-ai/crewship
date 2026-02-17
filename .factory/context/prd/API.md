# Crewship -- API & Wire Protocol (API.md)

**Verze:** 3.0
**Datum:** 2026-02-17
**Runtime:** Next.js API Routes (REST) + crewshipd Go service (WebSocket + Webhooks)
**Validace:** Zod (vstupy) + RFC 7807 Problem Details (chyby)
**Auth:** NextAuth.js (Auth.js v5) — JWT session token
**Autorizace:** CASL (aplikacni uroven)
**Rate limiting:** In-memory (Go + Next.js middleware)
**Verzovani:** URL prefix `/api/v1/`

---

## 1. PREHLED

### Dva procesy, dve komunikacni vrstvy

| Vrstva | Transport | Proces | Ucel |
|---|---|---|---|
| **REST API** | HTTPS | Next.js | CRUD, management, auth |
| **WebSocket** | WSS | crewshipd (Go) | Real-time streaming, chat, file events |
| **Webhook ingress** | HTTPS | crewshipd (Go) | Externi triggery (Grafana, n8n, atd.) |
| **IPC** | Unix socket | Next.js ↔ crewshipd | Interni komunikace mezi procesy |

### Architektura

```
Browser ──── REST API (/api/v1/*) ──── Next.js Route Handlers ──── Prisma → PostgreSQL
  │                                         │
  │                                    Unix socket (/run/crewship/crewship.sock)
  │                                         │
  └──── WebSocket (wss://) ──── crewshipd (Go)
                                    ├── Docker SDK → Agent kontejner
                                    ├── Log collector → JSONL soubory
                                    ├── File server → /output/ (fsnotify)
                                    ├── Webhook handler
                                    └── bbolt WAL (job state)

External ──── Webhook (/api/v1/webhooks/*) ──── crewshipd (Go) → Agent trigger
```

### Konvence

- Vsechny endpointy vyzaduji autentizaci (krome `/auth/*`, healthcheck, webhooks)
- Vsechny mutace se loguji do audit logu (middleware)
- Vsechny odpovedi: `Content-Type: application/json`
- Vsechny chyby: RFC 7807 Problem Details
- Cesty: `kebab-case` (`/agent-runs`, ne `/agentRuns`)
- Pagination: cursor-based
- Soft delete: DELETE nastavi `deleted_at`
- Timestamps: ISO 8601 UTC
- IDs: UUID v4

---

## 2. AUTENTIZACE

### 2.1 NextAuth.js session (REST API)

```typescript
// lib/auth.ts
import { auth } from '@/auth'

export async function requireAuth(): Promise<User> {
  const session = await auth()
  if (!session?.user?.id) {
    throw new ProblemDetailsError({
      type: "https://crewship.ai/errors/unauthorized",
      title: "Unauthorized",
      status: 401,
      code: "AUTH_REQUIRED",
    })
  }
  return prisma.user.findUniqueOrThrow({ where: { id: session.user.id } })
}
```

### 2.2 WebSocket autentizace (Go service)

```
1. Browser pozada Next.js API o short-lived WS token (5min, JWT)
2. Browser se pripoji na wss://host/ws?token=<jwt>
3. crewshipd validuje JWT pri handshake
4. crewshipd overi team membership pres IPC dotaz na Next.js
5. Po pripojeni: token neni dale potreba (connection = authenticated)
```

### 2.3 Webhook autentizace (Go service)

```
POST /api/v1/webhooks/{team-id}/{agent-id}/trigger
Headers: X-Webhook-Secret: <per-agent-secret>

crewshipd:
1. Extrahuje team-id a agent-id z URL
2. Nacte agent.webhook_secret z DB (pres IPC na Next.js)
3. Porovna s X-Webhook-Secret headerem (constant-time comparison)
4. Neplatny secret → 401 + audit log
```

### 2.4 Auth endpointy (NextAuth.js)

| Metoda | Path | Auth | Popis |
|---|---|---|---|
| `*` | `/api/auth/*` | Ne | NextAuth.js handler (login, callback, session) |
| `POST` | `/api/v1/auth/register` | Ne | Registrace (email + heslo) |

---

## 3. REST API ENDPOINTY (Next.js)

### 3.1 Organizations

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs` | Auth | Seznam org uzivatele |
| `POST` | `/api/v1/orgs` | Auth | Vytvorit organizaci |
| `GET` | `/api/v1/orgs/{orgId}` | Member | Detail organizace |
| `PATCH` | `/api/v1/orgs/{orgId}` | ADMIN+ | Upravit organizaci |
| `DELETE` | `/api/v1/orgs/{orgId}` | OWNER | Smazat organizaci (soft delete) |

### 3.2 Organization Members

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/members` | Member | Seznam clenu |
| `POST` | `/api/v1/orgs/{orgId}/members/invite` | ADMIN+ | Pozvat clena (email) |
| `PATCH` | `/api/v1/orgs/{orgId}/members/{memberId}` | ADMIN+ | Zmenit roli |
| `DELETE` | `/api/v1/orgs/{orgId}/members/{memberId}` | ADMIN+ | Odebrat clena |

### 3.3 Teams

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/teams` | Member | Seznam tymu (RBAC filtered) |
| `POST` | `/api/v1/orgs/{orgId}/teams` | MANAGER+ | Vytvorit tym |
| `GET` | `/api/v1/orgs/{orgId}/teams/{teamId}` | TeamMember | Detail tymu |
| `PATCH` | `/api/v1/orgs/{orgId}/teams/{teamId}` | MANAGER+ | Upravit tym |
| `DELETE` | `/api/v1/orgs/{orgId}/teams/{teamId}` | ADMIN+ | Smazat tym (soft delete) |

### 3.4 Team Members

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/teams/{teamId}/members` | TeamMember | Seznam clenu tymu |
| `POST` | `/api/v1/orgs/{orgId}/teams/{teamId}/members` | MANAGER+ | Pridat clena do tymu |
| `DELETE` | `/api/v1/orgs/{orgId}/teams/{teamId}/members/{memberId}` | MANAGER+ | Odebrat z tymu |

### 3.5 Agents

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/agents` | Member | Seznam agentu (RBAC filtered) |
| `POST` | `/api/v1/orgs/{orgId}/agents` | MANAGER+ | Vytvorit agenta |
| `GET` | `/api/v1/orgs/{orgId}/agents/{agentId}` | TeamMember | Detail agenta |
| `PATCH` | `/api/v1/orgs/{orgId}/agents/{agentId}` | MANAGER+ | Upravit agenta |
| `DELETE` | `/api/v1/orgs/{orgId}/agents/{agentId}` | ADMIN+ | Smazat agenta (soft delete) |
| `POST` | `/api/v1/orgs/{orgId}/agents/{agentId}/start` | MEMBER+ | Spustit agenta (IPC → Go) |
| `POST` | `/api/v1/orgs/{orgId}/agents/{agentId}/stop` | MEMBER+ | Zastavit agenta (IPC → Go) |
| `GET` | `/api/v1/orgs/{orgId}/agents/{agentId}/status` | TeamMember | Live status (IPC → Go) |

### 3.6 Agent Skills

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/agents/{agentId}/skills` | TeamMember | Prirazene skills |
| `POST` | `/api/v1/orgs/{orgId}/agents/{agentId}/skills` | MANAGER+ | Priradit skill |
| `DELETE` | `/api/v1/orgs/{orgId}/agents/{agentId}/skills/{skillId}` | MANAGER+ | Odebrat skill |

### 3.7 Agent Credentials

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/agents/{agentId}/credentials` | MANAGER+ | Prirazene credentials (masked) |
| `POST` | `/api/v1/orgs/{orgId}/agents/{agentId}/credentials` | MANAGER+ | Priradit credential |
| `DELETE` | `/api/v1/orgs/{orgId}/agents/{agentId}/credentials/{credId}` | MANAGER+ | Odebrat credential |

### 3.8 Skills (catalog)

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/skills` | Auth | Seznam dostupnych skills |
| `GET` | `/api/v1/skills/{skillId}` | Auth | Detail skillu |

### 3.9 Credentials (vault)

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/credentials` | MANAGER+ | Seznam credentials (masked values) |
| `POST` | `/api/v1/orgs/{orgId}/credentials` | MANAGER+ | Vytvorit credential (encrypt + store) |
| `PATCH` | `/api/v1/orgs/{orgId}/credentials/{credId}` | MANAGER+ | Upravit (re-encrypt value) |
| `DELETE` | `/api/v1/orgs/{orgId}/credentials/{credId}` | ADMIN+ | Smazat credential (soft delete) |

### 3.10 Conversation Sessions (metadata)

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/agents/{agentId}/sessions` | TeamMember | Seznam sessions (metadata) |
| `GET` | `/api/v1/orgs/{orgId}/sessions/{sessionId}` | TeamMember | Detail session + zpravy (IPC → Go cte JSONL) |
| `POST` | `/api/v1/orgs/{orgId}/agents/{agentId}/sessions` | MEMBER+ | Vytvorit session |

### 3.11 Agent Runs

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/agents/{agentId}/runs` | TeamMember | Historie behu |
| `GET` | `/api/v1/orgs/{orgId}/runs/{runId}` | TeamMember | Detail behu |

### 3.12 Files (agent output)

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/teams/{teamId}/files` | TeamMember | Seznam souboru (IPC → Go, fsnotify) |
| `GET` | `/api/v1/orgs/{orgId}/teams/{teamId}/files/{path}` | TeamMember | Stahnout soubor (IPC → Go) |
| `GET` | `/api/v1/orgs/{orgId}/teams/{teamId}/files/{path}/preview` | TeamMember | Preview (PDF, MD, image) |

### 3.13 Audit Log

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/orgs/{orgId}/audit-logs` | ADMIN+ | Audit log (cursor paginated) |

### 3.14 WebSocket Token

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `POST` | `/api/v1/ws/token` | Auth | Ziskat short-lived WS token (5min JWT) |

### 3.15 Health

| Metoda | Path | Auth | Popis |
|---|---|---|---|
| `GET` | `/api/v1/health` | Ne | Healthcheck |

```json
{
  "status": "ok",
  "version": "1.0.0",
  "checks": {
    "database": "ok",
    "crewshipd": "ok"
  }
}
```

---

## 4. WEBHOOK API (Go service)

### 4.1 Webhook trigger

```
POST /api/v1/webhooks/{team-id}/{agent-id}/trigger
Headers:
  Content-Type: application/json
  X-Webhook-Secret: <per-agent-secret>
Body:
  {
    "event": "alert",
    "source": "grafana",
    "data": { ... }
  }
```

**Response:**
- `202 Accepted` — agent trigger queued
- `401 Unauthorized` — invalid secret
- `404 Not Found` — agent not found
- `429 Too Many Requests` — rate limit exceeded

### 4.2 Webhook flow

```
1. External system → POST webhook → crewshipd (Go)
2. crewshipd validates X-Webhook-Secret (IPC → Next.js → DB)
3. crewshipd creates AgentRun (trigger_type=WEBHOOK) via IPC
4. crewshipd starts Docker exec with webhook payload as input
5. Agent processes webhook data, writes output
6. Result available in /output/ + session JSONL
```

---

## 5. WEBSOCKET PROTOCOL (Go service)

### 5.1 Connection

```
wss://host/ws?token=<short-lived-jwt>
```

### 5.2 Message format (JSON)

```typescript
// Client → Server
interface WSClientMessage {
  type: "subscribe" | "unsubscribe" | "send_message" | "ping"
  channel?: string           // "agent:{agentId}" | "team:{teamId}" | "files:{teamId}"
  payload?: {
    session_id?: string
    content?: string         // user message to agent
  }
}

// Server → Client
interface WSServerMessage {
  type: "agent_event" | "file_event" | "status" | "error" | "pong"
  channel?: string
  payload: unknown
}
```

### 5.3 Kanaly

| Kanal | Format | Eventy |
|---|---|---|
| `agent:{agentId}` | Agent streaming | `thinking`, `text`, `tool_call`, `tool_result`, `status_change` |
| `team:{teamId}` | Team events | `agent_started`, `agent_stopped`, `container_status` |
| `files:{teamId}` | File events (fsnotify) | `file_created`, `file_modified`, `file_deleted` |

### 5.4 Agent streaming events

```json
{"type":"agent_event","channel":"agent:uuid","payload":{"event":"thinking","content":"Analyzing..."}}
{"type":"agent_event","channel":"agent:uuid","payload":{"event":"text","content":"Here is the report..."}}
{"type":"agent_event","channel":"agent:uuid","payload":{"event":"tool_call","tool":"web-search","args":{"query":"..."}}}
{"type":"agent_event","channel":"agent:uuid","payload":{"event":"tool_result","tool":"web-search","result":{...}}}
{"type":"agent_event","channel":"agent:uuid","payload":{"event":"status_change","status":"IDLE"}}
```

### 5.5 File events (fsnotify)

```json
{"type":"file_event","channel":"files:team-uuid","payload":{
  "event":"file_created",
  "path":"reports/january-2026.pdf",
  "agent":"anna",
  "size":125000,
  "timestamp":"2026-02-11T10:30:00Z"
}}
```

### 5.6 Bezpecnost

- Auth: JWT validated pri handshake, connection = authenticated
- Channel subscription: Go service overuje team membership (IPC → Next.js)
- Rate limiting: 60 messages/min per connection
- Heartbeat: ping/pong kazdych 30s, timeout 60s
- Max connections per user: 10

---

## 6. IPC PROTOKOL (Next.js ↔ crewshipd)

### 6.1 Transport

```
Local: Unix socket (/run/crewship/crewship.sock)
K8s (Phase 3): gRPC
```

### 6.2 Protocol (MVP: HTTP over Unix socket)

Next.js posila HTTP requesty na Unix socket. crewshipd bezi jako HTTP server na socketu.

```typescript
// lib/crewshipd-client.ts
import http from 'http'

export async function crewshipdRequest(path: string, options?: RequestInit) {
  return fetch(`http://unix:${CREWSHIPD_SOCKET}:${path}`, options)
}

// Priklady:
await crewshipdRequest('/agents/uuid/start', { method: 'POST', body: JSON.stringify({...}) })
await crewshipdRequest('/agents/uuid/status')
await crewshipdRequest('/teams/uuid/files')
await crewshipdRequest('/sessions/uuid/messages?offset=0&limit=50')
```

### 6.3 IPC endpointy (Go service)

| Metoda | Path | Popis |
|---|---|---|
| `POST` | `/agents/{id}/start` | Spustit agenta (Docker exec) |
| `POST` | `/agents/{id}/stop` | Zastavit agenta |
| `GET` | `/agents/{id}/status` | Live status |
| `GET` | `/sessions/{id}/messages` | Cist JSONL zpravy |
| `GET` | `/teams/{id}/files` | Seznam souboru v /output/ |
| `GET` | `/teams/{id}/files/{path}` | Stahnout soubor |
| `POST` | `/teams/{id}/container/start` | Spustit kontejner tymu |
| `POST` | `/teams/{id}/container/stop` | Zastavit kontejner tymu |
| `GET` | `/teams/{id}/container/status` | Status kontejneru |
| `GET` | `/metrics` | Prometheus metriky |
| `GET` | `/health` | Healthcheck Go service |

---

## 7. CHYBOVE ODPOVEDI (RFC 7807)

```json
{
  "type": "https://crewship.ai/errors/not-found",
  "title": "Not Found",
  "status": 404,
  "detail": "Agent with ID 'abc123' was not found",
  "code": "AGENT_NOT_FOUND",
  "instance": "/api/v1/orgs/org-uuid/agents/abc123"
}
```

### Standardni chybove kody

| Status | Code | Popis |
|---|---|---|
| 400 | `VALIDATION_ERROR` | Neplatny vstup (Zod) |
| 401 | `AUTH_REQUIRED` | Chybejici nebo neplatny token |
| 403 | `FORBIDDEN` | Nedostatecna opravneni (CASL) |
| 404 | `NOT_FOUND` | Entita nenalezena |
| 409 | `CONFLICT` | Duplicitni slug, uz existuje |
| 422 | `UNPROCESSABLE` | Validni format, ale nelogicky (napr. prirazeni credential z jineho tymu) |
| 429 | `RATE_LIMIT` | Prekrocen rate limit |
| 500 | `INTERNAL_ERROR` | Neocekavana chyba serveru |
| 503 | `SERVICE_UNAVAILABLE` | DB nebo crewshipd nedostupne |

---

## 8. PAGINATION (cursor-based)

```
GET /api/v1/orgs/{orgId}/agents?cursor=abc123&limit=20

Response:
{
  "data": [...],
  "pagination": {
    "next_cursor": "xyz789",
    "has_more": true,
    "total": 45
  }
}
```

Cursor = opaque string (base64 encoded `created_at` + `id`).

---

## 9. RATE LIMITING

### Next.js middleware (in-memory Map)

```typescript
// middleware.ts — per-process in-memory
const rateLimits = new Map<string, { count: number, resetAt: number }>()
```

### Go service (`golang.org/x/time/rate`)

Token bucket per-IP a per-user. Viz SECURITY.md sekce 5.3.

> **Omezeni MVP:** In-memory, neskaluje pres vice instanci.
> Phase 2: distribuovany rate limiter.

---

## 10. DOCKER NETWORKING (agent izolace)

```
crewship-internal (platforma: Next.js + crewshipd + PostgreSQL)
  ↳ Agent kontejner NEMA pristup

crewship-agents (--internal, bez default route)
  ↳ Agent kontejner bezi zde
  ↳ Pristup POUZE k LLM API na explicitnim allowlistu (iptables)
  ↳ ICC disabled (kontejnery se navzajem nevidi)
```

```bash
# Vytvoreni siti
docker network create crewship-internal
docker network create crewship-agents --internal

# LLM allowlist (iptables na hostu)
iptables -A DOCKER-USER -s crewship-agents -d api.anthropic.com -p tcp --dport 443 -j ACCEPT
iptables -A DOCKER-USER -s crewship-agents -d api.openai.com -p tcp --dport 443 -j ACCEPT
iptables -A DOCKER-USER -s crewship-agents -j DROP
```

---

## 11. CLI commands (single binary mode)

| Command | Popis |
|---|---|
| `crewship start [--port N] [--db URL]` | Spusti platformu (default: SQLite, port 3001) |
| `crewship stop` | Zastavi platformu |
| `crewship status` | Status sluzeb (web, engine, Docker) |
| `crewship logs [--follow]` | Tail logy |
| `crewship doctor` | Diagnostika (Docker check, port check, DB check) |
| `crewship skill install <name>` | Instalace skillu z marketplace |
| `crewship skill list` | Seznam nainstalovanych skills |
| `crewship skill search <query>` | Hledani v marketplace |
| `crewship update` | Aktualizace na nejnovejsi verzi |
| `crewship version` | Verze |
| `crewship migrate --to <db-url>` | Migrace SQLite → PostgreSQL |

---

## 12. Skill Marketplace API (planovane)

### Endpoints
- `GET /api/v1/marketplace/skills` -- browse skills (search, category, badge filter)
- `GET /api/v1/marketplace/skills/:slug` -- skill detail (permissions, rating, install count)
- `POST /api/v1/marketplace/skills/:slug/install` -- install skill to agent
- `DELETE /api/v1/marketplace/skills/:slug/uninstall` -- uninstall skill
- `POST /api/v1/marketplace/skills` -- publish skill (community)
- `GET /api/v1/marketplace/categories` -- skill categories

---

## 13. Per-Agent Network Control API (planovane)

### Endpoints
- `GET /api/v1/agents/:id/network` -- current network config
- `PUT /api/v1/agents/:id/network` -- update network config
  ```json
  {
    "internet_enabled": true,
    "domain_whitelist": ["github.com", "api.openai.com"],
    "local_network_enabled": false,
    "local_network_cidr": null
  }
  ```

---

## 14. OTEVRENE OTAZKY

1. **API versioning** — jak budeme delat v2 endpointy? Novy prefix `/api/v2/`?
2. **Rate limit storage** — per-process Map staci pro single instance, ale ne pro multi-instance. Phase 2 reseni?
3. **WebSocket horizontal scaling** — jak predavat connections mezi Go instancemi? (Phase 3, K8s)
4. **File streaming** — velke soubory (>100MB) streamovat nebo chunked download?
5. **Webhook retry** — pokud agent neni dostupny, jak opakovat webhook? Persistent queue v bbolt?
