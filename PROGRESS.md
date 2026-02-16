# Crewship -- Progress Tracker

**Posledni aktualizace:** 2026-02-16
**Ucel:** Sledovani stavu implementace po epicich a taskach.

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
- [x] 0.15 Vytvorit CREW-EXECUTION.md (v1.0) — Crew Execution, Workflow sablony, Auto-hiring, Progress tracking

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
- [~] 2.1.8 Onboarding flow (signup auto-creates org, no dedicated /onboarding page yet)
- [x] 2.1.9 Logout funkcionalita (signOut v toolbar)

### 2.2 RBAC (CASL)
- [x] 2.2.1 defineAbilitiesFor() s 5 rolemi
- [x] 2.2.2 CASL check na API routes (agents, credentials, teams)
- [x] 2.2.3 RBAC check na frontend (useAbilities hook, skryvani dle role) — PR #18
- [ ] 2.2.4 Team-scoped permissions (MANAGER vidi jen prirazene tymy)

### 2.3 Zustand store
- [x] 2.3.1 Zakladni store (currentOrgId, sidebarOpen)
- [~] 2.3.2 useOrg() hook (fetches first org from API, MVP single-org)
- [ ] 2.3.3 Org switcher funkcionalita (zmena org, reload dat)

---

## EPIC 3: Frontend -- Layout a navigace ✅ ~90%

> Toolbar, sidebar, layout, sdilene komponenty.

- [x] 3.1 Root layout (Inter + JetBrains Mono, providers)
- [x] 3.2 Dashboard layout (sidebar + toolbar + main area)
- [x] 3.3 Top toolbar (logo, org switcher placeholder, search ⌘K, notifikace, settings, avatar)
- [x] 3.4 Sidebar navigace (Dashboard, Agents, Teams, Crews, Credentials, Skills, Audit, Settings)
- [x] 3.5 Mobile responsivita (sheet sidebar, sm: breakpoints)
- [x] 3.6 Sdilene komponenty (PageHeader, EmptyState, StatCard, FilterBar, AgentTabs)
- [~] 3.7 Command palette (⌘K -- komponenta existuje, ale neni funkcni)
- [ ] 3.8 Notifikacni system (bell icon existuje, ale zadna logika)
- [ ] 3.9 Org switcher dropdown (UI existuje, ale neni napojeny)

---

## EPIC 4: Frontend -- Stranky ✅ ~97%

> Vsechny stranky napojene na API s real daty.

### 4.1 Dashboard (/)
- [x] 4.1.1 Stat karty napojene na API (Total Agents, Running, API Keys)
- [x] 4.1.2 Agent list s FilterBar (filtr dle statusu funguje)
- [x] 4.1.3 AgentCard komponenta (status badge, team, LLM, skills/creds/sessions counts)
- [x] 4.1.4 Loading skeletons + empty state

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

### 4.4 Teams (/teams)
- [x] 4.4.1 Team list napojeny na API (TeamCard grid)
- [x] 4.4.2 TeamCard komponenta (barva, ikona, pocty)
- [x] 4.4.3 Team detail stranka (/teams/[teamId]) — PR #18
- [x] 4.4.4 Team members management — PR #18 (soucasti team detail)

### 4.5 Credentials (/credentials)
- [x] 4.5.1 Credential list napojeny na API (tabulka)
- [x] 4.5.2 Add credential dialog (form + sifrovani)
- [x] 4.5.3 Edit credential dialog
- [x] 4.5.4 Delete credential (s potvrzenim)

### 4.6 Skills (/skills)
- [x] 4.6.1 Skills z API (card layout)
- [x] 4.6.2 FilterBar filtruje dle source (All/Bundled/Managed/Marketplace/Custom)
- [ ] 4.6.3 Skill detail stranka

### 4.7 Settings (/settings)
- [x] 4.7.1 Org name/slug form napojeny na API (PUT)
- [x] 4.7.2 Members tabulka (fetch z API)
- [x] 4.7.3 Danger zone s potvrzenim
- [ ] 4.7.4 Billing/subscription tab

### 4.8 Audit (/audit)
- [x] 4.8.1 Audit log tabulka napojeny na API
- [ ] 4.8.2 Pokrocile filtry (date range, user picker)

### 4.9 Runs (/runs)
- [x] 4.9.1 Globalni runs page (across all agents, filterable) — PR #18
- [x] 4.9.2 GET /api/v1/runs endpoint — PR #18

### 4.10 Admin (/admin)
- [x] 4.10.1 Admin console (org management, user management, system stats) — PR #18
- [x] 4.10.2 GET /api/v1/admin/stats endpoint — PR #18
- [x] 4.10.3 GET /api/v1/admin/users endpoint — PR #18
- [x] 4.10.4 GET /api/v1/admin/organizations endpoint — PR #18

### 4.11 Crews (/crews)
- [x] 4.11.1 Phase 2 placeholder (Coming Soon badge)

---

## EPIC 5: REST API (Next.js) ✅ ~95%

> CRUD endpointy pro vsechny entity.

### 5.1 Collection routes (GET + POST)
- [x] 5.1.1 POST/GET /api/v1/agents (CASL auth, team_id validace, webhook_secret)
- [x] 5.1.2 POST/GET /api/v1/credentials (CASL auth, team_id validace, AES-256 encryption)
- [x] 5.1.3 POST/GET /api/v1/teams (CASL auth, slug uniqueness)
- [x] 5.1.4 GET /api/v1/orgs (auth + membership check)
- [x] 5.1.5 Auth handlers (GET/POST /api/auth/[...nextauth])
- [x] 5.1.6 POST /api/v1/auth/signup (bcrypt, auto-create org)

### 5.2 Detail routes (GET/PUT/DELETE)
- [x] 5.2.1 GET/PUT/DELETE /api/v1/agents/[agentId]
- [x] 5.2.2 GET/PUT/DELETE /api/v1/teams/[teamId]
- [x] 5.2.3 GET/PUT/DELETE /api/v1/credentials/[credentialId]
- [x] 5.2.4 GET/PUT/DELETE /api/v1/orgs/[orgId]

### 5.3 Sub-resource routes
- [x] 5.3.1 GET/POST /api/v1/teams/[teamId]/members
- [x] 5.3.2 DELETE /api/v1/teams/[teamId]/members/[memberId]
- [x] 5.3.3 GET /api/v1/orgs/[orgId]/members
- [x] 5.3.4 GET/POST /api/v1/agents/[agentId]/skills
- [x] 5.3.5 GET/POST /api/v1/agents/[agentId]/credentials
- [x] 5.3.6 GET /api/v1/agents/[agentId]/sessions
- [x] 5.3.7 GET /api/v1/agents/[agentId]/runs
- [x] 5.3.8 GET /api/v1/skills (list + search, filterable)
- [x] 5.3.9 GET /api/v1/audit (paginated, filterable)
- [x] 5.3.10 POST/GET /api/v1/orgs/[orgId]/invitations — PR #18
- [x] 5.3.11 GET /api/v1/runs (globalni runs across agents) — PR #18
- [x] 5.3.12 GET /api/v1/admin/stats + /admin/users + /admin/organizations — PR #18

### 5.4 Middleware a utility
- [x] 5.4.1 requireAuth() helper (session + org membership check)
- [x] 5.4.2 Zod validacni schemata (agents, teams, credentials, orgs, invitations)
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
- [x] 6.2.1 ContainerProvider interface (EnsureTeamRuntime, Stop, Remove, Exec, Status) — PR #19
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
- [x] 6.5.1 Container lifecycle (create per team, start/stop) — PR #19
- [x] 6.5.2 Docker exec (spusteni CLI session v kontejneru) — PR #19
- [x] 6.5.3 Agent runtime Dockerfile (non-root, UID 1001, --internal network) — PR #19
- [x] 6.5.4 Credential ENV injection (priority-based failover) — PR #19
- [ ] 6.5.5 Container TTL management (auto-stop po neaktivite)
- [ ] 6.5.6 Container resource limits (memory, CPU per team)

### 6.6 Log collector
- [x] 6.6.1 JSONL log writer (stdout capture -> soubory) — PR #19
- [ ] 6.6.2 Logrotate integrace (hodinova rotace, gzip)
- [ ] 6.6.3 Log streaming pres WebSocket

### 6.7 File server
- [x] 6.7.1 /output/ bind mount management — PR #19
- [x] 6.7.2 fsnotify real-time file watching — PR #19
- [x] 6.7.3 File list/download API — PR #19

### 6.8 Webhook ingress
- [x] 6.8.1 Webhook receiver (POST /webhooks/{team}/{agent}/trigger) — PR #19
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

- [x] 7.1 Create Agent form (/agents/new -- vsechna pole, team dropdown, slug auto-gen)
- [x] 7.2 Edit Agent form (/agents/[id]/settings -- napojeno na API, save + delete)
- [x] 7.3 Create Team form (/teams/new -- vsechna pole, color picker, slug auto-gen)
- [x] 7.4 Edit Team form (/teams/[teamId] detail stranka s edit + delete) — PR #18
- [x] 7.5 Add Credential dialog (form s show/hide hesla, scope, team select)
- [x] 7.6 Edit Credential dialog (pre-fill, optional value change)
- [x] 7.7 Invite Member dialog — PR #18
- [x] 7.8 Org settings form (napojeno na API, save + members list)

---

## EPIC 8: Testy ✅ ~80%

> Unit testy, integracni testy. Celkem: 148 testu (73 TS + 75 Go).

- [x] 8.1 Vitest unit testy pro encryption.ts (10 testu)
- [x] 8.2 Vitest unit testy pro validations.ts (23 testu)
- [x] 8.3 Vitest unit testy pro abilities.ts (20 testu)
- [x] 8.4 Vitest unit testy pro slugify.ts (11 testu)
- [x] 8.5 Vitest unit testy pro cn.ts (9 testu)
- [ ] 8.6 Vitest unit testy pro api-auth.ts
- [ ] 8.7 API route integration testy (agents, teams, credentials)
- [x] 8.8 Go unit testy (75 testu: config, logging, server, ws, bbolt, localfs, fileserver, logcollector, failover, webhook, auth, conversation) — PR #17 + #19 + #21
- [ ] 8.9 E2E testy (Playwright -- Phase 2)

---

## EPIC 9: Seed data a dev tooling ✅ ~80%

> Seed skripty pro development, dev helper funkce.

- [x] 9.1 Prisma seed skript (demo user, org, 2 teams, 4 agents, 3 skills, 2 credentials, plan, subscription, audit logs)
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

## Faze 2A/2B/3: Orchestrace + Crew Execution (po MVP) ❌ 0%

> Crew Leader + Virtual Director + Crew Execution. Nezacinat pred dokoncenim MVP.
> Specifikace: ORCHESTRATION.md (delegace, sidecar) + CREW-EXECUTION.md (execution, workflow, hiring)

### Phase 2A: Crew Leader + zakladni Crew Execution

**Orchestrace (ORCHESTRATION.md):**
- [ ] ORCH-01 AgentRole enum + DB migrace
- [ ] ORCH-02 Leader designation UI
- [ ] ORCH-03 Auto-generated leader system prompt
- [ ] ORCH-04 crewship-sidecar (loopback HTTP v kontejneru)
- [ ] ORCH-05 Delegacni protokol (HTTP REST na sidecar)
- [ ] ORCH-06 Leader → Worker delegace (Docker exec orchestrace)
- [ ] ORCH-07 Delegacni timeline v UI
- [ ] ORCH-08 DelegationLog tabulka (audit vsech delegaci)
- [ ] ORCH-09 Leader auto-routing (user → team → leader rozhodne)
- [ ] ORCH-10 Paralelni delegace (wait_group pattern)
- [ ] ORCH-11 Error handling + circuit breaker (3x retry → eskalace)
- [ ] ORCH-12 Leader summary/agregace

**Crew Execution (CREW-EXECUTION.md):**
- [ ] EXEC-01 CrewExecution + CrewExecutionTask Prisma modely + migrace
- [ ] EXEC-02 Sidecar endpointy pro execution (/execution/create, /plan, /task/:id)
- [ ] EXEC-03 ExecutionEngine v crewshipd (vytvareni, plan, dependency resolution)
- [ ] EXEC-04 JSONL progress writer (mirror do /output/, append-only)
- [ ] EXEC-05 Execution Board UI -- tabulkovy spreadsheet view
- [ ] EXEC-06 WebSocket real-time updaty (execution.* a task.* eventy)
- [ ] EXEC-07 Execution historie (seznam dokoncenych execution per tym)
- [ ] EXEC-08 Dashboard widget "Running Executions"

### Phase 2B: Workflow sablony + Auto-hiring + Director

**Orchestrace (ORCHESTRATION.md):**
- [ ] ORCH-13 Director agent role (specialni agent na urovni organizace)
- [ ] ORCH-14 Director lightweight execution (LLM call bez Docker kontejneru)
- [ ] ORCH-15 Director → Leader delegace (cross-team)
- [ ] ORCH-16 Director auto-routing
- [ ] ORCH-17 Cross-team agregace
- [ ] ORCH-18 Director UI (dashboard card + chat)
- [ ] ORCH-19 Leader modes (active/passive)
- [ ] ORCH-20 Worker output compression (auto-sumarizace)
- [ ] ORCH-21 Cost estimation per crew operation
- [ ] ORCH-22 Per-crew budget limits
- [ ] ORCH-23 crewship-agent binary (API-direct runtime)
- [ ] ORCH-24 Trace ID across delegaci
- [ ] ORCH-25 Meilisearch conversation search

**Crew Execution (CREW-EXECUTION.md):**
- [ ] EXEC-09 Workflow sablony (JSON format, vestavene: dev-test-loop, sequential, parallel)
- [ ] EXEC-10 Loop controller v crewshipd (condition check, iterace)
- [ ] EXEC-11 Dev-test loop integrace (Developer → Tester → zpet)
- [ ] EXEC-12 Auto-hiring: SUPERVISED mode (UI notifikace, schvaleni)
- [ ] EXEC-13 Auto-hiring: SEMI_AUTO mode (automaticke prirazeni existujicich)
- [ ] EXEC-14 Team hiring_autonomy nastaveni v UI
- [ ] EXEC-15 Docasni agenti (is_temporary, lifecycle: hired → working → expired)
- [ ] EXEC-16 Director routing mode (passive router, dual model, full reasoning, budget)
- [ ] EXEC-17 Inline metriky v Execution Board (duration, token count, cost per task)

### Phase 3: Pokrocila orchestrace

- [ ] EXEC-18 Auto-hiring: FULL_AUTO mode (plne autonomni + marketplace)
- [ ] EXEC-19 Git worktree integrace (per-worker branch, leader merge)
- [ ] EXEC-20 Cross-team execution (director koordinuje vice tymu)
- [ ] EXEC-21 Execution replay/debug
- [ ] EXEC-22 Primo lidr-lidr komunikace (bez directora, s RBAC)
- [ ] EXEC-23 Execution analytics (grafy, trendy, srovnani efektivity)
- [ ] EXEC-24 Custom workflow sablony (uzivatel tvori v UI)
- [ ] EXEC-25 Director full reasoning + budget limit
- [ ] ORCH-26 Orchestracni vizualizace (graf delegaci v realtime)
- [ ] ORCH-27 Auto-leader election
- [ ] ORCH-29 NATS JetStream integrace
- [ ] ORCH-30 gVisor runtime
- [ ] ORCH-31 Delegation replay/debug
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
| 6 | Go backend | 🟡 | ~75% |
| 7 | Create/Edit forms | ✅ | 100% |
| 8 | Testy | ✅ | ~80% (148 testu) |
| 9 | Seed data | ✅ | ~80% |
| 10 | Nasazeni | 🟡 | ~40% |

### Co zbyva pro kompletni MVP

**Frontend (male):**
1. **Google OAuth** -- Phase 2 (zatim disabled button)
2. **Team-scoped permissions** -- MANAGER vidi jen prirazene tymy
3. **Org switcher funkcionalita** -- zmena org, reload dat
4. **Command palette** -- ⌘K funkcni vyhledavani
5. **Notifikacni system** -- bell icon + logika
6. **Advanced filtry na audit page** -- date range, user picker
7. **Skill detail stranka** -- /skills/[skillId]
8. **Billing/subscription tab** -- v Settings

**Go backend (stredni):**
9. ~~**WebSocket JWT validace**~~ ✅ PR #21
10. ~~**Chat message routing**~~ ✅ PR #21
11. **Real-time streaming** -- agent status broadcasting + log streaming
12. **Container TTL management** -- auto-stop po neaktivite
13. **Container resource limits** -- memory, CPU per team
14. **Logrotate integrace** -- hodinova rotace, gzip
15. ~~**Conversation session**~~ ✅ PR #21 (writer + reader hotovy, metadata sync zbyva)
16. **Webhook → orchestrator** -- napojeni trigger handleru na spusteni agenta

**DevOps:**
17. **Next.js production Dockerfile**
18. **crewshipd production Dockerfile**
19. **docker-compose.prod.yml** (full stack)
20. **Coolify deployment konfigurace**

**Testy:**
21. **API route integration testy** (agents, teams, credentials)
22. **E2E testy** (Playwright -- Phase 2)

### Dalsi kroky (Phase 2 -- Orchestrace + Crew Execution)

23. **Phase 2A** -- Crew Leader delegace (sidecar, DelegationLog) + zakladni Crew Execution (Board UI, JSONL progress)
24. **Phase 2B** -- Workflow sablony (dev-test loop), Auto-hiring (supervised/semi-auto), Director routing
25. **Phase 3** -- Full auto hiring + marketplace, Git worktree, cross-team execution, analytics

### Merge historie

| PR | Nazev | Datum |
|----|-------|-------|
| #21 | E2E chat flow (providers wiring, JWT auth, conversation store, ChatBridge, Chat UI) | 2026-02-16 |
| #19 | Docker runtime (providers, orchestrator, log collector, file server, webhook) | 2026-02-16 |
| #18 | Frontend polish (team detail, runs, admin console, RBAC, invite dialog) | 2026-02-16 |
| #17 | Go backend foundation (config, logging, HTTP, IPC, WebSocket, providers) | 2026-02-16 |
| #16 | Complete MVP frontend (auth, API, forms, tests, seed) | 2026-02-16 |
| #15 | README.md | 2026-02-16 |
| #9 | MVP UI (scaffolding, dashboard, agent detail pages) | 2026-02-15 |
