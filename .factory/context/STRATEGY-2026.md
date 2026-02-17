# Crewship -- Strategicky dokument 2026

**Datum:** 2026-02-17
**Autor:** Pavel Srba + AI
**Ucel:** Kompletni strategicka analyza trhu, konkurence, distribuce a roadmap.
Tento dokument je vysledkem hloubkove analyzy OpenClaw ekosystemu, bezpecnostnich incidentu
a community feedbacku z unora 2026.

---

## 1. OpenClaw -- Hluboka analyza

### 1.1 Proc je OpenClaw populární (157k+ GitHub stars za 60 dni)

OpenClaw (puvodni Clawdbot, pak Moltbot) od Petera Steinbergera je nejrychleji rostouci
open-source AI projekt zacatku roku 2026. Klicove faktory uspechu:

1. **Messaging-first pristup** -- WhatsApp, Telegram, Slack, Discord, Signal (50+ platform).
   Uzivatel pise agentovi tam, kde uz je. Zadna nova aplikace.
2. **Full system access** -- agent spousti shell prikazy, ovlada browser, cte/pise soubory.
   "Osobni AI zamestnanec" na tvojem pocitaci.
3. **Skills ekosystem** -- ClawHub marketplace s 700+ skills, one-command install.
4. **Persistent memory** -- agent si pamatuje kontext napric konverzacemi.
5. **Viralni moment** -- znamy autor (iOS komunita), rychle rebrandy genrerovaly buzz,
   ClawCon event, Moltbook (socialni sit pro agenty).
6. **Open-source (MIT)** -- plna transparentnost, community contributions.

### 1.2 Kriticke bezpecnostni problemy

> Zdroje: Kaspersky, MITRE ATLAS, Snyk, Cisco, Koi Security, ZeroPath, runZero
> (vsechny publikovany v unoru 2026)

| Problem | Zavaznost | Detail |
|---|---|---|
| **Zadna container isolation** | KRITICKA | Agent bezi PRIMO NA HOSTU. Full shell access, full filesystem. Jeden prompt injection = kompromitovany cely stroj. |
| **CVE-2026-25253** | CVSS 8.8 | One-click RCE -- staci kliknout na malicious link, utocnik ziska remote code execution pres WebSocket/auth token theft. Opraveno v 2026.1.29, ale tisice instanci neaktualizovanych. |
| **42,900 exposed instanci** | KRITICKA | Pres 42k instanci verejne pristupnych na internetu (Shodan scan), 15,200 zranitelnych na RCE. Utocnik ziska full control. |
| **341+ malicious skills na ClawHub** | KRITICKA | 12-20% VSECH skills obsahuje malware. Atomic Stealer (macOS keylogger/data stealer), koordinovany supply-chain utok. 354 malicious balicku nahrano jednim utocnikem. |
| **36% skills ma prompt injection** | VYSOKA | Vice nez tretina skills obsahuje prompt injection zranitelnosti -- mohou hijacknout chovani agenta, exfiltrovat data. |
| **Credentials v plaintext** | VYSOKA | API klice a credentials ulozene nesifrovane v config souborech. Moltbook leakl 32,000 agent credentials z verejne pristupne DB. |
| **Zadny sandbox pro skills** | VYSOKA | Skills bezi se STEJNYMI opravnenimi jako hlavni agent. Libovolny skill = libovolny kod s plnym pristupem k systemu. |
| **Zadny audit trail** | STREDNI | Zadny log co agent udelal, kdy, s jakymi daty. Zadna moznost zpetne kontroly. |
| **Session cookie theft** | VYSOKA | Malicious weby mohou exploitovat browser relay server a ukrást session cookies z jinych tabu (Gmail, Microsoft 365). |

**Reakce bezpecnostni komunity:**
- **Kaspersky** -- publikoval enterprise risk management guide
- **MITRE ATLAS** -- formalni investigace, demonstrace supply-chain utoku
- **Snyk** -- audit ClawHub, identifikace malicious skills
- **Cisco** -- varovani pred prompt injection v skills
- **Koi Security** -- identifikace 341 malicious skills, Atomic Stealer kampan

### 1.3 Provozni problemy

| Problem | Detail |
|---|---|
| **Astronomicke API naklady** | $20 pres noc za basic operace, $200+ za par dni heavy use, $500-750/mesic. ZADNY cost control, zadne budgety, zadne limity, zadny alerting. |
| **Slozita instalace** | Node.js 22+, config soubory, env variables, messaging platform setup. Operational burden odrazuje bezne uzivatele. |
| **Nespolehlive workflow** | Inkonsistentni vysledky pri multi-tool taskach. Agent "zapomina" uprostred slozitejsich ukolu. |
| **Spatne skalovani** | Nefunguje v multi-user/workspace kontextu. Jeden agent = jeden uzivatel. Zadny crew management. |
| **Obrovska codebase** | 430,000+ radku kodu. Tezko auditovatelne, tezko contributable pro novy vyvojare. |
| **Nestabilni identita** | 3 rebrandy za par tydnu (Clawdbot → Moltbot → OpenClaw) kvuli trademark issues. Signalizuje chaoticky management. |
| **Hosting slozitost** | Self-hosting vyzaduje VPS, Docker, config management. Ekosystem 30+ hostingovych nastroju (SimpleClaw, ClawdHost, soulstack...) signalizuje, ze zakladni setup je prilis tezky. |

### 1.4 Co komunita rika (HN, Reddit, Medium)

**Pozitivni hlasy:**
- "Zmenil mi to zivot" -- pro jednoduche automatizace (kalendar, emaily) funguje dobre
- Messaging integrace je killer feature -- uzivatel nemusi otevirat novou app
- Open source = duvera (ironicky, vzhledem k security)
- Rychle iterace, aktivni komunita

**Negativni hlasy:**
- **"Anatomy of a Dumpster Fire"** (Medium) -- jak se OpenClaw stal "most hacked AI on the internet"
- **"Not ready for serious work"** (Elephas) -- konsenzus, ze pro profesionalni pouziti prilis riskantni
- **"Prompt injection je neresitelny bez sandboxu"** (HN diskuze o NanoClaw)
- **Naklady** -- uzivatele si stezuji na nepredvidatelne API naklady
- **Nestabilita** -- OpenClaw prestava reagovat (HN thread "My OpenClaw doesn't respond")
- **Frustrace z instalace** -- komplikovany setup generoval cely ekosystem "instalacnich" nastroju

**Community-driven alternativy, ktere vznikly kvuli OpenClaw problemum:**
- **NanoClaw** -- containerized alternativa primo resici security
- **Goose** (Block/Square) -- enterprise-grade Rust agent, memory safety
- **Nanobot** -- lightweight Python (4k LOC vs OpenClaw 430k LOC)

### 1.5 Co uzivatelum chybi (primo z community feedbacku)

1. **Sandboxing/isolation** -- #1 pozadavek. Lidi chteji, aby agent nemohl znicit jejich system.
2. **Cost control** -- budgety, limity, alerting kdyz agent prekroci spending.
3. **Crew/workspace support** -- multi-user, RBAC, sdileni agentu v crew.
4. **Jednodussi setup** -- "proc to nejde nainstalovat jednim prikazem?"
5. **Audit trail** -- co agent udelal, kdy, s jakymi daty.
6. **Vetted skills** -- curated marketplace, ne wild west.
7. **Visual orchestration** -- ne jen chat, ale i dashboard s prehledem.
8. **Lepsi UI** -- OpenClaw nema vlastni UI, je to messaging-only.

---

## 2. Crewship vs OpenClaw -- Kompetitivni matice

### 2.1 Security porovnani

Crewship resi **KAZDY** zasadni bezpecnostni problem OpenClaw:

| OpenClaw problem | Crewship reseni | Status |
|---|---|---|
| Zadna container isolation | Docker kontejnery pro kazdeho agenta, non-root UID 1001, `--internal` network | ✅ Implementovano |
| Credentials v plaintext | AES-256-GCM sifrovani s key versioning (`v1:base64data`) | ✅ Implementovano |
| Malicious skills (zadny sandbox) | Sandboxed skills s deklarovanymi permissions (filesystem, network, secrets) | 📋 Faze 1 |
| Zadny audit trail | Append-only audit log, immutable, queryable | ✅ Implementovano |
| Prompt injection → full access | Agent v kontejneru nemuze uniknout ani kdyz je injected. Container = hranice. | ✅ Architektura |
| Exposed control panels | Web UI na localhost, auth required, RBAC na kazdem endpointu | ✅ Implementovano |
| Session cookie theft | Zadny browser relay server. Agenti bezi v kontejnerech, ne na hostu. | ✅ Architektura |
| Supply-chain attacks (skills) | Curated official skills + community review + sandbox enforcement | 📋 Faze 1 |

### 2.2 Feature porovnani

| Oblast | OpenClaw | Crewship | Vyhoda |
|---|---|---|---|
| **Instalace** | npm + config + messaging setup | `brew install crewship && crewship start` | Crewship |
| **Security** | Bezi na hostu, zadny sandbox | Docker kontejner, non-root, encrypted creds | Crewship |
| **UI** | Zadne vlastni UI (messaging-only) | Full web dashboard (chat, files, logs, settings) | Crewship |
| **RBAC** | Zadne (single-user) | 5 roli (Owner→Viewer), per-crew izolace | Crewship |
| **Audit** | Zadny | Immutable, append-only, queryable | Crewship |
| **Multi-workspace** | 1 instance = 1 uzivatel | Cela firma v jedne instanci | Crewship |
| **Orchestrace** | Zadna (flat, 1 agent) | Coordinator → Lead → Agent hierarchie | Crewship |
| **Cost control** | Zadny (lidi plati $750/mesic) | Per-agent budgety, alerting, limity | Crewship |
| **Network control** | Zadny (full internet access) | Per-agent: internet ON/OFF, whitelist, VPN | Crewship |
| **Skills bezpecnost** | 20% malware, zadny sandbox | Sandboxed, curated, permissions model | Crewship |
| **Messaging integrace** | 50+ platform (WhatsApp, Telegram...) | Web UI (MVP), messaging Phase 2 | OpenClaw |
| **Community** | 157k+ stars, massive hype | Novy projekt | OpenClaw |
| **Codebase size** | 430,000+ LOC | ~15,000 LOC (Go + TS) | Crewship |
| **Credential management** | 1 klic v plaintext | Multi-key pool, failover, encrypted | Crewship |
| **Webhooky** | Reaguje na messaging zpravy | Native webhook triggers (Grafana, CI/CD, n8n) | Crewship |
| **File output** | Zadny /output/ koncept | Persistent output, archivace, file browser | Crewship |

### 2.3 Klicovy marketing message

> **"OpenClaw dava AI agentovi klice od celeho tveho pocitace.**
> **Crewship dava kazdemu agentovi vlastni zamcenou kancelar --**
> **s presne definovanym pristupem k nastrojum, siti a datum."**

**Landing page -- tri vety:**

1. **Bezpecne** -- Kazdy agent bezi v izolovanem kontejneru. Zadny pristup k hostiteli.
2. **Jednoduche** -- `brew install crewship && crewship start`. Hotovo.
3. **Orchestrovane** -- Hierarchie agentu s visual dashboardem a curated skill marketplace.

---

## 3. Trh a konkurence (unor 2026)

### 3.1 Prehled konkurentu

| Platforma | Typ | Instalace | Isolation | Orchestrace | UI | Nase vyhoda |
|---|---|---|---|---|---|---|
| **OpenClaw** | Personal assistant | npm + config | ZADNA (host) | ZADNA | Messaging-only | Security, UI, missions, crews |
| **Docker cagent** | Agent runtime | Docker | Docker sandbox | Agent handoff | CLI only | UI, RBAC, crews, marketplace |
| **AgentSystems** | Agent app store | Docker compose | Container + egress | ZADNA | Basic web | Orchestrace, single binary, better UX |
| **NanoClaw** | Secure OpenClaw alt | Docker | Container | ZADNA | Minimal | Full platform vs wrapper |
| **Netclode** | Cloud coding agent | K8s + microVM | microVM | ZADNA | iOS app | Self-hosted, sirsi use case |
| **Goose** (Block) | Enterprise agent | Binary | Modular | ZADNA | CLI | UI, marketplace, crews |
| **Manus AI** | Cloud SaaS | Zero-setup | Cloud | ZADNA | Web | Self-hosted, open-source |

### 3.2 ClawHub Marketplace -- pouceni

ClawHub (OpenClaw skill marketplace) ma 700+ skills, ale:
- **20% skills obsahuje malware** (audit Snyk + Koi Security)
- **36% skills ma prompt injection** (audit Cisco)
- **Zadny sandbox** -- skills bezi se stejnymi opravnenimi jako agent
- **VirusTotal scanning pridano az po incidentech** (reaktivni, ne proaktivni)
- **Identity verification az po supply-chain utoku** (taky reaktivni)
- **CLI-only instalace** (zadne UI)
- **Zadny review proces** pro community skills

### 3.3 Kde je prostor na trhu

Crewship je **JEDINY** projekt, ktery kombinuje:
1. Jednoprikazovou instalaci (Ollama model)
2. Container isolation (Docker) -- kazdy agent v sandboxu
3. Multi-level orchestraci (Coordinator → Lead → Agent hierarchie)
4. Full web UI s dashboardem
5. Per-agent network policies (klikaci, ne iptables)
6. Curated skill marketplace s sandbox enforcement
7. Enterprise features (RBAC, audit, encrypted credentials)

**Zadny konkurent nema vice nez 3 z techto 7 bodu.**

---

## 4. Distribucni strategie -- Single Binary (Ollama model)

### 4.1 Inspirace

| Projekt | Instalace | Uspech |
|---|---|---|
| **Ollama** | `curl -fsSL ollama.ai/install.sh \| sh` + brew | Dominuje lokalni LLM trhu |
| **Gitea** | Single binary, brew, apt | 50k+ stars, GitLab alternativa |
| **k9s** | brew, apt, binary | Standard pro K8s monitoring |
| **lazygit** | brew, apt, binary | Standard pro git TUI |

### 4.2 Instalacni prikazy

```bash
# macOS
brew install crewship

# Linux (Debian/Ubuntu)
curl -fsSL https://get.crewship.ai | sh

# Linux (RPM)
dnf install crewship

# Windows
winget install crewship

# Docker (fallback)
docker run -d -p 8080:8080 --name crewship ghcr.io/crewship-ai/crewship:latest
```

### 4.3 Co `crewship start` udela

```
1. Detekuje Docker (nainstaluje pokud chybi? -- TBD)
2. Spusti embedded web server (Next.js static build)
3. Inicializuje SQLite databazi (~/.crewship/crewship.db)
4. Spusti crewshipd engine (WebSocket, Docker orchestrace)
5. Otevre http://localhost:8080 v prohlizeci
6. Uzivatel vidi onboarding wizard
```

### 4.4 Architektura single binary

```
crewship (Go binary, ~50-80 MB)
  ├── Embedded: Next.js static build (embed.FS)
  │     └── HTML/CSS/JS/assets -- servovane pres Go HTTP server
  ├── crewshipd engine:
  │     ├── WebSocket gateway (goroutines)
  │     ├── Docker SDK (kontejnerova orchestrace)
  │     ├── Log collector (JSONL)
  │     ├── File server (fsnotify)
  │     ├── Webhook ingress
  │     └── Skill sandbox enforcement
  ├── Database:
  │     ├── SQLite (default, zero deps) -- ~/.crewship/crewship.db
  │     └── PostgreSQL (opt-in: crewship start --db postgres://...)
  ├── CLI:
  │     ├── crewship start [--port 8080] [--db sqlite|postgres://...]
  │     ├── crewship stop
  │     ├── crewship status
  │     ├── crewship logs [--follow]
  │     ├── crewship skill install <name>
  │     ├── crewship skill list
  │     └── crewship update
  └── Auto-update: go-selfupdate nebo equinox
```

### 4.5 SQLite vs PostgreSQL

| Aspekt | SQLite (default) | PostgreSQL (opt-in) |
|---|---|---|
| Setup | Zero deps, instant | Docker container nebo external |
| Vhodne pro | Solo dev, maly tym (1-10 lidi) | Vetsi tym, enterprise, high availability |
| Prisma podpora | Ano (prisma/sqlite) | Ano (prisma/postgresql) |
| Concurrent writes | Limitovane (WAL mode pomaha) | Plne |
| Backup | Kopie souboru | pg_dump, replikace |
| Prikaz | `crewship start` | `crewship start --db postgres://user:pass@host/db` |

**Gitea model:** SQLite jako default, PostgreSQL/MySQL jako opt-in. Funguje pro 50k+ stars projekt.

### 4.6 Cross-platform build

```yaml
# GoReleaser konfigurace
builds:
  - binary: crewship
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags: -s -w -X main.version={{.Version}}

# Distribuce
brew:
  tap: crewship-ai/homebrew-tap
  formula: crewship

nix:
  flake: crewship-ai/nix

scoop:
  bucket: crewship-ai/scoop-bucket
```

---

## 5. Skill Marketplace strategie

### 5.1 Proc je marketplace moat

Marketplace vytvari **network effect**: vic skills → vic uzivatelu → vic skill autoru → vic skills.
To je lock-in, ktery se tezko kopiruje. OpenClaw to pochopil, ale provedeni je katastrofalne
(20% malware, zadny sandbox, CLI-only).

### 5.2 Crewship Skill Store -- jak to udelat lepe

```
┌──────────────────────────────────────────────────┐
│  Crewship Skill Store (v UI dashboardu)          │
│                                                   │
│  Search: "deploy to kubernetes"                   │
│                                                   │
│  ┌───────────────┐  ┌───────────────┐            │
│  │ K8s Deploy    │  │ Git PR Review │            │
│  │ ★★★★★ (342)   │  │ ★★★★☆ (128)  │            │
│  │ by: official  │  │ by: community │            │
│  │ [Install]     │  │ [Install]     │            │
│  │               │  │               │            │
│  │ Permissions:  │  │ Permissions:  │            │
│  │ - network: ON │  │ - network: OFF│            │
│  │ - fs: /output │  │ - fs: read    │            │
│  │ - secrets: 0  │  │ - secrets: 1  │            │
│  └───────────────┘  └───────────────┘            │
│                                                   │
│  Categories:                                      │
│  DevOps | Marketing | Finance | Research          │
│  Code Review | Data | Customer Support            │
│                                                   │
│  Badges: [Official] [Verified] [Community]        │
└──────────────────────────────────────────────────┘
```

### 5.3 Diferenciatory oproti ClawHub

| Aspekt | ClawHub (OpenClaw) | Crewship Skill Store |
|---|---|---|
| **Sandbox** | ZADNY -- skills bezi s plnymi pravy | Kazdy skill deklaruje permissions, Docker je vynucuje |
| **Instalace** | CLI (`claw skill install xyz`) | One-click v UI dashboardu |
| **Review proces** | Zadny (VirusTotal az post-hoc) | Official = rucne reviewed; Community = automated scan + sandbox test |
| **Permissions model** | Vsechno nebo nic | Granularni: filesystem (r/w/none), network (on/off/whitelist), secrets (list) |
| **Kategorie** | Flat list | Strukturovane kategorie + tagy |
| **Kvalita signaly** | Zadne | Rating, install count, "Official"/"Verified" badge |
| **Revenue sharing** | ZADNY | Community autori dostavaji podil z Cloud/Enterprise tier |
| **Skill compose** | NELZE | Kombinovani skills do workflow ("Research + Summarize + Email") |

### 5.4 Official skills pro launch (15-20)

**DevOps (5):**
1. `git-operations` -- clone, branch, commit, PR, merge
2. `docker-management` -- build, run, logs, cleanup
3. `k8s-deploy` -- kubectl apply, rollout status, logs
4. `ci-cd-monitor` -- GitHub Actions / GitLab CI status, retry
5. `server-ssh` -- SSH execute, file transfer, health check

**Development (5):**
6. `code-review` -- analyze diff, suggest improvements, security scan
7. `test-runner` -- run tests, parse output, generate report
8. `documentation` -- generate docs from code, update README
9. `dependency-audit` -- check outdated, vulnerabilities, suggest updates
10. `database-admin` -- migrations, backups, query optimization

**Business (5):**
11. `web-research` -- search, scrape, summarize findings
12. `report-generator` -- CSV/PDF/Markdown reports z dat
13. `email-sender` -- compose + send pres SMTP/API
14. `calendar-manager` -- Google Calendar / Outlook integrace
15. `slack-notifier` -- post zpravy, reagovat na eventy

**Data (3-5):**
16. `csv-processor` -- parse, transform, analyze CSV/Excel
17. `api-client` -- generic REST API calls s auth
18. `file-converter` -- PDF, Markdown, HTML, DOCX konverze
19. `image-processor` -- resize, compress, OCR (optional)
20. `monitoring-alert` -- Grafana/Prometheus/PagerDuty integrace

### 5.5 Skill format

```yaml
# skill.yaml
name: "git-operations"
version: "1.0.0"
author: "crewship-official"
category: "devops"
description: "Git repository operations -- clone, branch, commit, PR, merge"
badge: "official"  # official | verified | community

permissions:
  filesystem:
    read: ["/workspace/*", "/output/*"]
    write: ["/workspace/*", "/output/*"]
  network:
    enabled: true
    whitelist: ["github.com", "gitlab.com", "bitbucket.org"]
  secrets:
    required: ["GIT_TOKEN"]
    optional: ["GIT_SSH_KEY"]
  shell:
    allowed: ["git", "gh", "curl"]
    denied: ["rm -rf /", "sudo"]

instructions: |
  You are a Git operations specialist. You can:
  - Clone repositories
  - Create branches and switch between them
  - Stage, commit, and push changes
  - Create and manage Pull Requests via GitHub CLI (gh)
  ...
```

---

## 6. Per-Agent Network Control (killer feature)

### 6.1 Koncept

Kazdy agent (kontejner) ma individualne nastavitelny sitovy pristup.
Uzivatel to konfiguruje v UI klikáním -- zadne iptables, zadne Docker network prikazy.

```
Agent: "Research Bot"
  ├── Internet: ON (whitelist: google.com, arxiv.org, scholar.google.com)
  ├── Local network: OFF
  └── Remote access: NONE

Agent: "Deploy Bot"
  ├── Internet: OFF
  ├── Local network: ON (192.168.1.0/24)
  └── Remote access: K8s cluster (gke-prod-1)

Agent: "Code Reviewer"
  ├── Internet: OFF
  ├── Local network: OFF
  └── Remote access: GitLab (gitlab.company.com:443)

Agent: "Customer Support"
  ├── Internet: ON (whitelist: api.zendesk.com, smtp.gmail.com)
  ├── Local network: OFF
  └── Remote access: NONE
```

### 6.2 Implementace (pod kapotou)

```
UI kliknuti → API call → crewshipd → Docker network policies

Technicky:
1. Kazdy crew/agent ma vlastni Docker network
2. Network mode: "internal" (default, zadny internet)
3. Whitelist → iptables rules v kontejneru (nebo Docker network connect k bridge s filtrem)
4. Local network → macvlan/ipvlan driver s CIDR rozsahem
5. Remote access → WireGuard/Tailscale sidecar kontejner (Phase 2)
```

### 6.3 Proc to nikdo jiny nema

- **OpenClaw**: agent bezi na hostu, MA pristup ke vsemu (vcetne internetu, LAN, vsech portu)
- **Docker cagent**: basic Docker sandbox, ale zadne per-agent policies, zadne UI
- **AgentSystems**: default-deny egress, ale ne konfigurovatelne per-agent
- **CrewAI/LangGraph/AutoGen**: zadna izolace, bezi v Python procesu

Crewship je jediny, kdo kombinuje **granularni network control** s **klikacim UI**.

---

## 7. Monetizacni model (3 tiery)

### 7.1 Prehled

| Tier | Nazev | Cilovy zakaznik | Cena | Distribuce |
|---|---|---|---|---|
| **Free** | Community | Solo dev, student, hacker | $0 | Single binary, SQLite, Docker |
| **Cloud** | Cloud | Startup, mala firma (5-50 lidi) | $15-30/user/mesic | crewship.ai hosted, PostgreSQL |
| **Enterprise** | Self-managed | Korporat (100+ lidi) | $50-100/user/mesic | Helm chart na zakaznikuv K8s |

### 7.2 Co je v kazdem tieru

**Free (Community):**
- Single binary instalace
- SQLite databaze
- Docker kontejnerova izolace
- Vsechny official skills
- Community skills (z marketplace)
- 1 workspace, unlimited agents
- Per-agent network control
- Audit log (lokalni)
- Web UI dashboard
- CLI nastroje

**Cloud:**
- Vse z Free +
- Hosted na crewship.ai (zero infra management)
- PostgreSQL (managed)
- Crew collaboration (sdileni agentu, RBAC)
- Skill marketplace (community + premium skills)
- Priority support (email)
- SSO (Google, GitHub)
- Usage analytics dashboard
- Automatic backups

**Enterprise (Self-managed):**
- Vse z Cloud +
- Helm chart pro K8s (GKE, EKS, AKS)
- SSO/SAML (Okta, Azure AD)
- Compliance audit trail (export, retention policies)
- SLA (99.9% uptime)
- Dedicated support (Slack channel)
- Custom skill development
- On-premise deployment option
- GPU node support (lokalni LLM pres Ollama)

### 7.3 Srovnani s trhem

| | OpenClaw | Manus AI | Claude Code | **Crewship Free** | **Crewship Cloud** |
|---|---|---|---|---|---|
| Cena | $0 (BYOK) + $500+/m API | $39-199/m | $20-200/m | **$0 (BYOK)** | **$15-30/user/m** |
| Self-hosted | Ano | Ne | Ne | **Ano** | Hosted |
| Container isolation | Ne | N/A (cloud) | Managed sandbox | **Ano** | **Ano** |
| Multi-user | Ne | Ne | Ne | **Ano** | **Ano** |
| Skills marketplace | ClawHub (nebezpecny) | Ne | Ne | **Ano (bezpecny)** | **Ano + premium** |

---

## 8. Fazovy plan

### Faze 1: Open Source Wow (aktualni → +8 tydnu)

**Cil:** 1,000 GitHub stars, 5 tech influencer videi, 100 aktivnich instanci.

**Deliverables:**
- [ ] Single binary distribuce (GoReleaser, brew, curl installer)
- [ ] SQLite jako default DB (Prisma migrace, zero deps)
- [ ] Embedded Next.js (static export v Go binary)
- [ ] `crewship start` / `crewship stop` / `crewship status` CLI
- [ ] 15-20 official skills s permissions modelem
- [ ] Skill Store UI v dashboardu (browse, install, uninstall)
- [ ] Per-agent network control UI (internet on/off, whitelist)
- [ ] Per-agent cost budgety a alerting
- [ ] Onboarding wizard (prvni spusteni → agent za 60 sekund)
- [ ] Landing page (crewship.ai) s demo videem
- [ ] README s "brew install crewship" hero section
- [ ] Benchmarky: OpenClaw vs Crewship (security, setup time, resource usage)

**Marketing:**
- Tweet: `brew install crewship && crewship start` -- 2 prikazy, running AI orchestrace
- Video: "OpenClaw vs Crewship security comparison" (side-by-side)
- Blog: "Why your AI agent shouldn't have root access to your computer"
- HN launch post: "Show HN: Crewship -- self-hosted AI agent platform with container isolation"

### Faze 2: Monetizace (+3-6 mesicu)

**Cil:** 100 platicich workspaces, $10k MRR.

**Deliverables:**
- [ ] crewship.ai cloud tier (hosted PostgreSQL, managed infra)
- [ ] Community skill marketplace (submit, review, publish)
- [ ] Revenue sharing pro skill autory
- [ ] Crew collaboration features (shared agents, RBAC invites)
- [ ] Lead orchestrace (Phase 2A z PROGRESS.md)
- [ ] Messaging integrace (Slack, Discord -- Phase 2 kanaly)
- [ ] Usage analytics dashboard
- [ ] Stripe billing integrace
- [ ] Auto-update mechanism pro single binary

### Faze 3: Enterprise (+6-12 mesicu)

**Cil:** 5 enterprise kontraktu, $50k MRR.

**Deliverables:**
- [ ] Helm chart pro K8s (GKE, EKS, AKS)
- [ ] SSO/SAML (Okta, Azure AD, Google Workspace)
- [ ] Coordinator orchestrace (Phase 2B z PROGRESS.md)
- [ ] Compliance features (audit export, retention policies, data residency)
- [ ] GPU node support (lokalni LLM pres Ollama)
- [ ] Premium skills (enterprise-only)
- [ ] Dedicated support tier
- [ ] SOC 2 compliance (zacit proces)

---

## 9. Technicka rozhodnuti k implementaci

### 9.1 SQLite integrace ✅ IMPLEMENTOVANO

**Pristup:** Go `database/sql` s pure-Go SQLite driverem (`modernc.org/sqlite`).
Prisma schema se pouziva pouze pro TypeScript type generation.
Go migration system (`internal/database/migrate.go`) spravuje schema pro oba providery.

```go
// internal/database/database.go
func Open(databaseURL string) (*sql.DB, error) {
    if strings.HasPrefix(databaseURL, "file:") || strings.HasSuffix(databaseURL, ".db") {
        return sql.Open("sqlite", databaseURL) // modernc.org/sqlite
    }
    return sql.Open("postgres", databaseURL)
}
```

### 9.2 Embedded Next.js

```go
// cmd/crewship/main.go
//go:embed web/out/*
var webFS embed.FS

func main() {
    // Serve static Next.js build
    http.Handle("/", http.FileServer(http.FS(webFS)))
    // API proxy na crewshipd engine
    http.Handle("/api/", reverseProxy(crewshipdEngine))
}
```

**Build pipeline:**
1. `pnpm build` → `next export` → `web/out/`
2. `go build -o crewship ./cmd/crewship` → embeduje `web/out/`
3. GoReleaser cross-compile → binary pro kazdy OS/arch

### 9.3 CLI design

```
crewship                      # help
crewship start                # spusti vse (SQLite, localhost:3001)
crewship start --port 8080    # custom port
crewship start --db postgres://user:pass@host/db  # PostgreSQL
crewship stop                 # zastavi vse
crewship status               # stav sluzeb
crewship logs                 # tail logy
crewship logs --follow        # stream logy
crewship skill install <name> # nainstaluje skill z marketplace
crewship skill list           # seznam nainstalovanych skills
crewship skill search <query> # hledani v marketplace
crewship update               # aktualizace na nejnovejsi verzi
crewship version              # verze
crewship doctor               # diagnostika (Docker check, port check, DB check)
```

### 9.4 Data directory

```
~/.crewship/
  ├── crewship.db           # SQLite databaze (pokud SQLite mode)
  ├── config.yaml           # uzivatelska konfigurace
  ├── skills/               # nainstalovane skills
  │   ├── git-operations/
  │   ├── web-research/
  │   └── ...
  ├── output/               # agent vystupy
  │   └── {workspace-id}/{crew-name}/{agent-name}/
  ├── chats/                # JSONL chats
  │   └── {workspace-id}/{agent-id}/{session-id}.jsonl
  ├── logs/                 # JSONL logy
  │   └── crews/{crew-id}/agents/{agent-id}/current.jsonl
  └── crewship.pid          # PID soubor
```

---

## 10. Rizika a mitigace

| Riziko | Pravdepodobnost | Dopad | Mitigace |
|---|---|---|---|
| Docker neni nainstalovan u uzivatele | Vysoka | Blocker | `crewship doctor` detekuje, navede na instalaci. Budouci: auto-install. |
| SQLite limity pri vetsi zatezi | Stredni | Degradace | WAL mode, connection pooling. Upgrade path na PostgreSQL. |
| Next.js static export limity (no SSR) | Stredni | Feature omezeni | API routes zustanou v Go. UI je SPA, data pres API. |
| OpenClaw prida container isolation | Nizka | Konkurence | Nase orchestrace + marketplace + UI je hlubsi diferenciator. |
| Pomalá adopce (tezky cold start) | Stredni | Business | Agresivni marketing, tech influencers, "vs OpenClaw" content. |
| Security incident v nasem marketplace | Nizka | Reputation | Sandbox enforcement od zacatku, ne post-hoc. Review proces. |

---

## 11. Metriky uspechu

### Faze 1
- GitHub stars: 1,000+
- Aktivni instance (telemetrie opt-in): 100+
- Tech influencer videa/clanky: 5+
- Community skills submitted: 10+
- Cas od instalace k prvnimu agentu: < 5 minut

### Faze 2
- Platici workspaces (Cloud tier): 100+
- MRR: $10,000+
- Community skills v marketplace: 50+
- Retence (mesicni): 70%+

### Faze 3
- Enterprise kontrakty: 5+
- MRR: $50,000+
- Community skill autoru: 100+
- SOC 2 Type II certifikace: zahajeno

---

## Reference

- [Kaspersky: OpenClaw enterprise risk management](https://www.kaspersky.com/blog/moltbot-enterprise-risk-management/55317/)
- [MITRE ATLAS: OpenClaw investigation](https://www.mitre.org/sites/default/files/2026-02/PR-26-00176-1-MITRE-ATLAS-OpenClaw.pdf)
- [CVE-2026-25253: One-click RCE](https://hackers-arise.com/cve-2026-25253)
- [OpenClaw Security Fallout: 341 malicious skills](https://www.newsbreak.com/winbuzzer-com-302470011/4475008339343)
- [42,900 exposed instances](https://elephas.app/resources/openclaw-ai-agent-security-risks)
- [Anatomy of a Dumpster Fire (Medium)](https://medium.com/@nitikakumari065/anatomy-of-a-dumpster-fire-how-openclaw-became-the-most-hacked-ai-on-the-internet)
- [NanoClaw solves OpenClaw security (HN)](https://news.ycombinator.com/item?id=46976845)
- [6 OpenClaw Competitors (Emergent)](https://emergent.sh/learn/best-openclaw-alternatives-and-competitors)
- [ClawHub Developer Guide](https://www.digitalapplied.com/blog/clawhub-skills-marketplace-developer-guide-2026)
- [Docker cagent (GitHub)](https://github.com/docker/cagent)
- [AgentSystems (GitHub)](https://github.com/agentsystems/agentsystems)
