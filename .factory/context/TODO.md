# Crewship — Produktovy souhrn + ukoly pro budouci iterace

**Posledni aktualizace:** 2026-02-11
**Autor:** Pavel Srba + AI
**Ucel:** Tento soubor je hlavni zdroj kontextu pro kazdou novou AI iteraci.
Precti ho PRVNI, nez zacnes cokoliv delat.

---

## 1. CO JE CREWSHIP

Open-source (FSL licence) platforma pro orchestraci AI agentu.
Agenti jsou prezentovani jako **"virtualni zamestnanci"** organizovani do crews/oddeleni.
Cilovka: firmy od 20 do 500 lidi, ktere chteji AI automatizaci bez developera.

**One-liner:** "Crewship je Linux stroj, kde pracuji AI zamestnanci. Das jim instrukce,
pristupove udaje a dovednosti. Pracuji 24/7 a rano si stahnes vysledky."

**Domena:** crewship.ai | **GitHub:** github.com/crewship-ai | **npm:** @crewship/*

---

## 2. JAK CREWSHIP FUNGUJE (end-to-end)

### Zakladni flow (chat):

```
1. Uzivatel se prihlasi (NextAuth.js) → vidi dashboard s crews
2. Otevre crew "Marketing" → vidi agenty (Anna, Bob)
3. Klikne na agenta "Anna" → otevre chat UI
4. Napise zpravu: "Vytvor mi report o socialnich sitich za leden"
5. Zprava jde pres WebSocket (Go service) → Go service spusti Docker exec v kontejneru crew
6. V kontejneru bezi CLI session (Claude Code / Codex CLI / Gemini CLI)
7. Agent pouziva skills (web-scraper, csv-writer) a credentials (Twitter API key)
8. Agent pise do /output/ → PDF report
9. Uzivatel vidi real-time stream akci agenta v UI (pres WebSocket)
10. Rano si stahne report z file browseru
```

### Zakladni flow (webhook):

```
1. Grafana detekuje CPU > 95% na produkci
2. Posle POST /api/v1/webhooks/{crew-id}/{agent-id}/trigger s X-Webhook-Secret
3. Go service prijme webhook, overi secret, spusti agenta
4. SRE agent SSH do serveru, analyzuje logy, restartuje service
5. Napise incident report do /output/
6. Posle Slack notifikaci
7. Rano SRE inzenyr precte report — agent uz problem vyresil
```

### Architektura (single Go binary):

```
crewship (Go binary, ~50-80 MB)
├─ Embedded UI (Next.js static export via embed.FS)
├─ REST API (/api/v1/*)
├─ Auth (NextAuth-compatible JWE, internal/api/)
├─ database/sql → SQLite (default) / PostgreSQL (opt-in)
├─ WebSocket gateway (goroutines)
├─ Docker orchestrator (SDK for Go)
├─ Log collector (JSONL + logrotate)
├─ File server (fsnotify)
├─ Webhook ingress
└─ Job state (bbolt WAL)
```

### Storage model:

```
EPHEMERAL (kontejner):
  /workspace/          ← agent scratch space, docasne soubory, CLI state
                         Kontejner = cattle (disposable, nahraditelny)

PERSISTENT (host filesystem):
  /output/             ← agent vysledky (reporty, kod, data)
  /var/lib/crewship/output/{workspace-id}/{crew-name}/{agent-name}/
                         Kdyz se crew smaze → presune do _archived/ (NE smazat)
                         Admin muze purgovat (GDPR)

LOGY (host + logrotate):
  /var/log/crewship/crews/{crew-id}/agents/{agent-id}/current.jsonl
                         Hodinova rotace, gzip, 30 dni retence
                         Linux logrotate — nula custom kodu

CHATS (host filesystem, JSONL):
  /var/lib/crewship/chats/{workspace-id}/{agent-id}/{chat-id}.jsonl
                         Kazdy chat = jeden JSONL soubor
                         Metadata (session ID, agent, cas, status) → PostgreSQL
                         Samotne zpravy → JSONL (NE v PostgreSQL)
```

### Kontejnerovy model:

```
1 kontejner = 1 crew (ne 1 agent!)
Agenti v crew sdili kontejner (ale kazdy ma svuj /workspace/{agent-name}/)
Kontejner:
  - Non-root (UID 1001) — NIKDY root
  - --internal Docker network — bez internetu (krome LLM API allowlist)
  - Nemuze pristoupit k hostu, DB, jinym kontejnerum
  - Nemuze eskalovat na root (no sudo, no setuid)
```

### RBAC:

```
OWNER  → vidi vse, spravuje vse, pristup k auditu
ADMIN  → vidi vse, spravuje vse, pristup k auditu
MANAGER → jen prirazene crews, vytvari agenty, spravuje crew credentials
MEMBER  → jen prirazene crews, pouziva agenty, nemuze menit
VIEWER  → jen prirazene crews, read-only
```

### Klicova technologicka rozhodnuti:

| Rozhodnuti | Volba | Duvod |
|---|---|---|
| Backend | Go (`crewshipd`) | Nativni Docker/K8s ekosystem, 50 MB RAM, goroutines |
| WebSocket | Go native | Tisice spojeni, zadna knihovna |
| Job queue | Go channels + bbolt WAL | Zadny Redis, prezije crash |
| Logy | JSONL + logrotate | Zadny PostgreSQL overhead, Linux nativni |
| **Chats** | **JSONL soubory** | **Konzistentni s logy, lehci DB, metadata v PostgreSQL** |
| Auth | Go (NextAuth-compatible JWE) | Single binary, JWT/JWE, bcrypt, no Node.js dependency |
| DB access | Go database/sql | Primy pristup k DB, Prisma jen pro TS type generation |
| UI | Tailwind v4 + shadcn/ui | CSS-first config, new-york styl |
| Icons | lucide-react ONLY | Zadna jina knihovna |
| Rate limiting | Go in-memory (MVP) | Bez Redis, token bucket per-process |
| Credentials | AES-256-GCM encrypted | ENV var injekce do kontejneru |
| IPC | ~~Unix socket~~ In-process | Single binary, zadny IPC |
| Monitoring | cAdvisor + Prometheus | Container metriky + Go /metrics endpoint |

### Business positioning:

```
n8n / Make / Zapier = RUCE (udelej krok A, pak B, pak C — rigidni workflow)
Crewship             = MOZEK (analyzuj situaci, rozhodni se, jednej — autonomni agent)

Kooperace: n8n posle webhook → Crewship agent analyzuje a jedna.
```

---

## 3. OPENCLAW POROVNANI A INSPIRACE

> OpenClaw (163k+ GitHub stars, MIT) je open-source single-user personal AI assistant.
> Crewship prebira dobre vzory a PRIDAVA enterprise features. Analyza z 2026-02-11.

### Co OpenClaw umi a Crewship BUDE mit (uz navrzene)

| OpenClaw feature | Crewship ekvivalent | Feature ID |
|---|---|---|
| Skills system (53+ skills, markdown+YAML) | Skill registry + marketplace | SKILL-01..09 |
| Persistent memory (across sessions) | /workspace/.claude/, agent memory | AGENT-02 |
| 50+ messaging integrace (WhatsApp, Telegram, Discord, Slack) | Phase 2 messaging kanaly | MSG-01..03 |
| Always-on monitoring (email, weby) | Agent loop mode (Phase 2) | RUN-12 |
| Browser automation | Tool profile FULL + browser skill | SKILL-05 |
| File management | /output/ + fsnotify real-time | FILE-01..04 |
| Local execution (privacy) | Self-hosted (Docker/Coolify/K8s) | DOCKER-01..07 |
| AgentSkills (100+ preconfigured) | Bundled + managed + marketplace | SKILL-01..09 |

### Co Crewship DOPLNI (nove, inspirovane OpenClaw)

| Feature | Popis | Proc OpenClaw nema |
|---|---|---|
| **Credential pooling** (CRED-07) | Vice API klicu pro stejny env var, round-robin/failover | OpenClaw = 1 klic, single user |
| **Rate limit failover** (RUN-11) | Automaticke prepnuti klice pri 429 | OpenClaw nema pool, nema co prepnout |
| **Proactive monitoring** (RUN-12) | Agent loop mode + webhooky (silnejsi nez polling) | OpenClaw monitoruje, ale jen z jednoho stroje |
| **Cross-provider fallback** | Anthropic vycerpa → prepne na OpenAI | OpenClaw pouziva primarne Claude |
| **Context preservation** (RUN-14) | JSONL catch-up pri key switch/restartu | OpenClaw nema key rotation |

### Co OpenClaw umi a Crewship NEMA (vedome rozhodnuti)

| OpenClaw feature | Proc ne | Alternativa |
|---|---|---|
| Personal assistant mode (single user) | Crewship je pro FIRMY (multi-tenant, RBAC) | Web UI + RBAC |
| iMessage/SMS integrace | Apple/carrier specificke, slozite | Web UI (MVP), messaging Phase 2 |
| Local desktop access (clipboard, Finder) | Agenti bezi v kontejneru, ne na desktopu | Container = bezpecnejsi |
| Knowledge graphs (Penfield plugin) | Overkill pro MVP | JSONL konverzace + /workspace/ |
| DigitalOcean 1-Click Deploy | Hosting-specificke | Coolify/Docker Compose |

### Kde je Crewship LEPSI nez OpenClaw

| Oblast | OpenClaw | Crewship |
|---|---|---|
| **RBAC** | Zadne role, single-user | 5 roli (Owner→Viewer), per-crew |
| **Audit log** | Zadny | Immutable, append-only, queryable |
| **Container izolace** | Bezi PRIMO NA HOSTU (riziko!) | Docker kontejner, non-root, --internal network |
| **Multi-tenant** | 1 uzivatel = 1 instance | Cela firma v jedne instanci |
| **Webhooky** | Reaguje na zpravy z messaging apps | Reaguje na libovolne externi eventy (Grafana, n8n, CI/CD) |
| **Credential encryption** | Config v plaintextu! | AES-256-GCM + key versioning |
| **File output management** | Zadny /output/ koncept | Persistent output, archivace, file browser |
| **Credential pool** | 1 API klic | Multi-key pool s failover |
| **Crew organizace** | Zadna (flat) | Crews/oddeleni s izolaci |
| **Security hardening** | Minimalni (no sandbox) | cap-drop ALL, read-only root, network isolation |

---

## 4. CO JE SPATNE V DOKUMENTECH (kriticka revize 2026-02-11)

5 PRD dokumentu + stale soubory obsahuji **~250 referenci na starou architekturu**
(Redis, BullMQ, Supabase Auth, Vercel, Railway, Node.js worker).
**NELZE podle nich kodit.** Musi se prepsat pred zahajenim vyvoje.

---

## 5. UKOLY

### FAZE 0: Oprava dokumentace ✅ HOTOVO (2026-02-11)

- [x] **0.1** Prepsat `prd/DATABASE.md`
  - Smazat sekci 8 "Redis Schema" (cela)
  - Smazat modely `Chat` a `ConversationMessage` z Prisma schema
  - Pridat `Chat` jako metadata-only model (chat_id, agent_id, workspace_id, title, mode, status, started_at, ended_at, message_count, jsonl_path) — samotne zpravy jsou v JSONL
  - Smazat vsechny reference na Supabase Auth Phase 2
  - Opravit `AgentRun.triggered_by` na **nullable** (webhook/cron triggery nemaji uzivatele)
  - Pridat `webhook_secret` sloupec do modelu `Agent`
  - Pridat `AgentCredential` constraint — credential scope musi odpovidat crew agenta
  - Smazat Redis v docker-compose ukce
  - Aktualizovat sekci 9 (Auth) — smazat Supabase Auth adapter, nechat jen NextAuth.js
  - ~15 stale referenci k oprave

- [x] **0.2** Prepsat `prd/SECURITY.md`
  - Smazat vsechny Redis/BullMQ reference (~40)
  - Prepsat architekturni diagram (2 procesy: Next.js + Go, ne 4)
  - Prepsat network diagram (agent kontejner → Go service → Next.js)
  - Prepsat rate limiting sekci (Go in-memory token bucket misto Redis)
  - Prepsat session invalidaci (NextAuth.js, ne Redis blacklist)
  - Pridat Unix socket security (file permissions, /run/crewship/, SO_PEERCRED)
  - Pridat webhook auth sekci (per-agent secret, HMAC validace)
  - Prepsat supply chain tabulku (smazat bullmq, ioredis, pridat Go deps)
  - Prepsat GDPR data deletion (smazat `redis.del()`, pridat JSONL cleanup)
  - Smazat Supabase reference

- [x] **0.3** Prepsat `prd/API.md`
  - Smazat vsechny Redis PubSub / BullMQ reference (~30)
  - Prepsat WebSocket sekci — Go native, ne Node.js (`import { redis }`, `subscribeRedis()`)
  - Prepsat health endpoint — kontroluje DB + Go service, NE Redis
  - Pridat webhook API endpoints (POST /api/v1/webhooks/{crew-id}/{agent-id}/trigger)
  - Pridat file API endpoints (GET /api/v1/crews/{id}/files/*, download)
  - Smazat agent status "Redis + DB fallback" — Go service drzi stav v pameti + bbolt
  - Prepsat agent start flow — ne BullMQ job, ale IPC na Go service
  - Aktualizovat architekturni diagram na 2 procesy

- [x] **0.4** Prepsat `prd/DEPLOYMENT.md`
  - Smazat VSECHNY Redis/BullMQ/Vercel/Railway reference (~60)
  - Prepsat na 2 procesy: Next.js + crewshipd (ne 4: App + Worker + Redis + Gateway)
  - Prepsat docker-compose — jen PostgreSQL (Redis smazan)
  - Prepsat Coolify deployment sekci (2 services, ne 4)
  - Smazat `pnpm worker:dev` — Go service misto BullMQ worker
  - Prepsat network diagram (crewship-internal = Next.js + Go + PostgreSQL)
  - Prepsat horizontalni scaling (Go service = single binary, ne BullMQ competing consumers)
  - Prepsat health monitoring (Go /metrics endpoint, ne Redis ping)
  - Smazat Vercel Cron IPs

- [x] **0.5** Prepsat `prd/AGENT-RUNTIME.md`
  - Smazat vsechny BullMQ/Redis reference (~20)
  - Prepsat container lifecycle — Go service vytvari/spravuje kontejnery (ne BullMQ worker)
  - Prepsat real-time streaming — Go captures Docker stdout → WebSocket (ne Redis PubSub)
  - Prepsat agent status management — Go in-memory + bbolt (ne Redis hset/hgetall)
  - Prepsat log collection — JSONL soubory + logrotate (ne Redis Stream → PostgreSQL)
  - Prepsat filesystem events — fsnotify → WebSocket (ne Redis Stream)
  - Smazat BullMQ retry konfiguraci — Go service ma vlastni retry logiku
  - Smazat "Crewship Worker image" sekci — Go service je single binary

- [x] **0.6** Smazat `knowledge-transfer.md` (90% stale, info uz v business.md)

- [x] **0.7** Prepsat `config/rate-limits.yml`
  - Smazat vsechny Advine endpointy (organizations, projects, integrations, sklik, google-ads, meta-ads...)
  - Pridat Crewship endpointy (crews, agents, skills, credentials, webhooks)
  - Smazat Vercel Cron IPs
  - Zmenit poznamku "Redis unavailable" na "Go in-memory rate limiter"

- [x] **0.8** Smazat `workers/` adresar (prazdny, Go nahradi BullMQ worker)

- [x] **0.9** Opravit `middleware.ts`
  - Soucasny: `export { default } from "next-auth/middleware"` (NextAuth v4 syntax)
  - Novy: Auth.js v5 pouziva `auth()` middleware — viz https://authjs.dev/getting-started/session-management/protecting

- [x] **0.10** Opravit `vitest.config.ts`
  - Smazat `supabase/` z coverage exclude (neexistuje)
  - Smazat `lib/redis-config.ts` z coverage exclude (neexistuje)
  - Pridat `cmd/` a `internal/` do exclude (Go soubory)

- [x] **0.11** Opravit `eslint.config.mjs`
  - Zmenit `@typescript-eslint/no-explicit-any: "off"` na `"warn"` (novy projekt, zadny tech debt)

- [x] **0.12** Opravit socket path v `.env.example`
  - `/tmp/crewship.sock` → `/run/crewship/crewship.sock` (produkce)
  - `/tmp/crewship.sock` ok pro dev, ale dokumentovat bezpecnostni implikace

- [x] **0.13** Pridat key versioning do `lib/encryption.ts`
  - Encrypted data prefix: `v1:base64data` → umoznuje budouci key rotation
  - Decrypt funkce kontroluje verzi a pouzije spravny klic

### FAZE 1: Scaffolding (po dokonceni Faze 0)

- [ ] **1.1** Vytvorit `package.json` + `pnpm install` (vsechny dependencies)
- [ ] **1.2** Regenerovat `components/ui/*.tsx` pro Tailwind v4 (`npx shadcn@latest init` + add)
- [ ] **1.3** Vytvorit kompletni `prisma/schema.prisma` (z opraveneho DATABASE.md)
- [ ] **1.4** Vytvorit `.gitignore` + `git init` + prvni commit
- [ ] **1.5** Vytvorit `next.config.ts` (output, images, webpack aliases)
- [ ] **1.6** Go module dependencies (`go mod tidy` po pridani importu)
- [ ] **1.7** Overit ze `pnpm dev` + `go run ./cmd/crewshipd` startuje bez chyb

### FAZE 2: MVP features (po dokonceni Faze 1)

- [ ] **2.1** Auth (NextAuth.js + Prisma adapter, login/signup pages)
- [ ] **2.2** Workspace + Crew CRUD
- [ ] **2.3** Agent CRUD + skills/credentials assignment
- [ ] **2.4** Go WebSocket gateway
- [ ] **2.5** Docker container lifecycle (create/start/stop per crew)
- [ ] **2.6** Chat UI + real-time streaming
- [ ] **2.7** File browser + download
- [ ] **2.8** Webhook ingress
- [ ] **2.9** Credentials vault (AES-256-GCM, ENV var injection)
- [ ] **2.10** RBAC (CASL-based, check on every API endpoint)
- [ ] **2.11** Audit log (append-only)
- [ ] **2.12** Dashboard (crew overview, agent status, resource usage)

### FAZE 2A: Lead orchestrace (po MVP)

- [ ] **2A.1** AgentRole enum (AGENT/LEAD/COORDINATOR) + DB migrace
- [ ] **2A.2** Lead designation UI (oznaceni agenta jako lead, max 1 per crew)
- [ ] **2A.3** Auto-generated lead system prompt (kontext crew, agenty, role)
- [ ] **2A.4** Assignment protokol — parsovani @assign/@ask prikazu ze stdout v crewshipd
- [ ] **2A.5** Lead → Agent assignment (Docker exec orchestrace v ramci kontejneru crew)
- [ ] **2A.6** Assignment tabulka + audit assignments
- [ ] **2A.7** Lead auto-routing (uzivatel pise do crew → lead rozhodne komu assignovat)
- [ ] **2A.8** Paralelni assignment (wait_group pattern pro vice agentu soucasne)
- [ ] **2A.9** Error handling + fallback (lead reaguje na selhani agenta)
- [ ] **2A.10** Lead summary/agregace (lead shrnuje vysledky pred odeslani)
- [ ] **2A.11** Activity feed v chat UI (vizualizace assignments)

### FAZE 2B: Coordinator (po validaci leads)

- [ ] **2B.1** Coordinator agent role (specialni agent na urovni workspace, crew_id = null)
- [ ] **2B.2** Coordinator lightweight execution (LLM call bez Docker kontejneru)
- [ ] **2B.3** Coordinator → Lead assignment (cross-crew orchestrace)
- [ ] **2B.4** Coordinator auto-routing (coordinator rozhodne kterou crew oslovit)
- [ ] **2B.5** Cross-crew agregace (coordinator sbira odpovedi od vice crews)
- [ ] **2B.6** Coordinator UI (dashboard card + dedicovany chat)

---

## 6. AKTUALNI STAV DOKUMENTU

| Dokument | Stav | Poznamka |
|---|---|---|
| `AGENTS.md` | ✅ Aktualni | Hlavni coding guidelines, dvoujazycny projekt |
| `architecture.md` | ✅ Aktualni | 2 procesy, storage model, monitoring |
| `tech-stack.md` | ✅ Aktualni | Go sekce, smazany stare deps |
| `business.md` | ✅ Aktualni | Positioning, webhook priklady, konkurence |
| `DESIGN.md` | ✅ Aktualni | Tailwind v4, tweakcn, shadcn/ui, lucide-react |
| **TODO.md** | ✅ **Tento soubor** | Produktovy souhrn + ukoly |
| `prd/PRD.md` | ⚠️ Castecne | Nove features pridany, ale ~15 starych ref zustava (DOCKER-01 zminuje Redis, auth sekce Supabase) |
| `prd/DEPENDENCIES.md` | ⚠️ Castecne | Go deps pridany, ale ~10 starych ref zustava |
| `prd/DATABASE.md` | ✅ Aktualni (v2.0) | Prepsano 2026-02-11: smazana Redis schema, ConversationMessage → JSONL, AgentRun nullable, webhook_secret |
| `prd/SECURITY.md` | ✅ Aktualni (v2.0) | Prepsano 2026-02-11: Go architektura, Unix socket security, webhook auth, in-memory rate limiting |
| `prd/API.md` | ✅ Aktualni (v2.0) | Prepsano 2026-02-11: 2 procesy, IPC protokol, webhook API, file API |
| `prd/DEPLOYMENT.md` | ✅ Aktualni (v2.0) | Prepsano 2026-02-11: smazany Redis/Vercel/Railway, Coolify deployment, 2 services |
| `prd/AGENT-RUNTIME.md` | ✅ Aktualni (v2.0) | Prepsano 2026-02-11: Go orchestrator, Docker exec, JSONL logy, bbolt WAL, fsnotify. Aktualizovano 2026-02-13: orchestracni runtime (lead/coordinator) |
| `prd/ORCHESTRATION.md` | ✅ **Novy (v1.0)** | 2026-02-13: Lead + Coordinator, 3-urovnova hierarchie, assignment protokol, industry kontext |

**Vsechny dokumenty jsou ted pouzitelne pro kodovani.** PRD.md a DEPENDENCIES.md maji drobne stale reference, ale klicove sekce jsou spravne.
