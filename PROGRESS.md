# Crewship -- Progress Tracker

**Posledni aktualizace:** 2026-02-17
**Architektura:** Single Go binary s embedded Next.js static export, SQLite default DB.

---

## Legenda

- [x] Hotovo a otestovano
- [~] Castecne (existuje, ale ne kompletni)
- [ ] Nezacato

---

## CORE: Co funguje (otestovano, v main)

### Single Binary Architecture ✅

- [x] Go binary s embedded Next.js static export (`embed.FS`)
- [x] `crewship start` -- spusti HTTP server, SQLite DB, embedded UI (port 8080)
- [x] `crewship version` -- verze, commit, build date, OS/arch
- [x] `crewship doctor` -- diagnostika (Docker, data dir, DB)
- [x] `crewship start --no-docker` -- dashboard-only mode bez Dockeru
- [x] Docker check pri startu (odmitne bez Dockeru, jasna chybova hlaska)
- [x] SQLite jako default DB (pure-Go `modernc.org/sqlite`, WAL mode, 20-table migration)
- [x] Data dir: `~/.crewship/` (DB, output, logs, chats)
- [x] SPA routing (vsechny cesty vcetne dynamickych jako `/agents/x/chat`)
- [x] Dev environment: `./dev.sh start` (Go :8080 + Next.js :3001 s HMR)

### Auth ✅

- [x] Signup (bcrypt hash, auto-create workspace + membership)
- [x] Login (NextAuth-compatible callback, CSRF cookie validace)
- [x] Session (JWT/JWE decode, user info)
- [x] Signout (token invalidation)
- [x] JWT auth middleware na vsech API endpointech
- [x] RBAC: 5 roli (Owner/Admin/Manager/Member/Viewer)
- [x] Workspace context middleware (workspace membership check)

### REST API (50+ routes v Go) ✅

- [x] Workspaces: CRUD + members + invitations
- [x] Crews: CRUD + members
- [x] Agents: CRUD + skills/credentials/chats/runs sub-resources
- [x] Credentials: CRUD s AES-256-GCM sifrovani
- [x] Skills: list s filtry
- [x] Runs: list s paginaci + stats
- [x] Audit: list s filtry
- [x] Admin: stats, users, workspaces (OWNER only)
- [x] Internal routes: credentials decrypt, chat create/resolve (token auth)
- [x] Proxy routes: agent debug/files/logs/stop (IPC forwarding)
- [x] Health endpoint: `GET /api/health`

### Agent Execution (Docker) ✅

- [x] Docker provider (EnsureCrewRuntime, Exec, Stop, Remove, Status)
- [x] Container lifecycle (create per crew, non-root UID 1001, `--internal` network)
- [x] Docker exec (spusteni CLI session v kontejneru)
- [x] CLI adaptery: Claude Code, Codex CLI, Gemini CLI, OpenCode
- [x] Claude Code stream-json parsing (token-level streaming)
- [x] Credential ENV injection (priority-based failover)
- [x] Claude OAuth credential file injection (`.credentials.json` + `.claude.json`)
- [x] Agent workspace + output directory creation
- [x] Conversation context injection (posledních 10 zprav do system promptu)
- [x] Run state tracking (bbolt KV: running/completed/error)
- [x] Crash recovery (RecoverFromCrash -- stale run cleanup)
- [x] Cooldown manager (credential rotation pri rate limiting)

### WebSocket Chat ✅

- [x] WebSocket gateway (goroutines, hub pattern, channel pub/sub, ping/pong)
- [x] JWT auth na WebSocket (NextAuth JWE dekrypce)
- [x] ChatBridge: WS → conversation store → orchestrator → Docker exec → stream zpet
- [x] JSONL conversation persistence (one file per session)
- [x] WS token endpoint (`GET /api/v1/ws-token`)

### Security ✅

- [x] AES-256-GCM credential encryption (key versioning `v1:base64data`)
- [x] RBAC check na KAZDEM API endpointu
- [x] CSRF token validace s cookie (login flow)
- [x] Constant-time token comparison (internal auth)
- [x] Path traversal ochrana (file download routes)
- [x] Non-root kontejnery (UID 1001)
- [x] `--internal` Docker network (zadny internet pro agenty)
- [x] Audit log (DB tabulka, queryable)
- [x] Webhook HMAC validace (per-agent secret)

### Frontend (Next.js static export) ✅

- [x] Dashboard (stat karty, agent list, filter bar)
- [x] Agent list + detail (overview, chat, sessions, files, runs, logs, settings, skills, credentials)
- [x] Crew list + detail + members management
- [x] Credentials management (add/edit/delete, 3 typy: AI CLI Token/API Key/Secret)
- [x] Skills list s filtry
- [x] Audit log tabulka
- [x] Runs page (globalni across agents)
- [x] Admin console (stats, users, workspaces)
- [x] Settings (workspace name, members, danger zone)
- [x] Login + Signup stranky
- [x] 3-layer layout (toolbar + sidebar + main)
- [x] Real-time WS chat s streaming messages

### CI/CD ✅

- [x] GitHub Actions CI: lint, typecheck, build (Next.js), Go vet, Go build, Go test, pnpm test
- [x] GoReleaser config (`.goreleaser.yml`: darwin/linux/windows x amd64/arm64)
- [x] Release workflow (`.github/workflows/release.yml`: tag-triggered, builds + publishes)
- [x] Homebrew formula (v `.goreleaser.yml`, ceka na `crewship-ai/homebrew-tap` repo)
- [x] Production Dockerfile (multi-stage: frontend build → Go build → Alpine runner)
- [x] docker-compose.prod.yml
- [x] Dependabot (npm weekly, Go weekly, GHA monthly)
- [x] CodeRabbit review na PR

### Testy ✅

- [x] Go: 18 test packages, vsechny passing (api, auth, chatbridge, config, conversation, database, encryption, fileserver, llmproxy, logcollector, logging, orchestrator, bbolt, docker, localfs, server, webhook, ws)
- [x] TypeScript: 92 testu v 8 souborech (store, slugify, cn, crewshipd-client, encryption, abilities, use-chat, validations)
- [x] E2E integration test: 25 test cases (SPA routing, auth flow, CRUD, credentials encryption)

---

## EPIC: Onboarding Flow

> **Cil:** Novy uzivatel od `crewship start` k prvnimu chat s agentem za < 60 sekund.
> **Priorita:** P0 (bez onboardingu uzivatel neví co delat, bounce rate bude vysoka).
> **Ref:** PRD ONBOARD-01..05, STRATEGY Section 14, Section 11 metrika "< 5 min to first agent".

### Soucasny stav

- [x] Signup vytvori ucet + auto-workspace (jmeno, slug, OWNER role)
- [x] Login s CSRF cookie validaci
- [x] Dashboard s empty state ("No agents yet" + "Create First Agent" button)
- [ ] Zadny guided wizard po prvnim prihlaseni
- [ ] Zadny onboarding state tracking (nevime jestli uzivatel uz proslo wizardem)
- [ ] Zadne crew blueprinty / sablony
- [ ] Zadne agent templates (predkonfigurovane role)

### Faze 1: Guided Wizard (P0 -- MVP)

> Krok-po-kroku wizard po prvnim prihlaseni. Uzivatel je proveden celym setupem.
> Inspirace: PRD Section 14 (10-step flow).

#### Backend (Go API)

- [ ] **ONBOARD-B01** -- Onboarding state v DB: `users.onboarding_completed` boolean (migrace)
- [ ] **ONBOARD-B02** -- `GET /api/v1/onboarding/status` -- vraci stav onboardingu (completed, current_step)
- [ ] **ONBOARD-B03** -- `POST /api/v1/onboarding/complete` -- oznaci onboarding jako dokonceny
- [ ] **ONBOARD-B04** -- Quick-create endpoint: `POST /api/v1/onboarding/setup` -- atomicky vytvori crew + agent + priradi credential (1 API call misto 3)

#### Frontend (Next.js)

- [ ] **ONBOARD-F01** -- Route `/onboarding` s multi-step wizard komponentou
- [ ] **ONBOARD-F02** -- Redirect logika: po loginu check onboarding_completed → redirect na /onboarding nebo /dashboard
- [ ] **ONBOARD-F03** -- Step 1: Welcome screen ("Vitejte v Crewship! Pojdme vytvorit vas prvni workspace.") -- workspace uz existuje, jen pojmenovani/potvrzeni
- [ ] **ONBOARD-F04** -- Step 2: Crew creation (nazev + popis, nebo vyber ze sablony "DevOps", "Support", "Research")
- [ ] **ONBOARD-F05** -- Step 3: Agent creation (jmeno, role, vyber CLI adapteru + LLM modelu)
- [ ] **ONBOARD-F06** -- Step 4: Credential setup (API klic pro zvoleny LLM -- formular s masked inputem)
- [ ] **ONBOARD-F07** -- Step 5: Success screen + "Start your first chat" CTA → redirect na agent chat
- [ ] **ONBOARD-F08** -- Progress indicator (stepper/breadcrumb v horni casti wizardu)
- [ ] **ONBOARD-F09** -- Skip moznost (pro zkusene uzivatele -- "I'll set up manually" link)
- [ ] **ONBOARD-F10** -- Responsive design (mobile-first, md: breakpoints)

#### UX detaily

- Wizard je fullscreen overlay (ne v dashboard layoutu) -- ciste, bez distrakce
- Kazdy step ma ilustraci/ikonu, kratky popis co delame a proc
- Validace na kazdem kroku pred posunem dal (nazev crew, API klic format)
- Po dokonceni wizardu: confetti/success animace + presmerovani do chatu s agentem
- "Back" tlacitko na kazdem kroku (krome Step 1)

### Faze 2: Crew Blueprinty (P1)

> Predkonfigurovane sablony crew s agenty, skills, prompty. Vyber v onboarding wizardu.

- [ ] **ONBOARD-BP01** -- Blueprint YAML format (viz PRD Section 15): crews, agents, skills, prompty
- [ ] **ONBOARD-BP02** -- 3-5 bundled blueprintů: "Solo Developer", "DevOps Team", "Customer Support", "Research Crew", "Marketing Agency"
- [ ] **ONBOARD-BP03** -- Blueprint picker UI v onboarding Step 2 (karty s preview: kolik agentu, jake skills)
- [ ] **ONBOARD-BP04** -- `POST /api/v1/blueprints/apply` -- aplikuje blueprint (vytvori crew + agenty + priradi skills)
- [ ] **ONBOARD-BP05** -- Blueprint detail modal (popis, seznam agentu, screenshot)

### Faze 3: Empty State Upgrade (P1)

> Vylepseni empty states na dashboardu pro uzivatele co skipnou wizard.

- [ ] **ONBOARD-ES01** -- Dashboard empty state s onboarding checklistem (progress bar: "3 of 5 steps done")
- [ ] **ONBOARD-ES02** -- Checklist items: Create crew, Add agent, Add credential, Send first message, Explore skills
- [ ] **ONBOARD-ES03** -- Kazdy checklist item je klikatelny → naviguje na prislusnou stranku
- [ ] **ONBOARD-ES04** -- Po splneni vsech kroku: checklist zmizi, zobrazi se normalni dashboard
- [ ] **ONBOARD-ES05** -- Tooltip hints na sidebar polozkach pro noveho uzivatele ("Start here" badge)

### Faze 4: Crewship AI Onboarding (P2 -- Phase 2 roadmap)

> Konverzacni onboarding pres meta-agenta. Uzivatel popisuje potreby, AI navrrhne strukturu.

- [ ] **ONBOARD-AI01** -- Crewship AI meta-agent chat interface
- [ ] **ONBOARD-AI02** -- AI zna celou platformu (crews, skills, network policies, credentials)
- [ ] **ONBOARD-AI03** -- AI navrhne crew strukturu na zaklade popisu uzivatele
- [ ] **ONBOARD-AI04** -- One-click aplikace navrhu (AI vola API pro vytvoreni crew/agentu)

### Metriky uspechu

| Metrika | Cil |
|---|---|
| Cas signup → prvni chat | < 60 sekund (wizard path) |
| Wizard completion rate | > 80% (kdo zacne, dokonci) |
| Wizard skip rate | < 15% |
| First-day retention | > 60% (vrati se do 24h) |
| Onboarding-to-active | > 50% (posle alespon 5 zprav agentovi) |

### Technicke poznamky

- Wizard state (current step) ulozen v `localStorage` (ne v DB -- jednodussi, neni treba sync)
- `onboarding_completed` flag v DB -- jediny persistent stav (pro redirect logiku)
- Wizard pouziva shadcn/ui komponenty (Card, Button, Input, Select, Progress)
- Stepper komponenta: bud shadcn/ui Stepper (pokud existuje) nebo vlastni z Progress + Badge
- Vsechny API calls pres existujici endpointy (crews, agents, credentials) + novy quick-create

---

## CO CHYBI pro real launch

### P0: MUST HAVE (bez toho to nejede)

- [ ] **Agent runtime Docker image** -- `docker/agent-runtime/Dockerfile` s Claude Code, Node.js, git, jq. Bez nej agent container nema co spustit.
- [ ] **Homebrew tap repo** -- vytvorit `crewship-ai/homebrew-tap` na GitHubu + nastavit `HOMEBREW_TAP_TOKEN` secret
- [ ] **Tagged release** -- `git tag v0.1.0 && git push github v0.1.0` → GoReleaser vytvori GitHub Release + binaries
- [~] **Onboarding flow** -- signup vytvori workspace, ale neni dedicated wizard pro prvni agenta (viz EPIC: Onboarding Flow vyse)

### P1: SHOULD HAVE (pro rozumne demo)

- [ ] **Container TTL** -- auto-stop kontejneru po neaktivite (setri resources)
- [ ] **Container resource limits** -- memory + CPU per crew (uz je v ContainerProvider, neni enforced)
- [ ] **Real-time log streaming** -- WebSocket (logcollector existuje, neni napojeny na WS broadcast)
- [ ] **Real-time agent status** -- WebSocket broadcast zmeny stavu (idle→running→stopped)
- [ ] **Webhook → orchestrator** -- webhook handler existuje, ale neni napojen na RunAgent

### P2: NICE TO HAVE (ne blokuje spusteni)

- [ ] Workspace switcher funkcionalita (UI existuje, neni napojeny)
- [ ] Command palette (Cmd+K -- komponenta existuje, neni funkcni)
- [ ] Notifikacni system (bell icon existuje, zadna logika)
- [ ] Crew-scoped permissions (MANAGER vidi jen prirazene crews)
- [ ] Logrotate integrace (hodinova rotace, gzip)
- [ ] Advanced audit filtry (date range, user picker)
- [ ] Skill detail stranka
- [ ] Google OAuth (disabled button existuje)
- [ ] `crewship stop` / `crewship status` / `crewship logs` CLI commands

---

## PLANOVANE FEATURES (neexistuje, neni implementovano)

> Viz STRATEGY-2026.md Section 0 pro detailni prehled.

### Faze 1: Open Source Launch

- [ ] Skill marketplace (Skill Store UI, browse/install/uninstall)
- [ ] 15-20 official skills s permissions modelem (skill.yaml format)
- [ ] Skill sandbox enforcement (deklarovane permissions, Docker enforcement)
- [ ] Per-agent network policies UI (internet on/off, whitelist domen)
- [ ] Per-agent cost budgety a alerting
- [ ] LLM API allowlist (granularni iptables, ne jen --internal)
- [ ] Onboarding wizard (prvni spusteni → running agent za 60 sekund)
- [ ] Install script (`curl -fsSL https://get.crewship.ai | sh`)
- [ ] Landing page (crewship.ai)
- [ ] Auto-update (`crewship update`)

### Faze 2: Monetizace (+3-6 mesicu)

- [ ] crewship.ai cloud tier (hosted PostgreSQL, managed infra)
- [ ] Community skill marketplace + revenue sharing
- [ ] Lead orchestrace (Phase 2A -- sidecar, assignment protocol)
- [ ] Mission engine (Mission Board UI, JSONL progress, task dependencies)
- [ ] Messaging integrace (Slack, Discord)
- [ ] Stripe billing
- [ ] Usage analytics dashboard

### Faze 3: Enterprise (+6-12 mesicu)

- [ ] K8s Helm chart (GKE, EKS, AKS)
- [ ] SSO/SAML (Okta, Azure AD)
- [ ] Coordinator orchestrace (cross-crew, lightweight LLM call)
- [ ] Compliance features (audit export, retention, data residency)
- [ ] GPU node support (lokalni LLM pres Ollama)
- [ ] SOC 2 compliance

---

## Merge historie

| PR | Nazev | Datum |
|----|-------|-------|
| #27 | feat: GoReleaser, Docker enforcement, cleanup legacy code | 2026-02-17 (open) |
| #26 | refactor: SQLite + single binary + Go API + static export | 2026-02-17 |
| #25 | refactor: apply NAMING.md convention to entire codebase | 2026-02-17 |
| #24 | feat: AI Elements chat UI, agent pages upgrade, dev tooling | 2026-02-17 |
| #23 | chore: upgrade base images to latest stable | 2026-02-17 |
| #22 | feat: production deployment (Dockerfiles, Prisma migration, SessionResolver) | 2026-02-16 |
| #21 | feat: E2E chat flow (JWT auth, conversation store, ChatBridge, Chat UI) | 2026-02-16 |
| #19 | feat: Docker runtime (providers, orchestrator, log collector, file server, webhook) | 2026-02-16 |
| #18 | feat: frontend polish (crew detail, runs, admin console, RBAC, invite) | 2026-02-16 |
| #17 | feat: Go backend foundation (config, logging, HTTP, IPC, WebSocket, providers) | 2026-02-16 |
| #16 | feat: complete MVP frontend (auth, API, forms, tests, seed) | 2026-02-16 |
| #15 | docs: README.md | 2026-02-16 |
| #9 | feat: MVP UI (scaffolding, dashboard, agent detail pages) | 2026-02-15 |

---

## Souhrnne statistiky

| Oblast | Stav |
|--------|------|
| Single binary | ✅ 30MB, funguje, SQLite + embedded UI |
| Auth flow | ✅ signup/login/session/signout + CSRF + JWT |
| REST API | ✅ 50+ routes, RBAC, tested |
| Agent execution | ✅ Docker exec, CLI adaptery, credential injection |
| WebSocket chat | ✅ real-time streaming, conversation persistence |
| Security | ✅ AES-256-GCM, RBAC, CSRF, container isolation |
| Frontend | ✅ all pages, real-time chat, responsive |
| CI/CD | ✅ lint/typecheck/build/test + GoReleaser + Dockerfile |
| Go testy | ✅ 18 packages passing |
| TS testy | ✅ 92 tests passing |
| Distribution | 🟡 GoReleaser ready, needs tap repo + first tagged release |
| Agent runtime image | ❌ Docker image for agent containers not built yet |
| Skill system | ❌ no marketplace, no skills, no sandbox |
| Network policies | ❌ --internal only, no granular allowlist |
| Cost control | ❌ not implemented |
| Orchestrace | ❌ DB schema only, no runtime multi-agent logic |
