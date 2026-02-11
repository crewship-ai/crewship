# Crewship -- Product Requirements Document (PRD)

**Verze:** 1.1
**Datum:** 2026-02-11
**Autor:** Pavel Srba + AI analyza
**Status:** Draft

> **⚠️ POZOR: Sekce 8 (Technologicke rozhodnuti) a 9 (Advine Reuse) obsahuji stale reference
> na BullMQ, Redis, ws, pino, Supabase Auth, Vercel, Railway. Tyto technologie NEPOUZIVAME.
> Pro aktualni architekturu viz: AGENTS.md, architecture.md, tech-stack.md a PRD dokumenty v2.0
> (DATABASE.md, SECURITY.md, API.md, DEPLOYMENT.md, AGENT-RUNTIME.md).
> Tento soubor pouzivej POUZE jako feature list (sekce 4: feature IDs).**

---

## 1. VIZE PRODUKTU

### Co stavime
Open-source (FSL licence) platforma pro orchestraci AI agentu prezentovanych jako **"virtualni zamestnanci"**. Cil: umoznit firmam vytvorit virtualni oddeleni (obchod, vyvoj, zakaznicka podpora) kde AI agenti plni ulohy autonomne nebo v konverzaci s lidskymi kolegy.

### Pracovni nazev
**Crewship** (cesky "vcelin" -- misto kde vceli kolonie spolupracuji). Finalni nazev TBD.

### Elevator pitch
> "Crewship je open-source platforma kde vytvorite virtualni firmu. Misto najimani lidi pridavate AI zamestnance do tymu, date jim dovednosti a opravneni, a oni pracuji -- odpovidaji na emaily, generuji reporty, monitoruji systemy. Vsechno pod vasim dohledem, s plnym audit logem a enterprise zabezpecenim."

### Klicova diferenciace od konkurence
1. **"Lidska" terminologie** -- tym, zamestnanec, dovednost, opravneni. Ne agent, orchestrator, MCP.
2. **Enterprise-ready od dne 1** -- RBAC, audit log, sifrovane credentials, RLS izolace.
3. **Open-source + self-hosted** -- FSL licence (→ Apache 2.0 po 2 letech).
4. **Skills marketplace** -- modularni, community-driven ekosystem.
5. **Terminal-first** -- CLI agenti (Claude Code, OpenCode, Codex CLI, Gemini CLI), strukturovane logy.
6. **Crewship AI (meta-agent)** -- AI ktery pomaha vytvoret AI tym ("stroj co obrabi stroj").

### Architekturni vzor: inspirace OpenClaw
Crewship je architektonicky inspirovana projektem **OpenClaw** (163k+ GitHub stars, MIT licence).
OpenClaw je single-user personal AI assistant s Gateway daemonem, workspace konceptem a tool systemem.
Crewship prebira tyto vzory a PRIDAVA multi-tenancy, RBAC, enterprise security a web dashboard.

| Koncept | OpenClaw | Crewship |
|---|---|---|
| Runtime | Single host, single user | Multi-tenant, per-team izolace |
| Gateway | Jeden daemon, localhost | Multi-tenant WS server, cloud |
| Workspace | `AGENTS.md`, `SOUL.md`, `TOOLS.md`, `IDENTITY.md` | Stejny format (kompatibilita) |
| Skills | Lokalni soubory v workspace | Marketplace + lokalni + bundled |
| Tools | exec, read/write, web_search, browser, message | Stejne + enterprise tools |
| Multi-agent | sessions_send, sessions_spawn | Stejny pattern pro orchestraci |
| Channels | WhatsApp, Telegram, Discord, Slack, Signal | Stejne kanaly (Phase 2) |
| Security | Token auth, single user | RBAC, RLS, encrypted vault, audit |
| Billing | Zadny | Stripe subscription + BYOK |

---

## 2. CILOVI UZIVATELE (PERSONY)

### Persona 1: "Pepa" -- Majitel male firmy
- **Profil:** 35-55 let, firma 20-50 lidi, netechnicky
- **Potreba:** Chce automatizovat rutinni prace (emaily, reporty, CRM) bez najimani dalsich lidi
- **Bolest:** Nerozumi pojmum jako MCP, orchestrator, prompt engineering
- **Jak ho ziskame:** "Lidska" terminologie, Crewship AI (meta-agent) ho provede setupem
- **Tier:** Pro/Team (placeny cloud)

### Persona 2: "Marek" -- IT Admin / CTO
- **Profil:** 28-40 let, technicky zamereny, spravuje infrastrukturu firmy
- **Potreba:** Orchestrovat AI agenty pro DevOps, monitoring, deployment
- **Bolest:** Existujici nastroje jsou bud prilis jednoduche (Zapier) nebo prilis slozite (LangGraph)
- **Jak ho ziskame:** Open-source, self-hosting, REST API, skills system
- **Tier:** Team/Enterprise nebo self-hosted

### Persona 3: "Jana" -- Freelance vyvojar
- **Profil:** 25-35 let, vyvojarka, buduje projekty pro klienty
- **Potreba:** Rychle protypovat AI automatizace pro klienty
- **Bolest:** CrewAI/AutoGen vyzaduji hodne kodu, zadne UI pro klienta
- **Jak ho ziskame:** Free tier, snadny setup, sablony oddeleni
- **Tier:** Community (free) → Pro

---

## 3. TERMINOLOGIE

| UI termin (anglicky) | UI termin (cesky) | Kod | Popis |
|---|---|---|---|
| Company | Firma | Organization | Multi-tenant root entita |
| Department / Team | Oddeleni / Tym | Team | Izolacni boundary, kontejner |
| Virtual Employee | Virtualni zamestnanec | Agent | CLI session s LLM, skilly, credentials |
| Skill | Dovednost | Skill | Balicek nastroju + MCP + skripty |
| Permission / Credential | Opravneni | Credential | Sifrovany API klic v trezoru |
| Department Head | Sef oddeleni | OrchestratorAgent | Deleguje praci na worker agenty |
| Task | Ukol | AgentRun | Jednotlivy beh agenta |
| Conversation | Konverzace | ConversationSession | Chat session s agentem |

---

## 4. FEATURE LIST -- ROZDELENI DO FAZI

### LEGENDA PRIORIT
- **P0** = Must have (MVP se bez toho nespusti)
- **P1** = Should have (dulezite, ale MVP prezije bez toho)
- **P2** = Nice to have (odlozeno na dalsi fazi)
- **P3** = Future (vize, ne plan)

---

### PHASE 1: MVP (8-12 tydnu, solo dev + AI)

**Cil:** Funkcni platforma kde uzivatel vytvori firmu, tym, agenta, prida mu skilly a credentials, spusti ho a vidi konverzaci v chat UI.

#### 4.1 Autentizace a uzivatelsky ucet [P0]

> **MVP: Jen NextAuth.js (Auth.js v5)** s Prisma adapterem. Supabase Auth az Phase 2.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| AUTH-01 | Email + heslo prihlaseni | P0 | NextAuth.js s Prisma adapterem |
| AUTH-02 | Google OAuth | P0 | OAuth pres Google (reuse Advine kodu) |
| AUTH-03 | GitHub OAuth | P1 | OAuth pres GitHub (dev komunita) |
| AUTH-04 | Logout + session management | P0 | NextAuth.js session (JWT nebo DB) |
| AUTH-05 | Zapomenute heslo | P0 | Reset hesla pres email (Resend) |
| AUTH-06 | Magic link prihlaseni | P2 | Supabase Auth az Phase 2 (cloud only) |

#### 4.2 Organizace (Firma) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ORG-01 | Vytvoreni organizace | P0 | Novy uzivatel vytvori firmu (nazev, slug) |
| ORG-02 | Nastaveni organizace | P0 | Editace nazvu, popisu, loga |
| ORG-03 | Pozvani clenu | P0 | Pozvanka emailem + RBAC role |
| ORG-04 | Sprava clenu | P0 | Zmena role, odebreni clena |
| ORG-05 | RBAC role | P0 | Owner, Admin, Manager, Member, Viewer |
| ORG-06 | Smazani organizace | P1 | Soft delete + grace period (GDPR) |

#### 4.3 Tymy (Oddeleni) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| TEAM-01 | Vytvoreni tymu | P0 | Nazev, popis, barva/ikona |
| TEAM-02 | Prirazeni clenu do tymu | P0 | Manager priradi uzivatele do tymu |
| TEAM-03 | Seznam tymu | P0 | Dashboard s prehledem tymu |
| TEAM-04 | Editace tymu | P0 | Zmena nazvu, popisu, clenu |
| TEAM-05 | Smazani tymu | P1 | Soft delete (agenti se deaktivuji) |
| TEAM-06 | Sablony tymu | P1 | Preddefinovane sablony ("IT Firma", "Obchod", "Podpora") |

#### 4.4 Agenti (Virtualni zamestnanci) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| AGENT-01 | Vytvoreni agenta | P0 | Jmeno, popis, role, prirazeni do tymu |
| AGENT-02 | Konfigurace agenta | P0 | CLI adapter (Claude Code/OpenCode/Codex/Gemini), LLM model, system prompt, temperature, max tokens |
| AGENT-03 | Prirazeni skillu | P0 | Vyber skillu z registru a prirazeni agentovi |
| AGENT-04 | Prirazeni credentials | P0 | Vyber credentials z vaultu pro agenta |
| AGENT-05 | Spusteni agenta | P0 | Start agent session (BullMQ job) |
| AGENT-06 | Zastaveni agenta | P0 | Graceful stop + force kill |
| AGENT-07 | Status agenta | P0 | Idle, Running, Error, Stopped |
| AGENT-08 | Seznam agentu v tymu | P0 | Prehled agentu s jejich statusem |
| AGENT-09 | Timeout per agent | P0 | Max doba behu (safety) |
| AGENT-10 | Editace agenta | P0 | Zmena konfigurace |
| AGENT-11 | Smazani agenta | P1 | Soft delete |
| AGENT-12 | Sablony agentu | P1 | Preddefinovane role ("Obchodnik", "DevOps", "QA") |

#### 4.5 Skills (Dovednosti) [P0]

> **Architekturni poznamka:** Skills v Pasece jsou inspirovane OpenClaw modelem.
> Skill = soubor(y) v agent workspace, ktere definuji nastroje, navody a MCP servery.
> Agent ma pristup k built-in tools (exec, read/write, web_search, browser, message)
> a skills rozsiruja jeho schopnosti o dalsi nastroje a knowledge.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| SKILL-01 | Registr skillu | P0 | Seznam dostupnych skillu (bundled + managed + workspace) |
| SKILL-02 | Instalace skillu | P0 | Kopirovani skill souboru do workspace agenta |
| SKILL-03 | Detail skillu | P0 | Popis, credentials schema, tools, MCP info |
| SKILL-04 | Prirazeni skillu agentovi | P0 | M:N vztah agent-skill (soubory v workspace) |
| SKILL-05 | Built-in tools | P0 | exec, read/write/edit, web_search, web_fetch, browser, message |
| SKILL-06 | Tool profiles | P0 | minimal, coding, messaging, full (co agent smi pouzivat) |
| SKILL-07 | Odebrani skillu | P0 | Odebrani z workspace agenta |
| SKILL-08 | Skill format specifikace | P0 | Dokumentovany format (markdown + yaml metadata) |
| SKILL-09 | Bundled skilly (3+) | P0 | coding-assistant, web-researcher, customer-support |

#### 4.6 Credentials Vault (Trezor opravneni) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| CRED-01 | Pridani credential | P0 | Nazev, hodnota (sifruje se AES-256-GCM) |
| CRED-02 | Seznam credentials | P0 | Nazvy + masked hodnoty (***) |
| CRED-03 | Smazani credential | P0 | Permanentni smazani |
| CRED-04 | Injekce do agenta | P0 | Credential → ENV var pri startu agenta |
| CRED-05 | Scope credentials | P0 | Org-level vs Team-level credentials |
| CRED-06 | Rotace credential | P1 | Update hodnoty bez zmeny reference |
| CRED-07 | Credential pool | P1 | Vice klicu pro stejny env var s priority (round-robin/failover) |

#### 4.7 Gateway (Multi-tenant WebSocket Server) [P0]

> **Architekturni poznamka:** Gateway je multi-tenant verze OpenClaw Gateway daemonu.
> Typed WebSocket protokol: `{type:"req", method, params}` → `{type:"res", ok, payload}`.
> Routuje zpravy z web UI (a pozdeji Discord/Telegram) na agenty.
> Kazda organizace/tym ma izolovanou session.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| GW-01 | WebSocket server | P0 | Typed WS protokol (req/res/event pattern) |
| GW-02 | Multi-tenant routing | P0 | Izolace sessions per org/team |
| GW-03 | Autentizace | P0 | JWT token validace na connect |
| GW-04 | Agent events | P0 | Streaming agent outputu jako events |
| GW-05 | Health/heartbeat | P0 | Connection health monitoring |
| GW-06 | Reconnect handling | P1 | Automaticky reconnect po vypadku |

#### 4.8 Chat UI (Konverzace s agentem) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| CHAT-01 | Synchronni chat | P0 | Uzivatel pise → agent odpovida v realnem case |
| CHAT-02 | Streaming odpovedi | P0 | Block streaming pres Gateway WS events |
| CHAT-03 | Historie konverzaci | P0 | Seznam predchozich sessions (PostgreSQL = autoritativni, JSONL v workspace = sekundarni/debug) |
| CHAT-04 | Kontext konverzace | P0 | Agent vidi historii aktualniho chatu (workspace) |
| CHAT-05 | Markdown rendering | P0 | Odpovedi agenta formatovane (code, bold, lists) |
| CHAT-06 | Code block s kopirovanim | P0 | Zvyrazneni syntaxe + copy button |
| CHAT-07 | Agent status indikator | P0 | "Thinking...", "Running tool...", "Done" |
| CHAT-08 | Tool execution vizualizace | P1 | Zobrazeni jake tools agent pouziva (exec, web_search...) |

#### 4.9 Agent Runtime (Backend) [P0]

> **Architekturni poznamka:** Agent = CLI nastroj (Claude Code, Codex CLI, Gemini CLI)
> bezici jako child_process v izolovanem workspace. Platforma NEVOLA LLM API primo --
> spousti CLI nastroje ktere to delaji samy. Kazdy agent ma svuj workspace adresar
> s bootstrap soubory (AGENTS.md, SOUL.md, TOOLS.md, IDENTITY.md).
>
> **Non-interactive mody (overeno Feb 2026):**
> - Claude Code: `claude -p "prompt"` + pipe (`cat file | claude -p`)
> - OpenCode: `opencode run "prompt"` + **HTTP REST API** (`opencode serve`) -- provider-agnostic (75+ provideru)
> - Codex CLI: dedicated non-interactive mode + **JSON-RPC API** (nejrobustnejsi)
> - Gemini CLI: `gemini -p "prompt"` + "yolo" mode pro autonomni beh
>
> **Izolace v MVP:** Docker kontejner per tym (read-only root, cap-drop ALL, izolovana sit).
> **Izolace v Enterprise:** Docker kontejner per agent (dedicovane resources, vyssi bezpecnost).

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| RUN-01 | BullMQ job queue | P0 | Agent spousteni pres frontu |
| RUN-02 | CLI adapter pattern | P0 | Pluginovatelne adaptery: Claude Code, OpenCode, Codex CLI, Gemini CLI |
| RUN-03 | Agent workspace setup | P0 | Vytvoreni workspace adresare s bootstrap soubory |
| RUN-04 | CLI session management | P0 | Spusteni CLI jako child_process, lifecycle management |
| RUN-05 | Stdout/stderr streaming | P0 | Real-time output z CLI do Gateway (WebSocket events) |
| RUN-06 | Session transcripts | P0 | Dual storage: PostgreSQL (autoritativni) + JSONL v workspace (kompatibilita s OpenClaw, debug) |
| RUN-07 | Credentials injection | P0 | ENV vars z vaultu pri startu procesu |
| RUN-08 | Docker kontejner izolace | P0 | Docker per team (Community/Pro), Docker per agent (Enterprise). Read-only root, cap-drop ALL, izolована síť |
| RUN-09 | Agent error handling | P0 | Retry logika, timeout, error reporting |
| RUN-10 | Graceful shutdown | P0 | SIGTERM → SIGKILL po timeout |
| RUN-11 | Rate limit failover | P1 | Automaticke prepnuti klice pri 429 error (credential pool) |

#### 4.9 Log Viewer [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| LOG-01 | Real-time log stream | P0 | WebSocket tail logu agenta |
| LOG-02 | Filtrovani logu | P1 | Podle agenta, tymu, severity |
| LOG-03 | Hledani v lozich | P1 | Fulltext search |

#### 4.10 Dashboard [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| DASH-01 | Team overview | P0 | Pocet agentu, aktivnich tasku, posledni aktivita |
| DASH-02 | Agent status cards | P0 | Karty agentu s jejich stavem |
| DASH-03 | Quick actions | P1 | Rychle spusteni agenta, prechod do chatu |

#### 4.11 Audit Log [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| AUDIT-01 | Logovani state-changing akci | P0 | CRUD operace, spusteni agenta, zmena konfigurace |
| AUDIT-02 | Zobrazeni audit logu | P0 | Tabulka s filtry (kdo, co, kdy) |
| AUDIT-03 | Audit per organizace | P0 | Izolace logu per org |

#### 4.12 Onboarding [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ONBOARD-01 | Guided wizard (free tier) | P0 | Krok po kroku: firma → tym → agent → skill → chat |
| ONBOARD-02 | Template picker | P1 | Vyber sablony ("IT firma", "Obchodni oddeleni") |

#### 4.13 Stripe Billing [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| BILL-01 | Stripe integrace | P1 | Subscription CRUD (create, update, cancel) |
| BILL-02 | Plan tiers v DB | P1 | Free, Pro, Team, Enterprise s limity |
| BILL-03 | Enforcement limitu | P1 | Pocet agentu, tymu, skillu per plan |
| BILL-04 | Billing portal | P1 | Stripe Customer Portal (faktury, platebni metody) |
| BILL-05 | Webhook handler | P1 | Stripe eventy (payment succeeded, subscription updated) |

---

### PHASE 2: Rozsireni (8-12 tydnu po MVP)

**Cil:** Messaging kanaly, orchestrace, meta-agent, marketplace, vice LLM provideru.

#### 4.14 Asynchronni rezimy [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ASYNC-01 | Task mode | P1 | Uzivatel zada ukol, agent bezi na pozadi |
| ASYNC-02 | Task status tracking | P1 | Pending → Running → Completed/Failed |
| ASYNC-03 | Task vysledky | P1 | Zobrazeni vysledku po dokonceni |
| ASYNC-04 | Human-in-the-loop | P1 | Agent pozastavi a ceka na schvaleni nebezpecne akce |

#### 4.15 M:1 Kolaborace [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| COLLAB-01 | Sdileny chat | P1 | Vice uzivatelu pise jednomu agentovi |
| COLLAB-02 | Identifikace uzivatelu | P1 | Agent vi kdo mu pise |
| COLLAB-03 | Real-time sync | P1 | Vsichni vidi zpravy ostatnich okamzite |

#### 4.16 Orchestrace (Sef → Pracovnici) [P1]

> **Implementace:** Pouzijeme OpenClaw pattern `sessions_send` / `sessions_spawn`.
> Orchestrator agent posle zpravu worker agentovi pres session messaging.
> Worker zpracuje a vrati vysledek. Ping-pong komunikace s max turns limitem.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ORCH-01 | Orchestrator agent role | P1 | Agent oznaceny jako "sef" muze delegovat |
| ORCH-02 | sessions_send | P1 | Sef posle zpravu worker agentovi pres session |
| ORCH-03 | sessions_spawn | P1 | Sef spusti sub-agenta pro konkretni task |
| ORCH-04 | Vysledky zpet | P1 | Worker vrati vysledek sefovi (announce pattern) |
| ORCH-05 | Error handling + timeout | P1 | Co kdyz worker selze, max turns, timeout |
| ORCH-06 | agents_list | P1 | Orchestrator vidi seznam agentu ktere muze targetnout |

#### 4.17 Cron Joby [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| CRON-01 | Schedule per agent | P1 | Nastaveni cron vyrazu |
| CRON-02 | Schedule management UI | P1 | Pridani, editace, smazani schedulu |
| CRON-03 | Run history | P1 | Historie automatickych spusteni |

#### 4.17b Agent Loop Modes [P1]

> **Inspirovano:** Ralph Loop mechanismus (iterativni smycka v Claude Code).
> Agent muze bezet opakovane — monitoring, continuous tasks, completion-based loops.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| RUN-12 | Agent loop mode | P2 | Opakovany beh agenta: once (default), loop (interval), until (podminka) |
| RUN-13 | Completion criteria | P2 | Agent bezi dokud neni splnena podminka (file_exists, exit_code, custom) |
| RUN-14 | Context preservation | P2 | JSONL catch-up pri prepnuti klice/restartu — agent pokracuje kde skoncil |

#### 4.18 Vice LLM Provideru [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| LLM-01 | Anthropic (Claude) | P1 | Claude API integrace |
| LLM-02 | Ollama (local) | P1 | Lokalni LLM pro self-hosting |
| LLM-03 | LLM adapter pattern | P1 | Jednotne rozhrani pro vsechny providery |

#### 4.19 Messaging Kanaly [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| MSG-01 | Discord integrace | P1 | Agent odpovida v Discord kanalu |
| MSG-02 | Telegram integrace | P1 | Agent odpovida v Telegram chatu |
| MSG-03 | Channel adapter pattern | P1 | Jednotne rozhrani pro kanaly |

#### 4.20 Skills Marketplace [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| MARKET-01 | Browse skillu | P1 | Prohlizeni dostupnych skillu z marketplace |
| MARKET-02 | One-click install | P1 | Instalace skillu z marketplace |
| MARKET-03 | Rating/review | P2 | Hodnoceni skillu komunitou |
| MARKET-04 | Publikace skillu | P2 | Upload vlastniho skillu do marketplace |

#### 4.21 Crewship AI (Meta-agent) [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| META-01 | Konverzacni onboarding | P1 | AI se pta co uzivatel potrebuje a nastavuje |
| META-02 | Znalost architektury | P1 | Meta-agent zna celou platformu |
| META-03 | Generovani skill.yaml | P1 | Meta-agent vytvori skill sablonu |
| META-04 | Debugging agenta | P1 | Meta-agent pomaha resit problemy |

#### 4.22 Verejne REST API [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| API-01 | API klice | P1 | Generovani a sprava API klicu |
| API-02 | REST endpointy | P1 | CRUD pro agenty, tasky, skills pres API |
| API-03 | Webhooky | P1 | agent.completed, agent.failed, agent.needs_approval |
| API-04 | API dokumentace | P1 | OpenAPI/Swagger spec |

#### 4.23 Git-like Verzovani Konfigurace [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| VER-01 | Config history | P1 | Kazda zmena konfigurace agenta = nova verze |
| VER-02 | Diff view | P1 | Porovnani dvou verzi konfigurace |
| VER-03 | Rollback | P1 | Navrat k predchozi verzi konfigurace |

#### 4.24 Docker Compose (Lokalni dev + Self-hosting) [P0]

> **Poznamka:** Docker compose je P0 protoze je to primarni zpusob lokalniho vyvoje.
> Kopirujeme hardened `docker-compose.yml` z Advine (PostgreSQL 16 + Redis 7).

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| DOCKER-01 | docker-compose.yml (dev) | P0 | PostgreSQL 16 + Redis 7 pro lokalni dev (z Advine) |
| DOCKER-02 | .env.example | P0 | Vsechny env vars s komentari |
| DOCKER-03 | Self-hosting dokumentace | P1 | Krok po kroku navod |
| DOCKER-04 | Full docker-compose (prod) | P1 | Frontend + API + Gateway + Worker + DB + Redis |
| DOCKER-05 | Container lifecycle (TTL) | P1 | Auto-shutdown idle kontejneru (org default + team override) |
| DOCKER-06 | Skill dependencies init | P0 | Init skript instaluje apt/pip/npm balicky pri prirazeni skillu |
| DOCKER-07 | Host resource check | P0 | Go service kontroluje dostupne RAM/CPU pred vytvorenim kontejneru |

#### 4.X Webhooky [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| WEBHOOK-01 | Webhook ingress endpoint | P0 | POST /api/v1/webhooks/{team}/{agent}/trigger -- externi triggery (Grafana, n8n, Make, jiny Crewship) |
| WEBHOOK-02 | Webhook secret per agent | P0 | Kazdy agent ma unikatni webhook secret pro autentizaci |
| WEBHOOK-03 | Webhook retry/delivery log | P1 | Log vsech prichozcich webhooku, retry logika |

#### 4.X File Management [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| FILE-01 | Persistent output storage | P0 | /output/ bind mount -- soubory prezijou restart i smazani tymu (_archived/) |
| FILE-02 | File browser v UI | P0 | Stromovy prohlizec souboru agenta s download tlacitkem |
| FILE-03 | File preview | P1 | PDF, Markdown, obrazky primo v prohlizeci |
| FILE-04 | fsnotify notifikace | P0 | Real-time WebSocket notifikace pri vytvoreni/zmene souboru |

#### 4.X Monitoring [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| MONITOR-01 | cAdvisor container metrics | P0 | CPU, RAM, disk, sit per tym/kontejner |
| MONITOR-02 | Web terminal | P0 | xterm.js -- SSH-like pristup do kontejneru z prohlizece |
| MONITOR-03 | Agent activity stream | P0 | Real-time feed akci agenta (stdout pres WebSocket) |
| MONITOR-04 | Prometheus /metrics | P1 | Go service vystavuje metriky pro Prometheus/Grafana |

#### 4.X Go Service (crewshipd) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| GO-01 | WebSocket gateway | P0 | Nativni Go WebSocket server (goroutines) |
| GO-02 | Docker orchestrator | P0 | Container lifecycle pres Docker SDK for Go |
| GO-03 | Log collector | P0 | Docker stdout → JSONL soubory + logrotate |
| GO-04 | bbolt WAL | P0 | Durable job state, prezije crash Go service |
| GO-05 | Unix socket IPC | P0 | Komunikace s Next.js pres /tmp/crewship.sock |
| GO-06 | Graceful shutdown | P0 | SIGTERM handling, flush logy, stop kontejnery |
| GO-07 | Config YAML | P1 | Jeden konfiguracni soubor misto env vars |

---

### PHASE 3: Enterprise (8-12 tydnu po Phase 2)

#### 4.25 K8s Agent Sandbox [P2]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| K8S-01 | Dedicated kontejner per tym | P2 | Izolace agentu v K8s |
| K8S-02 | gVisor/Kata izolace | P2 | Extra security pro agent runtime |
| K8S-03 | Resource limity | P2 | CPU/RAM per kontejner |
| K8S-04 | Network policies | P2 | Tymy se navzajem nevidi |

#### 4.26 RAG Konektor [P2]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| RAG-01 | pgvector integrace | P2 | Vektorova DB pro agent memory |
| RAG-02 | Document upload | P2 | Nahrani dokumentu do knowledge base |
| RAG-03 | Automaticke chunkovani | P2 | Rozdeleni dokumentu na chunky + embedding |

#### 4.27 Enterprise Features [P2]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ENT-01 | SSO (SAML/OIDC) | P2 | Enterprise prihlaseni |
| ENT-02 | Compliance reporting | P2 | Export audit logu, data retention |
| ENT-03 | Advanced billing/metering | P2 | Per-token billing, usage dashboardy |
| ENT-04 | Plne audit logy | P2 | Vcetne LLM promptu a responsu |
| ENT-05 | SLA monitoring | P2 | Uptime, response time metriky |

#### 4.28 Pokrocile Skills [P2]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ASKILL-01 | Ansible skill | P2 | Spravce infrastruktury |
| ASKILL-02 | Terraform skill | P2 | Infrastructure as Code |
| ASKILL-03 | SSH skill | P2 | Vzdaleny pristup k serveru |
| ASKILL-04 | WhatsApp Business API | P2 | Oficialni WA integrace |

---

### PHASE 4+: Budoucnost (vize)

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| FUT-01 | Mobilni app (React Native) | P3 | Nativni mobilni klient |
| FUT-02 | Voice interface | P3 | Hlasova komunikace s agenty |
| FUT-03 | Multi-org kolaborace | P3 | Agenti z ruznych org spolupracuji |
| FUT-04 | Agent-to-agent marketplace | P3 | Sdileni agentu mezi organizacemi |
| FUT-05 | AI model fine-tuning | P3 | Fine-tune modelu na firemni data |

---

## 5. USER STORIES -- MVP (PHASE 1)

### US-01: Registrace a prvni firma
**Jako** novy uzivatel
**chci** se zaregistrovat a vytvorit firmu
**abych** mohl zacit pouzivat platformu.

**Acceptance criteria:**
1. Uzivatel se registruje pres magic link nebo email+heslo
2. Po prvnim prihlaseni se spusti guided wizard
3. Wizard vyzve k vytvoreni firmy (nazev, slug)
4. Firma se vytvori a uzivatel je automaticky Owner
5. Uzivatel je presmerovan na dashboard

### US-02: Pozvani clena do firmy
**Jako** Owner/Admin
**chci** pozvat kolegu do firmy
**abych** mohl delegovat spravu agentu.

**Acceptance criteria:**
1. Owner/Admin vlozi email a vybere roli (Admin/Manager/Member/Viewer)
2. System posle email s pozvankou
3. Pozvaný se registruje/prihlasi a je automaticky prirazen do org
4. Zobrazi se v seznamu clenu s prirazenu roli

### US-03: Vytvoreni tymu
**Jako** Owner/Admin/Manager
**chci** vytvorit tym (oddeleni)
**abych** organizoval agenty do logickych skupin.

**Acceptance criteria:**
1. Uzivatel vytvori tym (nazev, popis, volitelne z sablony)
2. Priradi cleny z organizace do tymu
3. Tym se zobrazi na dashboardu
4. Clenove tymu vidi agenty v nem

### US-04: Vytvoreni agenta
**Jako** Owner/Admin/Manager (v prirazenem tymu)
**chci** vytvorit virtualniho zamestnance
**abych** mohl automatizovat ukoly.

**Acceptance criteria:**
1. Uzivatel vytvori agenta v tymu (jmeno, popis, role)
2. Vybere CLI adapter (Claude Code, OpenCode, Codex CLI, Gemini CLI) a LLM model
3. Nastavi system prompt (instrukce pro agenta)
4. Volitelne nastavi temperature, max tokens, timeout
5. Agent se zobrazi v seznamu agentu tymu se statusem "Idle"

### US-05: Instalace a prirazeni skillu
**Jako** Owner/Admin/Manager
**chci** pridat dovednosti agentovi
**abych** rozsirit jeho schopnosti.

**Acceptance criteria:**
1. Uzivatel prohlizi registr skillu (built-in + instalovane)
2. Muze nahrat novy skill z YAML souboru
3. Priradi skill agentovi
4. Zobrazi se credentials schema skillu (jake klice jsou potreba)
5. Uzivatel je vyzvan k pridani chybejicich credentials

### US-06: Sprava credentials
**Jako** Owner/Admin/Manager
**chci** bezpecne ulozit API klice
**abych** je mohl pouzit pro agenty.

**Acceptance criteria:**
1. Uzivatel prida credential (nazev + hodnota)
2. Hodnota se okamzite zasifruje (AES-256-GCM)
3. V seznamu se zobrazuje jen nazev + ***
4. Credential je dostupny pro prirazeni agentovi
5. Pri spusteni agenta se injektuje jako ENV var

### US-07: Chat s agentem
**Jako** Member (v prirazenem tymu)
**chci** konverzovat s agentem v chat UI
**abych** mohl zadat ulohu a sledovat jeji plneni.

**Acceptance criteria:**
1. Uzivatel otevra chat s agentem
2. Pise zpravu a odesle
3. Agent zpracovava a odpovida v realnem case (streaming)
4. Odpoved je formatovana (markdown, code blocks)
5. Zobrazuje se status ("Thinking...", "Running tool...", "Done")
6. Konverzace se uklada a je dostupna v historii

### US-08: Sledovani logu
**Jako** Manager/Admin
**chci** videt logy agenta v realnem case
**abych** mohl sledovat co agent dela a debugovat problemy.

**Acceptance criteria:**
1. Uzivatel otevra log viewer
2. Vidi real-time stream strukturovanych logu
3. Kazdy log entry ma: timestamp, agent, akce, vysledek
4. Muze filtrovat podle agenta a severity

### US-09: Audit log
**Jako** Owner/Admin
**chci** videt vsechny dulezite akce v organizaci
**abych** mel prehled kdo co udelal.

**Acceptance criteria:**
1. Kazda state-changing akce se loguje (kdo, co, kdy, odkud)
2. Audit log je dostupny v admin sekci
3. Lze filtrovat podle uzivatele, typu akce, casu
4. Audit log je izolovany per organizace

### US-10: Dashboard
**Jako** prihlaseny uzivatel
**chci** videt prehled svych tymu a agentu
**abych** mel rychly prehled o stavu.

**Acceptance criteria:**
1. Po prihlaseni vidim team overview
2. Kazdy tym ukazuje: pocet agentu, aktivnich tasku, posledni aktivitu
3. Agent karty ukazuji status (Idle/Running/Error)
4. Kliknutim se dostanu na detail tymu/agenta

---

## 6. CO NENI V MVP (EXPLICITNI VYLOUCENI)

Nasledujici features jsou VEDOME odlozeny z MVP do pozdejsich fazi. Duvod: solo dev + AI, realisticka kapacita 8-12 tydnu.

| Feature | Proc ne v MVP | Faze |
|---|---|---|
| M:1 chat (vice uzivatelu na agenta) | Architektonicky slozite (real-time sync) | Phase 2 |
| Asynchronni task mode | Vyzaduje task management UI | Phase 2 |
| Human-in-the-loop schvalovani | Vyzaduje approval workflow | Phase 2 |
| Inter-agent komunikace/orchestrace | Nejslozitejsi cast, vyzaduje navrh protokolu | Phase 2 |
| Cron joby pro agenty | Scheduler infrastruktura | Phase 2 |
| Webhooky | Webhook delivery infrastruktura | Phase 2 |
| Verejne REST API | API key management + rate limiting | Phase 2 |
| Git-like verzovani konfigurace | Config history + diff UI | Phase 2 |
| Vice LLM provideru (Anthropic, Ollama) | LLM adapter pattern | Phase 2 |
| Discord/Telegram integrace | Channel adapter pattern | Phase 2 |
| Skills marketplace (remote browse) | Marketplace backend | Phase 2 |
| Crewship AI meta-agent | Vyzaduje hlubokou znalost platformy | Phase 2 |
| Tailwind 4 migrace | 34 Advine komponent funguje s v3, migrace neni kriticka | Phase 2 |
| RLS politiky (Row-Level Security) | CASL staci pro MVP, RLS jako defense-in-depth | Phase 2 |
| Supabase Auth adapter | NextAuth.js staci pro MVP, Supabase az pro cloud | Phase 2 |
| Magic link prihlaseni | Vyzaduje Supabase Auth | Phase 2 |
| Stripe billing (pokrocily) | Billing dashboard, invoicing | Phase 2 |
| K8s Agent Sandbox (migrace z Docker) | K8s Pods, gVisor, dedicovane resources | Phase 3 |
| RAG/pgvector | Vektorova DB pipeline | Phase 3 |
| SSO (SAML/OIDC) | Enterprise auth | Phase 3 |
| WhatsApp Business API | Oficialni API = placene | Phase 3 |
| Mobilni app | React Native | Phase 4 |

**MVP Stripe poznamka:** V MVP bude zakladni Stripe integrace -- subscription create, plan tiers, limit enforcement. Plny billing dashboard (faktury, usage metriky) az Phase 2.

---

## 7. NEFUNKCIONALNI POZADAVKY

### 7.1 Vykon
- Chat response: prvni token do 2s od odeslani zpravy
- Dashboard load: pod 1s (server-side rendering)
- Agent startup: pod 5s od spusteni
- Concurrent agents: 5 per worker instance, 20+ s horizontalnim scalingem (4+ workery)

### 7.2 Bezpecnost
- Vsechny credentials sifrovane AES-256-GCM at rest
- MVP: CASL RBAC kontrola na KAZDEM API endpointu (aplikacni uroven)
- Phase 2: RLS politiky na VSECH tabulkach (defense-in-depth)
- **Docker kontejner izolace** -- agent bezi v izolovanem kontejneru (read-only root, cap-drop ALL, no-new-privileges)
- **Bezpecnostni log** -- auditd (syscall logging) + inotify (filesystem changes) oddeleny od aplikacniho audit logu
- **Network izolace** -- agent kontejnery v oddelene Docker siti, bez pristupu k platforme (DB, Redis, Gateway)
- Agent nikdy nevidi plaintext credentials
- Audit log pro vsechny state-changing akce
- OWASP Top 10 pokryte (XSS, CSRF, injection, atd.)

### 7.3 Skalovatlenost
- Phase 1: 100 organizaci, 1000 agentu, 1 worker instance
- Phase 2: 1000 organizaci, 10000 agentu, horizontalni scaling
- Phase 3: Enterprise -- dedicated resources per organizace

### 7.4 Dostupnost
- Cloud: 99.5% uptime (Phase 1), 99.9% (Phase 3)
- Self-hosted: zavisla na infrastrukture uzivatele

### 7.5 Kompatibilita
- Browsery: Chrome, Firefox, Safari, Edge (posledni 2 verze)
- Responsivni design (mobil, tablet, desktop)
- Node.js 22 LTS+ (self-hosting)

### 7.6 GDPR / Ochrana dat
- Pravo na vymazani uctu + vsech dat (30 dni grace period)
- Export dat (JSON/CSV)
- Data retention politiky (konfigurovatelne per org)
- Zpracovani pouze v EU/US (konfigurovatelne)

---

## 8. TECHNOLOGICKE ROZHODNUTI

### Vendor-neutralita (KRITICKE ROZHODNUTI)
- Platforma MUSI fungovat s **plain PostgreSQL** (self-hosting) I **Supabase** (cloud)
- **Prisma 7.3** = JEDINY zpusob pristupu k DB (zadne `supabase.from()` pro queries)
- **Auth MVP:** Jen **NextAuth.js (Auth.js v5)** s Prisma adapterem -- funguje vsude (Supabase Auth adapter az Phase 2)
- **Autorizace MVP:** Jen **CASL** (aplikacni uroven) -- RLS az Phase 2 jako defense-in-depth
- **User tabulka** je standalone (ne sync z `auth.users` triggerem)

### Jazyk a runtime
- **TypeScript 5.8** (strict mode, zero `any`) -- jeden jazyk pro cely stack
  - TS 7.0 (Go-based, Project Corsa) je v alpha, migrace az bude stable (odhad H2/2026)
- **Node.js 22 LTS** -- stabilni runtime, nativni TypeScript type stripping
- Bottleneck platformy NENI jazyk ale LLM latence a CLI startup

### Frontend
- **Next.js 16.1** (App Router, RSC) -- frontend + REST API routes
- **React 19.2** -- latest stable
- **Tailwind CSS 3.4** -- zustavame na v3 (vsech 34 Advine komponent funguje)
  - Migrace na Tailwind 4 odlozena do Phase 2 (neni kriticka, setri cas)
- **shadcn/ui** -- 34 komponent z Advine (primo reuse, funguje s Tailwind 3)
- **Zustand 5** -- client state management
- **Lucide React** -- ikony

### Backend a validace
- **Zod 4** -- runtime validace (14x rychlejsi nez v3, JSON Schema podpora)
- **Prisma 7.3** -- ORM v TypeScriptu (bez Rust engine), schema = source of truth
- **BullMQ 5.67** -- Redis-based job queue (250k+ jobs/sec)
- **CASL 6.8** (@casl/ability + @casl/prisma) -- RBAC (z Advine: 6 roli, plne definovane abilities)
  - MVP: JEDINA autorizacni vrstva (zadne RLS)
  - Phase 2: RLS jako defense-in-depth
- **Stripe 20.2** -- billing a subscriptions (jen enterprise mode)
- **Auth.js v5 (NextAuth.js)** -- JEDINY auth system v MVP (email+heslo, Google OAuth, GitHub OAuth)
  - Prisma adapter pro DB session storage
  - Phase 2: Supabase Auth adapter pro cloud (magic link, SSO)

### Gateway (WebSocket)
- **ws 8.19** (low-level) + **vlastni typed protokol** -- inspirovany OpenClaw wire protocol
- JSON framy: `{type:"req", id, method, params}` → `{type:"res", id, ok, payload|error}`
- Events: `{type:"event", event, payload}`
- Multi-tenant routing na zaklade org_id/team_id z JWT

### Infrastruktura a tooling
- **Monorepo s Turborepo** -- frontend (Next.js) + gateway + worker v jednom repu, sdilene typy
- **pnpm** -- package manager (rychly, efektivni disk usage)
- **Resend** -- transakcni emaily (pozvanky, notifikace). Free tier 100/den. Auth emaily resi Supabase.
- **Pino** -- strukturovane JSON logování (MVP). Sentry + PostHog az Phase 2.
- **Vitest** -- unit testy (services) + integracni testy (API routes)

### Lokalni vyvoj
- **Docker compose** (z Advine, hardened): PostgreSQL 16 + Redis 7
  - `docker compose up` = hotovo, ciste prostredi
  - Read-only kontejnery, no-new-privileges, healthchecks
- **Frontend:** `pnpm dev` (Next.js dev server)
- **Gateway:** `pnpm gateway:dev` (standalone WS server)
- **Worker:** `pnpm worker:dev` (BullMQ worker)
- **Workspace storage:** Lokalni filesystem

### Cloud / Self-hosted deployment (rozhodnuto, viz DEPLOYMENT.md)
- **Platforma:** Vlastni Proxmox server s Coolify (self-hosted PaaS)
- **App (Next.js):** Coolify Docker service (frontend + API + WS Gateway)
- **Worker:** Coolify Docker service (BullMQ + Docker socket pristup)
- **Agent Runtime:** Docker kontejnery spravovane Workerem (dynamicky)
- **DB:** PostgreSQL 16 (Coolify managed)
- **Redis:** Redis 7 (Coolify managed)
- **Workspace storage:** Lokalni disk (MVP), NFS/R2 (Phase 2+)
- **SSL:** Coolify auto-SSL (Let's Encrypt)
- **CI/CD:** Git push → Coolify webhook (MVP), GitHub Actions (Phase 2)
- **Kontejner abstrakce:** `ContainerProvider` interface (Docker MVP, K8s Phase 3)

### Proc NE jine jazyky/technologie
| Alternativa | Proc NE |
|---|---|
| Go (pro Gateway) | Dva codebasy pro solo dev = neudrzitelne |
| Rust | Strma krivka uceni, pomaly vyvoj |
| Bun | Mladsi, riziko nekompatibility s npm balicky |
| tRPC | Vendor lock-in, slozitejsi pro verejne API |
| Socket.IO | Vetsi overhead, nepotrebujeme fallback na polling |
| Valibot/ArkType | Mensi komunita nez Zod, mene integrace |
| Tailwind 4 (v MVP) | 34 Advine komponent funguje s v3, migrace na v4 az Phase 2 |
| TypeScript 7.0 | Alpha stav, nestabilni pro production, migrace pozdeji |
| Multi-repo | Slozitejsi sprava pro solo dev, ztrata sdilenych typu |
| Sentry/PostHog v MVP | Pridava zavislosti a setup, neni kriticke pro launch |

---

## 9. ADVINE REUSE -- CO KOPIRUJEME Z PPC PLATFORMY

Crewship je pivot z **Advine** (PPC monitoring SaaS). Advine ma robustni enterprise infrastrukturu kterou primo reusujeme. Odhad usporeneho casu: **3-4 tydny**.

### 9.1 Primo kopirovatelne (Copy)

| Modul | Soubor(y) v Advine | Popis |
|---|---|---|
| Prisma DB klient | `lib/db.ts` | Prisma 7.3 s pg adapter, singleton, proxy lazy-loading |
| Sifrovani | `lib/encryption.ts` | AES-256-GCM, IV+AuthTag+Ciphertext, validace authTag delky |
| Pino logger | `lib/logger.ts`, `logger.config.ts`, `logger.types.ts` | Rotace, redakce, pretty-print dev, JSON prod |
| Redis config | `lib/redis-config.ts` | Multi-env (prod/stage/local), auto-detect |
| Rate limiting | `lib/rate-limit.ts` | Upstash + ioredis, fail-closed v prod, fail-open v dev |
| CSRF ochrana | `lib/csrf.ts` | Origin-based, zero-dependency |
| Security middleware | `lib/security-middleware.ts` | Brute force tracking, auth failure escalace |
| API middleware | `lib/api-middleware.ts` | Request ID, logging, context management |
| Request context | `lib/request-context.ts` | AsyncLocalStorage per request |
| Utility funkce | `lib/utils/cn.ts`, `date.ts`, `format.ts` | clsx+twMerge, date-fns, formatovani |
| Docker compose | `deployments/docker/docker-compose.yml` | PostgreSQL 16 + Redis 7, hardened |
| Hooks | `hooks/use-mobile.tsx`, `use-reduced-motion.ts` | Responzivni design |
| shadcn/ui | `components/ui/` (34 komponent) | Plna sada: dialog, dropdown, tabs, sidebar, chart... |
| Vitest setup | `vitest.config.ts`, `vitest.setup.ts` | Happy-DOM, coverage v8 |
| ESLint config | `eslint.config.mjs` | TS strict, React hooks |

### 9.2 Adaptovatelne (Adapt -- zmena domeny z PPC na Agenty)

| Modul | Soubor(y) | Co se meni |
|---|---|---|
| CASL RBAC | `lib/permissions/abilities.ts`, `types.ts`, `server.ts` | Subjects: Campaign→Agent, Integration→Skill, Alert→AgentRun |
| Auth helpers | `lib/auth-helpers.ts` | Abstrakce na auth adapter (Supabase + NextAuth) |
| Audit logger | `lib/security/audit-logger.ts` | Refaktor: `supabase.from()` → `prisma.auditLog.create()` |
| Anomaly detection | `lib/security/anomaly-detector.ts` | Typy eventu pro Agent domenu |
| Feature flags | `lib/services/feature-flags.service.ts` | Odstranit PostHog, nechat DB flagy |
| Subscription service | `lib/services/subscription.service.ts` | Refaktor na Prisma-only, Crewship plany |
| Zod validace | `lib/validation.ts` | Novy schemat pro Agent, Team, Skill |
| Email service | `lib/services/email.service.ts` | Resend templates pro Paseku |
| BullMQ worker | `workers/sync-worker.ts` | Pattern pro agent-worker (nahradit sync logiku) |
| Zustand store | `lib/store.ts` | Novy state pro Paseku |
| Prisma schema | `prisma/schema.prisma` | Reuse: Org, User, Member, AuditLog, FeatureFlag. Replace: Campaign, Metric, Alert → Agent, Skill, Conversation |

### 9.3 Nepouzitelne (PPC-specific, zahazujeme)

- Vsechny PPC sync services (google-ads, sklik, meta-ads, linkedin, amazon, microsoft)
- Alert engine, alert rules, alert delivery, KPI baselines
- Campaign, Metric, Connection, Integration modely
- PPC dashboard stranky (monitoring, campaigns, analysis, quality-score)
- PPC komponenty (alerts/, analytics/, entity-picker/)
- Landing page (PPC marketing copy)
- Sentry + PostHog integrace (odlozeno na Phase 2)

### 9.4 Package.json -- dependencies uz mame

Advine `package.json` uz obsahuje VSECHNY Crewship dependencies na spravnych verzich:

| Dependency | Verze v Advine | Pouziti v Pasece |
|---|---|---|
| next | 16.1.6 | Frontend + API routes |
| react | 19.2.4 | UI |
| @prisma/client | 7.3.0 | ORM |
| zod | 4.3.6 | Validace |
| zustand | 5.0.11 | Client state |
| bullmq | 5.67.2 | Job queue |
| @casl/ability | 6.8.0 | RBAC |
| pino | 10.3.0 | Logging |
| resend | 6.9.1 | Emaily |
| @supabase/ssr | 0.8.0 | Auth (ONLY) |
| ioredis | 5.9.2 | Redis |
| jose | 6.1.3 | JWT |
| lucide-react | 0.563.0 | Ikony |
| tailwindcss | 3.4.18 | Styling |

---

## 10. COMMUNITY vs ENTERPRISE MODE

Crewship bezi ve dvou modech, rizenych env var `CREWSHIP_MODE`:

```env
CREWSHIP_MODE=community   # self-hosting, free, open-source
CREWSHIP_MODE=enterprise   # cloud, placeny, plny feature set
```

### Srovnani modu

| Feature | Community (free, self-host) | Enterprise (cloud, placeny) |
|---|---|---|
| Organizace | 1 (auto-created pri startu) | Neomezene (registrace) |
| RBAC | Vsichni jsou ADMIN (zjednoduseny) | Plnych 5 roli (OWNER→VIEWER) |
| Auth (MVP) | NextAuth.js (email+heslo, OAuth) | NextAuth.js (stejne jako community) |
| Auth (Phase 2) | NextAuth.js | Supabase Auth (magic link, SSO) |
| Autorizace (MVP) | CASL (aplikacni uroven) | CASL |
| Autorizace (Phase 2) | CASL | CASL + RLS (defense-in-depth) |
| Stripe billing | Vypnuty (free navzdy) | Aktivni (Free/Pro/Team/Enterprise) |
| Audit log | Loguje se, zakladni UI (tabulka) | Plny UI s filtry, export, retention |
| Feature flags | ENV vars nebo `.env` soubor | DB + admin UI + rollout % |
| Config history | Vypnuto | Plny versioning s diff view |
| Invitations | Sharing link (jednoduchy) | Email s tokenem + expiraci |
| Docker compose | Primarni zpusob instalace | Volitelne (cloud je default) |
| Podpora | Community (GitHub Issues) | Placena (email, prioritni) |

### Architektura
- **Stejna DB schema** (20 tabulek) pro oba mody
- **Stejny codebase** -- zadna separatni verze (CE vs EE)
- Feature flagy v DB rozlisuji co je dostupne
- Community seed automaticky vytvori 1 org + admin usera
- Model inspirovany **GitLab CE/EE** (jeden repo, feature toggles)

---

## 11. BYOK (BRING YOUR OWN KEY) MODEL

Uzivatel pouziva VLASTNI API klice pro LLM providery. Crewship NEPROXUJE a NETRACKUJE API volani.

### Jak to funguje
1. Uzivatel prida svuj OpenAI API klic jako credential
2. Klic se zasifruje a ulozi do vaultu
3. Pri spusteni agenta se klic injektuje jako ENV var
4. Agent vola OpenAI API primo s uzivatlovym klicem
5. Crewship NEPOCITA tokeny, NETRACKUJE spotrebu
6. Pokud API vrati error (rate limit, insufficient funds), agent zastavi a reportuje chybu

### Proc BYOK
- Zadny vendor lock-in
- Uzivatel plati jen za to co spotrebuje
- Zadny markup na API calls
- Zjednodusuje architekturu (zadny billing per token)
- Podpora libovolneho LLM providera (vcetne self-hosted Ollama)

---

## 12. AGENT WORKSPACE (BOOTSTRAP SOUBORY)

Kazdy agent ma svuj workspace adresar. Format je kompatibilni s OpenClaw.

### Struktura workspace
```
/var/crewship/workspaces/{org_slug}/{team_slug}/{agent_slug}/
├── AGENTS.md          # Operacni instrukce + "pamet" agenta (kompatibilni s Codex i OpenCode)
├── CLAUDE.md          # Kopie/symlink AGENTS.md (Claude Code cte CLAUDE.md)
├── SOUL.md            # Osobnost, hranice, ton komunikace
├── TOOLS.md           # Navody k pouzivani nastroju (generovane z tool profile)
├── IDENTITY.md        # Jmeno agenta, emoji, styl (generovane z agent config)
├── USER.md            # Profil uzivatele/firmy (kontextove info)
├── opencode.json      # OpenCode config (jen pokud cli_adapter = opencode)
├── skills/            # Skill soubory prirazene agentovi
│   ├── coding.md
│   ├── web-research.md
│   └── ...
├── sessions/          # Transkripty konverzaci (JSONL, sekundarni -- debug/kompatibilita)
│   ├── {session_id}.jsonl
│   └── ...
└── data/              # Pracovni data agenta (soubory, outputy)
```

### Bootstrap soubory (injektovane)
Pri prvnim turnu nove session Crewship injektuje obsah bootstrap souboru do agent kontextu:

| Soubor | Ucel | Editovatelny uzivatelem |
|---|---|---|
| `AGENTS.md` | Operacni instrukce, pravidla, omezeni | Ano |
| `CLAUDE.md` | Kopie AGENTS.md (Claude Code cteni) | Automaticky (sync s AGENTS.md) |
| `SOUL.md` | Osobnost, ton, hranice chování | Ano |
| `TOOLS.md` | Navody k nastrojum (generovane z tool profile) | Ano |
| `IDENTITY.md` | Jmeno, role, emoji, styl odpovedi | Ano (generuje se z agent config) |
| `USER.md` | Profil firmy/uzivatele, kontext | Ano |
| `opencode.json` | OpenCode config (jen pro OpenCode agenty) | Automaticky |

### Generovani z UI
Kdyz uzivatel vytvori agenta pres web UI:
1. `IDENTITY.md` se generuje z agent config (jmeno, role, popis)
2. `SOUL.md` se generuje ze system prompt
3. `AGENTS.md` se generuje z prirazenych skillu a credentials
4. `TOOLS.md` se generuje z tool profile (coding/messaging/full)
5. Uzivatel muze vsechny soubory editovat pres "Advanced" UI

---

## 13. AGENT REZIMY (INTERACTION MODES)

### 13.1 Chat Mode (MVP)
- Uzivatel sedi u chatu a v realnem case komunikuje
- Agent odpovida okamzite (streaming)
- Vhodne pro: dotazy, analyzu, kratke ukoly
- Timeout: 30 minut neaktivity

### 13.2 Task Mode (Phase 2)
- Uzivatel zada ukol a odejde
- Agent bezi na pozadi
- Notifikace pri dokonceni nebo kdyz agent potrebuje vstup
- Vhodne pro: dlouhe operace, batch processing, cron joby

### 13.3 Collaborative Mode (Phase 2)
- Vice uzivatelu komunikuje se stejnym agentem
- Sdileny chat -- vsichni vidi zpravy ostatnich
- Agent identifikuje kdo mu pise
- Vhodne pro: teamove projekty, sdileny asistent

---

## 14. ONBOARDING FLOW

### Free Tier: Guided Wizard
```
1. Registrace (email+heslo nebo OAuth) -- magic link az Phase 2
2. "Vitejte v Pasece! Pojdme vytvorit vasi prvni firmu."
   → Nazev firmy, slug
3. "Skvele! Ted vytvorime vas prvni tym."
   → Vyber sablony NEBO vlastni nazev
4. "Pridejme virtualniho zamestnance do tymu."
   → Jmeno, role, vyber LLM modelu
5. "Dejme mu nejake dovednosti."
   → Vyber z built-in skillu (web-search, email-send)
6. "Posledni krok -- pridejte API klic."
   → Formular na OpenAI API klic
7. "Hotovo! Spustte svuj prvni chat."
   → Presmerovani na chat UI
```

### Placeny Tier: Crewship AI (Phase 2)
```
1. Registrace + subscription
2. Otevre se chat s Crewship AI
3. "Ahoj! Jsem Crewship AI. Pomahu vam vytvorit virtualni tym.
    Povidejte mi o vasi firme -- co delate a co chcete automatizovat?"
4. Uzivatel popisuje potreby
5. Crewship AI navrhne strukturu (tymy, agenty, skilly)
6. "Navrhuji vytvorit Obchodni oddeleni s agentem Karel (lead gen)
    a agentem Lucie (customer support). Souhlasíte?"
7. Po schvaleni Crewship AI automaticky vse vytvori
```

---

## 15. SABLONY ODDELENI A AGENTU

### Format: YAML v repozitari
Sablony budou staticke YAML soubory v `skills/templates/`. Uzivatel si muze vytvorit vlastni a exportovat.

### Preddefinovane sablony oddeleni

#### IT Firma
```yaml
name: it-company
display_name: "IT Company"
description: "Complete IT team with DevOps, QA, and support"
teams:
  - name: development
    display_name: "Development Team"
    agents:
      - name: devops-bot
        role: "DevOps Engineer"
        system_prompt: "You are a DevOps engineer..."
        skills: [docker-manage, k8s-deploy, ci-cd-pipeline]
      - name: qa-bot
        role: "QA Tester"
        system_prompt: "You are a QA engineer..."
        skills: [test-runner, bug-reporter]
  - name: support
    display_name: "Customer Support"
    agents:
      - name: support-bot
        role: "Support Agent"
        system_prompt: "You are a customer support agent..."
        skills: [email-send, ticket-manager, knowledge-search]
```

#### Obchodni oddeleni
```yaml
name: sales-department
display_name: "Sales Department"
description: "Sales team with lead gen and CRM management"
teams:
  - name: sales
    agents:
      - name: lead-gen
        role: "Lead Generator"
        skills: [web-search, crm-hubspot, email-send]
      - name: account-manager
        role: "Account Manager"
        skills: [crm-hubspot, email-send, calendar-manage]
```

---

## 16. METRIKY USPECHU

### Phase 1 (MVP Launch)
- 100+ GitHub stars do 1 mesice
- 50+ registrovanych uzivatelu do 1 mesice
- 10+ aktivnich organizaci (min 1 agent spusteny)
- 0 kritickych security issues

### Phase 2
- 500+ GitHub stars
- 200+ registrovanych uzivatelu
- 20+ community skills v marketplace
- 5+ placicich zakazniku (Pro tier)

### Phase 3
- 2000+ GitHub stars
- 1000+ uzivatelu
- 1 enterprise zakaznik
- 100+ skills v marketplace

---

## 17. RIZIKA A MITIGACE

| Riziko | Pravdepodobnost | Dopad | Mitigace |
|---|---|---|---|
| Solo dev = pomaly vyvoj | Vysoka | Vysoky | AI agenti (Claude, Codex), uzky MVP scope |
| LLM API nestabilita | Stredni | Stredni | Error handling, retry logika, BYOK (uzivatel resi) |
| Security breach (credentials leak) | Nizka | Kriticky | AES-256-GCM, RLS, audit log, penetration testing |
| Zadna komunita (skills marketplace prazdny) | Stredni | Vysoky | 5+ built-in skillu, template sablony, aktivni marketing |
| Konkurence (OpenClaw, n8n, CrewAI) | Stredni | Stredni | Diferenciace: enterprise-ready, "lidska" terminologie |
| Scope creep | Vysoka | Vysoky | Striktni PRD, brutalni prioritizace, code freeze pred launchem |
| GDPR compliance | Nizka | Vysoky | Data retention, pravo na vymazani, EU hosting |

---

## 18. TIMELINE (REALISTICKA PRO SOLO DEV + AI)

> **Poznamka:** Diky reuse z Advine (30+ souboru infrastruktury) se scaffolding zkratil
> z 2 tydnu na ~1 tyden. Celkove usporime 2-3 tydny.

### Phase 1: MVP (Combined Alpha+Beta)
| Tyden | Milestone | Deliverables |
|---|---|---|
| 1 | Scaffolding (Advine reuse) | Git repo, package.json (z Advine), Docker compose, Prisma schema, kopie infra modulu |
| 2-3 | Core backend | Auth adapter (Supabase + NextAuth), Org CRUD, Team CRUD, RBAC (Advine CASL), vendor-neutral RLS |
| 4-5 | Agent system | Agent CRUD, Workspace setup, Skills registry, Credentials vault (Advine encryption), BullMQ queue |
| 6-7 | Gateway + Runtime | Multi-tenant WS Gateway, CLI adapter (Claude Code), child_process management, streaming |
| 8-9 | Chat UI + Tools | Chat UI (block streaming), tool execution vizualizace, Log viewer |
| 10 | Dashboard + Polish | Dashboard (Advine sidebar reuse), Audit log (Advine audit-logger), Onboarding wizard |
| 11-12 | Billing + Launch | Stripe (enterprise mode), community/enterprise mode toggle, testing, docs |

### Phase 2: Rozsireni
| Tyden | Milestone |
|---|---|
| 1-3 | Multi-LLM + Task mode + Human-in-the-loop |
| 4-6 | Orchestrace + Cron + M:1 chat |
| 7-9 | Messaging (Discord, Telegram) + REST API |
| 10-12 | Crewship AI meta-agent + Skills marketplace + Docker compose |

### Phase 3: Enterprise
| Tyden | Milestone |
|---|---|
| 1-4 | K8s Agent Sandbox + RAG |
| 5-8 | SSO + Compliance + Advanced billing |
| 9-12 | Ansible/Terraform skills + WhatsApp |

---

## 19. OTEVRENE OTAZKY (K RESENI V DALSICH DOKUMENTECH)

1. **CLI adapter detaily** (→ AGENT-RUNTIME.md) -- jak presne spoustime Claude Code / Codex CLI / Gemini CLI jako child_process? Jaky je API kontrakt?
2. **Orchestrace protokol** (→ AGENT-RUNTIME.md) -- sessions_send / sessions_spawn implementace pro multi-tenant
3. ~~**Cloud platforma**~~ -- VYRESENO: Coolify na vlastnim Proxmox serveru (viz DEPLOYMENT.md)
4. **Finalni nazev** -- Crewship, Advine, nebo neco jineho?
5. **Pricing detaily** -- presne limity per tier (pocet agentu, skillu, workspace storage)?
6. **Skills security review** (→ SECURITY.md) -- jak validovat community skills? Sandbox pro exec?
7. **Agent sandbox** (→ AGENT-RUNTIME.md) -- exec tool bezpecnost, co agent smi/nesmi spoustet
8. **Database schema detaily** (→ DATABASE.md) -- presne typy, indexy, constraints
9. **Wire protocol specifikace** (→ API.md) -- kompletni WS message types, events, error handling
10. ~~**Workspace storage**~~ -- VYRESENO: Lokalni disk MVP, NFS/R2 Phase 2+ (viz DEPLOYMENT.md sekce 10)

---

*Dalsi dokument k vytvoreni: DATABASE.md (datovy model)*
