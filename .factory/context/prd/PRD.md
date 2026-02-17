# Crewship -- Product Requirements Document (PRD)

**Verze:** 2.0
**Datum:** 2026-02-17
**Autor:** Pavel Srba + AI analyza
**Status:** Draft

> Aktualizovano 2026-02-17: Dvoujazycna architektura (TS + Go), single binary distribuce,
> SQLite default, skill marketplace, per-agent network control.

---

## 1. VIZE PRODUKTU

### Co stavime
Open-source (FSL licence) platforma pro orchestraci AI agentu prezentovanych jako **"virtualni zamestnanci"**. Cil: umoznit firmam i jednotlivcum vytvorit virtualni oddeleni (obchod, vyvoj, zakaznicka podpora) kde AI agenti plni ulohy autonomne nebo v konverzaci s lidskymi kolegy.

### Pracovni nazev
**Crewship** (Crew + Ship + "-ship" suffix -- trojity vyznam).
Domena: crewship.ai | GitHub: github.com/crewship-ai

### Elevator pitch
> "Crewship je open-source platforma kde vytvorite virtualni firmu. Nainstalujete jednim prikazem,
> spustite, a za 60 sekund mate bezicho AI agenta v izolovanem kontejneru. Pridavate AI zamestnance
> do crew, date jim dovednosti a opravneni, nastavite sitovy pristup, a oni pracuji -- generuji reporty,
> monitoruji systemy, resolvuji tikety. Vsechno pod vasim dohledem, s plnym audit logem a enterprise
> zabezpecenim. `brew install crewship && crewship start` -- hotovo."

### Klicova diferenciace od konkurence
1. **Jednoprikazova instalace** -- `brew install crewship && crewship start`. Ollama model distribuce.
2. **Container isolation by default** -- kazdy agent v Docker kontejneru (non-root, cap-drop ALL).
3. **Per-agent network control** -- internet ON/OFF, domain whitelist, local network -- klikaci UI.
4. **"Lidska" terminologie** -- crew, zamestnanec, dovednost, opravneni. Ne agent, orchestrator, MCP.
5. **Enterprise-ready od dne 1** -- RBAC, audit log, sifrovane credentials, cost control.
6. **Curated Skills marketplace** -- sandbox enforcement, permissions model, Official/Verified/Community badges.
7. **3-tier orchestrace** -- Coordinator → Lead → Agent hierarchie (inspirace realnymi firmami).
8. **Zero-deps Free tier** -- single binary, SQLite, Docker. Zadny cloud, zadna registrace.
9. **Open-source + self-hosted** -- FSL licence (→ Apache 2.0 po 2 letech).
10. **Crewship AI (meta-agent)** -- AI ktery pomaha vytvoret AI crew.

### Architekturni vzor: inspirace OpenClaw
Crewship je architektonicky inspirovana projektem **OpenClaw** (157k+ GitHub stars, MIT licence).
OpenClaw je single-user personal AI assistant s Gateway daemonem, workspace konceptem a tool systemem.
Crewship prebira tyto vzory a PRIDAVA container isolation, multi-tenancy, RBAC, enterprise security,
web dashboard, per-agent network control a curated skill marketplace.

Crewship resi **KAZDY** zasadni bezpecnostni problem OpenClaw:

| OpenClaw problem | Crewship reseni |
|---|---|
| Zadna container isolation | Docker kontejnery, non-root UID 1001, `--internal` network |
| Credentials v plaintext | AES-256-GCM sifrovani s key versioning (`v1:base64data`) |
| Malicious skills (zadny sandbox) | Sandboxed skills s deklarovanymi permissions |
| Zadny audit trail | Append-only audit log, immutable, queryable |
| Prompt injection → full access | Agent v kontejneru nemuze uniknout (container = hranice) |
| 42,900 exposed instanci | Localhost default, auth required, RBAC na kazdem endpointu |
| Astronomicke API naklady ($750/m) | Per-agent cost budgety, alerting, limity |

---

## 2. CILOVI UZIVATELE (PERSONY)

### Persona 1: "Pepa" -- Majitel male firmy
- **Profil:** 35-55 let, firma 20-50 lidi, netechnicky
- **Potreba:** Chce automatizovat rutinni prace (emaily, reporty, CRM) bez najimani dalsich lidi
- **Bolest:** Nerozumi pojmum jako MCP, orchestrator, prompt engineering
- **Jak ho ziskame:** "Lidska" terminologie, Crewship AI (meta-agent) ho provede setupem
- **Tier:** Crew (placeny cloud)

### Persona 2: "Marek" -- IT Admin / CTO
- **Profil:** 28-40 let, technicky zamereny, spravuje infrastrukturu firmy
- **Potreba:** Orchestrovat AI agenty pro DevOps, monitoring, deployment
- **Bolest:** Existujici nastroje jsou bud prilis jednoduche (Zapier) nebo prilis slozite (LangGraph)
- **Jak ho ziskame:** Open-source, self-hosting, REST API, skills system, single binary
- **Tier:** Free (self-hosted) nebo Enterprise (K8s Helm chart)

### Persona 3: "Jana" -- Freelance vyvojar
- **Profil:** 25-35 let, vyvojarka, buduje projekty pro klienty
- **Potreba:** Rychle protypovat AI automatizace pro klienty
- **Bolest:** CrewAI/AutoGen vyzaduji hodne kodu, zadne UI pro klienta
- **Jak ho ziskame:** Free tier, snadny setup, sablony oddeleni
- **Tier:** Free → Crew

### Persona 4: "David" -- Developer na laptope
- **Profil:** 20-35 let, vyvojar, chce osobniho AI asistenta na svem stroji
- **Potreba:** Osobni AI agenti pro coding, research, automatizaci -- bez cloud zavislosti
- **Bolest:** OpenClaw je nebezpecny (host access, zadny sandbox), setup je slozity
- **Jak ho ziskame:** `brew install crewship && crewship start` za 2 sekundy. SQLite, zero deps.
- **Tier:** Free (single binary, SQLite, lokalni Docker)

---

## 3. TERMINOLOGIE

| UI termin (anglicky) | UI termin (cesky) | Kod | Popis |
|---|---|---|---|
| Workspace | Pracovni prostor | Workspace | Multi-tenant root entita |
| Crew | Crew / Tym | Crew | Izolacni boundary, kontejner |
| Virtual Employee | Virtualni zamestnanec | Agent | CLI session s LLM, skilly, credentials |
| Skill | Dovednost | Skill | Balicek nastroju + MCP + skripty |
| Permission / Credential | Opravneni | Credential | Sifrovany API klic v trezoru |
| Crew Lead | Sef crew | Agent (role=LEAD) | 1 per crew, primarni kontakt pro uzivatel, prideluje ukoly agentum |
| Coordinator | Koordinator | Agent (role=COORDINATOR) | 1 per workspace, koordinuje cross-crew, prideluje ukoly leadum |
| Agent | Agent / Zamestnanec | Agent (role=AGENT) | Default role, specializovany na konkretni ukoly |
| Task | Ukol | AgentRun | Jednotlivy beh agenta |
| Assignment | Prideleni | Assignment | Lead/Coordinator prideluje ukol podrizenemu |
| Chat | Chat / Konverzace | Chat | Chat session s agentem |

---

## 4. FEATURE LIST -- ROZDELENI DO FAZI

### LEGENDA PRIORIT
- **P0** = Must have (MVP se bez toho nespusti)
- **P1** = Should have (dulezite, ale MVP prezije bez toho)
- **P2** = Nice to have (odlozeno na dalsi fazi)
- **P3** = Future (vize, ne plan)

---

### PHASE 1: MVP (8-12 tydnu, solo dev + AI)

**Cil:** Funkcni platforma kde uzivatel nainstaluje jednim prikazem, vytvori workspace, crew, agenta, prida mu skilly a credentials, nastavi sitovy pristup, spusti ho a vidi konverzaci v chat UI. Vcetne single binary distribuce a skill marketplace.

#### 4.1 Autentizace a uzivatelsky ucet [P0]

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| AUTH-01 | Email + heslo prihlaseni | P0 | NextAuth.js (Auth.js v5) s Prisma adapterem |
| AUTH-02 | Google OAuth | P0 | OAuth pres Google |
| AUTH-03 | GitHub OAuth | P1 | OAuth pres GitHub (dev komunita) |
| AUTH-04 | Logout + session management | P0 | NextAuth.js session (JWT nebo DB) |
| AUTH-05 | Zapomenute heslo | P0 | Reset hesla pres email (Resend) |
| AUTH-06 | Magic link prihlaseni | P2 | Phase 2 (vyzaduje email service) |

#### 4.2 Workspace (Firma) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ORG-01 | Vytvoreni workspace | P0 | Novy uzivatel vytvori firmu (nazev, slug) |
| ORG-02 | Nastaveni workspace | P0 | Editace nazvu, popisu, loga |
| ORG-03 | Pozvani clenu | P0 | Pozvanka emailem + RBAC role |
| ORG-04 | Sprava clenu | P0 | Zmena role, odebreni clena |
| ORG-05 | RBAC role | P0 | Owner, Admin, Manager, Member, Viewer |
| ORG-06 | Smazani workspace | P1 | Soft delete + grace period (GDPR) |

#### 4.3 Crews (Oddeleni) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| TEAM-01 | Vytvoreni crew | P0 | Nazev, popis, barva/ikona |
| TEAM-02 | Prirazeni clenu do crew | P0 | Manager priradi uzivatele do crew |
| TEAM-03 | Seznam crews | P0 | Dashboard s prehledem crews |
| TEAM-04 | Editace crew | P0 | Zmena nazvu, popisu, clenu |
| TEAM-05 | Smazani crew | P1 | Soft delete (agenti se deaktivuji) |
| TEAM-06 | Sablony crew | P1 | Preddefinovane sablony ("IT Firma", "Obchod", "Podpora") |

#### 4.4 Agenti (Virtualni zamestnanci) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| AGENT-01 | Vytvoreni agenta | P0 | Jmeno, popis, role, prirazeni do crew |
| AGENT-02 | Konfigurace agenta | P0 | CLI adapter (Claude Code/OpenCode/Codex/Gemini), LLM model, system prompt, temperature, max tokens |
| AGENT-03 | Prirazeni skillu | P0 | Vyber skillu z registru a prirazeni agentovi |
| AGENT-04 | Prirazeni credentials | P0 | Vyber credentials z vaultu pro agenta |
| AGENT-05 | Spusteni agenta | P0 | Start agent session (crewshipd job orchestrace) |
| AGENT-06 | Zastaveni agenta | P0 | Graceful stop + force kill |
| AGENT-07 | Status agenta | P0 | Idle, Running, Error, Stopped |
| AGENT-08 | Seznam agentu v crew | P0 | Prehled agentu s jejich statusem |
| AGENT-09 | Timeout per agent | P0 | Max doba behu (safety) |
| AGENT-10 | Editace agenta | P0 | Zmena konfigurace |
| AGENT-11 | Smazani agenta | P1 | Soft delete |
| AGENT-12 | Sablony agentu | P1 | Preddefinovane role ("Obchodnik", "DevOps", "QA") |

#### 4.5 Skills (Dovednosti) [P0]

> **Architekturni poznamka:** Skills v Crewship jsou inspirovane OpenClaw modelem.
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
| CRED-05 | Scope credentials | P0 | Workspace-level vs Crew-level credentials |
| CRED-06 | Rotace credential | P1 | Update hodnoty bez zmeny reference |
| CRED-07 | Credential pool | P1 | Vice klicu pro stejny env var s priority (round-robin/failover) |

#### 4.7 Gateway (WebSocket Server) [P0]

> **Architekturni poznamka:** Gateway je nativni Go WebSocket server v crewshipd.
> Typed WebSocket protokol: `{type:"req", method, params}` → `{type:"res", ok, payload}`.
> Routuje zpravy z web UI (a pozdeji Discord/Telegram) na agenty.
> Kazdy workspace/crew ma izolovanou session.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| GW-01 | WebSocket server | P0 | Nativni Go WebSocket (goroutines), typed protokol (req/res/event) |
| GW-02 | Multi-tenant routing | P0 | Izolace sessions per workspace/crew |
| GW-03 | Autentizace | P0 | JWT token validace na connect |
| GW-04 | Agent events | P0 | Streaming agent outputu jako events |
| GW-05 | Health/heartbeat | P0 | Connection health monitoring |
| GW-06 | Reconnect handling | P1 | Automaticky reconnect po vypadku |

#### 4.8 Chat UI (Konverzace s agentem) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| CHAT-01 | Synchronni chat | P0 | Uzivatel pise → agent odpovida v realnem case |
| CHAT-02 | Streaming odpovedi | P0 | Block streaming pres Gateway WS events |
| CHAT-03 | Historie konverzaci | P0 | Seznam predchozich sessions (PostgreSQL/SQLite metadata, JSONL v workspace) |
| CHAT-04 | Kontext konverzace | P0 | Agent vidi historii aktualniho chatu (workspace) |
| CHAT-05 | Markdown rendering | P0 | Odpovedi agenta formatovane (code, bold, lists) |
| CHAT-06 | Code block s kopirovanim | P0 | Zvyrazneni syntaxe + copy button |
| CHAT-07 | Agent status indikator | P0 | "Thinking...", "Running tool...", "Done" |
| CHAT-08 | Tool execution vizualizace | P1 | Zobrazeni jake tools agent pouziva (exec, web_search...) |

#### 4.9 Agent Runtime (Backend) [P0]

> **Architekturni poznamka:** Agent = CLI nastroj (Claude Code, Codex CLI, Gemini CLI)
> bezici v Docker kontejneru s izolovanym workspace. Platforma NEVOLA LLM API primo --
> spousti CLI nastroje ktere to delaji samy. Kazdy agent ma svuj workspace adresar
> s bootstrap soubory (AGENTS.md, SOUL.md, TOOLS.md, IDENTITY.md).
>
> **Non-interactive mody (overeno Feb 2026):**
> - Claude Code: `claude -p "prompt"` + pipe
> - OpenCode: `opencode run "prompt"` + HTTP REST API (`opencode serve`)
> - Codex CLI: dedicated non-interactive mode + JSON-RPC API
> - Gemini CLI: `gemini -p "prompt"` + "yolo" mode
>
> **Izolace:** Docker kontejner per agent (read-only root, cap-drop ALL, izolovaná sít).
> Go crewshipd ridi lifecycle pres Docker SDK for Go.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| RUN-01 | Job orchestrace (crewshipd) | P0 | Agent spousteni pres Go orchestrator (bbolt WAL durable state) |
| RUN-02 | CLI adapter pattern | P0 | Pluginovatelne adaptery: Claude Code, OpenCode, Codex CLI, Gemini CLI |
| RUN-03 | Agent workspace setup | P0 | Vytvoreni workspace adresare s bootstrap soubory |
| RUN-04 | CLI session management | P0 | Spusteni CLI v Docker kontejneru, lifecycle management (Docker SDK for Go) |
| RUN-05 | Stdout/stderr streaming | P0 | Real-time output z CLI do Gateway (Go WebSocket events) |
| RUN-06 | Session transcripts | P0 | Dual storage: DB metadata + JSONL v workspace (kompatibilita s OpenClaw, debug) |
| RUN-07 | Credentials injection | P0 | ENV vars z vaultu pri startu kontejneru |
| RUN-08 | Docker kontejner izolace | P0 | Docker per agent. Read-only root, cap-drop ALL, izolovaná sit |
| RUN-09 | Agent error handling | P0 | Retry logika, timeout, error reporting |
| RUN-10 | Graceful shutdown | P0 | SIGTERM → SIGKILL po timeout |
| RUN-11 | Rate limit failover | P1 | Automaticke prepnuti klice pri 429 error (credential pool) |

#### 4.10 Log Viewer [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| LOG-01 | Real-time log stream | P0 | WebSocket tail logu agenta (Go → JSONL → WS event) |
| LOG-02 | Filtrovani logu | P1 | Podle agenta, crew, severity |
| LOG-03 | Hledani v lozich | P1 | Fulltext search |

#### 4.11 Dashboard [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| DASH-01 | Crew overview | P0 | Pocet agentu, aktivnich tasku, posledni aktivita |
| DASH-02 | Agent status cards | P0 | Karty agentu s jejich stavem |
| DASH-03 | Quick actions | P1 | Rychle spusteni agenta, prechod do chatu |

#### 4.12 Audit Log [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| AUDIT-01 | Logovani state-changing akci | P0 | CRUD operace, spusteni agenta, zmena konfigurace |
| AUDIT-02 | Zobrazeni audit logu | P0 | Tabulka s filtry (kdo, co, kdy) |
| AUDIT-03 | Audit per workspace | P0 | Izolace logu per workspace |

#### 4.13 Onboarding + Blueprints [P0-P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ONBOARD-01 | Guided wizard (free tier) | P0 | Krok po kroku: workspace → crew → agent → skill → chat |
| ONBOARD-02 | Crew Blueprint picker | P1 | Vyber sablony crew ("SEO Crew", "DevOps Crew", "Support Crew") |
| ONBOARD-03 | Workspace Blueprint picker | P1 | Vyber sablony celeho workspace ("Marketing Agency", "IT Startup", "Solo Dev") |
| ONBOARD-04 | Blueprint marketplace | P2 | Komunitni sdileni blueprintu (rating, klonování, YAML definice) |
| ONBOARD-05 | Blueprint export/import | P2 | Export vlastniho blueprintu, import z URL/souboru |

> **Blueprinty fungují na dvou úrovních:**
>
> **Crew Blueprint:** Predkonfigurovana crew s agenty, skills, prompty, credential placeholdery.
> Příklad: "SEO Crew" = Lead (Content Strategist) + Agent (SEO Writer) + Agent (Keyword Researcher).
>
> **Workspace Blueprint:** Predkonfigurovany cely workspace s vice crews.
> Příklad: "Marketing Agency" = Content Crew + Analytics Crew + Social Media Crew (9 agentů celkem).
>
> Distribuce: YAML soubory, stejny marketplace jako skills (komunitni rating, Official/Verified/Community badges).

#### 4.14 Stripe Billing [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| BILL-01 | Stripe integrace | P1 | Subscription CRUD (create, update, cancel) |
| BILL-02 | Plan tiers v DB | P1 | Free, Crew, Enterprise s limity |
| BILL-03 | Enforcement limitu | P1 | Pocet agentu, crews, skillu per plan |
| BILL-04 | Billing portal | P1 | Stripe Customer Portal (faktury, platebni metody) |
| BILL-05 | Webhook handler | P1 | Stripe eventy (payment succeeded, subscription updated) |

#### 4.15 Single Binary Distribuce [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| DIST-01 | Single binary build (GoReleaser) | P0 | Cross-platform Go binary (linux/darwin/windows, amd64/arm64) |
| DIST-02 | brew tap | P0 | `brew install crewship` (Homebrew formula) |
| DIST-03 | curl installer script | P0 | `curl -fsSL https://get.crewship.ai \| sh` |
| DIST-04 | `crewship start/stop/status/logs` CLI | P0 | Kompletni CLI pro spravu platformy |
| DIST-05 | Embedded Next.js (embed.FS) | P0 | Static Next.js build embeddovany v Go binary |
| DIST-06 | SQLite default DB | P0 | Zero-deps databaze (~/.crewship/crewship.db), PostgreSQL opt-in |
| DIST-07 | Auto-update mechanism | P1 | `crewship update` + go-selfupdate |

#### 4.16 Per-Agent Network Control [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| NET-01 | Per-agent internet ON/OFF | P0 | Kazdy agent ma individualne nastavitelny pristup k internetu |
| NET-02 | Per-agent domain whitelist | P0 | Povoleni jen konkretnich domen (google.com, api.openai.com) |
| NET-03 | Per-agent local network access | P1 | Pristup k lokalni siti (CIDR rozsah, napr. 192.168.1.0/24) |
| NET-04 | Network control UI | P0 | Klikaci konfigurace v dashboardu (zadne iptables rucne) |

#### 4.17 Per-Agent Cost Control [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| COST-01 | Per-agent budget limits | P0 | Maximalní utrata za den/tyden/mesic per agent |
| COST-02 | Cost alerting | P0 | Notifikace kdyz agent prekroci % rozpoctu (50%, 80%, 100%) |
| COST-03 | Usage dashboard | P1 | Prehled nakladu per agent/crew/workspace |

#### 4.18 Skills Marketplace [P0-P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| MARKET-01 | Browse skillu | P0 | Skill Store UI v dashboardu (kategorie, tagy, hledani) |
| MARKET-02 | One-click install | P0 | Instalace skillu z marketplace jednim klikem |
| MARKET-03 | Rating/review | P2 | Hodnoceni skillu komunitou |
| MARKET-04 | Publikace skillu | P2 | Upload vlastniho skillu do marketplace |
| MARKET-05 | Skill sandbox enforcement | P0 | Kazdy skill deklaruje permissions, Docker je vynucuje |
| MARKET-06 | Skill permissions model | P0 | Granularni: filesystem (r/w/none), network (on/off/whitelist), secrets (list) |
| MARKET-07 | Official/Verified/Community badges | P0 | Kvalitni signaly: Official = rucne reviewed, Verified = auto-scan passed, Community = user-submitted |
| MARKET-08 | Revenue sharing pro skill autory | P2 | Community autori dostavaji podil z Crew/Enterprise tier |

#### 4.19 Webhooky [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| WEBHOOK-01 | Webhook ingress endpoint | P0 | POST /api/v1/webhooks/{crew}/{agent}/trigger -- externi triggery (Grafana, n8n, Make) |
| WEBHOOK-02 | Webhook secret per agent | P0 | Kazdy agent ma unikatni webhook secret pro autentizaci |
| WEBHOOK-03 | Webhook retry/delivery log | P1 | Log vsech prichozcich webhooku, retry logika |

#### 4.20 File Management [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| FILE-01 | Persistent output storage | P0 | /output/ bind mount -- soubory prezijou restart i smazani crew |
| FILE-02 | File browser v UI | P0 | Stromovy prohlizec souboru agenta s download tlacitkem |
| FILE-03 | File preview | P1 | PDF, Markdown, obrazky primo v prohlizeci |
| FILE-04 | fsnotify notifikace | P0 | Real-time WebSocket notifikace pri vytvoreni/zmene souboru |

#### 4.21 Monitoring [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| MONITOR-01 | cAdvisor container metrics | P0 | CPU, RAM, disk, network per agent/kontejner |
| MONITOR-02 | Web terminal | P0 | xterm.js -- SSH-like pristup do kontejneru z prohlizece |
| MONITOR-03 | Agent activity stream | P0 | Real-time feed akci agenta (stdout pres WebSocket) |
| MONITOR-04 | Prometheus /metrics | P1 | Go service vystavuje metriky pro Prometheus/Grafana |

#### 4.22 Go Service (crewshipd) [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| GO-01 | WebSocket gateway | P0 | Nativni Go WebSocket server (goroutines) |
| GO-02 | Docker orchestrator | P0 | Container lifecycle pres Docker SDK for Go |
| GO-03 | Log collector | P0 | Docker stdout → JSONL soubory + logrotate |
| GO-04 | bbolt WAL | P0 | Durable job state, prezije crash Go service |
| GO-05 | Unix socket IPC | P0 | Komunikace s Next.js pres /tmp/crewship.sock |
| GO-06 | Graceful shutdown | P0 | SIGTERM handling, flush logy, stop kontejnery |
| GO-07 | Config YAML | P1 | Jeden konfiguracni soubor misto env vars |

#### 4.23 Docker Infrastructure [P0]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| DOCKER-01 | docker-compose.yml (dev) | P0 | PostgreSQL 16 pro lokalni dev |
| DOCKER-02 | .env.example | P0 | Vsechny env vars s komentari |
| DOCKER-03 | Self-hosting dokumentace | P1 | Krok po kroku navod |
| DOCKER-04 | Full docker-compose (prod) | P1 | Crewship + PostgreSQL (alternativa k single binary) |
| DOCKER-05 | Container lifecycle (TTL) | P1 | Auto-shutdown idle kontejneru |
| DOCKER-06 | Skill dependencies init | P0 | Init skript instaluje apt/pip/npm balicky pri prirazeni skillu |
| DOCKER-07 | Host resource check | P0 | Go service kontroluje dostupne RAM/CPU pred vytvorenim kontejneru |

---

### PHASE 2: Rozsireni (8-12 tydnu po MVP)

**Cil:** Messaging kanaly, orchestrace, meta-agent, marketplace community submissions, vice LLM provideru.

#### 4.24 Asynchronni rezimy [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ASYNC-01 | Task mode | P1 | Uzivatel zada ukol, agent bezi na pozadi |
| ASYNC-02 | Task status tracking | P1 | Pending → Running → Completed/Failed |
| ASYNC-03 | Task vysledky | P1 | Zobrazeni vysledku po dokonceni |
| ASYNC-04 | Human-in-the-loop (approval flow) | P0 | Agent pozastavi a ceka na schvaleni nebezpecne akce |
| ASYNC-05 | Trust levels per agent | P0 | LOW/MEDIUM/HIGH/CUSTOM granularita schvalovani |
| ASYNC-06 | Approval via messaging channels | P1 | Schvaleni pres WhatsApp/Discord/Telegram (viz MSG-08) |

> **Approval flow detaily (ASYNC-04 + ASYNC-05):**
>
> Kazdy agent ma konfigurovatelny **Trust Level**:
>
> | Level | Chovani | Priklad |
> |---|---|---|
> | `LOW` (auto) | Auto-approve vse (file write, search, read, API calls) | Trusted agent, rutinni ukoly |
> | `MEDIUM` (default) | Auto-approve bezpecne akce, approve destructivni | Default pro nove agenty |
> | `HIGH` (paranoid) | Approve kazdou akci (kazdy tool call) | Testovani, nebezpecne prostredi |
> | `CUSTOM` | Pravidla jako firewall rules (per-action) | Pokrocili uzivatele |
>
> **MEDIUM (default) pravidla:**
> - Auto-approve: file read, file write do /output/, web search, grep
> - Require approval: git push, external API call, file delete mimo /output/, bash s sudo
> - Block always: network access mimo whitelist, escalace na root
>
> **Approval flow:**
> 1. Agent vola tool → crewshipd zkontroluje trust level pravidla
> 2. Pokud vyzaduje approval → agent se pozastavi (AWAITING_APPROVAL status)
> 3. crewshipd posle approval request do VSECH nakonfigurovanych kanalu:
>    - Crewship UI chat (vzdy, default)
>    - Messaging kanal (Discord/Telegram/Slack/WhatsApp pokud nakonfigurovano)
>    - Email (SMTP, pokud nakonfigurovano)
>    - Webhook (custom endpoint)
> 4. Uzivatel odpovi v JAKEMKOLI kanalu → approval se propaguje
> 5. Agent pokracuje (APPROVED) nebo se zastavi (REJECTED)
> 6. Timeout: konfigurovatelny (default 30 min) → auto-reject

#### 4.25 M:1 Kolaborace [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| COLLAB-01 | Sdileny chat | P1 | Vice uzivatelu pise jednomu agentovi |
| COLLAB-02 | Identifikace uzivatelu | P1 | Agent vi kdo mu pise |
| COLLAB-03 | Real-time sync | P1 | Vsichni vidi zpravy ostatnich okamzite |

#### 4.26 Orchestrace: Crew Lead + Coordinator [P1]

> **Architektura:** 3-urovnova hierarchie (Coordinator → Lead → Agent) inspirovana
> realnymi firemnimi strukturami. Uzivatel si primarne povida s Crew Leadem sve crew.
> Pro cross-crew otazky existuje Coordinator na urovni workspace.
> Plna specifikace: **`prd/ORCHESTRATION.md`**

**Phase 2A: Crew Lead (in-crew orchestrace)**

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ORCH-01 | AgentRole enum (AGENT/LEAD/COORDINATOR) | P1 | Novy enum + DB migrace |
| ORCH-02 | Lead designation UI | P1 | Oznaceni agenta jako leada v crew settings (max 1 per crew) |
| ORCH-03 | Auto-generated lead system prompt | P1 | System prompt s informacemi o crew |
| ORCH-04 | Assignment protokol (stdout parsing) | P1 | Parsovani @assign/@ask prikazu ze stdout leada v crewshipd |
| ORCH-05 | Lead → Agent assignment | P1 | Docker exec orchestrace |
| ORCH-06 | agents_list | P1 | Lead vidi seznam agentu ktere muze targetnout |
| ORCH-07 | Assignment tabulka | P1 | Auditovani vsech assignments |
| ORCH-08 | Lead auto-routing | P1 | Uzivatel pise do crew → lead rozhodne komu pridelit |
| ORCH-09 | Paralelni assignment | P1 | wait_group pattern pro vice agentu soucasne |
| ORCH-10 | Error handling + fallback | P1 | Lead reaguje na selhani agenta |
| ORCH-11 | Lead summary/agregace | P1 | Lead shrnuje vysledky pred odeslani uzivateli |

**Phase 2B: Coordinator (cross-crew orchestrace)**

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ORCH-12 | Coordinator agent role | P1 | Specialni agent na urovni workspace (max 1 per workspace) |
| ORCH-13 | Coordinator lightweight execution | P1 | LLM call bez Docker kontejneru (jen reasoning + assignment) |
| ORCH-14 | Coordinator → Lead assignment | P1 | Cross-crew orchestrace pres leady |
| ORCH-15 | Coordinator auto-routing | P1 | Coordinator rozhodne kterou crew oslovit |
| ORCH-16 | Cross-crew agregace | P1 | Coordinator sbira odpovedi od vice crews |
| ORCH-17 | Coordinator UI (dashboard card + chat) | P1 | Karta coordinatora na dashboardu, dedicovany chat |

**Phase 3: Pokrocila orchestrace**

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ORCH-18 | Lead ↔ Lead primo | P2 | Cross-crew komunikace bez coordinatora |
| ORCH-19 | Coordinator s tools + kontejnerem | P2 | Coordinator dostane vlastni kontejner a nastroje |
| ORCH-20 | Coordinator long-term memory | P2 | Strategicke cile, KPIs, trendy workspace |
| ORCH-21 | Orchestracni vizualizace (OrchVis) | P2 | Real-time graf assignments |
| ORCH-22 | Auto-lead election | P2 | AI doporuci ktery agent by mel byt lead |
| ORCH-23 | Coordinator → Coordinator (multi-workspace) | P3 | Spoluprace mezi workspaces pres webhooky |

#### 4.27 Scheduled Missions (Cron) [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| CRON-01 | Schedule per agent | P1 | Nastaveni cron vyrazu nebo lidsky jazyk ("every Monday 8:00") |
| CRON-02 | Schedule management UI | P1 | Kalendarovy pohled + pridani/editace/smazani schedulu |
| CRON-03 | Run history | P1 | Historie automatickych spusteni s vysledky |
| CRON-04 | Scheduled missions | P1 | Cron spusti celou mission (Lead koordinuje agenty dle sablony) |
| CRON-05 | Natural language schedule | P2 | "Kazde pondeli rano priprav weekly report" → cron expression |

> **Implementace:** Go `github.com/robfig/cron` scheduler v crewshipd.
> Scheduled mission = ulozenou mission sablonu + cron trigger.
> Uzivatel nastavi jednou, Crewship pracuje non-stop (24/7 AI employee pitch).

#### 4.28 Agent Loop Modes [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| RUN-12 | Agent loop mode | P2 | Opakovany beh agenta: once (default), loop (interval), until (podminka) |
| RUN-13 | Completion criteria | P2 | Agent bezi dokud neni splnena podminka |
| RUN-14 | Context preservation | P2 | JSONL catch-up pri restartu -- agent pokracuje kde skoncil |

#### 4.29 Vice LLM Provideru [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| LLM-01 | Anthropic (Claude) | P1 | Claude API integrace |
| LLM-02 | Ollama (local) | P1 | Lokalni LLM pro self-hosting |
| LLM-03 | LLM adapter pattern | P1 | Jednotne rozhrani pro vsechny providery |

#### 4.30 Messaging Channel Gateway [P1]

> **Architektura:** Messaging kanaly jsou **opt-in modul v crewshipd** (Go), NE skills.
> Duvod: messaging session musi byt persistent long-running process s otevrenymi
> connections. Skill je ephemeral (spusti se, udela praci, skonci). Gateway musi
> bezet non-stop a routovat zpravy ke spravnym agentum/crews.
>
> **Inspirace:** OpenClaw Gateway -- centralni daemon co vlastni vsechny messaging
> sessions (WhatsApp pres Baileys/whatsmeow, Telegram pres grammY, Slack, Discord).
> Crewship prebira tento pattern ale integruje ho do existujiciho crewshipd procesu.

| ID | Feature | Priorita | Popis |
|---|---|---|---|
| MSG-01 | Channel Gateway modul v crewshipd | P1 | Persistent long-running messaging sessions (ne skill) |
| MSG-02 | Discord integrace | P1 | discordgo bot — agent odpovida v Discord kanalu |
| MSG-03 | Telegram integrace | P1 | go-telegram-bot-api — agent odpovida v Telegram chatu |
| MSG-04 | Slack integrace | P1 | slack-go bot — agent v Slack workspace |
| MSG-05 | WhatsApp integrace | P2 | whatsmeow (Go Baileys) — QR code pairing, WA Business API |
| MSG-06 | Channel adapter pattern | P1 | Jednotne rozhrani pro kanaly (ChannelProvider interface) |
| MSG-07 | Message routing | P1 | Incoming zprava z kanalu → spravny agent/crew (slug matching) |
| MSG-08 | Approval pres messaging | P1 | Agent posle approval request → uzivatel odpovi v kanalu → propagace |
| MSG-09 | Channel konfigurace UI | P1 | Nastaveni kanalu v workspace settings (API klice, bot tokeny) |

```
crewshipd (Go service)
  ├── WebSocket gateway (existujici — agent ↔ UI)
  ├── Docker orchestrator (existujici)
  └── [Phase 2] Channel Gateway (opt-in)
        ├── Discord   (discordgo)
        ├── Telegram   (go-telegram-bot-api)
        ├── Slack      (slack-go)
        ├── WhatsApp   (whatsmeow, Phase 2B)
        └── Custom webhook (incoming/outgoing)
```

#### 4.31 Crewship AI (Meta-agent) [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| META-01 | Konverzacni onboarding | P1 | AI se pta co uzivatel potrebuje a nastavuje |
| META-02 | Znalost architektury | P1 | Meta-agent zna celou platformu |
| META-03 | Generovani skill.yaml | P1 | Meta-agent vytvori skill sablonu |
| META-04 | Debugging agenta | P1 | Meta-agent pomaha resit problemy |

#### 4.32 Verejne REST API [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| API-01 | API klice | P1 | Generovani a sprava API klicu |
| API-02 | REST endpointy | P1 | CRUD pro agenty, tasky, skills pres API |
| API-03 | Webhooky | P1 | agent.completed, agent.failed, agent.needs_approval |
| API-04 | API dokumentace | P1 | OpenAPI/Swagger spec |

#### 4.33 Git-like Verzovani Konfigurace [P1]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| VER-01 | Config history | P1 | Kazda zmena konfigurace agenta = nova verze |
| VER-02 | Diff view | P1 | Porovnani dvou verzi konfigurace |
| VER-03 | Rollback | P1 | Navrat k predchozi verzi konfigurace |

---

### PHASE 3: Enterprise (8-12 tydnu po Phase 2)

#### 4.34 K8s Agent Sandbox [P2]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| K8S-01 | Dedicated kontejner per agent | P2 | Izolace agentu v K8s (Helm chart) |
| K8S-02 | gVisor/Kata izolace | P2 | Extra security pro agent runtime |
| K8S-03 | Resource limity | P2 | CPU/RAM per kontejner |
| K8S-04 | Network policies | P2 | K8s NetworkPolicy per agent |

#### 4.35 RAG Konektor [P2]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| RAG-01 | pgvector integrace | P2 | Vektorova DB pro agent memory |
| RAG-02 | Document upload | P2 | Nahrani dokumentu do knowledge base |
| RAG-03 | Automaticke chunkovani | P2 | Rozdeleni dokumentu na chunky + embedding |

#### 4.36 Enterprise Features [P2]
| ID | Feature | Priorita | Popis |
|---|---|---|---|
| ENT-01 | SSO (SAML/OIDC) | P2 | Enterprise prihlaseni (Okta, Azure AD) |
| ENT-02 | Compliance reporting | P2 | Export audit logu, data retention, data residency |
| ENT-03 | Advanced billing/metering | P2 | Per-token billing, usage dashboardy |
| ENT-04 | Plne audit logy | P2 | Vcetne LLM promptu a responsu |
| ENT-05 | SLA monitoring | P2 | Uptime, response time metriky |
| ENT-06 | GPU node support | P2 | Lokalni LLM pres Ollama na dedicated GPU |

#### 4.37 Pokrocile Skills [P2]
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

### US-01: Instalace a prvni spusteni (NOVY)
**Jako** vyvojar na laptope
**chci** nainstalovat Crewship jednim prikazem
**abych** mel beziciho AI agenta za 60 sekund.

**Acceptance criteria:**
1. `brew install crewship` nainstaluje single binary
2. `crewship start` spusti platformu (SQLite, localhost:3001)
3. Prohlizec se otevre s onboarding wizardem
4. Do 60 sekund od prvniho prikazu je agent pripraveny na chat
5. `crewship stop` ciste zastavi vse

### US-02: Registrace a prvni firma
**Jako** novy uzivatel
**chci** se zaregistrovat a vytvorit workspace
**abych** mohl zacit pouzivat platformu.

**Acceptance criteria:**
1. Uzivatel se registruje pres email+heslo nebo OAuth
2. Po prvnim prihlaseni se spusti guided wizard
3. Wizard vyzve k vytvoreni workspace (nazev, slug)
4. Workspace se vytvori a uzivatel je automaticky Owner
5. Uzivatel je presmerovan na dashboard

### US-03: Pozvani clena do workspace
**Jako** Owner/Admin
**chci** pozvat kolegu do workspace
**abych** mohl delegovat spravu agentu.

**Acceptance criteria:**
1. Owner/Admin vlozi email a vybere roli (Admin/Manager/Member/Viewer)
2. System posle email s pozvankou
3. Pozvaný se registruje/prihlasi a je automaticky prirazen do workspace
4. Zobrazi se v seznamu clenu s prirazenu roli

### US-04: Vytvoreni crew
**Jako** Owner/Admin/Manager
**chci** vytvorit crew (oddeleni)
**abych** organizoval agenty do logickych skupin.

**Acceptance criteria:**
1. Uzivatel vytvori crew (nazev, popis, volitelne z sablony)
2. Priradi cleny z workspace do crew
3. Crew se zobrazi na dashboardu
4. Clenove crew vidi agenty v ni

### US-05: Vytvoreni agenta
**Jako** Owner/Admin/Manager (v prirazene crew)
**chci** vytvorit virtualniho zamestnance
**abych** mohl automatizovat ukoly.

**Acceptance criteria:**
1. Uzivatel vytvori agenta v crew (jmeno, popis, role)
2. Vybere CLI adapter (Claude Code, OpenCode, Codex CLI, Gemini CLI) a LLM model
3. Nastavi system prompt (instrukce pro agenta)
4. Nastavi sitovy pristup (internet on/off, whitelist)
5. Nastavi cost budget (volitelne)
6. Agent se zobrazi v seznamu agentu crew se statusem "Idle"

### US-06: Instalace a prirazeni skillu
**Jako** Owner/Admin/Manager
**chci** pridat dovednosti agentovi
**abych** rozsirit jeho schopnosti.

**Acceptance criteria:**
1. Uzivatel prohlizi Skill Store (kategorie, hledani, Official/Verified/Community badges)
2. One-click instalace skillu z marketplace
3. Priradi skill agentovi
4. Zobrazi se permissions skillu (filesystem, network, secrets)
5. Uzivatel je vyzvan k pridani chybejicich credentials

### US-07: Sprava credentials
**Jako** Owner/Admin/Manager
**chci** bezpecne ulozit API klice
**abych** je mohl pouzit pro agenty.

**Acceptance criteria:**
1. Uzivatel prida credential (nazev + hodnota)
2. Hodnota se okamzite zasifruje (AES-256-GCM)
3. V seznamu se zobrazuje jen nazev + ***
4. Credential je dostupny pro prirazeni agentovi
5. Pri spusteni agenta se injektuje jako ENV var do kontejneru

### US-08: Chat s agentem
**Jako** Member (v prirazene crew)
**chci** konverzovat s agentem v chat UI
**abych** mohl zadat ulohu a sledovat jeji plneni.

**Acceptance criteria:**
1. Uzivatel otevra chat s agentem
2. Pise zpravu a odesle
3. Agent zpracovava a odpovida v realnem case (streaming pres Go WS)
4. Odpoved je formatovana (markdown, code blocks)
5. Zobrazuje se status ("Thinking...", "Running tool...", "Done")
6. Konverzace se uklada a je dostupna v historii

### US-09: Sledovani logu
**Jako** Manager/Admin
**chci** videt logy agenta v realnem case
**abych** mohl sledovat co agent dela a debugovat problemy.

**Acceptance criteria:**
1. Uzivatel otevra log viewer
2. Vidi real-time stream strukturovanych logu (JSONL)
3. Kazdy log entry ma: timestamp, agent, akce, vysledek
4. Muze filtrovat podle agenta a severity

### US-10: Audit log
**Jako** Owner/Admin
**chci** videt vsechny dulezite akce ve workspace
**abych** mel prehled kdo co udelal.

**Acceptance criteria:**
1. Kazda state-changing akce se loguje (kdo, co, kdy, odkud)
2. Audit log je dostupny v admin sekci
3. Lze filtrovat podle uzivatele, typu akce, casu
4. Audit log je izolovany per workspace

### US-11: Dashboard
**Jako** prihlaseny uzivatel
**chci** videt prehled svych crews a agentu
**abych** mel rychly prehled o stavu.

**Acceptance criteria:**
1. Po prihlaseni vidim crew overview
2. Kazda crew ukazuje: pocet agentu, aktivnich tasku, posledni aktivitu
3. Agent karty ukazuji status (Idle/Running/Error)
4. Kliknutim se dostanu na detail crew/agenta

---

## 6. CO NENI V MVP (EXPLICITNI VYLOUCENI)

| Feature | Proc ne v MVP | Faze |
|---|---|---|
| M:1 chat (vice uzivatelu na agenta) | Architektonicky slozite (real-time sync) | Phase 2 |
| Asynchronni task mode | Vyzaduje task management UI | Phase 2 |
| Human-in-the-loop schvalovani | Vyzaduje approval workflow | Phase 2 |
| Inter-agent komunikace/orchestrace | Nejslozitejsi cast, navrh protokolu | Phase 2 |
| Cron joby pro agenty | Scheduler infrastruktura | Phase 2 |
| Verejne REST API | API key management + rate limiting | Phase 2 |
| Git-like verzovani konfigurace | Config history + diff UI | Phase 2 |
| Vice LLM provideru (Anthropic, Ollama) | LLM adapter pattern | Phase 2 |
| Discord/Telegram integrace | Channel adapter pattern | Phase 2 |
| Community skill submissions | Review pipeline | Phase 2 |
| Crewship AI meta-agent | Vyzaduje hlubokou znalost platformy | Phase 2 |
| RLS politiky (Row-Level Security) | CASL staci pro MVP, RLS jako defense-in-depth | Phase 2 |
| K8s Agent Sandbox | K8s Pods, gVisor, dedicovane resources | Phase 3 |
| RAG/pgvector | Vektorova DB pipeline | Phase 3 |
| SSO (SAML/OIDC) | Enterprise auth | Phase 3 |
| WhatsApp Business API | Oficialni API = placene | Phase 3 |
| Mobilni app | React Native | Phase 4 |

---

## 7. NEFUNKCIONALNI POZADAVKY

### 7.1 Vykon
- Chat response: prvni token do 2s od odeslani zpravy
- Dashboard load: pod 1s (server-side rendering)
- Agent startup: pod 5s od spusteni
- `crewship start`: platforma pripravena pod 10s (SQLite mode)
- Concurrent agents: 5 per instance (single binary), 20+ s PostgreSQL + horizontalnim scalingem

### 7.2 Bezpecnost
- Vsechny credentials sifrovane AES-256-GCM at rest
- MVP: CASL RBAC kontrola na KAZDEM API endpointu (aplikacni uroven)
- Phase 2: RLS politiky na VSECH tabulkach (defense-in-depth)
- **Docker kontejner izolace** -- agent bezi v izolovanem kontejneru (read-only root, cap-drop ALL, no-new-privileges, non-root UID 1001)
- **Per-agent network izolace** -- individualni Docker network policies (internet, whitelist, LAN)
- **Skill sandbox** -- kazdy skill deklaruje permissions, Docker vynucuje
- Agent nikdy nevidi plaintext credentials
- Audit log pro vsechny state-changing akce (append-only)
- OWASP Top 10 pokryte (XSS, CSRF, injection, atd.)

### 7.3 Skalovatlenost
- Free tier: 1 instance, SQLite, 1-10 uzivatelu
- Crew tier: PostgreSQL, 5-50 uzivatelu, horizontalni scaling
- Enterprise tier: K8s Helm chart, 100+ uzivatelu, dedicated resources

### 7.4 Dostupnost
- Free (self-hosted): zavisla na infrastrukture uzivatele
- Crew (cloud): 99.5% uptime
- Enterprise: 99.9% uptime (SLA)

### 7.5 Kompatibilita
- Browsery: Chrome, Firefox, Safari, Edge (posledni 2 verze)
- Responsivni design (mobil, tablet, desktop)
- OS: macOS (arm64/amd64), Linux (arm64/amd64), Windows (amd64)
- Prerekvizita: Docker (pro agent kontejnery)

### 7.6 GDPR / Ochrana dat
- Pravo na vymazani uctu + vsech dat (30 dni grace period)
- Export dat (JSON/CSV)
- Data retention politiky (konfigurovatelne per workspace)
- Zpracovani pouze v EU/US (konfigurovatelne)

---

## 8. TECHNOLOGICKE ROZHODNUTI

### Dvoujazycna architektura (KRITICKE ROZHODNUTI)

Crewship je **dvoujazycny projekt** -- TypeScript (Next.js) pro UI, API a auth + Go (crewshipd) pro runtime, WebSocket, Docker orchestraci, logy a soubory.

```
TypeScript (Next.js):  UI, CRUD API routes, auth (NextAuth.js), Prisma ORM
Go (crewshipd):        WebSocket gateway, Docker orchestrace, log collector,
                       file server (fsnotify), webhook ingress, bbolt WAL,
                       per-agent network policies, skill sandbox enforcement
```

Komunikace: Unix socket `/tmp/crewship.sock` (lokalni dev), gRPC (K8s).

### Proc dva jazyky

| Duvod | Detail |
|---|---|
| Go = systemovy jazyk | Docker SDK, goroutines pro WS, nativni binary, embed.FS |
| TS = produktivita UI | Next.js ekosystem, React, Prisma, shadcn/ui |
| Single binary | Go binary embeduje static Next.js build (embed.FS) |
| Performance | Go WS gateway zvladne 10k+ concurrent connections |
| Bottleneck neni jazyk | LLM latence a CLI startup >> runtime overhead |

### Frontend
- **Next.js** (App Router, RSC) -- frontend + REST API routes
- **React** -- latest stable
- **Tailwind CSS 4** -- CSS-first konfigurace, `@theme inline` v `app/globals.css`, oklch tokens
- **shadcn/ui** (new-york) -- jedina povolena UI knihovna
- **Zustand** -- client state management
- **lucide-react** -- jedina povolena ikona knihovna

### Backend (TypeScript)
- **NextAuth.js v5** (Auth.js) -- auth s Prisma adapterem (email+heslo, Google OAuth, GitHub OAuth)
- **Prisma** -- ORM, schema = source of truth. Podporuje PostgreSQL i SQLite.
- **Zod** -- runtime validace
- **CASL** -- RBAC (jedina autorizacni vrstva v MVP, RLS az Phase 2)

### Backend (Go -- crewshipd)
- **Go stdlib** + minimalni zavislosti (zadne frameworky)
- **Nativni WebSocket** -- goroutines, typed protokol (req/res/event)
- **Docker SDK for Go** -- container lifecycle, network policies
- **bbolt** -- embedded KV store, WAL pro durable job state
- **fsnotify** -- file watching, real-time notifikace
- **slog** -- strukturovane logovani (JSON stdout)
- **Provider pattern**: ContainerProvider (Docker/K8s), StorageProvider (LocalFS/S3), StateProvider (bbolt/PG)

### Databaze
- **SQLite** (default) -- zero deps, `~/.crewship/crewship.db`. Vhodne pro single binary, solo dev, maly tym.
- **PostgreSQL 16** (opt-in) -- `crewship start --db postgres://...`. Vhodne pro Crew/Enterprise.
- Prisma podporuje oba providery. Prepinani pres `DB_PROVIDER` env var.

### Distribuce
- **GoReleaser** -- cross-platform build (linux/darwin/windows, amd64/arm64)
- **Homebrew** -- `brew install crewship` (tap: crewship-ai/homebrew-tap)
- **curl installer** -- `curl -fsSL https://get.crewship.ai | sh`
- **embed.FS** -- static Next.js build embeddovany v Go binary
- **go-selfupdate** -- `crewship update` auto-update

### Infrastruktura
- **pnpm** -- package manager
- **Resend** -- transakcni emaily (pozvanky, notifikace)
- **Vitest** -- unit + integracni testy (TypeScript)
- **go test** -- unit + integracni testy (Go)
- **ESLint 9** -- linting (pinned, v10 awaiting @typescript-eslint)
- **Docker Compose** -- PostgreSQL 16 pro lokalni dev

### Co NEPOUZIVAME (a proc)

| Technologie | Proc NE |
|---|---|
| Redis / BullMQ | Go crewshipd + bbolt WAL nahrazuje. Zadna dalsi zavislost. |
| ws (Node.js WebSocket) | Go nativni WebSocket (goroutines) je efektivnejsi. |
| pino (logging) | Go slog pro backend. Frontend loguje pres Go service. |
| Supabase Auth | NextAuth.js (Auth.js v5) staci. Vendor-neutral. |
| Turborepo | Single repo, pnpm workspace, nepotrebujeme monorepo tooling. |
| Socket.IO | Vetsi overhead, nepotrebujeme fallback na polling. |
| tRPC | Vendor lock-in, slozitejsi pro verejne API. |
| Tailwind 3 / tailwind.config.ts | Tailwind 4 = CSS-first, @theme inline. |
| Bun | Mladsi, riziko nekompatibility. |
| Multi-repo | Slozitejsi sprava pro maly tym. |

---

## 9. PREHLED ARCHITEKTURY

### System diagram

```
User → Browser (Next.js UI) → HTTP API (Next.js routes)
                                    ↓
                              Prisma ORM → PostgreSQL/SQLite
                                    ↓
              Unix socket (/tmp/crewship.sock)
                                    ↓
User → Browser (WebSocket) → crewshipd (Go) → Docker SDK
                                    ↓                ↓
                              bbolt WAL      Docker kontejner
                                              ↓         ↓
                                         CLI agent   /output/
                                              ↓
                                         LLM API

External → Webhook (crewshipd) → Agent trigger → same flow
```

### Datove toky
```
UI (React) ←→ Next.js API routes ←→ Prisma ←→ DB (PostgreSQL/SQLite)
UI (React) ←→ WebSocket ←→ crewshipd (Go) ←→ Docker ←→ Agent CLI ←→ LLM
```

### Storage model
```
EPHEMERAL (container):     /workspace/  ← agent scratch space, disposable
PERSISTENT (host):         /output/     ← agent deliverables, survives everything
LOGS (host + logrotate):   /var/log/crewship/ (prod) nebo ~/.crewship/logs/ (dev)
CONVERSATIONS (host):      ~/.crewship/conversations/ (JSONL per session)
DATABASE:                  ~/.crewship/crewship.db (SQLite) nebo PostgreSQL
```

### Single binary architektura
```
crewship (Go binary, ~50-80 MB)
  ├── embed.FS: Next.js static build (HTML/CSS/JS/assets)
  ├── crewshipd engine:
  │     ├── HTTP server (servuje embedded UI + API proxy)
  │     ├── WebSocket gateway (goroutines)
  │     ├── Docker SDK (kontejnerova orchestrace)
  │     ├── Log collector (JSONL)
  │     ├── File server (fsnotify)
  │     ├── Webhook ingress
  │     ├── Network policy enforcer
  │     └── Skill sandbox enforcement
  ├── Database:
  │     ├── SQLite (default) -- ~/.crewship/crewship.db
  │     └── PostgreSQL (opt-in: --db postgres://...)
  └── CLI:
        ├── crewship start [--port 3001] [--db sqlite|postgres://...]
        ├── crewship stop
        ├── crewship status
        ├── crewship logs [--follow]
        ├── crewship skill install/list/search
        ├── crewship update
        ├── crewship doctor
        └── crewship version
```

---

## 10. TIERS -- FREE / CREW / ENTERPRISE

Crewship pouziva 3-tier model inspirovany Gitea (SQLite default → PostgreSQL opt-in) a Ollama (single binary distribuce):

### Srovnani tieru

| Feature | Free (self-hosted) | Crew (cloud) | Enterprise (K8s) |
|---|---|---|---|
| **Cena** | $0 navzdy | $15-30/user/mesic | $50-100/user/mesic |
| **Distribuce** | Single binary (brew, curl) | crewship.ai (hosted) | Helm chart na K8s |
| **Databaze** | SQLite (default) | PostgreSQL (managed) | PostgreSQL (dedicated) |
| **Workspaces** | 1 (auto-created) | Neomezene | Neomezene |
| **Agenti** | Unlimited | Unlimited | Unlimited |
| **RBAC** | Vsichni ADMIN (zjednoduseny) | 5 roli (Owner→Viewer) | 5 roli + custom |
| **Auth** | NextAuth.js (email, OAuth) | NextAuth.js (email, OAuth) | SSO/SAML (Okta, Azure AD) |
| **Skills** | Official + Community | + Premium skills | + Custom development |
| **Network control** | Per-agent (Docker policies) | Per-agent (Docker policies) | Per-agent (K8s NetworkPolicy) |
| **Cost control** | Per-agent budgety | Per-agent budgety + workspace billing | + chargeback reports |
| **Audit log** | Lokalni (append-only) | Cloud + export + retention | + compliance (SOC 2) |
| **Container izolace** | Docker (per agent) | Docker (per agent) | K8s Pod + gVisor/Kata |
| **Podpora** | Community (GitHub Issues) | Priority (email) | Dedicated (Slack channel, SLA) |
| **Infra management** | Self (Docker required) | Zero (hosted) | Self (K8s cluster) |

### Architektura
- **Stejny codebase** -- zadna separatni verze (CE vs EE)
- **Stejna DB schema** pro vsechny tiery (Prisma, SQLite/PostgreSQL)
- Feature flagy v DB rozlisuji co je dostupne
- Free tier seed automaticky vytvori 1 workspace + admin usera
- Model inspirovany **Gitea** (jeden repo, SQLite default, PostgreSQL opt-in)

---

## 11. BYOK (BRING YOUR OWN KEY) MODEL

Uzivatel pouziva VLASTNI API klice pro LLM providery. Crewship NEPROXUJE a NETRACKUJE API volani.

### Jak to funguje
1. Uzivatel prida svuj API klic jako credential
2. Klic se zasifruje (AES-256-GCM) a ulozi do vaultu
3. Pri spusteni agenta se klic injektuje jako ENV var do Docker kontejneru
4. Agent vola LLM API primo s uzivatlovym klicem
5. Crewship trackuje naklady pres cost control (COST-01/02/03) pokud uzivatel nastavi budget
6. Pokud API vrati error (rate limit, insufficient funds), credential pool prepne na dalsi klic

### Proc BYOK
- Zadny vendor lock-in
- Uzivatel plati jen za to co spotrebuje
- Zadny markup na API calls
- Podpora libovolneho LLM providera (vcetne self-hosted Ollama)

---

## 12. AGENT WORKSPACE (BOOTSTRAP SOUBORY)

Kazdy agent ma svuj workspace adresar. Format je kompatibilni s OpenClaw.

### Struktura workspace
```
~/.crewship/workspaces/{workspace_slug}/{crew_slug}/{agent_slug}/
├── AGENTS.md          # Operacni instrukce + "pamet" agenta
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
├── sessions/          # Transkripty konverzaci (JSONL, debug/kompatibilita)
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
| `SOUL.md` | Osobnost, ton, hranice chovani | Ano |
| `TOOLS.md` | Navody k nastrojum (generovane z tool profile) | Ano |
| `IDENTITY.md` | Jmeno, role, emoji, styl odpovedi | Ano (generuje se z agent config) |
| `USER.md` | Profil firmy/uzivatele, kontext | Ano |

---

## 13. AGENT REZIMY (INTERACTION MODES)

### 13.1 Chat Mode (MVP)
- Uzivatel sedi u chatu a v realnem case komunikuje
- Agent odpovida okamzite (streaming pres Go WebSocket)
- Vhodne pro: dotazy, analyzu, kratke ukoly
- Timeout: 30 minut neaktivity

### 13.2 Task Mode (Phase 2)
- Uzivatel zada ukol a odejde
- Agent bezi na pozadi v Docker kontejneru
- Notifikace pri dokonceni nebo kdyz agent potrebuje vstup
- Vhodne pro: dlouhe operace, batch processing, cron joby

### 13.3 Collaborative Mode (Phase 2)
- Vice uzivatelu komunikuje se stejnym agentem
- Sdileny chat -- vsichni vidi zpravy ostatnich
- Agent identifikuje kdo mu pise
- Vhodne pro: teamove projekty, sdileny asistent

---

## 14. ONBOARDING FLOW

### Single Binary: Guided Wizard
```
1. `brew install crewship && crewship start`
2. Prohlizec se otevre na localhost:3001
3. Registrace (email+heslo nebo OAuth)
4. "Vitejte v Crewship! Pojdme vytvorit vas prvni workspace."
   → Nazev workspace, slug
5. "Skvele! Ted vytvorime vasi prvni crew."
   → Vyber sablony NEBO vlastni nazev
6. "Pridejme virtualniho zamestnance do crew."
   → Jmeno, role, vyber CLI adapteru + LLM modelu
7. "Nastavime pristup k siti."
   → Internet on/off, whitelist (vizualne, klikani)
8. "Dejme mu nejake dovednosti."
   → Skill Store: one-click instalace (web-search, email-send)
9. "Posledni krok -- pridejte API klic."
   → Formular na API klic + cost budget (volitelne)
10. "Hotovo! Spustte svuj prvni chat."
    → Presmerovani na chat UI
```

### Cloud (Crew Tier): Crewship AI (Phase 2)
```
1. Registrace na crewship.ai + subscription
2. Otevre se chat s Crewship AI
3. "Ahoj! Jsem Crewship AI. Pomahu vam vytvorit virtualni crew."
4. Uzivatel popisuje potreby
5. Crewship AI navrhne strukturu (crews, agenty, skilly, network policies)
6. Po schvaleni Crewship AI automaticky vse vytvori
```

---

## 15. SABLONY ODDELENI A AGENTU

### Format: YAML v repozitari
Sablony budou staticke YAML soubory v `skills/templates/`.

### Preddefinovane sablony oddeleni

#### IT Firma
```yaml
name: it-company
display_name: "IT Company"
description: "Complete IT crew with DevOps, QA, and support"
crews:
  - name: development
    display_name: "Development Crew"
    agents:
      - name: devops-bot
        role: "DevOps Engineer"
        system_prompt: "You are a DevOps engineer..."
        skills: [docker-manage, k8s-deploy, ci-cd-pipeline]
        network:
          internet: true
          whitelist: [github.com, registry.npmjs.org]
      - name: qa-bot
        role: "QA Tester"
        system_prompt: "You are a QA engineer..."
        skills: [test-runner, bug-reporter]
        network:
          internet: false
  - name: support
    display_name: "Customer Support Crew"
    agents:
      - name: support-bot
        role: "Support Agent"
        system_prompt: "You are a customer support agent..."
        skills: [email-send, ticket-manager, knowledge-search]
        network:
          internet: true
          whitelist: [api.zendesk.com, smtp.gmail.com]
```

---

## 16. METRIKY USPECHU

### Phase 1 (MVP Launch -- "Open Source Wow")
- 1,000+ GitHub stars do 2 mesicu
- 100+ aktivnich instanci (telemetrie opt-in)
- 5+ tech influencer videi/clanku
- 10+ community skills submitted
- Cas od instalace k prvnimu agentu: < 5 minut
- 0 kritickych security issues

### Phase 2 (Monetizace)
- 100+ platicich crews (Cloud tier)
- $10,000+ MRR
- 50+ community skills v marketplace
- 70%+ mesicni retence

### Phase 3 (Enterprise)
- 5+ enterprise kontraktu
- $50,000+ MRR
- 100+ community skill autoru
- SOC 2 Type II certifikace: zahajeno

---

## 17. RIZIKA A MITIGACE

| Riziko | Pravdepodobnost | Dopad | Mitigace |
|---|---|---|---|
| Solo dev = pomaly vyvoj | Vysoka | Vysoky | AI agenti (Claude, Codex), uzky MVP scope |
| Docker neni nainstalovan u uzivatele | Vysoka | Blocker | `crewship doctor` detekuje, navede na instalaci |
| LLM API nestabilita | Stredni | Stredni | Error handling, retry logika, credential pool failover |
| Security breach (credentials leak) | Nizka | Kriticky | AES-256-GCM, container isolation, audit log, pen testing |
| Zadna komunita (marketplace prazdny) | Stredni | Vysoky | 15-20 built-in official skills, template sablony, marketing |
| Konkurence (OpenClaw, Docker cagent) | Stredni | Stredni | Diferenciace: single binary, network control, marketplace, orchestrace |
| SQLite limity pri vetsi zatezi | Stredni | Degradace | WAL mode + upgrade path na PostgreSQL |
| OpenClaw prida container isolation | Nizka | Konkurence | Orchestrace + marketplace + UI je hlubsi diferenciator |
| Scope creep | Vysoka | Vysoky | Striktni PRD, brutalni prioritizace, code freeze |
| GDPR compliance | Nizka | Vysoky | Data retention, pravo na vymazani, EU hosting |
| Pomalá adopce (cold start) | Stredni | Business | "vs OpenClaw" content, tech influencers, HN launch |

---

## 18. TIMELINE

### Phase 1: Open Source Wow (aktualni → +8 tydnu)

**Cil:** 1,000 GitHub stars, 100 aktivnich instanci, 5 tech influencer videi.

| Tyden | Milestone | Deliverables |
|---|---|---|
| 1-2 | Core platform | Auth (NextAuth.js), Workspace CRUD, Crew CRUD, RBAC (CASL), Prisma schema (SQLite + PG) |
| 3-4 | Agent system | Agent CRUD, Workspace setup, Skills registry, Credentials vault (AES-256-GCM), crewshipd job orchestrace |
| 5-6 | Gateway + Runtime | Go WebSocket gateway, CLI adapter (Claude Code), Docker kontejner lifecycle, stdout streaming |
| 7 | Chat UI + Dashboard | Chat UI (block streaming), Log viewer, Dashboard (crew/agent cards), Audit log |
| 8 | Single binary + Skills | GoReleaser build, brew tap, curl installer, embed.FS, 15-20 official skills, Skill Store UI |
| 9 | Network + Cost | Per-agent network control UI, cost budgets, alerting |
| 10 | Polish + Launch | Onboarding wizard, `crewship doctor`, landing page (crewship.ai), README hero, benchmarky vs OpenClaw |

### Phase 2: Monetizace (+3-6 mesicu)

**Cil:** 100 platicich crews, $10k MRR.

| Tyden | Milestone |
|---|---|
| 1-3 | Cloud tier (crewship.ai hosted), Stripe billing, auto-update |
| 4-6 | Crew Lead orchestrace (Phase 2A), Task mode, Human-in-the-loop |
| 7-9 | Community skill marketplace (submit, review, publish, revenue sharing) |
| 10-12 | Messaging integrace (Slack, Discord), Crewship AI meta-agent, REST API |

### Phase 3: Enterprise (+6-12 mesicu)

**Cil:** 5 enterprise kontraktu, $50k MRR.

| Tyden | Milestone |
|---|---|
| 1-4 | Helm chart pro K8s, Coordinator orchestrace (Phase 2B) |
| 5-8 | SSO/SAML, compliance features (audit export, retention, data residency) |
| 9-12 | GPU node support (Ollama), premium skills, SOC 2 proces |

---

## 19. OTEVRENE OTAZKY

1. **CLI adapter detaily** → viz AGENT-RUNTIME.md
2. **Orchestracni protokol** → viz ORCHESTRATION.md
3. **Prisma multi-provider** (SQLite + PostgreSQL) -- overit ze Prisma zvlada oba providery se stejnym schematem, pripadne embedded-postgres-go jako fallback
4. **Next.js static export** -- overit ze App Router + static export pokryje vsechny potrebne features (no SSR v single binary mode)
5. **Pricing detaily** -- presne limity per tier, A/B testovani cen
6. **Skills security review** → viz SECURITY.md
7. **Auto-update mechanismus** -- go-selfupdate vs equinox vs vlastni
8. **Docker auto-install** -- muze `crewship start` automaticky nainstalovat Docker? Nebo jen detekovat a navest?

---

*Navazujici dokumenty: DATABASE.md, SECURITY.md, AGENT-RUNTIME.md, ORCHESTRATION.md, API.md, DEPLOYMENT.md*
