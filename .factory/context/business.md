# Business Context

## Product Vision

Open-source (FSL) AI agent orchestration platform. Agents presented as
"virtual employees" -- understandable for non-technical business owners.

**One-liner:** "Crewship is a Linux machine where AI employees work. You give
them instructions, credentials and skills. They work 24/7 and you download
the results in the morning."

## Target Market

- Small businesses (20-50 people) -- primary ("Franta" persona)
- Mid-size companies (50-500) -- secondary
- Enterprise/corporate (500+) -- Phase 3+

## Licence: FSL (Functional Source License)

- Code publicly visible on GitHub
- Self-hosting free for own business use
- Non-compete: no one can create competing SaaS
- Converts to Apache 2.0 after 2 years
- Skills/Plugins: MIT (open ecosystem)
- Precedent: Sentry, GitButler, Codecov

## Pricing

TBD -- will be determined based on market feedback and MVP validation.

## Key Differentiators

1. **"Human" terminology** -- team, employee, skill, credential (not agent, orchestrator, MCP)
2. **Enterprise-ready from day 1** -- RBAC, audit log, encrypted credentials, container isolation
3. **Open-source + self-hosted** -- Supabase business model
4. **Skills marketplace** -- modular, community-driven ecosystem
5. **Terminal-first** -- CLI agents, structured logs, real file output
6. **Webhook-driven** -- agents react to external events (alerts, triggers, other systems)
7. **Two-language architecture** -- TypeScript UI + Go backend = efficient, K8s-native
8. **3-level AI hierarchy** -- Virtual Director → Crew Leader → Workers. User talks to the
   team leader, not individual agents. Natural for business users ("talk to department head").

## Orchestration Differentiator (vs CrewAI, LangGraph, AutoGen)

```
CrewAI / LangGraph / AutoGen  = FRAMEWORKS for DEVELOPERS (Python code-first)
Crewship                      = PLATFORM for BUSINESS USERS (UI-first, human terminology)
```

| Aspect | CrewAI | LangGraph | AutoGen | **Crewship** |
|---|---|---|---|---|
| Pattern | Hierarchical Crew | Graph/DAG | Conversation | **3-level hierarchy** |
| Target | Developers | Developers | Developers | **Business users** |
| Leader concept | Manager Agent (code) | Supervisor node (code) | GroupChat manager | **Crew Leader (UI, auto-prompt)** |
| Cross-team | Nested crews | Sub-graphs | Nested groups | **Virtual Director** |
| Configuration | Python code | Python code | Python code | **Web UI + auto-generated prompts** |
| Security | No isolation | No isolation | No isolation | **Container + RBAC + audit** |

> Full spec: `.factory/context/prd/ORCHESTRATION.md`

## Market Positioning: Crewship vs n8n/Make/Zapier

```
n8n / Make / Zapier  = HANDS (do step A, then B, then C -- brittle workflows)
Crewship             = BRAIN (analyze situation, decide, act -- autonomous agents)
```

**We cooperate, not compete:**
- n8n sends webhook → Crewship agent analyzes and acts
- Make detects event → triggers Crewship agent
- Zapier collects data → Crewship agent processes it

**Key difference:** n8n workflows break when API changes. Crewship agents adapt --
they have instructions, not rigid workflows. Agent figures out what to do.

### Example: SRE Agent

```
1. Grafana alert: "CPU > 95% on production"
2. Grafana sends webhook to Crewship
3. SRE agent wakes up, SSH into server
4. Analyzes logs, finds memory leak in service X
5. Restarts service X
6. Writes incident report to /output/
7. Sends Slack notification
8. Morning: SRE engineer reads report, agent already fixed it
```

This is what n8n CANNOT do. n8n can "if CPU > 95%, restart X". But it cannot
analyze WHY, decide WHAT to do, and write a report.

### Example: Dentist Office

```
1. Dentist runs Crewship instance, connected to calendar system
2. Patient (Pavel) has his own Crewship instance with personal assistant agent
3. Pavel tells agent: "Book me a dentist appointment"
4. Pavel's agent sends webhook to dentist's Crewship
5. Dentist's agent checks calendar, finds available slot
6. Agents negotiate via webhooks, confirm appointment
7. Both systems update their calendars
```

### Example: DevOps Agent

```
1. CI/CD pipeline fails (GitHub Actions webhook)
2. DevOps agent analyzes build logs
3. Finds dependency conflict, fixes package.json
4. Creates PR with fix
5. Writes analysis to /output/
```

## Competition

| Platform | Type | Our advantage |
|---|---|---|
| OpenClaw | Personal AI assistant | No RBAC, no audit, no teams, not for business |
| n8n | Workflow automation | Rigid workflows, needs developer, no autonomy |
| CrewAI | Python framework | Developer-only, no UI for business users |
| Relevance AI | Closed SaaS | Expensive, no self-hosting, vendor lock-in |
| Zapier Agents | SaaS agents | Limited autonomy, no container isolation |
| Docker cagent | Agent runtime (open-source, 2026) | Framework, no UI, no RBAC, no teams |

### OpenClaw vs Crewship — detailni porovnani

> **Kompletni analyza:** `.factory/context/STRATEGY-2026.md`
>
> OpenClaw (157k+ GitHub stars, MIT, Feb 2026) je open-source personal AI assistant
> od Petera Steinbergera. Bezi lokalne na desktopu uzivatele, pripojuje se na messaging
> apps (WhatsApp, Telegram, Discord, Slack, Signal), ma 700+ skills v ClawHub marketplace.

**Positioning:**
```
OpenClaw  = PERSONAL (single-user, bezi na desktopu, bez RBAC/audit)
Crewship  = BUSINESS + PERSONAL (multi-tenant, container isolation, RBAC, audit, self-hosted)
```

**Bezpecnostni krize OpenClaw (unor 2026):**

OpenClaw prosla masivni bezpecnostni krizi. Klicove incidenty:
- **CVE-2026-25253** (CVSS 8.8) -- one-click RCE pres malicious link
- **42,900 exposed instanci** na internetu (Shodan), 15,200 zranitelnych na RCE
- **341+ malicious skills** na ClawHub (Atomic Stealer malware, koordinovany utok)
- **20% vsech skills** obsahuje malware, **36% ma prompt injection** zranitelnosti
- **Credentials v plaintext** -- Moltbook leakl 32,000 agent credentials
- Varovani od: **Kaspersky, MITRE ATLAS, Snyk, Cisco, Koi Security**

Community reakce: "Anatomy of a Dumpster Fire" (Medium), "Not ready for serious work" (HN).

**Detailni feature porovnani:**

| Oblast | OpenClaw | Crewship | Vyhoda |
|---|---|---|---|
| Cilovy uzivatel | Jednotlivec (developer/power user) | Kdokoliv (solo dev az firma 500 lidi) | Crewship: sirsi trh |
| Instalace | npm + config + messaging setup | `brew install crewship && crewship start` | Crewship: jednodussi |
| Security | Bezi NA HOSTU (zadny sandbox!) | Docker kontejner, non-root, --internal | Crewship: bezpecnejsi |
| Credentials | Config soubor (plaintext!) | AES-256-GCM + key versioning | Crewship: sifrovane |
| API key management | 1 klic per provider | Credential pool (multi-key, failover) | Crewship: enterprise |
| RBAC | Zadne (single user) | 5 roli, per-team izolace | Crewship: enterprise |
| Audit | Zadny | Immutable, append-only, queryable | Crewship: compliance |
| Multi-tenant | 1 instance = 1 uzivatel | 1 instance = cela firma | Crewship: efektivnejsi |
| UI | ZADNE vlastni UI (messaging-only) | Full web dashboard (chat, files, logs, settings) | Crewship |
| Orchestrace | ZADNA (flat, 1 agent) | CEO → Lidr → Worker hierarchie | Crewship |
| Cost control | ZADNY ($500-750/mesic bez limitu) | Per-agent budgety, alerting | Crewship |
| Network control | ZADNY (full internet access) | Per-agent: internet ON/OFF, whitelist, VPN | Crewship |
| Skills bezpecnost | 20% malware, zadny sandbox | Sandboxed, curated, permissions model | Crewship |
| Messaging | 50+ platform (WhatsApp, Telegram...) | Web UI (MVP), messaging Phase 2 | OpenClaw: vice kanalu |
| Persistent memory | Across sessions | /workspace/.claude/, agent memory | Srovnatelne |
| Always-on | Daemon na desktopu | Agent loop mode + webhooky | Crewship: silnejsi triggery |
| Desktop access | Clipboard, Finder, System Prefs | Kontejner only (bezpecnejsi) | Trade-off |
| Cena | Free (BYOK) + $500+/m API | Free (BYOK) + Team $15-30/user + Enterprise | Crewship: transparentnejsi |
| Community | 157k+ stars, massive hype | Novy projekt | OpenClaw: vetsi komunita |
| Codebase | 430,000+ LOC | ~15,000 LOC | Crewship: cistsi, auditovatelnejsi |

**Co komunita na OpenClaw postrada (a Crewship resi):**
1. Sandboxing/isolation → Docker kontejnery
2. Cost control → per-agent budgety
3. Team/org support → multi-user RBAC
4. Jednoduchy setup → single binary (`brew install crewship`)
5. Audit trail → append-only log
6. Vetted skills → curated marketplace se sandbox enforcement
7. Visual orchestration → dashboard + hierarchie
8. Vlastni UI → full web dashboard

**Co se muzeme naucit od OpenClaw:**
- Skills format (markdown + YAML metadata) — kompatibilni format
- Always-on daemon koncept → agent loop mode
- Messaging-first UX → Phase 2 messaging integrace
- Community growth (README, examples, quick start) → marketing strategie
- Viralni moment (jednoduchy "wow" prikaz) → `brew install crewship`

### Docker cagent vs Crewship

> Docker cagent (open-source, 2026) je agent builder/runtime od Dockeru.
> YAML-driven agenti, multi-agent orchestrace, Docker sandbox, MCP tool server.
> Zaměření: vývojáři staví agenty, ne firemní platforma.

**Positioning:**
```
Docker cagent = FRAMEWORK (developer tool, YAML agents, runtime)
Crewship      = PLATFORM (business UI, RBAC, audit, teams, credentials vault)
```

| Oblast | Docker cagent | Crewship | Výhoda |
|---|---|---|---|
| Cílový uživatel | Vývojář (YAML definice) | Firma (web UI) | Crewship: širší trh |
| Agent definice | YAML (deklarativní) | UI + API (konfigurovatelné) | Crewship: ne-dev friendly |
| Multi-agent | Ano (agent→agent handoff) | Leader/Worker/Director hierarchie | Crewship: strukturovanější |
| RBAC | Žádné | 5 rolí, per-team izolace | Crewship: enterprise |
| Audit trail | Žádný | Immutable, append-only | Crewship: compliance |
| Credentials | Config/env vars | AES-256-GCM, credential pool, failover | Crewship: bezpečnější |
| UI | CLI/SDK only | Full web UI (chat, files, logs, settings) | Crewship: UX |
| Container isolation | Docker sandbox (vlastní) | Docker + Landlock + optional gVisor | Crewship: hlubší izolace |
| MCP tools | Ano (tool server) | Skill system (MCP-kompatibilní, Phase 2) | Srovnatelné |
| Webhooks | Omezené | Native webhook triggers | Crewship: silnější |

**Co se můžeme naučit od Docker cagent:**
- Agent builder UX — YAML definice jako inspirace pro "agent templates"
- MCP tool server — standardní tool interface
- Container orchestrace patterny — sidecar, multi-agent routing
- Docker sandbox approach — bezpečnostní patterny

## MVP Phases

- Phase 1 (6-8w): Teams, agents, skills, chat UI, credentials vault, file browser, webhooks
- Phase 2 (+4-6w): Scheduled tasks, multi-agent orchestration, cost tracking, cloud sync skills
- Phase 3 (+6-8w): K8s isolation, RAG, skills marketplace, SSO/SAML, approval flows

## Brand

- **Name:** Crewship (Crew + Ship + "-ship" suffix)
- **Domain:** crewship.ai
- **GitHub:** github.com/crewship-ai
- **npm:** @crewship/*
- **Twitter/X:** @crewshipai
- **Container registry:** ghcr.io/crewship-ai/*
