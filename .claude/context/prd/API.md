# Crewship -- API & Wire Protocol (API.md)

**Verze:** 4.1
**Datum:** 2026-02-20
**Runtime:** Go single binary (REST API + WebSocket + Webhooks)
**Validace:** Go validace (vstupy) + RFC 7807 Problem Details (chyby)
**Auth:** Go (NextAuth-compatible JWE) — JWT session token
**Autorizace:** Go RBAC middleware (aplikacni uroven)
**Rate limiting:** Go middleware (in-memory)
**Verzovani:** URL prefix `/api/v1/`

---

## 1. PREHLED

### Jeden proces, tri komunikacni vrstvy

| Vrstva | Transport | Proces | Ucel |
|---|---|---|---|
| **REST API** | HTTPS | Go (`crewship`) | CRUD, management, auth |
| **WebSocket** | WSS | Go (`crewship`) | Real-time streaming, chat, file events |
| **Webhook ingress** | HTTPS | Go (`crewship`) | Externi triggery (Grafana, n8n, atd.) |

### Architektura

```
Browser ──── REST API (/api/v1/*) ──── Go HTTP handlers ──── database/sql → SQLite / PostgreSQL
  │                                         │
  │                                    Embedded static UI (Next.js static export via embed.FS)
  │
  └──── WebSocket (wss://) ──── Go (ws gateway)
                                    ├── Docker SDK → Agent kontejner
                                    ├── Log collector → JSONL soubory
                                    ├── File server → /output/ (fsnotify)
                                    ├── Webhook handler
                                    └── bbolt WAL (job state)

External ──── Webhook (/api/v1/webhooks/*) ──── Go → Agent trigger
```

### Konvence

- Vsechny endpointy vyzaduji autentizaci (krome `/api/auth/*`, healthcheck, webhooks)
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

### 2.1 NextAuth-compatible session (Go)

Go implementuje NextAuth-compatible endpointy v `internal/api/nextauth.go`, takze
frontend next-auth/react SDK funguje bez zmeny. Autentizace probiha pres JWE token
(NextAuth format) validovany v Go (`internal/auth/`).

```go
// internal/api/middleware.go — RequireAuth middleware
// Extrahuje JWT z cookie/header, validuje, a vlozi user do contextu
func (m *AuthMiddleware) RequireAuth(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        user, err := m.validator.ValidateRequest(r)
        if err != nil {
            writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication required")
            return
        }
        ctx := context.WithValue(r.Context(), userContextKey, user)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}
```

### 2.2 WebSocket autentizace

```
1. Browser pozada Go API o short-lived WS token (5min, JWT) — GET /api/v1/ws-token
2. Browser se pripoji na wss://host/ws?token=<jwt>
3. Go validuje JWT pri handshake
4. Go overi crew membership z DB
5. Po pripojeni: token neni dale potreba (connection = authenticated)
```

### 2.3 Webhook autentizace

```
POST /api/v1/webhooks/{crew-id}/{agent-id}/trigger
Headers: X-Webhook-Secret: <per-agent-secret>

Go handler:
1. Extrahuje crew-id a agent-id z URL
2. Nacte agent.webhook_secret z DB
3. Porovna s X-Webhook-Secret headerem (constant-time comparison)
4. Neplatny secret → 401 + audit log
```

### 2.4 Auth endpointy

| Metoda | Path | Auth | Popis |
|---|---|---|---|
| `GET` | `/api/auth/csrf` | Ne | CSRF token |
| `GET` | `/api/auth/providers` | Ne | Seznam auth providers |
| `GET` | `/api/auth/session` | Ne | Aktualni session |
| `POST` | `/api/auth/callback/credentials` | Ne | Login (email + heslo) |
| `GET` | `/api/auth/signin` | Ne | Sign-in page redirect |
| `POST` | `/api/auth/signout` | Ne | Odhlaseni |
| `GET` | `/api/auth/error` | Ne | Auth error page |
| `POST` | `/api/v1/auth/signup` | Ne | Registrace (email + heslo, bcrypt) |

---

## 3. REST API ENDPOINTY (Go)

Vsechny REST endpointy jsou registrovany v `internal/api/router.go`.
Handlery jsou implementovany v odpovidajicich Go souborech (`workspaces.go`, `crews.go`, `agents.go`, atd.).
DB pristup pres `database/sql` (zadny ORM).

### 3.1 System

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/system/runtime` | Auth | Runtime info (verze, build, Go version) |

### 3.2 Workspaces

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/workspaces` | Auth | Seznam workspaces uzivatele |
| `POST` | `/api/v1/workspaces` | Auth | Vytvorit workspace |
| `GET` | `/api/v1/workspaces/{workspaceId}` | Member | Detail workspace |
| `PATCH` | `/api/v1/workspaces/{workspaceId}` | ADMIN+ | Upravit workspace |

### 3.2 Workspace Members

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/workspaces/{workspaceId}/members` | Member | Seznam clenu |
| `POST` | `/api/v1/workspaces/{workspaceId}/members` | ADMIN+ | Pridat clena |
| `DELETE` | `/api/v1/workspaces/{workspaceId}/members/{memberId}` | ADMIN+ | Odebrat clena |

### 3.3 Workspace Invitations

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/workspaces/{workspaceId}/invitations` | Member | Seznam pozvanek |
| `POST` | `/api/v1/workspaces/{workspaceId}/invitations` | ADMIN+ | Vytvorit pozvanku |

### 3.4 Crews

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/crews` | Member | Seznam crews (RBAC filtered) |
| `POST` | `/api/v1/crews` | MANAGER+ | Vytvorit crew |
| `GET` | `/api/v1/crews/{crewId}` | CrewMember | Detail crew |
| `PATCH` | `/api/v1/crews/{crewId}` | MANAGER+ | Upravit crew |
| `DELETE` | `/api/v1/crews/{crewId}` | ADMIN+ | Smazat crew (soft delete) |

### 3.5 Crew Members

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/crews/{crewId}/members` | CrewMember | Seznam clenu crew |
| `POST` | `/api/v1/crews/{crewId}/members` | MANAGER+ | Pridat clena do crew |
| `DELETE` | `/api/v1/crews/{crewId}/members/{memberId}` | MANAGER+ | Odebrat z crew |

### 3.6 Agents

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/agents` | Member | Seznam agentu (RBAC filtered) |
| `POST` | `/api/v1/agents` | MANAGER+ | Vytvorit agenta |
| `GET` | `/api/v1/agents/{agentId}` | CrewMember | Detail agenta |
| `PATCH` | `/api/v1/agents/{agentId}` | MANAGER+ | Upravit agenta |
| `DELETE` | `/api/v1/agents/{agentId}` | ADMIN+ | Smazat agenta (soft delete) |

### 3.7 Agent Skills

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/agents/{agentId}/skills` | CrewMember | Prirazene skills |
| `POST` | `/api/v1/agents/{agentId}/skills` | MANAGER+ | Priradit skill |
| `DELETE` | `/api/v1/agents/{agentId}/skills/{skillId}` | MANAGER+ | Odebrat skill |

### 3.8 Agent Credentials

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/agents/{agentId}/credentials` | MANAGER+ | Prirazene credentials (masked) |
| `POST` | `/api/v1/agents/{agentId}/credentials` | MANAGER+ | Priradit credential |
| `DELETE` | `/api/v1/agents/{agentId}/credentials/{assignmentId}` | MANAGER+ | Odebrat credential |

### 3.9 Agent Chats & Runs

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/agents/{agentId}/chats` | CrewMember | Seznam chat sessions |
| `POST` | `/api/v1/agents/{agentId}/chats` | CrewMember | Vytvorit novou chat session |
| `GET` | `/api/v1/agents/{agentId}/runs` | CrewMember | Historie behu |

### 3.10 Credentials (vault)

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/credentials` | MANAGER+ | Seznam credentials (masked values) |
| `POST` | `/api/v1/credentials` | MANAGER+ | Vytvorit credential (AES-256-GCM encrypt + store) |
| `GET` | `/api/v1/credentials/{credentialId}` | MANAGER+ | Detail credential (masked) |
| `PATCH` | `/api/v1/credentials/{credentialId}` | MANAGER+ | Upravit (re-encrypt value) |
| `DELETE` | `/api/v1/credentials/{credentialId}` | ADMIN+ | Smazat credential (soft delete) |

### 3.11 Skills (catalog)

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/skills` | Auth | Seznam dostupnych skills (supports `?search=`, `?category=`) |
| `GET` | `/api/v1/skills/{skillId}` | Auth | Detail skillu (includes content, agent_count, credential_requirements) |
| `POST` | `/api/v1/workspaces/{workspaceId}/skills/import` | MANAGER+ | Importovat skill z URL nebo obsahu SKILL.md |

#### Import endpointu

```json
// POST /api/v1/workspaces/{workspaceId}/skills/import
// Body (jeden z poli je povinny):
{ "url": "https://github.com/org/skills/blob/main/SKILL.md" }
// nebo
{ "content": "---\nname: my-skill\n---\n# My Skill..." }

// Response 201:
{
  "skill_id": "sk_abc123",
  "name": "My Skill",
  "slug": "my-skill",
  "created": true   // false = aktualizovan existujici skill
}
```

Podporovane URL formaty:
- `https://github.com/owner/repo/blob/branch/path.md` → automaticky konvertovano na raw URL
- `owner/repo/path.md` → `https://raw.githubusercontent.com/owner/repo/main/path.md`
- Jakekoliv jina HTTPS URL → beze zmeny

Bezpecnostni omezeni (SSRF ochrana):
- Povoleny POUZE HTTPS URL (HTTP je odmitnuto)
- Blokovany: localhost, 127.0.0.0/8, privatni IP (10.x, 172.16.x, 192.168.x), link-local (169.254.x)
- Klient obdrzi 400 Bad Request s RFC 7807 detail pro zakazane URL
- Omezeni se tykaji pouze `url` pole; `content` pole neni ovlivneno

### 3.12 Missions

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/missions` | Member | Seznam vsech missi v workspace (supports `?status=`) |
| `GET` | `/api/v1/crews/{crewId}/missions` | Member | Seznam missi crew (supports `?status=`, `?limit=`, `?offset=`) |
| `POST` | `/api/v1/crews/{crewId}/missions` | MANAGER+ | Vytvorit missi (requires lead_agent_id with LEAD role) |
| `GET` | `/api/v1/crews/{crewId}/missions/{missionId}` | Member | Detail misse (includes tasks array) |
| `PATCH` | `/api/v1/crews/{crewId}/missions/{missionId}` | MANAGER+ | Upravit missi (status transitions validated) |
| `DELETE` | `/api/v1/crews/{crewId}/missions/{missionId}` | MANAGER+ | Smazat missi (only PLANNING or CANCELLED) |
| `POST` | `/api/v1/crews/{crewId}/missions/{missionId}/tasks` | MANAGER+ | Vytvorit task (auto-BLOCKED if deps incomplete) |
| `PATCH` | `/api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}` | MANAGER+ | Upravit task (auto-unblocks dependents on COMPLETED) |

#### Mission status transitions
- PLANNING → IN_PROGRESS, CANCELLED
- IN_PROGRESS → REVIEW, FAILED, CANCELLED
- REVIEW → COMPLETED, IN_PROGRESS, FAILED, CANCELLED

#### Task status transitions
- PENDING → IN_PROGRESS, SKIPPED
- BLOCKED → PENDING, SKIPPED
- IN_PROGRESS → COMPLETED, FAILED, SKIPPED

#### WebSocket events (mission channels)
- `mission.created` → broadcast to `crew:{crewId}`
- `mission.status` → broadcast to `mission:{missionId}`
- `task.created` → broadcast to `mission:{missionId}`
- `task.status` → broadcast to `mission:{missionId}`

### 3.13 Runs

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/runs` | Member | Seznam behu (paginated) |

### 3.14 Audit Log

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/audit` | ADMIN+ | Audit log (cursor paginated) |

### 3.15 WebSocket Token

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/ws-token` | Auth | Ziskat short-lived WS token (5min JWT) |

### 3.16 Health

| Metoda | Path | Auth | Popis |
|---|---|---|---|
| `GET` | `/api/health` | Ne | Healthcheck |

```json
{
  "status": "ok"
}
```

### 3.17 Admin

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/admin/stats` | OWNER | Systemove statistiky |
| `GET` | `/api/v1/admin/users` | OWNER | Seznam uzivatelu |
| `GET` | `/api/v1/admin/workspaces` | OWNER | Seznam vsech workspaces |

### 3.18 Crewshipd Proxy (agent runtime)

Proxy endpointy pro komunikaci s crewshipd daemonem pres Unix socket (IPC).
Pouzivaji se pro runtime operace nad agenty (debug, soubory, logy, stop).

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/crewshipd` | Auth | Crewshipd healthcheck |
| `GET` | `/api/v1/agents/{agentId}/debug` | CrewMember | Agent debug info |
| `GET` | `/api/v1/agents/{agentId}/files` | CrewMember | Seznam souboru agenta |
| `GET` | `/api/v1/agents/{agentId}/files/download` | CrewMember | Stahnout soubor |
| `GET` | `/api/v1/agents/{agentId}/logs` | CrewMember | Agent logy |
| `POST` | `/api/v1/agents/{agentId}/stop` | CrewMember | Zastavit agenta |
| `GET` | `/api/v1/chats/{chatId}/messages` | Auth | Zpravy chatu (JSONL) |

### 3.19 Internal Routes (crewshipd IPC)

Interni endpointy pro crewshipd daemon. Autorizace pres `X-Internal-Token` header.

| Metoda | Path | Auth | Popis |
|---|---|---|---|
| `GET` | `/api/v1/internal/credentials` | Internal | Decrypted credentials pro agenta |
| `PATCH` | `/api/v1/internal/credentials/{credentialId}` | Internal | Update credential status |
| `POST` | `/api/v1/internal/chats` | Internal | Vytvorit chat session |
| `GET` | `/api/v1/internal/chats/{chatId}/resolve` | Internal | Resolve chat metadata |
| `POST` | `/api/v1/internal/runs` | Internal | Vytvorit agent run zaznam |
| `PATCH` | `/api/v1/internal/runs/{runId}` | Internal | Update run status (started, finished, error) |

### 3.20 Onboarding

| Metoda | Path | Role | Popis |
|---|---|---|---|
| `GET` | `/api/v1/onboarding/status` | Auth | Stav onboardingu (completed/pending) |
| `POST` | `/api/v1/onboarding/complete` | Auth | Oznacit onboarding jako dokonceny |
| `POST` | `/api/v1/onboarding/setup` | Auth | Provest pocatecni setup (workspace + crew) |

---

## 4. WEBHOOK API

### 4.1 Webhook trigger

```
POST /api/v1/webhooks/{crew-id}/{agent-id}/trigger
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
1. External system → POST webhook → Go handler
2. Go validuje X-Webhook-Secret z DB
3. Go vytvofi AgentRun (trigger_type=WEBHOOK)
4. Go spusti Docker exec s webhook payload jako vstup
5. Agent zpracuje webhook data, zapise output
6. Vysledek dostupny v /output/ + session JSONL
```

---

## 5. WEBSOCKET PROTOCOL

### 5.1 Connection

```
wss://host/ws?token=<short-lived-jwt>
```

### 5.2 Message format (JSON)

```typescript
// Client → Server
interface WSClientMessage {
  type: "subscribe" | "unsubscribe" | "send_message" | "ping"
  channel?: string           // "agent:{agentId}" | "crew:{crewId}" | "files:{crewId}"
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
| `crew:{crewId}` | Crew events | `agent_started`, `agent_stopped`, `container_status`, `mission.created` |
| `mission:{missionId}` | Mission events | `mission.status`, `task.created`, `task.status` |
| `files:{crewId}` | File events (fsnotify) | `file_created`, `file_modified`, `file_deleted` |

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
{"type":"file_event","channel":"files:crew-uuid","payload":{
  "event":"file_created",
  "path":"reports/january-2026.pdf",
  "agent":"anna",
  "size":125000,
  "timestamp":"2026-02-11T10:30:00Z"
}}
```

### 5.6 Bezpecnost

- Auth: JWT validated pri handshake, connection = authenticated
- Channel subscription: Go overuje crew membership z DB
- Rate limiting: 60 messages/min per connection
- Heartbeat: ping/pong kazdych 30s, timeout 60s
- Max connections per user: 10

---

## 6. CHYBOVE ODPOVEDI (RFC 7807)

```json
{
  "type": "https://crewship.ai/errors/not-found",
  "title": "Not Found",
  "status": 404,
  "detail": "Agent with ID 'abc123' was not found",
  "code": "AGENT_NOT_FOUND",
  "instance": "/api/v1/agents/abc123"
}
```

### Standardni chybove kody

| Status | Code | Popis |
|---|---|---|
| 400 | `VALIDATION_ERROR` | Neplatny vstup |
| 401 | `AUTH_REQUIRED` | Chybejici nebo neplatny token |
| 403 | `FORBIDDEN` | Nedostatecna opravneni (RBAC) |
| 404 | `NOT_FOUND` | Entita nenalezena |
| 409 | `CONFLICT` | Duplicitni slug, uz existuje |
| 422 | `UNPROCESSABLE` | Validni format, ale nelogicky (napr. prirazeni credential z jineho tymu) |
| 429 | `RATE_LIMIT` | Prekrocen rate limit |
| 500 | `INTERNAL_ERROR` | Neocekavana chyba serveru |
| 503 | `SERVICE_UNAVAILABLE` | DB nedostupne |

---

## 7. PAGINATION (cursor-based)

```
GET /api/v1/agents?cursor=abc123&limit=20

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

## 8. RATE LIMITING

### Go middleware (in-memory)

```go
// internal/api/middleware.go — rate limiting
// Token bucket per-IP a per-user (golang.org/x/time/rate)
// Konfigurace v config/rate-limits.yml
```

> **Omezeni MVP:** In-memory, neskaluje pres vice instanci.
> Phase 2: distribuovany rate limiter.

---

## 9. DOCKER NETWORKING (agent izolace)

```
crewship-internal (platforma: Go binary + PostgreSQL)
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

## 10. CLI commands (single binary)

| Command | Popis |
|---|---|
| `crewship start [--port N] [--db URL]` | Spusti platformu (default: SQLite, port 8080) |
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

## 11. PLANOVANE API (Phase 2+)

> Nasledujici API endpointy jsou v plánu, ale NEJSOU implementovany.

### Skill Marketplace API
- `GET /api/v1/marketplace/skills` -- browse skills
- `GET /api/v1/marketplace/skills/:slug` -- skill detail
- `POST /api/v1/marketplace/skills/:slug/install` -- install
- `DELETE /api/v1/marketplace/skills/:slug/uninstall` -- uninstall
- `POST /api/v1/marketplace/skills` -- publish
- `GET /api/v1/marketplace/categories` -- categories

### Per-Agent Network Control API
- `GET /api/v1/agents/:id/network` -- current network config
- `PUT /api/v1/agents/:id/network` -- update network config

### Agent Memory Management API
- `GET /api/v1/agents/:id/memory` -- memory status
- `GET /api/v1/agents/:id/memory/files` -- list memory files
- `PUT /api/v1/agents/:id/memory/config` -- update memory config
