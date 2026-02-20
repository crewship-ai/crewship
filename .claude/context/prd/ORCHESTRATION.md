# Crewship -- Orchestrace: Crew Lead + Coordinator

**Verze:** 2.1
**Datum:** 2026-02-20
**Status:** Phase 1 implementovana; Phase 2A v navrhu (neimplementovana)
**Zmeny v2.1:** Aktualizace stavu implementace — jasne rozliseni Phase 1 (hotovo) vs Phase 2A (plan).
**Zmeny v2.0:** Loopback HTTP sidecar (nahrazuje named pipe jako primarni),
dual runtime (CLI + API direct), Landlock per-agent izolace,
lead modes (active/passive), agent output compression, circuit breaker,
Meilisearch conversation search, trace ID across assignments, NATS odlozen na Phase 3.
Viz `ADR.md` pro zduvodneni rozhodnuti.

---

## STAV IMPLEMENTACE (2026-02-20)

Vetsina tohoto dokumentu popisuje **planovane Phase 2A/2B featury**, ktere NEJSOU implementovane.
Pouze zakladni Phase 1 orchestrace funguje. Nize je prehled:

### IMPLEMENTOVANO (Phase 1)

| Feature | Stav | Detail |
|---|---|---|
| CLI adaptery (4) | HOTOVO | CLAUDE_CODE, CODEX_CLI, GEMINI_CLI, OPENCODE (`internal/orchestrator/exec.go`) |
| Credential vyber | HOTOVO | Priority-based s round-robin v ramci tier (`internal/orchestrator/failover.go`) |
| Cooldown management | HOTOVO | 5min cooldown po 429 rate limit (`CooldownManager` ve `failover.go`) |
| Conversation history | HOTOVO | Poslednich 10 zprav, max 20k chars, injekce do system promptu |
| Agent memory | HOTOVO | File-first (AGENT.md + daily logs), FTS5 search pres sidecar (`internal/memory/`) |
| Container exec | HOTOVO | Jeden kontejner per crew, UID 1001 agent / UID 1002 sidecar |
| Sidecar — credential proxy | HOTOVO | Forward HTTP proxy s credential injection na `127.0.0.1:9119` |
| Sidecar — memory endpointy | HOTOVO | `POST /memory/search`, `GET /memory/status`, `POST /memory/reindex` |

### NENI IMPLEMENTOVANO (Phase 2A — plan)

| Feature | Stav | Detail |
|---|---|---|
| Assignment API (sidecar) | NENI | Endpointy `/assign`, `/ask`, `/broadcast`, `/results` NEexistuji v sidecar |
| AssignmentEngine | NENI | `internal/orchestrator/assignment.go` neexistuje |
| Lead execution context | NENI | Auto-generated lead system prompt neni implementovan |
| Lead modes (Active/Passive) | NENI | Zadna logika v orchestratoru |
| Coordinator lightweight exec | NENI | `RunCoordinator` neexistuje |
| Circuit breaker (CLOSED/OPEN/HALF) | NENI | Existuje pouze `CooldownManager` pro credential failover, NE circuit breaker pro agenty |
| Agent output compression | NENI | Zadna sumarizace vystupu |
| Trace ID across assignments | NENI | Zadna trace korelace |
| Assignment tabulka v UI | NENI | UI neexistuje |
| Human-in-the-loop approval | NENI | Zadny approval flow |
| Channel Gateway (messaging) | NENI | Discord/Telegram/Slack integrace neexistuje |

### Poznamka k terminologii DB vs dokument

DB schema pouziva **"delegation"** naming (`delegation_timeout_s`, `max_delegation_depth`, `max_parallel_delegates`),
zatimco tento dokument pouziva **"assignment"** (`assignment_timeout_s`, `max_assignment_depth`, `max_parallel_assignments`).
Tato nekonzistence je znama — DB schema je autoritativni. Tabulka `assignments` v DB existuje, ale neni vyuzivana zadnou logikou.

---

## 1. VIZE

Crewship pouziva **3-urovnovou hierarchii AI agentu** inspirovanou realnou firemni strukturou:

```
COORDINATOR (koordinator workspace)
  └── CREW LEAD (sef crew / oddeleni)
        └── AGENT (radovy agent / zamestnanec)
```

**Klicovy princip:** Uzivatel si **povida primarne s Crew Leadem** sve crew.
Lead rozhoduje, prideluje ukoly, agreguje a vraci vysledky. Uzivatel nemusi vedet,
ktery konkretni agent ukol zpracoval — staci mu komunikace se "sefem".

**Pro cross-crew otazky** existuje Coordinator — koordinator, ktery koordinuje
vice crews a agreguje informace z celeho workspace.

---

## 2. INDUSTRY KONTEXT (2026)

### 2.1 Prumyslove vzory

| Vzor | Zdroj | Popis |
|---|---|---|
| **Hub-and-Spoke** | Azure Architecture Center | Centralni orchestrator (hub) koordinuje specializovane agenty (spokes). Predvidatelny, konzistentni. |
| **Hierarchical Crew** | CrewAI 0.30+ | Manager Agent rozdeluje ukoly, validuje outputy sub-agentu. |
| **Supervisor Pattern** | LangGraph, Semantic Kernel | Supervisor dynamicky vybira agenta dle kontextu a potreby. |
| **Magentic-One** | Microsoft Research | Flexibilni multi-agent orchestrace, kde manager vybira agenta dle evoluce ukolu. |
| **OrchVis** | Georgia Tech (arXiv 2025) | Hierarchicka orchestrace s human oversight — vizualizace pro lidsky dohled. |
| **Multi-Agent Supervisor** | Databricks/BASF (2025) | Supervisor spravuje 11k+ zamestnancu pres jednotne rozhrani. |

### 2.2 Tržní data

- **80 % enterprise firem** planuje adopci multi-agent systemu do 2 let (On About AI, 2025)
- **45 % rychlejsi reseni problemu** pri multi-agent vs single-agent architekture
- **60 % presnejsi vysledky** diky specializaci agentu
- AI agents market: **$5.25B (2024) → $52.62B (2030)**, multi-agent = nejrychleji rostouci segment

### 2.3 Crewship diferenciace

```
CrewAI / LangGraph / AutoGen  = FRAMEWORKY pro DEVELOPERY (Python/code-first)
Crewship                      = PLATFORMA pro BYZNYS UZIVATELE (UI-first, lidska terminologie)
```

Koncept "povidam si se sefem crew" je prirozeny pro netechnicke uzivatele.
"Orchestruju multi-agent workflow pres DAG" prirozeny neni.

---

## 3. TRI UROVNE HIERARCHIE

### 3.1 Agent (Uroven 1) — Radovy agent

- **Default role** pro vsechny agenty
- Specializovany na konkretni ukoly (data analyst, copywriter, devops, QA...)
- Komunikuje primarne se svym Crew Leadem
- Produkuje output do `/output/`
- Muze byt osloven primo uzivatelem (bypass leada)
- Bezi v kontejneru sve crew (Docker exec)

### 3.2 Crew Lead (Uroven 2) — Sef crew

- **1 lead per crew** (oznaceny v Agent modelu jako `agent_role = LEAD`)
- **Primarni kontaktni bod** pro uzivatel ↔ crew komunikaci
- Zna vsechny agenty ve sve crew (automaticky generovany kontext)
- Rozhoduje: zpracovat sam vs pridelit agentovi
- Rozdeluje slozite ukoly na sub-tasky pro agenty
- Agreguje vysledky od agentu do koherentni odpovedi
- Kontroluje kvalitu (review pred odeslani uzivatel)
- Reportuje nahoru ke Coordinatorovi (pokud je osloven)
- Bezi v kontejneru sve crew (Docker exec)

**System prompt pattern (automaticky generovany):**
```
Jsi {agent.name}, sef crew "{crew.name}" ve workspace "{workspace.name}".

Tva crew:
- {agent1.name} ({agent1.role_title}): {agent1.description}
- {agent2.name} ({agent2.role_title}): {agent2.description}
- ...

Tvoje zodpovednosti:
1. Kdyz dostanes ukol, rozhodnes se jestli ho zvladnes sam nebo pridel.
2. Pro slozite ukoly rozloz praci a prirazes konkretnim clenum crew.
3. Vzdy shrnuj vysledky do srozumitelne odpovedi pro uzivatele.
4. Kontroluj kvalitu vystupu svych lidi pred odeslani.
5. Pokud nekdo ze tvych lidi selze, rozhodnes se o alternativnim postupu.

Prideleni pouzij prikaz: @assign({agent_slug}, "{ukol}")
Dotaz na clena crew: @ask({agent_slug}, "{otazka}")
```

### 3.3 Coordinator (Uroven 3) — Koordinator workspace

- **1 coordinator per workspace** (oznaceny jako `agent_role = COORDINATOR`)
- **Neni clenem zadne crew** — patri workspace jako celku
- Zna vsechny crews a jejich leady (automaticky generovany kontext)
- Prideluje ukoly Crew Leadum (ne primo agentum)
- Koordinuje cross-crew ukoly (napr. "kolik stoji provoz a jake mame trzby?")
- Agreguje informace z vice crews do jedne odpovedi
- Ma strategicky pohled na workspace
- **Opt-in** — defaultne vypnuty, aktivuje se v nastaveni workspace

**System prompt pattern (automaticky generovany):**
```
Jsi {agent.name}, koordinator workspace "{workspace.name}".

Tve crews a jejich sefove:
- {crew1.name} (sef: {lead1.name}): {crew1.description}
- {crew2.name} (sef: {lead2.name}): {crew2.description}
- ...

Tvoje zodpovednosti:
1. Kdyz dostanes otazku, rozhodnes se ktera crew/crews ji zpracuji.
2. Pro cross-crew ukoly koordinuj vice crews paralelne.
3. Agreguj vysledky od sefu crews do jedne koherentni odpovedi.
4. Poskytuj strategicky pohled na workspace.
5. Nikdy nedelej primou praci — vzdy pridel prislusne crew.

Prideleni crew: @assign_crew({crew_slug}, "{ukol}")
Dotaz na sefa crew: @ask_lead({crew_slug}, "{otazka}")
```

---

## 4. KOMUNIKACNI PATTERNY

### 4.1 Uzivatel → Crew Lead (90 % interakci)

Nejcastejsi use case. Uzivatel si otevre chat s leadem sve crew:

```
Uzivatel: "Potrebuju report o socialnich sitich za leden"

Marketing Lead (Anna):
  "Rozumim. Rozdelim to na dve casti:
   1. @assign(bob, "Stahni data z Twitter/Instagram/LinkedIn za leden 2026")
   2. @assign(claudia, "Napises report az budes mit data od Boba")
   
   Dam vam vedet az bude hotovo."

[Bob pracuje na datech... → vraci CSV do /output/]
[Claudia cte Bobova data, pise report... → vraci PDF do /output/]

Marketing Lead (Anna):
  "Report je hotovy! Bob stahl data z 3 platform (12,450 interakci)
   a Claudia napsala 15-strankovy report. Najdete ho v souborech:
   /output/reports/social-media-january-2026.pdf
   
   Klicove zjisteni: engagement na LinkedIn vzrostl o 23%..."
```

### 4.2 Uzivatel → Coordinator (cross-crew)

Pro otazky, ktere presahuji jednu crew:

```
Uzivatel: "Kolik nas stoji provoz serveru a jake mame mesicni trzby?"

Coordinator:
  "@ask_lead(finance, 'Jake jsou mesicni naklady na provoz serveru?')
   @ask_lead(sales, 'Jake jsou mesicni trzby za posledni mesic?')"

[Finance Lead → prideli accountantovi → 250,000 CZK]
[Sales Lead → prideli CRM agentovi → 1,200,000 CZK]

Coordinator:
  "Na zaklade dat od Financni a Obchodni crew:
   - Provozni naklady: 250,000 CZK/mesic
   - Mesicni trzby: 1,200,000 CZK
   - Marze: 79.2 %
   
   Poznamka: Naklady vzrostly o 8 % oproti minulemu mesici,
   ale trzby rostly rychleji (+15 %). Trend je pozitivni."
```

### 4.3 Uzivatel → Agent (primo, bypass)

Pokrocili uzivatele mohou chatovat primo s agentem:

```
Uzivatel otevre chat primo s "Bob" (data analyst):
  "Stahni mi raw data z Twitter API za posledni tyden"

Bob: "Stahuji data... [pracuje primo, bez leada]"
```

Lead o tomto vi (vidi v logu), ale neintervenuuje.

### 4.4 Webhook → Lead (externi trigger)

```
Grafana posle webhook → Marketing Lead
Lead: "Dostal jsem alert o poklesu engagementu o 40%.
  @assign(bob, 'Analyzuj pricibu poklesu engagementu za poslednich 24h')
  @assign(claudia, 'Priprav draft krizove komunikace')"
```

### 4.5 Lead ↔ Lead (cross-crew koordinace)

Kdyz lead potrebuje informaci od jine crew:

```
Marketing Lead: "@ask_lead(development, 'Je nejaky bug v API socialnich siti?')"
Development Lead: "Ano, nasli jsme bug v Twitter API integraci, fix deployujeme za 2h."
Marketing Lead → uzivatel: "Pokles engagementu souvisi s bugem v API, Development crew to resi."
```

Toto jde bud:
- **Pres coordinatora** (coordinator koordinuje) — bezpecnejsi, auditovatelne
- **Primo** (lead → lead) — rychlejsi, ale slozitejsi RBAC

**Doporuceni MVP:** Pres coordinatora. Lead-to-lead primo az Phase 3.

---

## 5. IMPLEMENTACNI ARCHITEKTURA

### 5.1 Jak lead prideluje ukoly (technicka vrstva)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2A. Sidecar existuje, ale pouze jako credential proxy a memory endpoint. Assignment endpointy (`/assign`, `/ask`, `/broadcast`, `/results`) neexistuji.

> **Rozhodnuti (ADR-001 v2):** Assignments jdou pres **loopback HTTP sidecar**
> (`crewship-sidecar`), ktery bezi uvnitr kazdeho crew kontejneru.
> CLI tools i vlastni API-direct runtime komunikuji pres `localhost:9119`.

```
┌─────────────────────── Crew Container ───────────────────────┐
│                                                               │
│  ┌─────────────────┐     localhost:9119      ┌────────────┐  │
│  │  Agent process   │ ──── HTTP POST ──────→ │ crewship-  │  │
│  │  (CLI tool nebo  │   /assign              │ sidecar    │  │
│  │   crewship-agent │   /ask                 │ (Go binary,│  │
│  │   API-direct)    │   /results             │  ~5MB)     │──┼──→ crewshipd (gRPC/WS)
│  │                  │   /status              │            │  │
│  │  stdout → user   │                        │ Validace,  │  │
│  │  output (cisty)  │                        │ buffering, │  │
│  │                  │ ←── HTTP response ──── │ retry,     │  │
│  │                  │   (vysledky assignments)│ auth       │  │
│  └─────────────────┘                        └────────────┘  │
│                                                               │
└───────────────────────────────────────────────────────────────┘
```

**Flow:**
```
1. Uzivatel posle zpravu leadovi pres WebSocket
2. crewshipd overí ze crewship-sidecar bezi v kontejneru (startuje s kontejnerem)
3. crewshipd spusti Docker exec pro leada v kontejneru crew
4. crewshipd cte stdout → streaming k uzivatel pres WebSocket + JSONL log
5. Lead (LLM) rozhodne co pridelit:
   - User-facing text → stdout (normalne, cisty)
   - Assignment → HTTP POST na localhost:9119/assign
     (CLI tool: pres bash tool `curl`; API-direct: nativni HTTP call)
6. crewship-sidecar prijme request, validuje:
   - Cil existuje? Connection povolena? RBAC ok? Circuit breaker?
7. crewship-sidecar posle prikaz do crewshipd (gRPC/WebSocket)
8. crewshipd spusti novy Docker exec pro agenta ve STEJNEM kontejneru
9. Agent stdout → JSONL log + crewshipd ceka na dokonceni
10. crewshipd posle vysledek agenta do sidecar
11. Lead polluje GET localhost:9119/results/{group} NEBO sidecar pushne callback
12. Lead agreguje a odpovida uzivatel (stdout → WebSocket)
```

**Proc loopback HTTP sidecar a ne named pipe (viz ADR-001 v2):**
- CLI tools (Claude Code, Codex, OpenCode) umi `curl` — je to standardni tool
- HTTP je debuggovatelny, testovatelny, standardni protokol
- Sidecar validuje prikazy PRED odeslanim do crewshipd
- Sidecar drzi persistentni spojeni s crewshipd — agent nemusi
- Funguje IDENTICKY pro CLI tools i vlastni crewship-agent runtime
- V K8s: sidecar je nativni pattern (sdileny Pod, localhost network)
- Zadna zavislost na LLM spolupracujicim s named pipe

**crewship-sidecar API (localhost:9119):**

Sidecar ma DVE role: (1) assignment proxy a (2) MCP Gateway (ADR-014).

```
ASSIGNMENT PROXY:
  POST /assign      — pridelit ukol agentovi
  POST /ask         — polozit otazku agentovi/leadovi
  POST /broadcast   — fire-and-forget zprava vice agentum
  GET  /results/:id — vyzvednout vysledky assignmentu (polling)
  GET  /status      — stav vsech aktivnich assignments
  WS   /events      — real-time stream vysledku (pro API-direct mode)

MCP GATEWAY (Phase 2, viz AGENT-RUNTIME.md 6A):
  MCP stdio proxy   — zachytava MCP tool cally od agenta
                      injektuje credentials, RBAC check, audit log
  GET /tools/search — tool search meta-tool (on-demand discovery, ADR-016)
  GET /credentials/:env_var — per-request credential (API-direct only, ADR-015)
```

**Autentizace sidecar:**
- Sidecar posloucha JEN na localhost (127.0.0.1:9119) — nedostupny z vne kontejneru
- Kazdy request obsahuje `X-Crewship-Session: {session-id}` header
- Sidecar overuje session-id proti crewshipd

### 5.2 Jak coordinator prideluje ukoly (technicka vrstva)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B. `RunCoordinator` neexistuje, zadna lightweight LLM call logika.

**Varianta C (doporucena): Coordinator jako lightweight agent**

Coordinator **nepotrebuje Docker kontejner** — nepise kod, jen prideluje a agreguje.
Bezi jako cisty LLM call v crewshipd (Go):

```
1. Uzivatel posle zpravu coordinatorovi pres WebSocket
2. crewshipd zavola LLM API primo (ne Docker exec) s coordinator system promptem
3. Coordinator rozhodne kterou crew/leady oslovit
4. crewshipd parsuje output, detekuje @assign_crew / @ask_lead prikazy
5. crewshipd posle zpravy prislusnym leadum (ti uz bezi v Docker exec)
6. crewshipd sbira odpovedi od leadu
7. crewshipd posle zpet coordinatorovi pro agregaci (dalsi LLM call)
8. Coordinator shrne a odpovi
9. crewshipd streamuje pres WebSocket k uzivatel
```

**Vyhody:**
- Zadny novy kontejner, zadny overhead
- Coordinator je rychly (jen LLM reasoning, zadne tools/exec)
- Jednodussi credentials (coordinator nepotrebuje vlastni — pouziva workspace-level LLM key)

**Nevyhody:**
- Coordinator nemuze pouzivat tools (web search, file write)
- Pokud to bude potreba → Varianta A (dedicovany kontejner) v Phase 3

**Rizika a mitigace (ADR-007):**
- LLM API timeout (30s+) blokuje goroutinu → **context.WithTimeout + circuit breaker**
- Coordinator crash neni izolovan → goroutina s **recover()**, panic nepadne cely process
- Bez tools = omezeny → MVP postacujici, Phase 3 prida kontejner

### 5.3 Assignment protokol (Sidecar HTTP API)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2A. DB schema pro `assignments` tabulku existuje, ale logika chybi. Sidecar nema zadne assignment endpointy.

> **Rozhodnuti (ADR-001 v2):** Assignments pres loopback HTTP na crewship-sidecar.
> Standardni HTTP — funguje s CLI tools (curl) i API-direct runtime (nativni HTTP).

**Assignment (POST /assign):**
```json
POST http://localhost:9119/assign
X-Crewship-Session: {session-id}

{
  "target": "bob",
  "task": "Stahni data z Twitter API za leden 2026",
  "wait": true,
  "group": "data-collection",
  "timeout": "30m"
}

Response (202 Accepted):
{
  "assignment_id": "uuid",
  "status": "queued"
}
```

**Dotaz (POST /ask):**
```json
POST http://localhost:9119/ask
{ "target": "claudia", "question": "Kolik slov ma mit executive summary?" }
```

**Cross-crew (POST /assign — s crew_target):**
```json
POST http://localhost:9119/assign
{ "crew_target": "finance", "task": "Mesicni naklady na servery" }
```

**Broadcast (POST /broadcast):**
```json
POST http://localhost:9119/broadcast
{ "targets": ["bob", "claudia"], "message": "Deadline je dnes 18:00" }
```

**Vysledky (GET /results/:group):**
```json
GET http://localhost:9119/results/data-collection

{
  "group": "data-collection",
  "status": "completed",
  "results": [
    {"agent": "bob", "status": "completed", "summary": "Stazeno 12,450 zaznamu.", "output_path": "/mnt/agents/bob/result.json"},
    {"agent": "eve", "status": "failed", "error": "Rate limit exceeded"}
  ]
}
```

**Jak CLI tool prideluje (pres bash/curl):**
```bash
# Claude Code / OpenCode pouzije svuj bash tool:
curl -s -X POST http://localhost:9119/assign \
  -H "X-Crewship-Session: $CREWSHIP_SESSION_ID" \
  -H "Content-Type: application/json" \
  -d '{"target":"bob","task":"Stahni data","wait":true,"group":"data"}'

# Cekani na vysledky:
curl -s http://localhost:9119/results/data
```

**Jak crewship-agent (API-direct) prideluje:**
```go
// Nativni HTTP call — zadny curl, zadny LLM instrukce
resp, _ := http.Post("http://localhost:9119/assign", "application/json", body)
```

**Validace prikazu (sidecar strana):**
1. JSON parse → validace struktury
2. Session-id platny?
3. Target agent/crew existuje? (sidecar cachuje z crewshipd)
4. Connection povolena? (RBAC check)
5. Max assignment depth neprekrocen?
6. Circuit breaker neni otevreny pro target? (viz 5.8)
7. Backpressure check (queue depth)
8. Pokud validace selze → HTTP 422 s chybovou zpravou

### 5.4 Timeout a error handling

```
Lead prideluje agentovi:
  - Timeout: agent.timeout_seconds (default 1800s = 30 min)
  - Pokud agent selze: lead dostane error zpravu, muze:
    a) Zkusit jineho agenta
    b) Zkusit sam
    c) Reportovat uzivatel

Coordinator prideluje leadovi:
  - Timeout: 2x lead.timeout_seconds (lead ceka na agenty)
  - Pokud lead selze: coordinator informuje uzivatel a navrhne alternativu

Max assignment hloubka: 3 (coordinator → lead → agent → sub-task)
Max turns per assignment: 10 (bezpecnostni limit)
```

### 5.5 Paralelni assignments

Lead/Coordinator muze pridelit vice agentum **paralelne**:

```jsonl
{"type":"assign","target":"bob","task":"Data z Twitteru","wait":true,"group":"data-collection"}
{"type":"assign","target":"eve","task":"Data z LinkedInu","wait":true,"group":"data-collection"}
{"type":"wait_group","group":"data-collection"}
{"type":"assign","target":"claudia","task":"Report z dat Boba a Eve","wait":true}
```

crewshipd spusti Boba a Eve paralelne (2 Docker exec soucasne), ceka az oba
skonci (`wait_group`), a pak spusti Claudii s vysledky obou.

### 5.6 Lead Modes (ADR-004)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B. Zadna logika pro active/passive mody.

Lead muze bezet ve dvou modech — uzivatel voli per-lead konfiguraci:

**Active mode (default):**
```
Lead bezi po celou dobu Mission.
Rozhoduje v real-time, reaguje na vysledky agentu prubezne.

1. Lead se spusti (Docker exec, long-running)
2. Lead analyzuje ukol, prideluje agentum (HTTP → sidecar)
3. crewshipd spusti agenty, vysledky posila zpet do sidecar
4. Lead polluje/cte vysledky ze sidecar, rozhoduje co dal
5. Lead odpovi uzivatel az je spokojen s vysledky
```

- **Vyhody:** Flexibilni, lead muze menit strategii za behu
- **Nevyhody:** Drazsi (lead konzumuje LLM tokeny po celou dobu)
- **Use case:** Slozite ukoly, iterativni prace, debugging

**Passive mode:**
```
Lead se spusti 2x: init (task breakdown) a finalize (agregace).
Mezi tim crewshipd orchestruje agenty sam (deterministicky).

1. Lead se spusti — analyzuje ukol, vytvori task plan:
   {"type":"task_plan","tasks":[
     {"target":"bob","task":"...","order":1},
     {"target":"eve","task":"...","order":1},
     {"target":"claudia","task":"...","order":2,"depends_on":["bob","eve"]}
   ]}
2. Lead se ukonci (exit)
3. crewshipd provede task plan: spusti agenty dle order a depends_on
4. Vsichni agenti hotovi → crewshipd spusti leada znovu s vysledky
5. Lead agreguje, odpovi uzivatel
6. Lead se ukonci
```

- **Vyhody:** Levnejsi (2 LLM calls misto N), deterministicky
- **Nevyhody:** Lead nemuze reagovat na neocekavane vysledky
- **Use case:** Rutinni ukoly, reporty, sber dat

**Konfigurace:**
```prisma
// V Agent modelu:
  lead_mode    String?  @default("active")  // "active" | "passive"
```

### 5.7 Agent Output Compression (ADR-005)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B. Zadna sumarizace vystupu.

**Problem:** Agent vraci 50k tokens output. Lead musi tento output precist
→ lead context = system prompt + user msg + 50k agent output = drahé.

**Reseni:** crewshipd automaticky komprimuje agent output pred predanim leadovi.

```
Agent output flow:
1. Agent zapise plny vysledek do /output/bob/result.json (50k tokens)
2. crewshipd precte vysledek
3. Pokud vysledek > agent_output_max_tokens (default 2000):
   a) crewshipd zavola LLM s promptem: "Summarize this result in max 2000 tokens"
   b) Sumarizace → posle leadovi pres response pipe
   c) Plny vysledek → file reference: /mnt/agents/bob/result.json
4. Pokud vysledek <= limit:
   → posle primo leadovi (bez sumarizace)
```

**Co lead dostane:**
```jsonl
{
  "type": "assignment_result",
  "agent": "bob",
  "status": "completed",
  "summary": "Stazeno 12,450 zaznamu z Twitter, Instagram a LinkedIn za leden 2026. Highest engagement: LinkedIn post o AI trendech (2,340 reactions).",
  "full_output_path": "/mnt/agents/bob/social-media-data.json",
  "original_tokens": 48500,
  "summary_tokens": 1850
}
```

Lead muze precist plny output z filesystemu pokud potrebuje detail,
ale ve vetsine pripadu mu sumarizace staci pro agregaci.

**Konfigurace:**
```prisma
// V Agent modelu (lead only):
  agent_output_max_tokens  Int?  @default(2000)  // max tokens per agent result
```

**Naklady na sumarizaci:**
- Sumarizace = 1 LLM call per agent vysledek (levny model, napr. Claude Haiku)
- Trade-off: maly naklad na sumarizaci vs velka uspora na lead contextu
- Pri 5 agentech: 5 × Haiku call (~$0.01) vs 5 × 50k tokens v lead contextu (~$0.50)

### 5.8 Circuit Breaker pro assignments (ADR-006)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2A. Existuje pouze `CooldownManager` v `internal/orchestrator/failover.go` pro credential cooldown po 429, NE circuit breaker pro agenty. Kod v teto sekci (CircuitBreaker struct) je navrh, ne existujici implementace.

**Problem:** Agent opakovane selhava (bug, spatny prompt, nedostupna sluzba).
Bez circuit breakeru lead porad zkouší pridelit → plytva tokeny a casem.

**Implementace:**
```
Circuit Breaker states per agent:
  CLOSED  → normalni provoz, assignments prochazi
  OPEN    → agent docasne vyfadovan, assignments se odmitnou
  HALF    → zkusebni assignment (1 pokus), pokud uspeje → CLOSED

Prechody:
  CLOSED → OPEN:   3 po sobe jdouci faily (configurable: max_consecutive_failures)
  OPEN → HALF:     po cooldown periode (default 5 min)
  HALF → CLOSED:   zkusebni assignment uspel
  HALF → OPEN:     zkusebni assignment selhal → dalsi cooldown
```

**Error handling flow:**
```
1. Agent selze (exit code != 0 nebo timeout)
2. crewshipd inkrementuje failure counter pro agenta
3. Pokud failures < 3:
   a) Auto-retry s exponential backoff (1s, 2s, 4s)
   b) Kazdy retry = novy Docker exec se stejnym taskem
4. Pokud failures >= 3:
   a) Circuit OPEN — agent vyfadovan
   b) Eskalace na leada: {"type":"agent_unavailable","agent":"bob","reason":"3 consecutive failures","last_error":"..."}
   c) Lead rozhodne:
      - Priradit ukol jinemu agentovi
      - Zkusit sam
      - Informovat uzivatel
5. Po cooldown (5 min): circuit HALF → 1 zkusebni assignment
```

**Backpressure:**
- Max queue depth per lead: 10 cekajicich assignments (default)
- Pokud fronta plna → lead dostane error: `{"type":"backpressure","message":"assignment queue full"}`
- Lead muze pockat (wait_group) nebo informovat uzivatel

**Go implementace:**
```go
// internal/orchestrator/circuit_breaker.go

type CircuitState int

const (
    CircuitClosed CircuitState = iota
    CircuitOpen
    CircuitHalf
)

type CircuitBreaker struct {
    state       CircuitState
    failures    int
    maxFailures int           // default: 3
    cooldown    time.Duration // default: 5m
    lastFailure time.Time
    mu          sync.RWMutex
}

func (cb *CircuitBreaker) Allow() bool {
    cb.mu.RLock()
    defer cb.mu.RUnlock()
    switch cb.state {
    case CircuitClosed:
        return true
    case CircuitOpen:
        if time.Since(cb.lastFailure) > cb.cooldown {
            cb.mu.RUnlock()
            cb.mu.Lock()
            cb.state = CircuitHalf
            cb.mu.Unlock()
            cb.mu.RLock()
            return true // jeden zkusebni pokus
        }
        return false
    case CircuitHalf:
        return true // jeden pokus probehne
    }
    return false
}
```

### 5.9 Dual Runtime Architecture (ADR-009)

Crewship podporuje dva agent runtime mody — CLI-first a API-direct:

```prisma
enum AgentRuntime {
  CLI_CLAUDE_CODE    // Docker exec → claude --print
  CLI_OPENCODE       // Docker exec → opencode run
  CLI_CODEX          // Docker exec → codex
  CLI_GEMINI         // Docker exec → gemini -p
  API_DIRECT         // crewship-agent binary → LLM API primo
}
```

**CLI mode (Phase 1 — MVP):**
- Docker exec spusti CLI tool (Claude Code, OpenCode, Codex, Gemini CLI)
- CLI tool vola LLM API sam, ma vlastni tool use implementaci
- Assignments pres `curl localhost:9119/assign` (bash tool)
- Stdout → user output, CLI tool formatuje
- Token tracking: parsovani ze stdout (nepresne)

**API-direct mode (Phase 2):**
- Docker exec spusti `crewship-agent` (vlastni Go binary, ~5MB)
- `crewship-agent` vola LLM API primo pres oficilani SDK:
  - Anthropic: `github.com/anthropics/anthropic-sdk-go`
  - OpenAI: `github.com/openai/openai-go`
  - Google: `google.golang.org/genai`
  - Ollama: `github.com/ollama/ollama/api`
- Tool use implementovany nativne (file_read, file_write, bash, grep, web_search)
- Assignment pres nativni HTTP call na sidecar (zadny curl, zadny LLM instrukce)
- Presny token tracking z API response
- Plna lifecycle kontrola (pause, resume, cancel)

**Srovnani:**

| Aspekt | CLI mode | API-direct mode |
|---|---|---|
| Latence | CLI startup + LLM | Jen LLM call |
| Memory per agent | 200-300MB (Node.js) | ~10MB (Go goroutina) |
| Kontrola lifecycle | Zadna (CLI black box) | Plna |
| Token tracking | Nepresne (stdout parse) | Presne (API response) |
| Assignments | curl (LLM instrukce) | Nativni HTTP (spolehljive) |
| Cost estimation | Nemozne | Presne |
| Multi-provider | 1 CLI = 1 provider | Libovolny provider per call |
| Image size | ~500MB (Node.js + CLI) | ~50MB (Go binary) |
| Tool use | CLI-native (silne) | Vlastni implementace |
| Use case | Coding-heavy, power users | Obecne ukoly, enterprise |

**Doporuceni:**
- Phase 1: CLI_CLAUDE_CODE a CLI_OPENCODE jako default
- Phase 2: API_DIRECT jako alternativa (uzivatel voli per agent)
- Phase 3: API_DIRECT jako default, CLI jako "power adapter"

### 5.10 Trace ID across assignments (ADR-012)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B. Zadna trace korelace.

Kazda mission dostane unikatni `trace_id` ktery propojuje:
- Lead session
- Vsechny agent sessions
- Assignment logy
- JSONL konverzace
- Meilisearch indexy

```
trace_id: "mission-{uuid}"
  ├── lead_session: "session-{uuid}" (trace_id v JSONL metadata)
  ├── assignment_1: "assign-{uuid}" (trace_id v Assignment)
  │   └── agent_session: "session-{uuid}" (trace_id v JSONL metadata)
  ├── assignment_2: "assign-{uuid}"
  │   └── agent_session: "session-{uuid}"
  └── ...
```

Uzivatel muze v UI zobrazit celou mission jako timeline
a prokliknout se do libovolne agent session.

---

## 6. DATOVY MODEL — ZMENY

> **Status: DB SCHEMA EXISTUJE, LOGIKA NENI** — Tabulka `assignments` a sloupce `agent_role`, `delegation_timeout_s`, `max_delegation_depth`, `max_parallel_delegates` existuji v DB (migrace 2 v `migrate.go`), ale zadna Go logika je nevyuziva. Pozor: DB pouziva "delegation" naming (viz poznamka v STAV IMPLEMENTACE nahore).

### 6.1 Novy enum: AgentRole

```prisma
enum AgentRole {
  AGENT        // default — radovy agent, specializovany na konkretni ukoly
  LEAD         // 1 per crew — sef crew, orchestruje agenty
  COORDINATOR  // 1 per workspace — koordinator, orchestruje cross-crew
}
```

### 6.2 Zmeny v Agent modelu

```prisma
model Agent {
  // ... existujici pole ...
  
  agent_role      AgentRole   @default(AGENT)
  
  // crew_id se stava NULLABLE — coordinator nema crew
  crew_id         String?     @db.Uuid
  
  // lead/coordinator specific
  assignment_timeout_s     Int?    // override timeout pro assignments (default: 2x agent timeout)
  max_assignment_depth     Int?    @default(3)   // max hloubka assignmentu
  max_parallel_assignments Int?    @default(5)   // max paralelne bezicich assignments
  lead_mode                String? @default("active")  // "active" | "passive" (viz 5.6)
  agent_output_max_tokens  Int?   @default(2000)      // max tokens per agent result (viz 5.7)
  runtime                  String? @default("CLI_CLAUDE_CODE")  // AgentRuntime enum (viz 5.9)
}
```

### 6.3 Constraints

- **Max 1 LEAD per crew:** Aplikacni uroven (service layer check)
- **Max 1 COORDINATOR per workspace:** Aplikacni uroven
- **Coordinator.crew_id = null:** Aplikacni uroven (Prisma middleware)
- **Lead.crew_id != null:** Aplikacni uroven

### 6.4 Novy model: Assignment

```prisma
model Assignment {
  id              String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id    String   @db.Uuid
  session_id      String   @db.Uuid
  source_agent_id String   @db.Uuid  // kdo pridelil (lead/coordinator)
  target_agent_id String   @db.Uuid  // komu (agent/lead)
  task            String   @db.Text  // co bylo prideleno
  status          AssignmentStatus @default(PENDING)
  started_at      DateTime? @db.Timestamptz
  finished_at     DateTime? @db.Timestamptz
  result_summary  String?  @db.Text  // shrnuti vysledku (od targetu)
  error_message   String?  @db.Text
  group_id        String?  // pro paralelni assignments (wait_group)
  trace_id        String?  // mission trace (viz 5.10)
  created_at      DateTime @default(now()) @db.Timestamptz

  workspace    Workspace @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  session      Chat @relation(fields: [session_id], references: [id])
  source_agent Agent @relation("AssignedBy", fields: [source_agent_id], references: [id])
  target_agent Agent @relation("AssignedTo", fields: [target_agent_id], references: [id])

  @@index([session_id], name: "idx_assignment_session")
  @@index([source_agent_id], name: "idx_assignment_source")
  @@index([target_agent_id], name: "idx_assignment_target")
  @@map("assignments")
}

enum AssignmentStatus {
  PENDING
  RUNNING
  COMPLETED
  FAILED
  TIMEOUT
  CANCELLED
}
```

---

## 7. GO SERVICE — ORCHESTRACNI LOGIKA

### 7.1 AssignmentEngine (novy modul v crewshipd)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2A. Soubor `internal/orchestrator/assignment.go` neexistuje.

```go
// internal/orchestrator/assignment.go

type AssignmentEngine struct {
    orchestrator *Orchestrator
    ws           *ws.Server
    wal          *state.WAL
}

type AssignmentRequest struct {
    Type        string   // "assign", "ask", "assign_crew", "ask_lead", "broadcast"
    SourceAgent string   // agent ID ktery prideluje
    TargetAgent string   // agent slug (nebo crew slug pro assign_crew)
    Task        string   // popis ukolu / otazka
    Wait        bool     // cekej na odpoved?
    GroupID     string   // pro paralelni assignments
    SessionID   string   // parent session
}

type AssignmentResult struct {
    TargetAgent string
    Status      string // "completed", "failed", "timeout"
    Output      string // vysledek od target agenta
    Duration    time.Duration
}
```

### 7.2 crewship-sidecar (Go binary v kontejneru)

> **Status: CASTECNE IMPLEMENTOVANO** — Sidecar existuje a funguje jako credential proxy + memory endpoint. Assignment handling (`HandleAssign`, `HandleResults`, `CircuitBreaker`, `MCPServerProcess`) popsany nize NENI implementovan.

```go
// cmd/crewship-sidecar/main.go
// Lightweight HTTP server bezici uvnitr kazdeho crew kontejneru.
// Startuje s kontejnerem, posloucha na localhost:9119.

type Sidecar struct {
    crewshipdConn *grpc.ClientConn // nebo WebSocket connection
    crewID        string
    sessions      map[string]*SessionState
    breakers      map[string]*CircuitBreaker
    mcpServers    map[string]*MCPServerProcess  // skill_id → running MCP server
    credentials   map[string]string             // env_var → decrypted value (Phase 2)
    mu            sync.RWMutex
}

func (s *Sidecar) HandleAssign(w http.ResponseWriter, r *http.Request) {
    sessionID := r.Header.Get("X-Crewship-Session")
    if sessionID == "" {
        http.Error(w, "missing session ID", http.StatusUnauthorized)
        return
    }
    
    var req AssignmentRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "invalid JSON", http.StatusBadRequest)
        return
    }
    req.SessionID = sessionID
    
    // Validace
    if err := s.validateAssignment(req); err != nil {
        writeJSON(w, http.StatusUnprocessableEntity, ErrorResponse{Error: err.Error()})
        return
    }
    
    // Odeslani do crewshipd
    assignID, err := s.crewshipd.Assign(r.Context(), req)
    if err != nil {
        writeJSON(w, http.StatusBadGateway, ErrorResponse{Error: err.Error()})
        return
    }
    
    writeJSON(w, http.StatusAccepted, AssignmentResponse{
        AssignmentID: assignID,
        Status:       "queued",
    })
}

func (s *Sidecar) HandleResults(w http.ResponseWriter, r *http.Request) {
    groupID := r.PathValue("group")
    results, err := s.crewshipd.GetResults(r.Context(), groupID)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    writeJSON(w, http.StatusOK, results)
}
```

**Sidecar lifecycle:**
- Startuje jako dlouhodoby proces pri vytvoreni kontejneru (`docker exec -d`)
- Healthcheck: crewshipd periodicky pinguje sidecar
- Restart policy: pokud sidecar spadne, crewshipd ho restartuje
- Memory footprint: ~5MB base + ~2MB per MCP server (staticka Go binary)
- Phase 2: sidecar spousti MCP servery (stdio) pro prirazene skills
- Phase 2: sidecar drzi desifrovane credentials pro MCP servery (ne agent!)

**Sidecar role (ADR-014):**
1. Assignment proxy (Phase 1): POST /assign, /ask, /broadcast, GET /results
2. MCP Gateway (Phase 2): MCP stdio proxy, credential injection, RBAC, audit
   - Spousti MCP servery pro skills prirazene agentu v teto crew
   - Injektuje credentials do MCP serveru (agent je NEMA)
   - Zachytava MCP tool cally a loguje s credential_id
   - Vystavuje search_tools meta-tool pro on-demand discovery

### 7.3 crewship-agent (API-direct runtime, Phase 2)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B. `cmd/crewship-agent/` neexistuje.

```go
// cmd/crewship-agent/main.go
// Vlastni agent runtime — vola LLM API primo, tool use nativne.

type Agent struct {
    sidecarURL string // "http://localhost:9119"
    llmClient  LLMClient
    tools      []Tool
    sessionID  string
}

// LLMClient — provider-agnostic interface
type LLMClient interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

// Tool use — nativni implementace
var defaultTools = []Tool{
    &FileReadTool{},
    &FileWriteTool{},
    &BashExecTool{},
    &GrepTool{},
    &WebSearchTool{},
    &AssignTool{sidecarURL: "http://localhost:9119"}, // assignment pres sidecar
}

// AssignTool — LLM ho vola jako normalní tool
type AssignTool struct {
    sidecarURL string
}

func (d *AssignTool) Execute(ctx context.Context, args AssignArgs) (*ToolResult, error) {
    body, _ := json.Marshal(args)
    resp, err := http.Post(d.sidecarURL+"/assign", "application/json", bytes.NewReader(body))
    // ...
    return &ToolResult{Content: result}, nil
}
```

**Vyhody crewship-agent oproti CLI tools:**
- Assignment je TOOL (ne bash curl) — LLM ho vola spolehlive pres function calling
- Presny token usage z API response
- ~5MB binary vs ~300MB CLI tool
- Podpora vsech LLM provideru z jednoho binary

### 7.4 Coordinator execution (lightweight, bez Docker)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B. `internal/orchestrator/coordinator.go` neexistuje.

```go
// internal/orchestrator/coordinator.go

func (o *Orchestrator) RunCoordinator(ctx context.Context, req CoordinatorRequest) error {
    // Coordinator bezi jako cisty LLM call — zadny Docker exec
    
    // 1. Sestav system prompt s informacemi o crews a leadech
    systemPrompt := o.buildCoordinatorSystemPrompt(req.WorkspaceID)
    
    // 2. Zavolej LLM API primo
    response, err := o.llmClient.Chat(ctx, LLMRequest{
        Model:        req.Model,
        SystemPrompt: systemPrompt,
        Messages:     req.Messages,
        Temperature:  req.Temperature,
    })
    
    // 3. Parsuj assignment prikazy z odpovedi
    commands, text := ParseAssignmentCommands(response.Content)
    
    // 4. Stream text cast k uzivatel
    o.ws.Send(req.SessionID, AgentEvent{Type: "text", Content: text})
    
    // 5. Proved assignments
    for _, cmd := range commands {
        go o.executeAssignment(ctx, cmd, req.SessionID)
    }
    
    return nil
}
```

---

## 8. UI IMPLIKACE

### 8.1 Chat s leadem

- Chat UI je **stejny** jako pro bezneho agenta
- Navic: **assignment timeline** — uzivatel vidi kdy lead pridelil, komu, status
- Vizualizace: `[Lead Anna] → pridelila [Bob] → "sber dat"` s progressem
- Streaming: uzivatel vidi v realtime co lead premysli a co prideluje

### 8.2 Chat s coordinatorem

- Specialni chat UI (nebo stejny s jinym oznacenim)
- Cross-crew assignment vizualizace
- Uzivatel vidi ktere crews coordinator oslovil

### 8.3 Agent detail — role badge

- Agent: zadny badge (default)
- Lead: badge "Crew Lead" / ikona Crown
- Coordinator: badge "Coordinator" / ikona Building

### 8.4 Crew view — lead na prvnim miste

V seznamu agentu v crew je lead vzdy na prvnim miste, vizualne odliseny:

```
Crew: Marketing
├─ 👑 Anna (Crew Lead) — RUNNING
├─ Bob (Data Analyst) — IDLE
├─ Claudia (Copywriter) — IDLE
└─ Dave (SEO Specialist) — IDLE
```

### 8.5 Workspace view — coordinator card

Na dashboardu workspace se zobrazi karta coordinatora:

```
🏢 Coordinator: Max
   Status: IDLE
   Crews pod spravou: 4 (Marketing, Development, Finance, Support)
   [Chat s koordinatorem]
```

---

## 9. BEZPECNOST A RBAC

### 9.1 Kdo muze chatovat s kym

| Uzivatel role | Agent | Lead | Coordinator |
|---|---|---|---|
| VIEWER | Read-only | Read-only | Read-only |
| MEMBER | Chat (prirazena crew) | Chat (prirazena crew) | NE |
| MANAGER | Chat (prirazena crew) | Chat (prirazena crew) | NE |
| ADMIN | Chat (vsechny crews) | Chat (vsechny crews) | Chat |
| OWNER | Chat (vsechny crews) | Chat (vsechny crews) | Chat |

### 9.2 Assignment RBAC

- Lead muze pridelovat **jen agentum ve sve crew**
- Coordinator muze pridelovat **jen leadum** (ne primo agentum)
- Agent **nemuze pridelovat** (zadny pristup k assignment prikazum)

### 9.3 Credentials pri assignmentu

- **Lead → Agent:** Agent dedi credentials od crew (normalni flow)
- **Coordinator → Lead:** Coordinator pouziva workspace-level LLM credentials
- **Cross-crew assignment:** Lead ma pristup jen ke credentials sve crew

### 9.4 Audit log

Vsechny assignments se loguji:

```
action: "assignment.created"
entity_type: "Assignment"
metadata: {
  source_agent: "anna (lead)",
  target_agent: "bob (agent)",
  task: "Stahni data z Twitter API",
  crew: "marketing"
}
```

---

## 10. FAZOVANI IMPLEMENTACE

### Phase 2A: Crew Lead (s prvni vlnou orchestrace)

| ID | Feature | Popis |
|---|---|---|
| ORCH-01 | AgentRole enum + DB migrace | Pridani `agent_role` do Agent modelu |
| ORCH-02 | Lead designation UI | Oznaceni agenta jako leada v crew settings |
| ORCH-03 | Auto-generated lead system prompt | Automaticky system prompt s informacemi o crew |
| ORCH-04 | crewship-sidecar | Loopback HTTP sidecar v kontejneru (ADR-001 v2) |
| ORCH-05 | Assignment protokol (HTTP) | REST API na sidecar, validace, RBAC |
| ORCH-06 | Lead → Agent assignment | Docker exec orchestrace v ramci crew |
| ORCH-07 | Assignment timeline v UI | Vizualizace assignments v chatu |
| ORCH-08 | Assignment tabulka | Auditovani vsech assignments |
| ORCH-09 | Lead auto-routing | Uzivatel pise do crew, lead rozhodne komu |
| ORCH-10 | Paralelni assignments | wait_group pattern pro vice agentu soucasne |
| ORCH-11 | Error handling + circuit breaker | Auto-retry 3x → eskalace na leada (viz 5.8) |
| ORCH-12 | Lead summary/agregace | Lead shrnuje vysledky pred odeslani uzivatel |

### Phase 2B: Coordinator + advanced features

| ID | Feature | Popis |
|---|---|---|
| ORCH-13 | Coordinator agent role | Specialni agent na urovni workspace |
| ORCH-14 | Coordinator lightweight execution | LLM call bez Docker kontejneru (ADR-007) |
| ORCH-15 | Coordinator → Lead assignment | Cross-crew orchestrace |
| ORCH-16 | Coordinator auto-routing | Coordinator rozhodne kterou crew oslovit |
| ORCH-17 | Cross-crew agregace | Coordinator sbira odpovedi od vice crews |
| ORCH-18 | Coordinator UI (dashboard card + chat) | Specialni karta na dashboardu |
| ORCH-19 | Lead modes (active/passive) | Dva mody leada — viz 5.6 (ADR-004) |
| ORCH-20 | Agent output compression | Auto-sumarizace agent vystupu — viz 5.7 (ADR-005) |
| ORCH-21 | Cost estimation per mission | Odhad token nakladu pred spustenim mission |
| ORCH-22 | Per-mission budget limits | Max token/dollar budget per mission |
| ORCH-23 | crewship-agent binary (API-direct) | Vlastni Go runtime, primo LLM API (ADR-009) |
| ORCH-24 | Trace ID across assignments | Korelace sessions/assignments (ADR-012) |
| ORCH-25 | Meilisearch conversation search | Async JSONL → search index (ADR-011) |

### Phase 3: Pokrocila orchestrace + skalovani

| ID | Feature | Popis |
|---|---|---|
| ORCH-23 | Lead ↔ Lead primo | Primo cross-crew komunikace bez coordinatora |
| ORCH-24 | Coordinator s tools | Coordinator dostane vlastni kontejner a tools |
| ORCH-25 | Coordinator long-term memory | Strategicke cile, KPIs, trendy |
| ORCH-26 | Orchestracni vizualizace | Graf assignments v realtime (OrchVis pattern) |
| ORCH-27 | Auto-lead election | AI doporuci ktery agent by mel byt lead |
| ORCH-28 | Coordinator → Coordinator (multi-workspace) | Spoluprace mezi workspaces pres webhooky |
| ORCH-29 | NATS JetStream integrace | Message broker pro multi-node cluster (ADR-002) |
| ORCH-30 | gVisor runtime | Optional container runtime pro multi-tenant (ADR-003) |
| ORCH-31 | Assignment replay/debug | Prehrat celou mission krok po kroku |
| ORCH-32 | Landlock per-agent izolace | Filesystem izolace uvnitr kontejneru (ADR-010) |
| ORCH-33 | API-direct jako default | CLI tools jako legacy adapter |

---

## 11. SROVNANI S KONKURENCI

| Aspekt | CrewAI | LangGraph | AutoGen | **Crewship** |
|---|---|---|---|---|
| Orchestracni vzor | Hierarchical Crew | Graph/DAG | Conversation | **3-urovnova hierarchie** |
| Cilova skupina | Developeri (Python) | Developeri (Python) | Developeri (Python) | **Byznys uzivatele (UI)** |
| Lead koncept | Manager Agent | Supervisor node | GroupChat manager | **Crew Lead (lidsky)** |
| Cross-crew | Nested crews | Sub-graphs | Nested groups | **Coordinator** |
| Konfigurace | Kod (Python) | Kod (Python) | Kod (Python) | **Web UI + auto-prompty** |
| Deployment | Library (pip) | Library (pip) | Library (pip) | **Platform (Docker/K8s)** |
| Security | Zadna izolace | Zadna izolace | Zadna izolace | **Container + RBAC + audit** |
| Assignment vizualizace | Terminal logy | LangSmith | Terminal logy | **Real-time UI timeline** |

---

## 12. OTEVRENE OTAZKY

### Uzavrene (rozhodnuto)

4. ~~**Assignment format:**~~ → **Named pipe + JSON** (ADR-001). Cisty kanal, zadne kolize se stdout.
5. ~~**Context window:**~~ → **Agent output compression** (ADR-005). Auto-sumarizace na 2k tokens.
6. ~~**Naklady:**~~ → **Cost estimation** feature v Phase 2B (ORCH-21). UI ukazuje odhad pred spustenim.
7. ~~**Deadlock:**~~ → **Circuit breaker + backpressure** (ADR-006). Max queue 10, 3 retries, cooldown.

### Stale otevrene

1. **Coordinator kontejner (Phase 3):** Kdyz coordinator dostane tools, jaky kontejner? Dedicovany "workspace kontejner"?
2. **Lead volba:** Muze uzivatel zmenit leada crew za behu? Co se stane s probihajicimi assignments?
3. **Multi-lead:** Muze mit crew vice leadu (napr. den/noc smena)? Pravdepodobne ne v MVP.
8. **Metriky:** Jak merit efektivitu leada? (cas odpovedi, pocet assignments, uspesnost)
9. **Sumarizacni model:** Jaky LLM pouzit pro agent output compression? Haiku/GPT-4o-mini? Konfigurovatelne?
10. ~~**Pipe security:**~~ → Nahrazeno sidecar HTTP (localhost only, session auth). Viz ADR-001 v2.
11. **Sidecar port conflict:** Co kdyz agent proces pouziva port 9119? Konfigurovatelny port?
12. **crewship-agent tool coverage:** Pokryva crewship-agent vsechny tools co Claude Code? LSP?

---

## 13. PRIKLADY PRO RUZNE ODVETVI

### IT firma
```
Coordinator "Max"
├── Development (Lead: "Tomas")
│   ├── Agent "Alice" (Backend Dev)
│   ├── Agent "Charlie" (Frontend Dev)
│   └── Agent "Diana" (QA)
├── DevOps (Lead: "Viktor")
│   ├── Agent "SRE-bot" (Monitoring)
│   └── Agent "Deploy-bot" (CI/CD)
└── Support (Lead: "Petra")
    ├── Agent "Help-bot" (Ticketing)
    └── Agent "Docs-bot" (Knowledge Base)
```

### Marketing agentura
```
Coordinator "Jana"
├── Kreativa (Lead: "Anna")
│   ├── Agent "Copywriter"
│   ├── Agent "Designer"
│   └── Agent "Video Editor"
├── Analytics (Lead: "Martin")
│   ├── Agent "Data Analyst"
│   └── Agent "SEO Specialist"
└── Social Media (Lead: "Lucie")
    ├── Agent "Community Manager"
    └── Agent "Influencer Scout"
```

### Zubni ordinace (maly byznys)
```
Coordinator (neni — mala firma)
├── Recepce (Lead: "Recepční")
│   └── Agent "Objednávkový asistent"
└── Admin (Lead: "Účetní")
    └── Agent "Fakturant"
```

---

## 14. HUMAN-IN-THE-LOOP (APPROVAL FLOW)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B+. Zadny approval flow, trust levels, ani approval routing.

### 14.1 Trust Levels

Kazdy agent ma konfigurovatelny **Trust Level** ktery urcuje kdy se musi ptat na schvaleni:

| Level | Chovani | Popis |
|---|---|---|
| `LOW` | Auto-approve vse | Trusted agent, rutinni ukoly. Zadne preruseni. |
| `MEDIUM` (default) | Approve destructivni akce | Default pro nove agenty. Bezpecny zaklad. |
| `HIGH` | Approve kazdou akci | Testovani, nebezpecne prostredi, novy agent. |
| `CUSTOM` | Per-action pravidla | Pokrocili uzivatele, firewall-like rules. |

**MEDIUM pravidla (default):**
```
Auto-approve:
  ✅ file_read (cokoli)
  ✅ file_write do /output/
  ✅ web_search
  ✅ grep, ls, cat
  ✅ git status, git diff, git log

Require approval:
  ⚠️ git push
  ⚠️ external API call (mimo whitelist)
  ⚠️ file delete mimo /output/
  ⚠️ bash s sudo
  ⚠️ curl/wget na neznamy endpoint

Block always:
  🚫 network access mimo whitelist
  🚫 eskalace na root
  🚫 pristup mimo /workspace/ a /output/
```

### 14.2 Approval Flow

```
Agent vola tool
    ↓
crewshipd zkontroluje trust level pravidla
    ↓
Vyzaduje approval? ──── NE ──→ Provede se
    ↓ ANO
Agent se pozastavi (status: AWAITING_APPROVAL)
    ↓
crewshipd posle approval request do VSECH kanalu:
    ├── Crewship UI chat (vzdy, default)
    ├── Messaging kanal (Discord/Telegram/Slack/WhatsApp)
    ├── Email (SMTP)
    └── Webhook (custom endpoint)
    ↓
Uzivatel odpovi v JAKEMKOLI kanalu
    ↓
APPROVED → agent pokracuje
REJECTED → agent se zastavi, dostane error message
TIMEOUT (30 min default) → auto-reject
```

### 14.3 Approval v kontextu orchestrace

Kdyz Lead deleguje na agenta a agent narazí na approval:
- Agent se pozastavi → Lead je informovan ("Agent Bob ceka na schvaleni")
- Lead NEMUZE schvalit za uzivatele (bezpecnostni omezeni)
- Uzivatel schvali → agent pokracuje → Lead dostane vysledek
- Uzivatel odmitne → Lead dostane error → muze zkusit alternativni pristup

### 14.4 Konfigurace (DB)

```prisma
model Agent {
  // ... existujici pole ...
  trust_level    String  @default("MEDIUM")  // LOW | MEDIUM | HIGH | CUSTOM
  // CUSTOM rules ulozeny v JSON sloupci nebo samostatne tabulce
}
```

---

## 15. CHANNEL GATEWAY (MESSAGING)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 2B+. Discord/Telegram/Slack/WhatsApp integrace neexistuje.

### 15.1 Architektura

Messaging kanaly jsou **opt-in modul v crewshipd**, NE skills.
Messaging session musi byt persistent long-running process s otevrenymi connections.
Skill je ephemeral. Gateway musi bezet non-stop a routovat zpravy ke spravnym agentum.

**Inspirace:** OpenClaw Gateway — centralni daemon co vlastni vsechny messaging
sessions (WhatsApp pres Baileys, Telegram pres grammY, Slack, Discord).
Crewship integruje tento pattern do existujiciho crewshipd procesu.

```
crewshipd (Go service)
  ├── WebSocket gateway (existujici — browser ↔ agent)
  ├── Docker orchestrator (existujici)
  └── Channel Gateway (opt-in, Phase 2)
        ├── ChannelProvider interface
        ├── Discord   (discordgo)
        ├── Telegram   (go-telegram-bot-api)
        ├── Slack      (slack-go)
        ├── WhatsApp   (whatsmeow — Go reimpl of Baileys, Phase 2B)
        └── Custom webhook (incoming/outgoing)
```

### 15.2 Message Routing

```
Incoming zprava z kanalu (napr. Telegram):
1. Channel Gateway prijme zpravu
2. Identifikuje odesilatele (phone/user mapping → Crewship user)
3. Parsuje target: "@bob analyse this" → agent "bob"
   nebo routuje na crew lead (default)
4. Vytvori/najde chat session
5. Posle zpravu do crewshipd → Docker exec → agent
6. Agent odpovi → crewshipd → Channel Gateway → zpet do kanalu
```

### 15.3 Approval pres messaging

```
Agent Bob pozaduje schvaleni (git push)
  → crewshipd vytvori approval request
  → Posle do Crewship UI + Telegram (pokud nakonfigurovano)
  
Telegram zprava:
  "🔔 Agent Bob ceka na schvaleni:
   Akce: git push origin main
   Crew: Development
   
   Odpovezte: /approve nebo /reject"

Uzivatel odpovi /approve → propagace do crewshipd → Bob pokracuje
```

### 15.4 Proc ne skill

| Aspekt | Skill (ephemeral) | Gateway modul (persistent) |
|---|---|---|
| Lifecycle | Spusti se → udela praci → skonci | Bezi non-stop s crewshipd |
| WA session | Nova session kazdy run (QR scan) | Jedna session, perzistentni |
| Message routing | Agent sam routuje | crewshipd routuje centralne |
| Multi-agent | 1 skill = 1 agent | Gateway routuje na libovolneho agenta |
| Approval | Nelze (skill neposloucha) | Gateway aktivne posloucha odpovedi |

---

## 16. CREWSHIP CONNECT (CROSS-WORKSPACE, Phase 3)

> **Status: NENI IMPLEMENTOVANO** — planovane pro Phase 3.

### 16.1 Vize

Workspaces mohou komunikovat navzajem pres zabezpecene webhooky.
Priklad: Pavel ma osobniho AI asistenta → posle webhook na workspace zubare → objedna termin.

### 16.2 Trust Model

```
Workspace A (Pavel) → webhook → Workspace B (Zubar)
                        ↓
Trust vyzaduje:
  1. Oba workspaces se musi "propojit" (mutual trust — obastranny souhlas)
  2. Podepsane zpravy (JWT/HMAC s workspace secret)
  3. Rate limiting (max N zprav/min)
  4. Sandbox: prijaty webhook prochazi stejnym approval flow
  5. Scope: co muze externi workspace pozadovat (jen konkretni crew/agent)
```

### 16.3 Varianty implementace

| Varianta | Pro cloud tier | Pro self-hosted |
|---|---|---|
| **Centralni registry (crewship.ai)** | Discovery, trust broker | Vyzaduje cloud |
| **Federation (ActivityPub model)** | Decentralizovane | Komplexni, ale free |
| **Primo webhook** | Jednoduche | Nutny public endpoint |

**Doporuceni:** Phase 3, zacit s primym webhook (nejjednodussi), cloud tier prida registry.

---

*Tento dokument je zivy — bude se aktualizovat s kazdou novou iteraci implementace.*
*Posledni aktualizace stavu implementace: 2026-02-20.*
