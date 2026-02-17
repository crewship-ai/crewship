# Crewship -- Security (SECURITY.md)

**Verze:** 3.0
**Datum:** 2026-02-17

---

## 1. THREAT MODEL

### 1.1 Aktiva (co chranime)

| Aktivum | Kriticnost | Popis |
|---|---|---|
| Credentials vault | KRITICKA | API klice uzivatelu (BYOK) — sifrovane AES-256-GCM |
| User data | VYSOKA | Workspaces, crews, konfigurace agentu |
| Konverzace | VYSOKA | JSONL soubory — mohou obsahovat firemni data |
| Agent output | VYSOKA | /output/ — reporty, kod, firemni dokumenty |
| Session tokeny | VYSOKA | JWT tokeny (NextAuth) |
| Audit logy | STREDNI | Nesmi byt modifikovany (immutable) |
| Agent workspace | STREDNI | /workspace/ — docasne soubory agenta |

### 1.2 Threat actors

| Actor | Motivace | Schopnosti |
|---|---|---|
| Externi utocnik | Kradez credentials, data exfiltrace | Web attacks (OWASP Top 10) |
| Zblazneny agent (prompt injection) | Destrukce, unik dat, privilege escalation | Plny pristup uvnitr kontejneru |
| Insider (nespokojeny clen org) | Sabotaz, kradez dat | Legitimni pristup dle RBAC role |
| Supply chain | Backdoor v CLI nastrojich, npm/Go balicich | Kod v dependencies |

### 1.3 Attack surface

```
Internet → [WAF/CDN] → Next.js (REST API) → Prisma → PostgreSQL
                      → crewshipd (Go) → WebSocket → Browser
                      → crewshipd (Go) → Docker SDK → Agent kontejner → LLM API
                      → crewshipd (Go) → Webhook ingress
```

### 1.4 Trust boundaries

```
┌──────────────────────────────────────────────────────────────────┐
│ TRUST ZONE 1: Platforma (plne duveryhodne)                       │
│  Next.js + crewshipd (Go) + PostgreSQL                           │
│  → pristup k DB, credentials vault, audit logum                  │
│  → crewshipd ma Docker socket                                    │
│  → komunikace pres Unix socket (file permissions)                │
├──────────────────────────────────────────────────────────────────┤
│ TRUST ZONE 2: Autentizovany uzivatel (castecne duveryhodny)      │
│  → pristup omezen pres CASL RBAC                                 │
│  → muze byt insider threat                                       │
│  → vsechny akce auditovane                                       │
├──────────────────────────────────────────────────────────────────┤
│ TRUST ZONE 3: Agent kontejner (NEDUVERYHODNY)                    │
│  → muze byt prompt-injektovany                                   │
│  → plny pristup uvnitr kontejneru, ale NIC vne                   │
│  → --internal Docker network (bez internetu krome LLM allowlist) │
│  → vsechny akce logovane                                         │
├──────────────────────────────────────────────────────────────────┤
│ TRUST ZONE 4: Externi sluzby (neduveryhodne)                     │
│  → LLM API (Anthropic, OpenAI, Google, Ollama)                   │
│  → Webhook sendery (Grafana, n8n, externi systemy)               │
│  → npm registry, Go modules (supply chain risk)                  │
└──────────────────────────────────────────────────────────────────┘
```

### 1.5 Data flow diagram

```
Uzivatel (browser)
  │
  │ HTTPS (TLS 1.3)
  ▼
Next.js API ──── Prisma ──── PostgreSQL (credentials encrypted)
  │  │
  │  │ JWT validation, CASL RBAC, Zod validation
  │  │ Audit Log (kazda mutace)
  │  │
  │  └── Unix socket (/run/crewship/crewship.sock)
  │           │
  │           ▼
  │      crewshipd (Go)
  │        ├── WebSocket gateway → Browser (WSS)
  │        ├── Docker SDK → Agent kontejner (--internal network)
  │        ├── Webhook ingress (X-Webhook-Secret auth)
  │        ├── Log collector → JSONL soubory
  │        ├── File server → /output/ (fsnotify)
  │        └── bbolt WAL (job state)
  │
  ▼
Agent kontejner (crewship-agents sit)
  ├── CLI tool (Claude Code, Codex, Gemini) → LLM API (HTTPS)
  ├── stdout/stderr → crewshipd (Docker attach)
  ├── /workspace/ (ephemeral) + /output/ (persistent)
  └── NEMUZE: pristup k DB, hostu, jinym kontejnerum
```

---

## 2. IZOLACNI VRSTVY (DEFENSE-IN-DEPTH)

### 2.1 Vrstva 1: Docker kontejner (OS-level)

| Opatreni | Implementace |
|---|---|
| Read-only root filesystem | `--read-only` |
| Zadne nove privilegia | `--security-opt no-new-privileges` |
| Vsechny capabilities dropnuty | `--cap-drop ALL --cap-add NET_RAW` |
| Resource limity | `--memory 4g --cpus 2.0 --pids-limit 200` |
| Izolovana sit | `--network crewship-agents --internal` |
| Non-root user | `USER agent` (UID 1001) v Dockerfile |
| Tmpfs pro /tmp | `--tmpfs /tmp:rw,size=500m` |

### 2.2 Vrstva 2: Network izolace

```
crewship-internal (Next.js + crewshipd + PostgreSQL) ← agent NEMA pristup
crewship-agents (agent kontejnery, --internal) ← bez internetu!
  ↳ Vyjimka: LLM API endpoints na allowlistu (iptables rules)
```

- Agent NEMUZE pristoupit k PostgreSQL, crewshipd, Next.js
- Agent NEMUZE pristoupit k jinym agent kontejnerum (ICC disabled)
- Agent NEMUZE pristoupit k host localhost
- Agent NEMUZE pristoupit k cloud metadata (169.254.169.254 blokovany)
- Agent MUZE pristoupit JEN k LLM API endpointum na explicitnim allowlistu

### 2.3 Vrstva 3: CASL RBAC (aplikacni uroven)

5 roli: OWNER > ADMIN > MANAGER > MEMBER > VIEWER

```typescript
// lib/permissions/abilities.ts
export function defineAbilityFor(role: OrgRole, workspaceId: string) {
  return defineAbility((can, cannot) => {
    switch (role) {
      case "OWNER":
        can("manage", "all");
        break;
      case "ADMIN":
        can("manage", "all");
        cannot("delete", "Workspace");
        cannot("manage", "Subscription");
        break;
      case "MANAGER":
        can("read", "Crew", { workspace_id: workspaceId });
        can("create", "Crew", { workspace_id: workspaceId });
        can("update", "Crew", { workspace_id: workspaceId });
        can("manage", "Agent", { workspace_id: workspaceId });
        can("manage", "AgentSkill", { workspace_id: workspaceId });
        can("manage", "AgentCredential", { workspace_id: workspaceId });
        can("create", "Credential", { workspace_id: workspaceId });
        can("read", "Credential", { workspace_id: workspaceId });
        can("update", "Credential", { workspace_id: workspaceId });
        can("read", "Chat", { workspace_id: workspaceId });
        can("read", "AgentRun", { workspace_id: workspaceId });
        can("read", "AuditLog", { workspace_id: workspaceId });
        break;
      case "MEMBER":
        can("read", "Crew", { workspace_id: workspaceId });
        can("read", "Agent", { workspace_id: workspaceId });
        can("start", "Agent", { workspace_id: workspaceId });
        can("create", "Chat", { workspace_id: workspaceId });
        can("read", "Chat", { workspace_id: workspaceId });
        can("read", "AgentRun", { workspace_id: workspaceId });
        can("read", "Skill");
        break;
      case "VIEWER":
        can("read", "Crew", { workspace_id: workspaceId });
        can("read", "Agent", { workspace_id: workspaceId });
        can("read", "Chat", { workspace_id: workspaceId });
        can("read", "AgentRun", { workspace_id: workspaceId });
        can("read", "Skill");
        break;
    }
  });
}
```

#### Kde se CASL kontroluje
1. API middleware (`withRBAC`) — pred kazdym handlerem
2. Prisma middleware (Phase 2) — `@casl/prisma` filtruje dotazy
3. Go service — pri WebSocket subscribe overuje crew membership (IPC dotaz na Next.js)
4. UI — client-side ability pro schovani UI prvku (NE bezpecnostni vrstva!)

### 2.4 Vrstva 4: RLS (Phase 2 — defense-in-depth)

`current_setting('app.current_user_id', true)::uuid` pattern.
Funguje na jakomkoli PostgreSQL. Viz DATABASE.md sekce 6.

### 2.5 Vrstva 5: Audit log (forensic)

- Aplikacni audit: PostgreSQL `audit_logs` tabulka (immutable, zadny UPDATE/DELETE)
- Agent logy: JSONL soubory na hostu (/var/log/crewship/)
- Docker logy: `docker logs` per kontejner
- Phase 2: auditd (kernel-level syscall logging v kontejnerech)

---

## 3. CREDENTIALS BEZPECNOST

### 3.1 Sifrovani (AES-256-GCM s key versioning)

```
Plaintext → AES-256-GCM(key, iv=random16B) → "v1:" + Base64(IV + AuthTag + Ciphertext)
```

| Vlastnost | Hodnota |
|---|---|
| Algoritmus | AES-256-GCM (AEAD) |
| Klic | ENCRYPTION_KEY (env var, 32 bytes hex) |
| IV | 16 bytes, nahodny per encrypt |
| Auth tag | 16 bytes |
| Key version | Prefix "v1:" — umoznuje budouci key rotation |
| Ulozeni | PostgreSQL `encrypted_value` sloupec |

### 3.2 Zivotni cyklus credentialu

```
1. Uzivatel zada plaintext v UI
2. HTTPS → Next.js API (TLS terminace)
3. Server zasifruje (AES-256-GCM) → "v1:" + Base64 → PostgreSQL
4. Pri startu agenta: Next.js desifrovuje → posle Go service pres Unix socket
5. Go service preda jako ENV var do Docker exec
6. Po ukonceni procesu ENV var zmizi
7. Plaintext NIKDY na disku, NIKDY v logu, NIKDY v API response
```

### 3.3 Key rotation (Phase 2)

```
1. Novy ENCRYPTION_KEY_V2 se nastavi jako env var
2. Batch job precte vsechny credentials s prefixem "v1:"
3. Desifrovuje starym klicem, zasifruje novym → "v2:" prefix
4. Po migraci se stary klic odstrani
5. Decrypt funkce kontroluje prefix a pouzije spravny klic
```

### 3.4 Credential injection do kontejneru

```
Next.js API → desifrovani → Unix socket → crewshipd → Docker exec (ENV vars)
```

Go service NIKDY neuklada credentials na disk. Drzi je v pameti jen po dobu exec volani.

### 3.5 Credential pool bezpecnost

Kdyz agent ma vice klicu pro stejny env var (credential pool):

| Aspekt | Implementace |
|---|---|
| Sifrovani | Kazdy klic v poolu je sifrovany stejne (AES-256-GCM, key versioning "v1:") |
| Cooldown | 429 → klic jde na 5min cooldown, crewshipd drzi v pameti (ne na disku) |
| Audit log | Kazdy agent run zaznamenava KTERY klic byl pouzit (credential_id v metadata) |
| Key rotation | Vsechny klice v poolu podlehaji stejne key rotation politice |
| Priority | Klice maji priority (0=primary, 1+=fallback) — crewshipd vzdy zacina s primary |
| Exhaustion | Pokud vsechny klice v cooldown → run selze, uzivatel notifikovan |
| Memory only | Cooldown stavy jsou jen v Go pameti — po restartu crewshipd se resetuji |
| No disk | Credential plaintext se NIKDY neuklada na disk, ani pri failover/pool switch |

### 3.6 Log redaction

Strukturovane logovani (JSON) s redakci citlivych poli:

```
Redakovane patterns: password, token, secret, key, apiKey, api_key,
  encrypted_value, ENCRYPTION_KEY, NEXTAUTH_SECRET, *_API_KEY,
  authorization header, cookie header
```

### 3.7 Credential masking v API response

```typescript
// "sk-proj-abc123xyz789" → "sk-p***...***z789"
// Plaintext se z DB desifrovava JEN pri credential injection do agenta.
// API NIKDY nevraci plaintext — jen masked verzi.
```

---

## 4. AUTENTIZACE

### 4.1 NextAuth.js (Auth.js v5)

| Aspekt | Implementace |
|---|---|
| Session typ | JWT (default) |
| JWT expirece | 24h |
| Password hashing | bcrypt (cost factor 12) |
| OAuth | Google, GitHub |
| CSRF | Vestaveny v NextAuth (double-submit cookie) |

### 4.2 WebSocket autentizace

- Short-lived JWT (5 minut) ziskany z REST API
- Prenaseny v query parametru (WebSocket API omezeni)
- Go service (`crewshipd`) validuje JWT pri WebSocket handshake
- Po pripojeni: Go service overuje crew membership pres IPC dotaz na Next.js

### 4.3 Webhook autentizace

```
POST /api/v1/webhooks/{crew-id}/{agent-id}/trigger
Headers: X-Webhook-Secret: {per-agent-secret}
```

- Kazdy agent ma unikatni `webhook_secret` (generovany pri vytvoreni agenta)
- Secret ulozeny encrypted v PostgreSQL (AES-256-GCM, jako credentials)
- Go service validuje secret pri kazdem prichozim webhooku
- Neplatny secret → 401 Unauthorized + audit log

### 4.4 Brute force ochrana

- 5 pokusu/min per IP na login
- 3 pokusy/hod na password reset per IP
- 3 pokusy/hod na registraci per IP
- Progressive lockout: 1min, 5min, 15min, 1h
- Implementace: Go in-memory rate limiter (MVP), per-process
- Audit log: `auth.login.failed`, `auth.lockout`

### 4.5 Session bezpecnost

| Opatreni | Implementace |
|---|---|
| JWT v HttpOnly cookie | NextAuth default — neni pristupny z JS (XSS ochrana) |
| Secure flag | `Secure: true` (jen HTTPS) |
| SameSite | `SameSite: Lax` (CSRF ochrana) |
| Session invalidace | Logout = odstraneni cookie |
| Concurrent sessions | Povoleno |
| Session fixation | NextAuth generuje novy session ID po kazdem login |

### 4.6 Password politika

```
Min 8 znaku, max 128 znaku.
Zadne composition rules (NIST 800-63B doporuceni).
Zadne vynucene rotace hesla.
Phase 2: kontrola proti HaveIBeenPwned API.
```

### 4.7 Unix socket bezpecnost

```
Produkce: /run/crewship/crewship.sock
  - chmod 0660 (owner + group read/write)
  - chown crewship:crewship
  - Adresár /run/crewship/ s chmod 0750
  - Jen Next.js process a crewshipd mohou komunikovat

Dev: /tmp/crewship.sock
  - /tmp/ je world-writable — NENI bezpecne pro produkci
  - Prijatelne pro lokalni vyvoj na Mac/Linux
```

---

## 5. API BEZPECNOST

### 5.1 OWASP Top 10 pokryti

| OWASP | Riziko | Mitigace |
|---|---|---|
| A01 Broken Access Control | Data jine org | CASL + RLS (Phase 2) |
| A02 Cryptographic Failures | Credentials leak | AES-256-GCM, TLS, log redaction |
| A03 Injection | SQL/command injection | Prisma (parametrizovane), Zod, Docker izolace |
| A04 Insecure Design | Chybejici threat model | Tento dokument |
| A05 Security Misconfiguration | Default credentials | Hardened Docker, env validation |
| A06 Vulnerable Components | Supply chain | `pnpm audit`, Dependabot, `go mod verify` |
| A07 Auth Failures | Session hijack | bcrypt, JWT expirece, HttpOnly cookies |
| A08 Software Integrity | Tampered audit log | Immutable audit tabulka (no UPDATE/DELETE) |
| A09 Logging Failures | Nedostatecne logy | Structured JSON logging, audit log |
| A10 SSRF | Agent pristoupi k interni siti | --internal Docker network, LLM allowlist |

### 5.2 Security headers

```typescript
const securityHeaders = {
  "X-Content-Type-Options": "nosniff",
  "X-Frame-Options": "DENY",
  "X-XSS-Protection": "0",
  "Referrer-Policy": "strict-origin-when-cross-origin",
  "Permissions-Policy": "camera=(), microphone=(), geolocation=()",
  "Strict-Transport-Security": "max-age=63072000; includeSubDomains; preload",
  "Content-Security-Policy": [
    "default-src 'self'",
    "script-src 'self' 'unsafe-inline'",
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: https:",
    "font-src 'self'",
    "connect-src 'self' wss://*.crewship.ai",
    "frame-ancestors 'none'",
    "base-uri 'self'",
    "form-action 'self'",
  ].join("; "),
};
```

### 5.3 Rate limiting (bez Redis)

MVP: In-memory rate limiter v Go service (`golang.org/x/time/rate`).

| Endpoint | Limit | Scope |
|---|---|---|
| POST /api/auth/* | 5/min | Per IP |
| POST /api/v1/webhooks/* | 60/min | Per agent |
| POST /api/v1/* (mutace) | 30/min | Per user |
| GET /api/v1/* (cteni) | 200/min | Per user |
| WebSocket messages | 60/min | Per connection |

Next.js API routes: rate limiting pres middleware (in-memory Map, per-process).
Go service: `golang.org/x/time/rate` token bucket.

> **Omezeni MVP:** In-memory rate limiter neskaluje pres vice instanci.
> Phase 2: distribuovany rate limiter (sdileny stav pres bbolt nebo PostgreSQL).

### 5.4 Input validace

```typescript
// Zod schema na kazdy endpoint (body, query, params)
const MAX_LENGTHS = {
  name: 100, slug: 50, description: 1000,
  systemPrompt: 10_000, prompt: 50_000,
  credentialValue: 10_000, email: 255,
  password: 128, url: 500,
};

// Sanitizace file names
function sanitizeFilename(name: string): string {
  return name
    .replace(/\.\./g, "")          // path traversal
    .replace(/[\/\\]/g, "")        // coordinatory separators
    .replace(/[\x00-\x1f]/g, "")   // control characters
    .slice(0, 255);
}
```

### 5.5 CORS

```typescript
// Explicitni whitelist, ZADNY wildcard, NIKDY "*"
const ALLOWED_ORIGINS = new Set([
  process.env.NEXT_PUBLIC_APP_URL,
  ...(process.env.NODE_ENV === "development" ? ["http://localhost:3000"] : []),
]);
```

### 5.6 CSRF

Origin header validace na vsech POST/PATCH/DELETE requestech.
NextAuth ma vestaveny CSRF token (double-submit cookie).

---

## 6. AGENT-SPECIFICKY THREAT MODEL

### 6.1 Prompt injection

| Vektor | Mitigace |
|---|---|
| Primy ("ignoruj instrukce") | Docker izolace — agent nema kam data poslat krome stdout |
| Neprimy (z webu) | Tool profiles (MINIMAL = zadny exec), timeout |
| Data exfiltrace pres HTTP | --internal network, jen LLM allowlist |
| Data exfiltrace pres DNS | Phase 2: CoreDNS logging |
| Credential theft (ENV vars) | Agent je legitimne potrebuje, network izolace brani odeslani |

### 6.2 Container escape

| Vektor | Mitigace | Phase |
|---|---|---|
| Kernel exploit | Aktualizovany kernel, pravidelne patche | MVP |
| Docker socket | crewshipd ma socket, agent NE | MVP |
| Privilege escalation | `--cap-drop ALL`, `no-new-privileges`, non-root | MVP |
| Symlink attack | Read-only root filesystem | MVP |
| Seccomp profile | Whitelist syscalls | Phase 2 |
| gVisor | User-space kernel | Phase 3 |

### 6.3 Resource exhaustion

| Vektor | Mitigace | Limit |
|---|---|---|
| Fork bomb | `--pids-limit` | 200 |
| Memory exhaustion | `--memory` (OOM killer) | 2-16 GB dle tieru |
| Disk fill /tmp | Tmpfs size limit | 500 MB |
| CPU hogging | `--cpus` + agent timeout | 1-8 CPU, 30min default |

### 6.4 Tool profiles

| Profil | Riziko | Co agent MUZE | Co agent NEMUZE |
|---|---|---|---|
| MINIMAL | Nizke | Cist soubory, hledat | Spoustet prikazy, zapisovat |
| CODING | Stredni | Cist, psat, spoustet v workspace | Pristoupit mimo workspace |
| MESSAGING | Stredni | Cist, web search, posilat zpravy | Spoustet lokalni prikazy |
| FULL | Vysoke | Vse v ramci kontejneru | Pristoupit mimo kontejner |

---

## 7. DATA PROTECTION (GDPR)

### 7.1 Prehled

| Pozadavek | Implementace |
|---|---|
| Pravo na vymazani | Soft delete (30d) → hard delete + JSONL rm + workspace rm + container rm |
| Pravo na export | `POST /api/v1/user/export` → JSON dump |
| Data minimization | Jen nutne (zadne LLM prompty v audit logu) |
| Encryption at rest | Credentials: AES-256-GCM. DB: disk encryption (self-host) |
| Encryption in transit | TLS 1.3 (API, WS, DB connection) |

### 7.2 Hard delete cascade

```
1. Zastavit vsechny agenty (Go service → Docker stop)
2. Smazat Docker kontejnery vsech tymu
3. Smazat JSONL soubory (konverzace + logy)
4. Smazat /output/ adresar (nebo presunout do _archived/)
5. Cascade delete v DB (Prisma onDelete: Cascade)
6. Audit log o smazani
```

### 7.3 Data retention

| Data | Default retence | Akce po expiraci |
|---|---|---|
| Konverzace (JSONL) | 1 rok | Smazani souboru |
| Audit logy (PostgreSQL) | 2 roky | Archiv (enterprise) |
| Agent logy (JSONL) | 30 dni | logrotate smazani |
| Agent output (/output/) | Navzdy | Smazani pri hard delete |
| Soft-deleted entity | 30 dni grace | Hard delete |

---

## 8. INCIDENT RESPONSE

### 8.1 Severity levels

| Level | Priklad | Response time |
|---|---|---|
| P0 (Critical) | Credentials leak, DB breach, container escape | < 1h |
| P1 (High) | Auth bypass, RBAC bypass | < 4h |
| P2 (Medium) | XSS, CSRF, info disclosure | < 24h |
| P3 (Low) | Verbose errors, missing headers | < 1 week |

### 8.2 Response checklist

```
1. IDENTIFIKACE — co, kdy, rozsah, pokracuje utok?
2. CONTAINMENT — stop agenty, revoke sessions, disable features
3. ERADICATION — root cause, hotfix, rotate klice
4. RECOVERY — deploy fix, overit integritu, overit pristup
5. LESSONS LEARNED — post-mortem, update threat model
6. NOTIFICATION — GDPR 72h, email dotcenym, public disclosure
```

---

## 9. DEPENDENCY SECURITY

### 9.1 Supply chain ochrana

| Opatreni | Implementace |
|---|---|
| Lockfile | `pnpm-lock.yaml` + `go.sum` (committed) |
| Audit | `pnpm audit` + `govulncheck` v CI |
| Dependabot | GitHub Dependabot (auto PR) |
| CLI tools pinning | Konkretni verze v Dockerfile |
| Container scanning | Trivy na Docker image |

### 9.2 Kriticke dependencies

| Dependency | Riziko | Mitigace |
|---|---|---|
| `@prisma/client` | SQL injection | Zadne `$executeRaw` v app kodu |
| `next` | RSC/middleware exploity | Vzdy latest patch |
| `@casl/ability` | Auth bypass | Unit testy na ability matici |
| `jose` / NextAuth | Token forgery | NEXTAUTH_SECRET rotace |
| CLI tools | Arbitrary code execution | Docker izolace |
| Docker SDK for Go | Container escape | Pinned verze, security advisories |

---

## 10. SECURITY TESTING

### 10.1 Automatizovane (CI)

| Test | Nastroj |
|---|---|
| SAST | ESLint security rules, Semgrep |
| Dependency audit | `pnpm audit`, `govulncheck` |
| Secret scanning | GitHub secret scanning |
| Container scanning | Trivy |

### 10.2 Unit testy

```
- RBAC: KAZDY endpoint × KAZDA role
- Encryption: encrypt/decrypt, wrong key, tampered data, key versioning
- Validation: SQL injection, path traversal, XSS, max lengths
```

### 10.3 Integracni testy

```
- 401 bez auth tokenu
- 403 cross-workspace pristup
- 403 cross-crew pristup (MEMBER)
- Rate limiting
- Audit log completeness
- Credential masking v response
- WebSocket auth handshake
- Webhook secret validation
```

### 10.4 Manualni (pred launchem)

```
- RBAC review: kazdy route.ts ma withRBAC
- Credential flow: create → encrypt → inject → verify no leak
- Agent escape: docker exec → curl localhost:5432 (musi selhat)
- Network izolace: nmap z kontejneru (musi selhat)
- Prompt injection: 10 znamych vzoru
- Audit log integrity: UPDATE/DELETE musi selhat
- Security headers: curl -I
```

---

## 11. SECURITY KONFIGURACE (self-hosting)

```bash
# POVINNE pro produkci
ENCRYPTION_KEY=           # openssl rand -hex 32 (NIKDY NEZTRARIT)
NEXTAUTH_SECRET=          # openssl rand -hex 32
DATABASE_URL=             # s ?sslmode=require
NODE_ENV=production

# Socket
CREWSHIPD_SOCKET=/run/crewship/crewship.sock  # s chmod 0660

# Doporucene
CORS_ORIGIN=https://your-domain.com  # explicitni, zadny wildcard

# ZAKAZANO v produkci
# NODE_ENV=development
# CREWSHIPD_SOCKET=/tmp/crewship.sock  (world-writable)
```

---

## 12. Pouceni z OpenClaw bezpecnostnich incidentu (unor 2026)

OpenClaw prosla masivni bezpecnostni krizi (viz STRATEGY-2026.md):
- CVE-2026-25253 (CVSS 8.8): one-click RCE
- 42,900 exposed instanci, 15,200 zranitelnych
- 341+ malicious skills na ClawHub (20% vsech skills)
- 36% skills ma prompt injection zranitelnosti
- Credentials v plaintext

### Jak Crewship predchazi kazdemu problemu:
| OpenClaw problem | Crewship reseni |
|---|---|
| Zadna container isolation | Docker kontejnery, non-root UID 1001, --internal network |
| Credentials v plaintext | AES-256-GCM + key versioning |
| Malicious skills (zadny sandbox) | Skill permissions model + Docker enforcement |
| Zadny audit trail | Append-only audit log |
| Prompt injection → full access | Agent v kontejneru = bounded blast radius |
| Exposed control panels | localhost default, auth required, RBAC |
| Supply-chain attacks | Official/Verified/Community tiers, automated scanning |

---

## 13. Skill sandbox enforcement

### Permissions model
Kazdy skill deklaruje v `skill.yaml`:
- `filesystem`: read/write paths (whitelist)
- `network`: enabled/disabled + domain whitelist
- `secrets`: required/optional env var names
- `shell`: allowed/denied commands

### Enforcement
- Docker vynucuje filesystem a network permissions
- Skill bez permissions deklarace se NESPUSTI
- OFFICIAL skills: rucne reviewed Crewship tymem
- VERIFIED skills: automated VirusTotal + sandbox test
- COMMUNITY skills: sandbox-only (omezena prava)

---

## 14. OTEVRENE OTAZKY

1. **Workspace quota** — jak limitovat disk usage per crew? Docker `--storage-opt size=10G`?
2. **DNS exfiltrace** — CoreDNS s logovanim v kontejneru? (Phase 2)
3. **Skill security review** — jak validovat community skills? Sandbox + review?
4. **WAF** — Cloudflare/AWS WAF pred API?
5. **Penetration test** — externi pentest pred launch?
6. **Bug bounty** — HackerOne/Bugcrowd po launchi?
7. **Key rotation automation** — ENCRYPTION_KEY rotation bez downtime?
