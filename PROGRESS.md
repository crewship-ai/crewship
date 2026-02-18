# Crewship -- Progress Tracker

**Posledni aktualizace:** 2026-02-18
**Ucel:** Sledovani stavu implementace po epicich a taskach.

---

## Strategicke priority (2026-02-17)

> Detailni analyza: `.factory/context/STRATEGY-2026.md`

Na zaklade hloubkove analyzy OpenClaw ekosystemu (bezpecnostni incidenty, community feedback,
konkurencni krajina) jsme definovali strategicke priority pro Crewship:

### Distribuce: Single Binary (Ollama model)
- `brew install crewship && crewship start` -- 2 prikazy, running platforma
- Go binary s embedded Next.js (static export), SQLite default, PostgreSQL opt-in
- Cross-platform: macOS (brew), Linux (curl/apt/dnf), Windows (winget)
- Cil: "zero deps" instalace (Docker jedina zavislost pro agent kontejnery)

### 8 klicovych diferentiatoru (co uzivatelum chybi na OpenClaw)
1. **Container isolation** -- kazdy agent v Docker sandboxu (OpenClaw bezi na hostu)
2. **Cost control** -- per-agent budgety, limity, alerting (OpenClaw: $750/mesic bez kontroly)
3. **Crew/workspace support** -- multi-user, RBAC, sdileni agentu (OpenClaw: single-user)
4. **Jednoduchy setup** -- single binary, 2 prikazy (OpenClaw: npm + config + messaging)
5. **Audit trail** -- append-only, immutable (OpenClaw: zadny)
6. **Vetted skills** -- curated marketplace, sandbox enforcement (OpenClaw ClawHub: 20% malware)
7. **Visual orchestration** -- dashboard + Coordinator→Lead→Agent hierarchie (OpenClaw: zadna)
8. **Full UI** -- web dashboard s chat, files, logs, settings (OpenClaw: messaging-only)

### Skill Marketplace
- 15-20 official skills pro launch (DevOps, Development, Business, Data)
- Sandbox enforcement -- kazdy skill deklaruje permissions (fs, network, secrets)
- One-click install v UI dashboardu (ne CLI)
- Community skills s review procesem
- Revenue sharing pro autory (Team/Enterprise tier)

### Per-Agent Network Control (killer feature)
- Internet ON/OFF per agent, whitelist domen
- Local network access per agent (CIDR rozsah)
- Remote access per agent (K8s cluster, SSH, VPN)
- Konfigurace klikanim v UI, Docker network policies pod kapotou

### Monetizacni model (3 tiery)
- **Free:** single binary, SQLite, Docker, vsechny official skills, $0
- **Team:** crewship.ai hosted, PostgreSQL, collaboration, $15-30/user/mesic
- **Enterprise:** Helm chart, SSO/SAML, audit export, $50-100/user/mesic

---

## Legenda

- [x] Hotovo
- [~] Castecne (existuje, ale neni kompletni/funkcni)
- [ ] Nezacato

---

## EPIC 0: Dokumentace a pristprava ✅ 100%

- [x] 0.1 Prepsat DATABASE.md (v2.0)
- [x] 0.2 Prepsat SECURITY.md (v2.0)
- [x] 0.3 Prepsat API.md (v2.0)
- [x] 0.4 Prepsat DEPLOYMENT.md (v2.0)
- [x] 0.5 Prepsat AGENT-RUNTIME.md (v2.0)
- [x] 0.6 Smazat knowledge-transfer.md
- [x] 0.7 Prepsat rate-limits.yml
- [x] 0.8 Smazat workers/ adresar
- [x] 0.9 Opravit middleware.ts (Edge-compatible, next-auth/jwt)
- [x] 0.10 Opravit vitest.config.ts
- [x] 0.11 Opravit eslint.config.mjs (no-explicit-any: warn)
- [x] 0.12 Opravit socket path v .env.example
- [x] 0.13 Pridat key versioning do encryption.ts
- [x] 0.14 Vytvorit ORCHESTRATION.md (v1.0)
- [x] 0.15 Vytvorit CREW-EXECUTION.md (v1.0) — Mission, Workflow sablony, Auto-hiring, Progress tracking

---

## EPIC 1: Scaffolding ✅ ~95%

> Zakladni projekt -- tech stack, soubory, konfigurace.

- [x] 1.1 package.json + pnpm install (vsechny deps)
- [x] 1.2 shadcn/ui komponenty (22 komponent regenerovanych pro TW4)
- [x] 1.3 Prisma schema (20 tabulek, 16 enumu, indexy, constrainty)
- [x] 1.4 .gitignore + git init + GitHub repo
- [x] 1.5 next.config.ts (serverExternalPackages, output)
- [x] 1.6 Go module (go.mod existuje, zadne deps -- prazdny backend)
- [x] 1.7 `pnpm dev` startuje bez chyb
- [x] 1.8 Tailwind v4 + design tokens (globals.css, oklch)
- [x] 1.9 ESLint 9 + TypeScript strict mode
- [x] 1.10 Vitest setup (vitest.config.ts, vitest.setup.ts)
- [x] 1.11 Docker Compose (PostgreSQL 16)
- [x] 1.12 CI pipeline (.github/workflows/ci.yml -- lint, typecheck, build, test, go check)
- [x] 1.13 Dependabot konfigurace
- [x] 1.14 PR template + CodeRabbit konfigurace
- [x] 1.15 LICENSE (Apache 2.0)
- [x] 1.16 README.md (PR #15, ceka na merge)
- [x] 1.17 AGENTS.md (coding guidelines)
- [ ] 1.18 Prisma migrace (schema existuje, ale `db:push`/`db:migrate` nebylo spusteno -- neni DB)

---

## EPIC 2: Infrastruktura a auth ✅ ~80%

> Auth, session, onboarding, zakladni user flow.

### 2.1 Auth (NextAuth.js)
- [x] 2.1.1 NextAuth konfigurace (auth.ts, JWT strategie, Prisma adapter)
- [x] 2.1.2 Login stranka (/login, form s emailem + heslem)
- [x] 2.1.3 Middleware pro ochranu rout (Edge-compatible)
- [x] 2.1.4 Credentials provider (bcrypt verify, DB lookup)
- [x] 2.1.5 Registrace noveho uzivatele (signup stranka + API)
- [x] 2.1.6 Hesla (bcrypt hash + verify via bcryptjs)
- [ ] 2.1.7 Google OAuth provider (Phase 2)
- [~] 2.1.8 Onboarding flow (signup auto-creates workspace, no dedicated /onboarding page yet)
- [x] 2.1.9 Logout funkcionalita (signOut v toolbar)

### 2.2 RBAC (CASL)
- [x] 2.2.1 defineAbilitiesFor() s 5 rolemi
- [x] 2.2.2 CASL check na API routes (agents, credentials, crews)
- [x] 2.2.3 RBAC check na frontend (useAbilities hook, skryvani dle role) — PR #18
- [ ] 2.2.4 Crew-scoped permissions (MANAGER vidi jen prirazene crews)

### 2.3 Zustand store
- [x] 2.3.1 Zakladni store (currentWorkspaceId, sidebarOpen)
- [~] 2.3.2 useWorkspace() hook (fetches first workspace from API, MVP single-workspace)
- [ ] 2.3.3 Workspace switcher funkcionalita (zmena workspace, reload dat)

---

## EPIC 3: Frontend -- Layout a navigace ✅ ~90%

> Toolbar, sidebar, layout, sdilene komponenty.

- [x] 3.1 Root layout (Inter + JetBrains Mono, providers)
- [x] 3.2 Dashboard layout (sidebar + toolbar + main area)
- [x] 3.3 Top toolbar (logo, workspace switcher placeholder, search ⌘K, notifikace, settings, avatar)
- [x] 3.4 Sidebar navigace (Dashboard, Agents, Crews, Credentials, Skills, Audit, Settings)
- [x] 3.5 Mobile responsivita (sheet sidebar, sm: breakpoints)
- [x] 3.6 Sdilene komponenty (PageHeader, EmptyState, StatCard, FilterBar, AgentTabs)
- [~] 3.7 Command palette (⌘K -- komponenta existuje, ale neni funkcni)
- [x] 3.8 Toast system (sonner, Toaster v Providers) — PR #26
- [ ] 3.9 Notifikacni system (bell icon existuje, ale zadna logika)
- [ ] 3.10 Workspace switcher dropdown (UI existuje, ale neni napojeny)
- [x] 3.11 Error boundaries (app/error.tsx, app/global-error.tsx, app/not-found.tsx, dashboard/error.tsx) — PR #26

---

## EPIC 4: Frontend -- Stranky ✅ ~97%

> Vsechny stranky napojene na API s real daty.

### 4.1 Dashboard (/)
- [x] 4.1.1 Stat karty napojene na API (Total Agents, Running, API Keys)
- [x] 4.1.2 Agent list s FilterBar (filtr dle statusu funguje)
- [x] 4.1.3 AgentCard komponenta (status badge, crew, LLM, skills/creds/sessions counts)
- [x] 4.1.4 Loading skeletons + empty state
- [x] 4.1.5 Onboarding SetupNudge (3-krokovy progress: crew → agent → credentials) — PR #26

### 4.2 Agents list (/agents)
- [x] 4.2.1 Napojeno na GET /api/v1/agents
- [x] 4.2.2 AgentCard grid (responsive 1/2/3 cols)
- [x] 4.2.3 FilterBar filtruje dle statusu

### 4.3 Agent detail (/agents/[agentId])
- [x] 4.3.1 Overview tab (API data -- LLM config, system prompt, stats z _count)
- [x] 4.3.2 Chat tab (real-time WS chat, streaming messages, connection status) — PR #21
- [x] 4.3.3 Sessions tab (API data -- tabulka)
- [x] 4.3.4 Files tab (mock + "requires crewshipd" banner)
- [x] 4.3.5 Runs tab (API data -- tabulka se status/trigger/duration)
- [x] 4.3.6 Logs tab (mock + "requires crewshipd" banner)
- [x] 4.3.7 Settings tab (API data -- editable form + save + delete)
- [x] 4.3.8 Skills tab (API data -- card grid)
- [x] 4.3.9 Credentials tab (API data -- tabulka)
- [x] 4.3.10 History tab (mock + "audit system" banner)

### 4.4 Crews (/crews)
- [x] 4.4.1 Crew list napojeny na API (CrewCard grid, search, sort) — PR #26
- [x] 4.4.2 CrewCard komponenta (barva, ikona, pocty, created_at) — PR #26
- [x] 4.4.3 Crew detail stranka — refaktorovano do 7 sub-komponent (Header, Stats, EditForm, Agents, Members, AddMemberDialog, DangerZone) — PR #26
- [x] 4.4.4 Crew members management — PR #18
- [x] 4.4.5 Container config editing (memory, CPUs, TTL) — PR #26
- [x] 4.4.6 Credentials warning banner (amber banner kdyz agents existuji ale zadne credentials) — PR #26

### 4.5 Credentials (/credentials)
- [x] 4.5.1 Credential list napojeny na API (tabulka)
- [x] 4.5.2 Add credential dialog (form + sifrovani)
- [x] 4.5.3 Edit credential dialog
- [x] 4.5.4 Delete credential (s potvrzenim)
- [x] 4.5.5 Typovany credentials (AI_CLI_TOKEN / API_KEY / SECRET) -- enum CredentialType, CredentialProvider, CredentialStatus
- [x] 4.5.6 Add dialog s 3 taby (AI CLI Token / API Key / Secret) -- auto-fill env var name dle provider
- [x] 4.5.7 Tabulka s ikony (Bot/Key/Lock), status badges, provider labels
- [x] 4.5.8 Smazana /providers stranka + OAuth flow (nefunkcni server-side, nahrazeno setup-token)
- [ ] 4.5.9 TODO: User docs pro setup-token flow (claude setup-token -> paste do UI)
- [ ] 4.5.10 TODO: Auto-refresh pro AI_CLI_TOKEN (crewshipd CredentialMonitor refresh_token grant)

### 4.6 Skills (/skills)
- [x] 4.6.1 Skills z API (card layout)
- [x] 4.6.2 FilterBar filtruje dle source (All/Bundled/Managed/Marketplace/Custom)
- [ ] 4.6.3 Skill detail stranka

### 4.7 Settings (/settings)
- [x] 4.7.1 Workspace name/slug form napojeny na API (PUT)
- [x] 4.7.2 Members tabulka (fetch z API)
- [x] 4.7.3 Danger zone s AlertDialog potvrzenim (nahrazeno window.confirm) — PR #26
- [ ] 4.7.4 Billing/subscription tab

### 4.8 Audit (/audit)
- [x] 4.8.1 Audit log tabulka napojeny na API
- [ ] 4.8.2 Pokrocile filtry (date range, user picker)

### 4.9 Runs (/runs)
- [x] 4.9.1 Globalni runs page (across all agents, filterable) — PR #18
- [x] 4.9.2 GET /api/v1/runs endpoint — PR #18

### 4.10 Admin (/admin)
- [x] 4.10.1 Admin console (workspace management, user management, system stats) — PR #18
- [x] 4.10.2 GET /api/v1/admin/stats endpoint — PR #18
- [x] 4.10.3 GET /api/v1/admin/users endpoint — PR #18
- [x] 4.10.4 GET /api/v1/admin/workspaces endpoint — PR #18

### 4.11 Naming conventions
- [x] 4.11.1 "Team" → "Crew" terminologie opravena (crews pages, settings billing) — PR #25, #26

---

## EPIC 5: REST API (Next.js) ✅ ~95%

> CRUD endpointy pro vsechny entity.

### 5.1 Collection routes (GET + POST)
- [x] 5.1.1 POST/GET /api/v1/agents (CASL auth, crew_id validace, webhook_secret)
- [x] 5.1.2 POST/GET /api/v1/credentials (CASL auth, crew_id validace, AES-256 encryption)
- [x] 5.1.3 POST/GET /api/v1/crews (CASL auth, slug uniqueness)
- [x] 5.1.4 GET /api/v1/workspaces (auth + membership check)
- [x] 5.1.5 Auth handlers (GET/POST /api/auth/[...nextauth])
- [x] 5.1.6 POST /api/v1/auth/signup (bcrypt, auto-create workspace)

### 5.2 Detail routes (GET/PUT/DELETE)
- [x] 5.2.1 GET/PUT/DELETE /api/v1/agents/[agentId]
- [x] 5.2.2 GET/PUT/DELETE /api/v1/crews/[crewId]
- [x] 5.2.3 GET/PUT/DELETE /api/v1/credentials/[credentialId]
- [x] 5.2.4 GET/PUT/DELETE /api/v1/workspaces/[workspaceId]

### 5.3 Sub-resource routes
- [x] 5.3.1 GET/POST /api/v1/crews/[crewId]/members
- [x] 5.3.2 DELETE /api/v1/crews/[crewId]/members/[memberId]
- [x] 5.3.3 GET /api/v1/workspaces/[workspaceId]/members
- [x] 5.3.4 GET/POST /api/v1/agents/[agentId]/skills
- [x] 5.3.5 GET/POST /api/v1/agents/[agentId]/credentials
- [x] 5.3.6 GET /api/v1/agents/[agentId]/sessions
- [x] 5.3.7 GET /api/v1/agents/[agentId]/runs
- [x] 5.3.8 GET /api/v1/skills (list + search, filterable)
- [x] 5.3.9 GET /api/v1/audit (paginated, filterable)
- [x] 5.3.10 POST/GET /api/v1/workspaces/[workspaceId]/invitations — PR #18
- [x] 5.3.11 GET /api/v1/runs (globalni runs across agents) — PR #18
- [x] 5.3.12 GET /api/v1/admin/stats + /admin/users + /admin/workspaces — PR #18

### 5.4 Middleware a utility
- [x] 5.4.1 requireAuth() helper (session + workspace membership check)
- [x] 5.4.2 Zod validacni schemata (agents, crews, credentials, workspaces, invitations)
- [x] 5.4.3 createAuditLog() helper (lib/audit.ts)
- [ ] 5.4.4 Soft-delete middleware (Prisma middleware pro `deleted_at` filtr)
- [ ] 5.4.5 Rate limiting middleware (z config/rate-limits.yml)
- [ ] 5.4.6 API error handling (standardized error responses)

---

## EPIC 6: Go backend (crewshipd) 🟡 ~75%

> WebSocket gateway, Docker orchestrace, logy, soubory, webhooky.

### 6.1 Zaklady
- [x] 6.1.1 cmd/crewshipd/main.go (signal handling, bootstrap logger, config load) — PR #17
- [x] 6.1.2 go.mod (module definice, go 1.25, docker/bbolt/fsnotify/yaml deps)
- [x] 6.1.3 Konfiguracni system (YAML config parser + CREWSHIP_* env overrides, validace) — PR #17
- [x] 6.1.4 Structured logging (slog JSON stdout, context propagation) — PR #17
- [x] 6.1.5 Health endpoint (HTTP /healthz, /readyz) — PR #17
- [x] 6.1.6 Metrics endpoint (Prometheus /metrics) — PR #17

### 6.2 Provider interfaces
- [x] 6.2.1 ContainerProvider interface (EnsureCrewRuntime, Stop, Remove, Exec, Status) — PR #19
- [x] 6.2.2 StorageProvider interface (Read, Write, List, Delete, Exists, Watch) — PR #19
- [x] 6.2.3 StateProvider interface (Get, Set, Delete, List, ListByPrefix) — PR #19
- [x] 6.2.4 Docker provider implementace (MVP) — PR #19
- [x] 6.2.5 LocalFS provider implementace (MVP) — PR #19
- [x] 6.2.6 bbolt provider implementace (MVP) — PR #19

### 6.3 IPC (Next.js <-> Go)
- [x] 6.3.1 Unix socket server — PR #17
- [x] 6.3.2 IPC protokol (HTTP JSON over Unix socket) — PR #17
- [x] 6.3.3 Next.js IPC klient (lib/crewshipd-client.ts, typed wrappers) — PR #17

### 6.4 WebSocket gateway
- [x] 6.4.1 WebSocket server (goroutines, hub pattern, channel pub/sub, ping/pong) — PR #17
- [x] 6.4.2 Auth (JWT validace -- NextAuth JWE dekrypce, HKDF + go-jose/v4) — PR #21
- [x] 6.4.3 Chat message routing (user → ChatBridge → orchestrator → Docker exec → WS stream) — PR #21
- [ ] 6.4.4 Real-time agent status broadcasting
- [ ] 6.4.5 Real-time log streaming

### 6.5 Docker orchestrace
- [x] 6.5.1 Container lifecycle (create per crew, start/stop) — PR #19
- [x] 6.5.2 Docker exec (spusteni CLI session v kontejneru) — PR #19
- [x] 6.5.3 Agent runtime Dockerfile (non-root, UID 1001, --internal network) — PR #19
- [x] 6.5.4 Credential ENV injection (priority-based failover) — PR #19
- [ ] 6.5.5 Container TTL management (auto-stop po neaktivite)
- [ ] 6.5.6 Container resource limits (memory, CPU per crew)

### 6.6 Log collector
- [x] 6.6.1 JSONL log writer (stdout capture -> soubory) — PR #19
- [ ] 6.6.2 Logrotate integrace (hodinova rotace, gzip)
- [ ] 6.6.3 Log streaming pres WebSocket

### 6.7 File server
- [x] 6.7.1 /output/ bind mount management — PR #19
- [x] 6.7.2 fsnotify real-time file watching — PR #19
- [x] 6.7.3 File list/download API — PR #19

### 6.8 Webhook ingress
- [x] 6.8.1 Webhook receiver (POST /webhooks/{crew}/{agent}/trigger) — PR #19
- [x] 6.8.2 HMAC validace (per-agent webhook_secret) — PR #19
- [ ] 6.8.3 Agent trigger z webhooku (handler existuje, neni napojen na orchestrator)

### 6.9 Conversation session
- [x] 6.9.1 JSONL writer (zpravy do souboru) — PR #21
- [x] 6.9.2 JSONL reader (nacitani session, offset/limit) — PR #21
- [ ] 6.9.3 Session metadata sync (Go -> PostgreSQL pres IPC)

### 6.10 Chat flow wiring
- [x] 6.10.1 Provider injection do serveru (Docker, LocalFS, bbolt z configu) — PR #21
- [x] 6.10.2 IPC handlers s realnymi providery (container status/start/stop, file list, session messages) — PR #21
- [x] 6.10.3 ChatBridge (WS → conversation store → orchestrator → stream → WS) — PR #21
- [x] 6.10.4 WS token API endpoint (/api/v1/ws-token) — PR #21

---

## EPIC 7: Create/Edit forms (frontend) ✅ 100%

> Formulare pro vytvareni a editaci entit. Napojeni na API.

- [x] 7.1 Create Agent form (/agents/new -- vsechna pole, crew dropdown, slug auto-gen)
- [x] 7.2 Edit Agent form (/agents/[id]/settings -- napojeno na API, save + delete)
- [x] 7.3 Create Crew form (/crews/new -- vsechna pole, color picker, slug auto-gen)
- [x] 7.4 Edit Crew form (/crews/[crewId] detail stranka s edit + delete) — PR #18
- [x] 7.5 Add Credential dialog (form s show/hide hesla, scope, crew select)
- [x] 7.6 Edit Credential dialog (pre-fill, optional value change)
- [x] 7.7 Invite Member dialog — PR #18
- [x] 7.8 Workspace settings form (napojeno na API, save + members list)

---

## EPIC 8: Testy ✅ ~80%

> Unit testy, integracni testy. Celkem: 270 testu (142 TS + 128 Go).

- [x] 8.1 Vitest unit testy pro encryption.ts (10 testu)
- [x] 8.2 Vitest unit testy pro validations.ts (30 testu, vcetne updateCrewSchema + container fields) — PR #26
- [x] 8.3 Vitest unit testy pro abilities.ts (20 testu)
- [x] 8.4 Vitest unit testy pro slugify.ts (11 testu)
- [x] 8.5 Vitest unit testy pro cn.ts (9 testu)
- [ ] 8.6 Vitest unit testy pro api-auth.ts
- [ ] 8.7 API route integration testy (agents, teams, credentials)
- [x] 8.8 Go unit testy (76 testu: config, logging, server, ws, bbolt, localfs, fileserver, logcollector, failover, webhook, auth, conversation) — PR #17 + #19 + #21
- [ ] 8.9 E2E testy (Playwright -- Phase 2)

---

## EPIC 9: Seed data a dev tooling ✅ ~80%

> Seed skripty pro development, dev helper funkce.

- [x] 9.1 Prisma seed skript (demo user, workspace, 2 crews, 4 agents, 3 skills, 2 credentials, plan, subscription, audit logs)
- [x] 9.2 Bundled skills seed (Coding Assistant, Web Researcher, DevOps Helper)
- [ ] 9.3 Dev login bypass (auto-login pro development bez hesla)
- [x] 9.4 .env.example s vsemi promennymi

---

## EPIC 10: Nasazeni a DevOps 🟡 ~40%

> Docker images, Coolify deployment, CI/CD.

- [x] 10.1 Docker Compose pro local dev (PostgreSQL)
- [x] 10.2 GitHub Actions CI (lint, typecheck, build, test, go check)
- [x] 10.3 Dependabot (npm weekly, Go weekly, GHA monthly)
- [ ] 10.4 Next.js production Dockerfile
- [ ] 10.5 crewshipd production Dockerfile
- [x] 10.6 Agent runtime Dockerfile — PR #19
- [ ] 10.7 docker-compose.prod.yml (full stack)
- [ ] 10.8 Coolify deployment konfigurace
- [ ] 10.9 Environment variables management (secrets)

---

## Faze 2A/2B/3: Orchestrace + Mission (po MVP) ❌ 0%

> Lead + Coordinator + Mission. Nezacinat pred dokoncenim MVP.
> Specifikace: ORCHESTRATION.md (assignment, sidecar) + CREW-EXECUTION.md (mission, workflow, hiring)

### Phase 2A: Lead + zakladni Mission

**Orchestrace (ORCHESTRATION.md):**
- [ ] ORCH-01 AgentRole enum + DB migrace
- [ ] ORCH-02 Lead designation UI
- [ ] ORCH-03 Auto-generated lead system prompt
- [ ] ORCH-04 crewship-sidecar (loopback HTTP v kontejneru)
- [ ] ORCH-05 Assignment protokol (HTTP REST na sidecar)
- [ ] ORCH-06 Lead → Agent assignment (Docker exec orchestrace)
- [ ] ORCH-07 Activity feed v UI
- [ ] ORCH-08 Assignment tabulka (audit vsech assignments)
- [ ] ORCH-09 Lead auto-routing (user → crew → lead rozhodne)
- [ ] ORCH-10 Paralelni assignment (wait_group pattern)
- [ ] ORCH-11 Error handling + circuit breaker (3x retry → eskalace)
- [ ] ORCH-12 Lead summary/agregace

**Mission (CREW-EXECUTION.md):**
- [ ] EXEC-01 Mission + MissionTask Prisma modely + migrace
- [ ] EXEC-02 Sidecar endpointy pro mission (/mission/create, /plan, /task/:id)
- [ ] EXEC-03 MissionEngine v crewshipd (vytvareni, plan, dependency resolution)
- [ ] EXEC-04 JSONL progress writer (mirror do /output/, append-only)
- [ ] EXEC-05 Mission Board UI -- tabulkovy spreadsheet view
- [ ] EXEC-06 WebSocket real-time updaty (mission.* a task.* eventy)
- [ ] EXEC-07 Mission historie (seznam dokoncenych missions per crew)
- [ ] EXEC-08 Dashboard widget "Running Missions"

### Phase 2B: Workflow sablony + Auto-hiring + Coordinator

**Orchestrace (ORCHESTRATION.md):**
- [ ] ORCH-13 Coordinator agent role (specialni agent na urovni workspace)
- [ ] ORCH-14 Coordinator lightweight execution (LLM call bez Docker kontejneru)
- [ ] ORCH-15 Coordinator → Lead assignment (cross-crew)
- [ ] ORCH-16 Coordinator auto-routing
- [ ] ORCH-17 Cross-crew agregace
- [ ] ORCH-18 Coordinator UI (dashboard card + chat)
- [ ] ORCH-19 Lead modes (active/passive)
- [ ] ORCH-20 Agent output compression (auto-sumarizace)
- [ ] ORCH-21 Cost estimation per crew operation
- [ ] ORCH-22 Per-crew budget limits
- [ ] ORCH-23 crewship-agent binary (API-direct runtime)
- [ ] ORCH-24 Trace ID across assignments
- [ ] ORCH-25 Meilisearch chat search

**Mission (CREW-EXECUTION.md):**
- [ ] EXEC-09 Workflow sablony (JSON format, vestavene: dev-test-loop, sequential, parallel)
- [ ] EXEC-10 Loop controller v crewshipd (condition check, iterace)
- [ ] EXEC-11 Dev-test loop integrace (Developer → Tester → zpet)
- [ ] EXEC-12 Auto-hiring: SUPERVISED mode (UI notifikace, schvaleni)
- [ ] EXEC-13 Auto-hiring: SEMI_AUTO mode (automaticke prirazeni existujicich)
- [ ] EXEC-14 Crew hiring_autonomy nastaveni v UI
- [ ] EXEC-15 Docasni agenti (is_temporary, lifecycle: hired → working → expired)
- [ ] EXEC-16 Coordinator routing mode (passive router, dual model, full reasoning, budget)
- [ ] EXEC-17 Inline metriky v Mission Board (duration, token count, cost per task)

### Phase 3: Pokrocila orchestrace

- [ ] EXEC-18 Auto-hiring: FULL_AUTO mode (plne autonomni + marketplace)
- [ ] EXEC-19 Git worktree integrace (per-agent branch, lead merge)
- [ ] EXEC-20 Cross-crew mission (coordinator koordinuje vice crews)
- [ ] EXEC-21 Mission replay/debug
- [ ] EXEC-22 Primo lead-lead komunikace (bez coordinatora, s RBAC)
- [ ] EXEC-23 Mission analytics (grafy, trendy, srovnani efektivity)
- [ ] EXEC-24 Custom workflow sablony (uzivatel tvori v UI)
- [ ] EXEC-25 Coordinator full reasoning + budget limit
- [ ] ORCH-26 Orchestracni vizualizace (graf assignments v realtime)
- [ ] ORCH-27 Auto-lead election
- [ ] ORCH-29 NATS JetStream integrace
- [ ] ORCH-30 gVisor runtime
- [ ] ORCH-31 Assignment replay/debug
- [ ] ORCH-32 Landlock per-agent izolace
- [ ] ORCH-33 API-direct jako default

---

## Souhrnne statistiky

| Epic | Nazev | Stav | Hotovo |
|------|-------|------|--------|
| 0 | Dokumentace | ✅ | 100% |
| 1 | Scaffolding | ✅ | ~95% |
| 2 | Auth a infra | ✅ | ~80% |
| 3 | Layout a navigace | ✅ | ~90% |
| 4 | Frontend stranky | ✅ | ~97% |
| 5 | REST API | ✅ | ~95% |
| 6 | Go backend | ✅ | ~85% |
| 7 | Create/Edit forms | ✅ | 100% |
| 8 | Testy | ✅ | ~92% (270 testu: 128 Go + 142 TS) |
| 9 | Seed data | ✅ | ~80% |
| 10 | Nasazeni | ✅ | ~80% |

### Co zbyva pro spusteni MVP

#### P0: MUST HAVE (bez toho to nejede) -- PR #22 ✅

- [x] **Production Dockerfile: Next.js** (multi-stage, standalone output)
- [x] **Production Dockerfile: crewshipd** (multi-stage, static binary)
- [x] **docker-compose.prod.yml** (PostgreSQL + Next.js + crewshipd + agent-runtime sit)
- [x] **Session metadata sync** (Go → IPC → Next.js → Prisma)
- [x] **SessionResolver** (IPCResolver: crewshipd → Next.js, dekrypce credentials)
- [x] **Prisma migrace** (init migrace, 20 tabulek, migration_lock.toml)

#### P1: SHOULD HAVE (pro rozumne demo)

- [ ] **Container TTL** -- auto-stop po neaktivite
- [ ] **Container resource limits** -- memory, CPU per crew
- [ ] **Webhook → orchestrator** -- napojeni trigger handleru na RunAgent
- [ ] **Real-time log streaming** pres WebSocket
- [ ] **Coolify deployment config** (staging Proxmox)

#### P2: NICE TO HAVE (ne blokuje spusteni)

- [ ] Workspace switcher funkcionalita
- [ ] Command palette (⌘K)
- [ ] Notifikacni system (bell icon + logika)
- [ ] Crew-scoped permissions (MANAGER)
- [ ] Logrotate integrace
- [ ] Advanced audit filtry (date range, user picker)
- [ ] Skill detail stranka
- [ ] API route integration testy
- [ ] Google OAuth (Phase 2, disabled button uz existuje)
- [ ] Billing/subscription tab

### Dalsi kroky

#### Faze 1: Open Source Wow (aktualni priorita)
> Detail: `.factory/context/STRATEGY-2026.md` sekce 8

- [ ] Single binary distribuce (GoReleaser, brew tap, curl installer)
- [ ] SQLite jako default DB (Prisma migrace, zero deps)
- [ ] Embedded Next.js (static export v Go binary)
- [ ] `crewship start/stop/status/logs` CLI
- [ ] 15-20 official skills s permissions modelem
- [ ] Skill Store UI v dashboardu
- [ ] Per-agent network control UI
- [ ] Per-agent cost budgets a alerting
- [ ] Onboarding wizard
- [ ] Landing page + README s "brew install crewship"

#### Faze 2: Monetizace (+3-6 mesicu)
- crewship.ai cloud tier
- Community skill marketplace + revenue sharing
- Lead orchestrace (Phase 2A)
- Messaging integrace (Slack, Discord)
- Stripe billing

#### Faze 3: Enterprise (+6-12 mesicu)
- K8s Helm chart
- SSO/SAML
- Coordinator (Phase 2B)
- SOC 2 compliance

#### Phase 2A/2B/3 Orchestrace (nezmeneno)
- **Phase 2A** -- Lead assignment (sidecar, Assignment) + zakladni Mission (Board UI, JSONL progress)
- **Phase 2B** -- Workflow sablony (dev-test loop), Auto-hiring (supervised/semi-auto), Coordinator routing
- **Phase 3** -- Full auto hiring + marketplace, Git worktree, cross-crew mission, analytics

### Merge historie

| PR | Nazev | Datum |
|----|-------|-------|
| #26 | Crews improvements (detail refactor, search/sort, toast, error boundaries, onboarding, AlertDialog, container config, tests) | 2026-02-18 |
| #22 | Production deployment (Dockerfiles, compose, Prisma migration, SessionResolver) | 2026-02-16 |
| #21 | E2E chat flow (providers wiring, JWT auth, conversation store, ChatBridge, Chat UI) | 2026-02-16 |
| #19 | Docker runtime (providers, orchestrator, log collector, file server, webhook) | 2026-02-16 |
| #18 | Frontend polish (team detail, runs, admin console, RBAC, invite dialog) | 2026-02-16 |
| #17 | Go backend foundation (config, logging, HTTP, IPC, WebSocket, providers) | 2026-02-16 |
| #16 | Complete MVP frontend (auth, API, forms, tests, seed) | 2026-02-16 |
| #15 | README.md | 2026-02-16 |
| #9 | MVP UI (scaffolding, dashboard, agent detail pages) | 2026-02-15 |
