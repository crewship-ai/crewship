# Crewship -- Orchestrace: Crew Leader + Virtual Director

**Verze:** 2.0
**Datum:** 2026-02-15
**Status:** Architekturni navrh (implementace Phase 2)
**Zmeny v3.0:** Loopback HTTP sidecar (nahrazuje named pipe jako primarni),
dual runtime (CLI + API direct), Landlock per-agent izolace,
leader modes (active/passive), worker output compression, circuit breaker,
Meilisearch conversation search, trace ID across delegaci, NATS odlozen na Phase 3.
Viz `ADR.md` pro zduvodneni rozhodnuti.

---

## 1. VIZE

Crewship pouziva **3-urovnovou hierarchii AI agentu** inspirovanou realnou firemni strukturou:

```
VIRTUAL DIRECTOR (reditel organizace)
  └── CREW LEADER (sef tymu / oddeleni)
        └── WORKER (radovy agent / zamestnanec)
```

**Klicovy princip:** Uzivatel si **povida primarne s Crew Leaderem** sveho tymu.
Leader rozhoduje, deleguje, agreguje a vraci vysledky. Uzivatel nemusi vedet,
ktery konkretni worker agent ukol zpracoval — staci mu komunikace se "sefem".

**Pro cross-team otazky** existuje Virtual Director — reditel, ktery koordinuje
vice tymu a agreguje informace z cele organizace.

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

Koncept "povidam si se sefem tymu" je prirozeny pro netechnicke uzivatele.
"Orchestruju multi-agent workflow pres DAG" prirozeny neni.

---

## 3. TRI UROVNE HIERARCHIE

### 3.1 Worker (Uroven 1) — Radovy agent

- **Default role** pro vsechny agenty
- Specializovany na konkretni ukoly (data analyst, copywriter, devops, QA...)
- Komunikuje primarne se svym Crew Leaderem
- Produkuje output do `/output/`
- Muze byt osloven primo uzivatelem (bypass leadera)
- Bezi v kontejneru sveho tymu (Docker exec)

### 3.2 Crew Leader (Uroven 2) — Sef tymu

- **1 leader per team** (oznaceny v Agent modelu jako `agent_role = LEADER`)
- **Primarni kontaktni bod** pro uzivatel ↔ team komunikaci
- Zna vsechny agenty ve svem tymu (automaticky generovany kontext)
- Rozhoduje: zpracovat sam vs delegovat na workera
- Rozdeluje slozite ukoly na sub-tasky pro workery
- Agreguje vysledky od workeru do koherentni odpovedi
- Kontroluje kvalitu (review pred odeslani uzivatel)
- Reportuje nahoru k Virtual Directorovi (pokud je osloven)
- Bezi v kontejneru sveho tymu (Docker exec)

**System prompt pattern (automaticky generovany):**
```
Jsi {agent.name}, sef tymu "{team.name}" v organizaci "{org.name}".

Tve team:
- {worker1.name} ({worker1.role_title}): {worker1.description}
- {worker2.name} ({worker2.role_title}): {worker2.description}
- ...

Tvoje zodpovednosti:
1. Kdyz dostanes ukol, rozhodnes se jestli ho zvladnes sam nebo deleguj.
2. Pro slozite ukoly rozloz praci a prirazes konkretnim clenum tymu.
3. Vzdy shrnuj vysledky do srozumitelne odpovedi pro uzivatele.
4. Kontroluj kvalitu vystupu svych lidi pred odeslani.
5. Pokud nekdo ze tvych lidi selze, rozhodnes se o alternativnim postupu.

Delegace pouzij prikaz: @delegate({agent_slug}, "{ukol}")
Dotaz na clena tymu: @ask({agent_slug}, "{otazka}")
```

### 3.3 Virtual Director (Uroven 3) — Reditel organizace

- **1 director per organization** (oznaceny jako `agent_role = DIRECTOR`)
- **Neni clenen zadneho tymu** — patri organizaci jako celku
- Zna vsechny tymy a jejich leadery (automaticky generovany kontext)
- Deleguje na Crew Leadery (ne primo na workery)
- Koordinuje cross-team ukoly (napr. "kolik stoji provoz a jake mame trzby?")
- Agreguje informace z vice tymu do jedne odpovedi
- Ma strategicky pohled na organizaci
- **Opt-in** — defaultne vypnuty, aktivuje se v nastaveni organizace

**System prompt pattern (automaticky generovany):**
```
Jsi {agent.name}, virtualni reditel organizace "{org.name}".

Tve tymy a jejich sefove:
- {team1.name} (sef: {leader1.name}): {team1.description}
- {team2.name} (sef: {leader2.name}): {team2.description}
- ...

Tvoje zodpovednosti:
1. Kdyz dostanes otazku, rozhodnes se ktery tym/tymy ji zpracuji.
2. Pro cross-team ukoly koordinuj vice tymu paralelne.
3. Agreguj vysledky od sefu tymu do jedne koherentni odpovedi.
4. Poskytuj strategicky pohled na organizaci.
5. Nikdy nedelej primou praci — vzdy deleguj na prislusny tym.

Delegace na tym: @delegate_team({team_slug}, "{ukol}")
Dotaz na sefa tymu: @ask_leader({team_slug}, "{otazka}")
```

---

## 4. KOMUNIKACNI PATTERNY

### 4.1 Uzivatel → Crew Leader (90 % interakci)

Nejcastejsi use case. Uzivatel si otevre chat s leaderem sveho tymu:

```
Uzivatel: "Potrebuju report o socialnich sitich za leden"

Marketing Leader (Anna):
  "Rozumim. Rozdelim to na dve casti:
   1. @delegate(bob, "Stahni data z Twitter/Instagram/LinkedIn za leden 2026")
   2. @delegate(claudia, "Napises report az budes mit data od Boba")
   
   Dam vam vedet az bude hotovo."

[Bob pracuje na datech... → vraci CSV do /output/]
[Claudia cte Bobova data, pise report... → vraci PDF do /output/]

Marketing Leader (Anna):
  "Report je hotovy! Bob stahl data z 3 platform (12,450 interakci)
   a Claudia napsala 15-strankovy report. Najdete ho v souborech:
   /output/reports/social-media-january-2026.pdf
   
   Klicove zjisteni: engagement na LinkedIn vzrostl o 23%..."
```

### 4.2 Uzivatel → Virtual Director (cross-team)

Pro otazky, ktere presahuji jeden tym:

```
Uzivatel: "Kolik nas stoji provoz serveru a jake mame mesicni trzby?"

Director:
  "@ask_leader(finance, 'Jake jsou mesicni naklady na provoz serveru?')
   @ask_leader(sales, 'Jake jsou mesicni trzby za posledni mesic?')"

[Finance Leader → deleguje na accountanta → 250,000 CZK]
[Sales Leader → deleguje na CRM agenta → 1,200,000 CZK]

Director:
  "Na zaklade dat od Financniho a Obchodniho tymu:
   - Provozni naklady: 250,000 CZK/mesic
   - Mesicni trzby: 1,200,000 CZK
   - Marze: 79.2 %
   
   Poznamka: Naklady vzrostly o 8 % oproti minulemu mesici,
   ale trzby rostly rychleji (+15 %). Trend je pozitivni."
```

### 4.3 Uzivatel → Worker (primo, bypass)

Pokrocili uzivatele mohou chatovat primo s workerem:

```
Uzivatel otevre chat primo s "Bob" (data analyst):
  "Stahni mi raw data z Twitter API za posledni tyden"

Bob: "Stahuji data... [pracuje primo, bez leadera]"
```

Leader o tomto vi (vidi v logu), ale neintervenuuje.

### 4.4 Webhook → Leader (externi trigger)

```
Grafana posle webhook → Marketing Leader
Leader: "Dostal jsem alert o poklesu engagementu o 40%.
  @delegate(bob, 'Analyzuj pricibu poklesu engagementu za poslednich 24h')
  @delegate(claudia, 'Priprav draft krizove komunikace')"
```

### 4.5 Leader ↔ Leader (cross-team koordinace)

Kdyz leader potrebuje informaci od jineho tymu:

```
Marketing Leader: "@ask_leader(development, 'Je nejaky bug v API socialnich siti?')"
Development Leader: "Ano, nasli jsme bug v Twitter API integraci, fix deployujeme za 2h."
Marketing Leader → uzivatel: "Pokles engagementu souvisi s bugem v API, Development tym to resi."
```

Toto jde bud:
- **Pres directora** (director koordinuje) — bezpecnejsi, auditovatelne
- **Primo** (leader → leader) — rychlejsi, ale slozitejsi RBAC

**Doporuceni MVP:** Pres directora. Leader-to-leader primo az Phase 3.

---

## 5. IMPLEMENTACNI ARCHITEKTURA

### 5.1 Jak leader deleguje (technicka vrstva)

> **Rozhodnuti (ADR-001 v2):** Delegace jdou pres **loopback HTTP sidecar**
> (`crewship-sidecar`), ktery bezi uvnitr kazdeho team kontejneru.
> CLI tools i vlastni API-direct runtime komunikuji pres `localhost:9119`.

```
┌─────────────────────── Team Container ───────────────────────┐
│                                                               │
│  ┌─────────────────┐     localhost:9119      ┌────────────┐  │
│  │  Agent process   │ ──── HTTP POST ──────→ │ crewship-  │  │
│  │  (CLI tool nebo  │   /delegate            │ sidecar    │  │
│  │   crewship-agent │   /ask                 │ (Go binary,│  │
│  │   API-direct)    │   /results             │  ~5MB)     │──┼──→ crewshipd (gRPC/WS)
│  │                  │   /status              │            │  │
│  │  stdout → user   │                        │ Validace,  │  │
│  │  output (cisty)  │                        │ buffering, │  │
│  │                  │ ←── HTTP response ──── │ retry,     │  │
│  │                  │   (vysledky delegaci)   │ auth       │  │
│  └─────────────────┘                        └────────────┘  │
│                                                               │
└───────────────────────────────────────────────────────────────┘
```

**Flow:**
```
1. Uzivatel posle zpravu leaderovi pres WebSocket
2. crewshipd overí ze crewship-sidecar bezi v kontejneru (startuje s kontejnerem)
3. crewshipd spusti Docker exec pro leadera v kontejneru tymu
4. crewshipd cte stdout → streaming k uzivatel pres WebSocket + JSONL log
5. Leader (LLM) rozhodne co delegovat:
   - User-facing text → stdout (normalne, cisty)
   - Delegace → HTTP POST na localhost:9119/delegate
     (CLI tool: pres bash tool `curl`; API-direct: nativni HTTP call)
6. crewship-sidecar prijme request, validuje:
   - Cil existuje? Connection povolena? RBAC ok? Circuit breaker?
7. crewship-sidecar posle prikaz do crewshipd (gRPC/WebSocket)
8. crewshipd spusti novy Docker exec pro workera ve STEJNEM kontejneru
9. Worker stdout → JSONL log + crewshipd ceka na dokonceni
10. crewshipd posle vysledek workera do sidecar
11. Leader polluje GET localhost:9119/results/{group} NEBO sidecar pushne callback
12. Leader agreguje a odpovida uzivatel (stdout → WebSocket)
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

Sidecar ma DVE role: (1) delegacni proxy a (2) MCP Gateway (ADR-014).

```
DELEGACNI PROXY:
  POST /delegate    — delegovat ukol na workera
  POST /ask         — polozit otazku workerovi/leaderovi
  POST /broadcast   — fire-and-forget zprava vice agentum
  GET  /results/:id — vyzvednout vysledky delegace (polling)
  GET  /status      — stav vsech aktivnich delegaci
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

### 5.2 Jak director deleguje (technicka vrstva)

**Varianta C (doporucena): Director jako lightweight agent**

Director **nepotrebuje Docker kontejner** — nepise kod, jen deleguje a agreguje.
Bezi jako cisty LLM call v crewshipd (Go):

```
1. Uzivatel posle zpravu directorovi pres WebSocket
2. crewshipd zavola LLM API primo (ne Docker exec) s director system promptem
3. Director rozhodne ktery tym/leadery oslovit
4. crewshipd parsuje output, detekuje @delegate_team / @ask_leader prikazy
5. crewshipd posle zpravy prislusnym leaderum (ti uz bezi v Docker exec)
6. crewshipd sbira odpovedi od leaderu
7. crewshipd posle zpet directorovi pro agregaci (dalsi LLM call)
8. Director shrne a odpovi
9. crewshipd streamuje pres WebSocket k uzivatel
```

**Vyhody:**
- Zadny novy kontejner, zadny overhead
- Director je rychly (jen LLM reasoning, zadne tools/exec)
- Jednodussi credentials (director nepotrebuje vlastni — pouziva org-level LLM key)

**Nevyhody:**
- Director nemuze pouzivat tools (web search, file write)
- Pokud to bude potreba → Varianta A (dedicovany kontejner) v Phase 3

**Rizika a mitigace (ADR-007):**
- LLM API timeout (30s+) blokuje goroutinu → **context.WithTimeout + circuit breaker**
- Director crash neni izolovan → goroutina s **recover()**, panic nepadne cely process
- Bez tools = omezeny → MVP postacujici, Phase 3 prida kontejner

### 5.3 Delegacni protokol (Sidecar HTTP API)

> **Rozhodnuti (ADR-001 v2):** Delegace pres loopback HTTP na crewship-sidecar.
> Standardni HTTP — funguje s CLI tools (curl) i API-direct runtime (nativni HTTP).

**Delegace (POST /delegate):**
```json
POST http://localhost:9119/delegate
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
  "delegation_id": "uuid",
  "status": "queued"
}
```

**Dotaz (POST /ask):**
```json
POST http://localhost:9119/ask
{ "target": "claudia", "question": "Kolik slov ma mit executive summary?" }
```

**Cross-team (POST /delegate — s team_target):**
```json
POST http://localhost:9119/delegate
{ "team_target": "finance", "task": "Mesicni naklady na servery" }
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
    {"worker": "bob", "status": "completed", "summary": "Stazeno 12,450 zaznamu.", "output_path": "/mnt/agents/bob/result.json"},
    {"worker": "eve", "status": "failed", "error": "Rate limit exceeded"}
  ]
}
```

**Jak CLI tool deleguje (pres bash/curl):**
```bash
# Claude Code / OpenCode pouzije svuj bash tool:
curl -s -X POST http://localhost:9119/delegate \
  -H "X-Crewship-Session: $CREWSHIP_SESSION_ID" \
  -H "Content-Type: application/json" \
  -d '{"target":"bob","task":"Stahni data","wait":true,"group":"data"}'

# Cekani na vysledky:
curl -s http://localhost:9119/results/data
```

**Jak crewship-agent (API-direct) deleguje:**
```go
// Nativni HTTP call — zadny curl, zadny LLM instrukce
resp, _ := http.Post("http://localhost:9119/delegate", "application/json", body)
```

**Validace prikazu (sidecar strana):**
1. JSON parse → validace struktury
2. Session-id platny?
3. Target agent/team existuje? (sidecar cachuje z crewshipd)
4. Connection povolena? (RBAC check)
5. Max delegation depth neprekrocen?
6. Circuit breaker neni otevreny pro target? (viz 5.8)
7. Backpressure check (queue depth)
8. Pokud validace selze → HTTP 422 s chybovou zpravou

### 5.4 Timeout a error handling

```
Leader deleguje na workera:
  - Timeout: worker.timeout_seconds (default 1800s = 30 min)
  - Pokud worker selze: leader dostane error zpravu, muze:
    a) Zkusit jineho workera
    b) Zkusit sam
    c) Reportovat uzivatel

Director deleguje na leadera:
  - Timeout: 2x leader.timeout_seconds (leader ceka na workery)
  - Pokud leader selze: director informuje uzivatel a navrhne alternativu

Max delegacni hloubka: 3 (director → leader → worker → sub-task)
Max turns per delegace: 10 (bezpecnostni limit)
```

### 5.5 Paralelni delegace

Leader/Director muze delegovat na vice agentu **paralelne**:

```jsonl
{"type":"delegate","target":"bob","task":"Data z Twitteru","wait":true,"group":"data-collection"}
{"type":"delegate","target":"eve","task":"Data z LinkedInu","wait":true,"group":"data-collection"}
{"type":"wait_group","group":"data-collection"}
{"type":"delegate","target":"claudia","task":"Report z dat Boba a Eve","wait":true}
```

crewshipd spusti Boba a Eve paralelne (2 Docker exec soucasne), ceka az oba
skonci (`wait_group`), a pak spusti Claudii s vysledky obou.

### 5.6 Leader Modes (ADR-004)

Leader muze bezet ve dvou modech — uzivatel voli per-leader konfiguraci:

**Active mode (default):**
```
Leader bezi po celou dobu Crew execution.
Rozhoduje v real-time, reaguje na vysledky workeru prubezne.

1. Leader se spusti (Docker exec, long-running)
2. Leader analyzuje ukol, deleguje na workery (HTTP → sidecar)
3. crewshipd spusti workery, vysledky posila zpet do sidecar
4. Leader polluje/cte vysledky ze sidecar, rozhoduje co dal
5. Leader odpovi uzivatel az je spokojen s vysledky
```

- **Vyhody:** Flexibilni, leader muze menit strategii za behu
- **Nevyhody:** Drazsi (leader konzumuje LLM tokeny po celou dobu)
- **Use case:** Slozite ukoly, iterativni prace, debugging

**Passive mode:**
```
Leader se spusti 2x: init (task breakdown) a finalize (agregace).
Mezi tim crewshipd orchestruje workery sam (deterministicky).

1. Leader se spusti — analyzuje ukol, vytvori task plan:
   {"type":"task_plan","tasks":[
     {"target":"bob","task":"...","order":1},
     {"target":"eve","task":"...","order":1},
     {"target":"claudia","task":"...","order":2,"depends_on":["bob","eve"]}
   ]}
2. Leader se ukonci (exit)
3. crewshipd provede task plan: spusti workery dle order a depends_on
4. Vsechny workery hotovy → crewshipd spusti leadera znovu s vysledky
5. Leader agreguje, odpovi uzivatel
6. Leader se ukonci
```

- **Vyhody:** Levnejsi (2 LLM calls misto N), deterministicky
- **Nevyhody:** Leader nemuze reagovat na neocekavane vysledky
- **Use case:** Rutinni ukoly, reporty, sber dat

**Konfigurace:**
```prisma
// V Agent modelu:
  leader_mode    String?  @default("active")  // "active" | "passive"
```

### 5.7 Worker Output Compression (ADR-005)

**Problem:** Worker vraci 50k tokens output. Leader musi tento output precist
→ leader context = system prompt + user msg + 50k worker output = drahé.

**Reseni:** crewshipd automaticky komprimuje worker output pred predanim leaderovi.

```
Worker output flow:
1. Worker zapise plny vysledek do /output/bob/result.json (50k tokens)
2. crewshipd precte vysledek
3. Pokud vysledek > worker_output_max_tokens (default 2000):
   a) crewshipd zavola LLM s promptem: "Summarize this result in max 2000 tokens"
   b) Sumarizace → posle leaderovi pres response pipe
   c) Plny vysledek → file reference: /mnt/agents/bob/result.json
4. Pokud vysledek <= limit:
   → posle primo leaderovi (bez sumarizace)
```

**Co leader dostane:**
```jsonl
{
  "type": "delegation_result",
  "worker": "bob",
  "status": "completed",
  "summary": "Stazeno 12,450 zaznamu z Twitter, Instagram a LinkedIn za leden 2026. Highest engagement: LinkedIn post o AI trendech (2,340 reactions).",
  "full_output_path": "/mnt/agents/bob/social-media-data.json",
  "original_tokens": 48500,
  "summary_tokens": 1850
}
```

Leader muze precist plny output z filesystemu pokud potrebuje detail,
ale ve vetsine pripadu mu sumarizace staci pro agregaci.

**Konfigurace:**
```prisma
// V Agent modelu (leader only):
  worker_output_max_tokens  Int?  @default(2000)  // max tokens per worker result
```

**Naklady na sumarizaci:**
- Sumarizace = 1 LLM call per worker vysledek (levny model, napr. Claude Haiku)
- Trade-off: maly naklad na sumarizaci vs velka uspora na leader contextu
- Pri 5 workerech: 5 × Haiku call (~$0.01) vs 5 × 50k tokens v leader contextu (~$0.50)

### 5.8 Circuit Breaker pro delegace (ADR-006)

**Problem:** Worker opakovane selhava (bug, spatny prompt, nedostupna sluzba).
Bez circuit breakeru leader porad zkouší delegovat → plytva tokeny a casem.

**Implementace:**
```
Circuit Breaker states per worker:
  CLOSED  → normalni provoz, delegace prochazi
  OPEN    → worker docasne vyfadovan, delegace se odmitnou
  HALF    → zkusebni delegace (1 pokus), pokud uspeje → CLOSED

Prechody:
  CLOSED → OPEN:   3 po sobe jdouci faily (configurable: max_consecutive_failures)
  OPEN → HALF:     po cooldown periode (default 5 min)
  HALF → CLOSED:   zkusebni delegace uspela
  HALF → OPEN:     zkusebni delegace selhala → dalsi cooldown
```

**Error handling flow:**
```
1. Worker selze (exit code != 0 nebo timeout)
2. crewshipd inkrementuje failure counter pro workera
3. Pokud failures < 3:
   a) Auto-retry s exponential backoff (1s, 2s, 4s)
   b) Kazdy retry = novy Docker exec se stejnym taskem
4. Pokud failures >= 3:
   a) Circuit OPEN — worker vyfadovan
   b) Eskalace na leadera: {"type":"worker_unavailable","worker":"bob","reason":"3 consecutive failures","last_error":"..."}
   c) Leader rozhodne:
      - Priradit ukol jinemu workerovi
      - Zkusit sam
      - Informovat uzivatel
5. Po cooldown (5 min): circuit HALF → 1 zkusebni delegace
```

**Backpressure:**
- Max queue depth per leader: 10 cekajicich delegaci (default)
- Pokud fronta plna → leader dostane error: `{"type":"backpressure","message":"delegation queue full"}`
- Leader muze pockat (wait_group) nebo informovat uzivatel

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
- Delegace pres `curl localhost:9119/delegate` (bash tool)
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
- Delegace pres nativni HTTP call na sidecar (zadny curl, zadny LLM instrukce)
- Presny token tracking z API response
- Plna lifecycle kontrola (pause, resume, cancel)

**Srovnani:**

| Aspekt | CLI mode | API-direct mode |
|---|---|---|
| Latence | CLI startup + LLM | Jen LLM call |
| Memory per agent | 200-300MB (Node.js) | ~10MB (Go goroutina) |
| Kontrola lifecycle | Zadna (CLI black box) | Plna |
| Token tracking | Nepresne (stdout parse) | Presne (API response) |
| Delegace | curl (LLM instrukce) | Nativni HTTP (spolehljive) |
| Cost estimation | Nemozne | Presne |
| Multi-provider | 1 CLI = 1 provider | Libovolny provider per call |
| Image size | ~500MB (Node.js + CLI) | ~50MB (Go binary) |
| Tool use | CLI-native (silne) | Vlastni implementace |
| Use case | Coding-heavy, power users | Obecne ukoly, enterprise |

**Doporuceni:**
- Phase 1: CLI_CLAUDE_CODE a CLI_OPENCODE jako default
- Phase 2: API_DIRECT jako alternativa (uzivatel voli per agent)
- Phase 3: API_DIRECT jako default, CLI jako "power adapter"

### 5.10 Trace ID across delegaci (ADR-012)

Kazda crew execution dostane unikatni `trace_id` ktery propojuje:
- Leader session
- Vsechny worker sessions
- Delegacni logy
- JSONL konverzace
- Meilisearch indexy

```
trace_id: "crew-exec-{uuid}"
  ├── leader_session: "session-{uuid}" (trace_id v JSONL metadata)
  ├── delegation_1: "deleg-{uuid}" (trace_id v DelegationLog)
  │   └── worker_session: "session-{uuid}" (trace_id v JSONL metadata)
  ├── delegation_2: "deleg-{uuid}"
  │   └── worker_session: "session-{uuid}"
  └── ...
```

Uzivatel muze v UI zobrazit celou crew execution jako timeline
a prokliknout se do libovolne worker session.

---

## 6. DATOVY MODEL — ZMENY

### 6.1 Novy enum: AgentRole

```prisma
enum AgentRole {
  WORKER       // default — radovy agent, specializovany na konkretni ukoly
  LEADER       // 1 per team — sef tymu, orchestruje workery
  DIRECTOR     // 1 per org — reditel, orchestruje cross-team
}
```

### 6.2 Zmeny v Agent modelu

```prisma
model Agent {
  // ... existujici pole ...
  
  agent_role      AgentRole   @default(WORKER)
  
  // team_id se stava NULLABLE — director nema team
  team_id         String?     @db.Uuid
  
  // leader/director specific
  delegation_timeout_s    Int?    // override timeout pro delegace (default: 2x agent timeout)
  max_delegation_depth    Int?    @default(3)   // max hloubka delegace
  max_parallel_delegates  Int?    @default(5)   // max paralelne bezicich delegaci
  leader_mode             String? @default("active")  // "active" | "passive" (viz 5.6)
  worker_output_max_tokens Int?   @default(2000)      // max tokens per worker result (viz 5.7)
  runtime                 String? @default("CLI_CLAUDE_CODE")  // AgentRuntime enum (viz 5.9)
}
```

### 6.3 Constraints

- **Max 1 LEADER per team:** Aplikacni uroven (service layer check)
- **Max 1 DIRECTOR per org:** Aplikacni uroven
- **Director.team_id = null:** Aplikacni uroven (Prisma middleware)
- **Leader.team_id != null:** Aplikacni uroven

### 6.4 Novy model: DelegationLog

```prisma
model DelegationLog {
  id              String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  org_id          String   @db.Uuid
  session_id      String   @db.Uuid
  source_agent_id String   @db.Uuid  // kdo delegoval (leader/director)
  target_agent_id String   @db.Uuid  // komu (worker/leader)
  task            String   @db.Text  // co bylo delegovano
  status          DelegationStatus @default(PENDING)
  started_at      DateTime? @db.Timestamptz
  finished_at     DateTime? @db.Timestamptz
  result_summary  String?  @db.Text  // shrnuti vysledku (od targetu)
  error_message   String?  @db.Text
  group_id        String?  // pro paralelni delegace (wait_group)
  trace_id        String?  // crew execution trace (viz 5.10)
  created_at      DateTime @default(now()) @db.Timestamptz

  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)
  session      ConversationSession @relation(fields: [session_id], references: [id])
  source_agent Agent @relation("DelegatedBy", fields: [source_agent_id], references: [id])
  target_agent Agent @relation("DelegatedTo", fields: [target_agent_id], references: [id])

  @@index([session_id], name: "idx_delegation_session")
  @@index([source_agent_id], name: "idx_delegation_source")
  @@index([target_agent_id], name: "idx_delegation_target")
  @@map("delegation_logs")
}

enum DelegationStatus {
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

### 7.1 DelegationEngine (novy modul v crewshipd)

```go
// internal/orchestrator/delegation.go

type DelegationEngine struct {
    orchestrator *Orchestrator
    ws           *ws.Server
    wal          *state.WAL
}

type DelegationRequest struct {
    Type        string   // "delegate", "ask", "delegate_team", "ask_leader", "broadcast"
    SourceAgent string   // agent ID ktery deleguje
    TargetAgent string   // agent slug (nebo team slug pro delegate_team)
    Task        string   // popis ukolu / otazka
    Wait        bool     // cekej na odpoved?
    GroupID     string   // pro paralelni delegace
    SessionID   string   // parent session
}

type DelegationResult struct {
    TargetAgent string
    Status      string // "completed", "failed", "timeout"
    Output      string // vysledek od target agenta
    Duration    time.Duration
}
```

### 7.2 crewship-sidecar (Go binary v kontejneru)

```go
// cmd/crewship-sidecar/main.go
// Lightweight HTTP server bezici uvnitr kazdeho team kontejneru.
// Startuje s kontejnerem, posloucha na localhost:9119.

type Sidecar struct {
    crewshipdConn *grpc.ClientConn // nebo WebSocket connection
    teamID        string
    sessions      map[string]*SessionState
    breakers      map[string]*CircuitBreaker
    mcpServers    map[string]*MCPServerProcess  // skill_id → running MCP server
    credentials   map[string]string             // env_var → decrypted value (Phase 2)
    mu            sync.RWMutex
}

func (s *Sidecar) HandleDelegate(w http.ResponseWriter, r *http.Request) {
    sessionID := r.Header.Get("X-Crewship-Session")
    if sessionID == "" {
        http.Error(w, "missing session ID", http.StatusUnauthorized)
        return
    }
    
    var req DelegationRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "invalid JSON", http.StatusBadRequest)
        return
    }
    req.SessionID = sessionID
    
    // Validace
    if err := s.validateDelegation(req); err != nil {
        writeJSON(w, http.StatusUnprocessableEntity, ErrorResponse{Error: err.Error()})
        return
    }
    
    // Odeslani do crewshipd
    delegID, err := s.crewshipd.Delegate(r.Context(), req)
    if err != nil {
        writeJSON(w, http.StatusBadGateway, ErrorResponse{Error: err.Error()})
        return
    }
    
    writeJSON(w, http.StatusAccepted, DelegationResponse{
        DelegationID: delegID,
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
1. Delegacni proxy (Phase 1): POST /delegate, /ask, /broadcast, GET /results
2. MCP Gateway (Phase 2): MCP stdio proxy, credential injection, RBAC, audit
   - Spousti MCP servery pro skills prirazene agentu v tomto tymu
   - Injektuje credentials do MCP serveru (agent je NEMA)
   - Zachytava MCP tool cally a loguje s credential_id
   - Vystavuje search_tools meta-tool pro on-demand discovery

### 7.3 crewship-agent (API-direct runtime, Phase 2)

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
    &DelegateTool{sidecarURL: "http://localhost:9119"}, // delegace pres sidecar
}

// DelegateTool — LLM ho vola jako normalní tool
type DelegateTool struct {
    sidecarURL string
}

func (d *DelegateTool) Execute(ctx context.Context, args DelegateArgs) (*ToolResult, error) {
    body, _ := json.Marshal(args)
    resp, err := http.Post(d.sidecarURL+"/delegate", "application/json", bytes.NewReader(body))
    // ...
    return &ToolResult{Content: result}, nil
}
```

**Vyhody crewship-agent oproti CLI tools:**
- Delegace je TOOL (ne bash curl) — LLM ho vola spolehlive pres function calling
- Presny token usage z API response
- ~5MB binary vs ~300MB CLI tool
- Podpora vsech LLM provideru z jednoho binary

### 7.4 Director execution (lightweight, bez Docker)

```go
// internal/orchestrator/director.go

func (o *Orchestrator) RunDirector(ctx context.Context, req DirectorRequest) error {
    // Director bezi jako cisty LLM call — zadny Docker exec
    
    // 1. Sestav system prompt s informacemi o tymech a leaderech
    systemPrompt := o.buildDirectorSystemPrompt(req.OrgID)
    
    // 2. Zavolej LLM API primo
    response, err := o.llmClient.Chat(ctx, LLMRequest{
        Model:        req.Model,
        SystemPrompt: systemPrompt,
        Messages:     req.Messages,
        Temperature:  req.Temperature,
    })
    
    // 3. Parsuj delegacni prikazy z odpovedi
    commands, text := ParseDelegationCommands(response.Content)
    
    // 4. Stream text cast k uzivatel
    o.ws.Send(req.SessionID, AgentEvent{Type: "text", Content: text})
    
    // 5. Proved delegace
    for _, cmd := range commands {
        go o.executeDelegation(ctx, cmd, req.SessionID)
    }
    
    return nil
}
```

---

## 8. UI IMPLIKACE

### 8.1 Chat s leaderem

- Chat UI je **stejny** jako pro bezneho agenta
- Navic: **delegacni timeline** — uzivatel vidi kdy leader delegoval, na koho, status
- Vizualizace: `[Leader Anna] → delegoval na [Bob] → "sber dat"` s progressem
- Streaming: uzivatel vidi v realtime co leader premysli a co deleguje

### 8.2 Chat s directorem

- Specialni chat UI (nebo stejny s jinym oznacenim)
- Cross-team delegacni vizualizace
- Uzivatel vidi ktere tymy director oslovil

### 8.3 Agent detail — role badge

- Worker: zadny badge (default)
- Leader: badge "Team Leader" / ikona Crown
- Director: badge "Director" / ikona Building

### 8.4 Team view — leader na prvnim miste

V seznamu agentu v tymu je leader vzdy na prvnim miste, vizualne odliseny:

```
Team: Marketing
├─ 👑 Anna (Team Leader) — RUNNING
├─ Bob (Data Analyst) — IDLE
├─ Claudia (Copywriter) — IDLE
└─ Dave (SEO Specialist) — IDLE
```

### 8.5 Org view — director card

Na dashboardu organizace se zobrazi karta directora:

```
🏢 Virtual Director: Max
   Status: IDLE
   Tymy pod spravou: 4 (Marketing, Development, Finance, Support)
   [Chat s reditelem]
```

---

## 9. BEZPECNOST A RBAC

### 9.1 Kdo muze chatovat s kym

| Uzivatel role | Worker | Leader | Director |
|---|---|---|---|
| VIEWER | Read-only | Read-only | Read-only |
| MEMBER | Chat (prirazeny tym) | Chat (prirazeny tym) | NE |
| MANAGER | Chat (prirazeny tym) | Chat (prirazeny tym) | NE |
| ADMIN | Chat (vsechny tymy) | Chat (vsechny tymy) | Chat |
| OWNER | Chat (vsechny tymy) | Chat (vsechny tymy) | Chat |

### 9.2 Delegacni RBAC

- Leader muze delegovat **jen na agenty ve svem tymu**
- Director muze delegovat **jen na leadery** (ne primo na workery)
- Worker **nemuze delegovat** (zadny pristup k delegacnim prikazum)

### 9.3 Credentials pri delegaci

- **Leader → Worker:** Worker dedi credentials od tymu (normalni flow)
- **Director → Leader:** Director pouziva org-level LLM credentials
- **Cross-team delegace:** Leader ma pristup jen ke credentials sveho tymu

### 9.4 Audit log

Vsechny delegace se loguji:

```
action: "delegation.created"
entity_type: "DelegationLog"
metadata: {
  source_agent: "anna (leader)",
  target_agent: "bob (worker)",
  task: "Stahni data z Twitter API",
  team: "marketing"
}
```

---

## 10. FAZOVANI IMPLEMENTACE

### Phase 2A: Crew Leader (s prvni vlnou orchestrace)

| ID | Feature | Popis |
|---|---|---|
| ORCH-01 | AgentRole enum + DB migrace | Pridani `agent_role` do Agent modelu |
| ORCH-02 | Leader designation UI | Oznaceni agenta jako leadera v team settings |
| ORCH-03 | Auto-generated leader system prompt | Automaticky system prompt s informacemi o tymu |
| ORCH-04 | crewship-sidecar | Loopback HTTP sidecar v kontejneru (ADR-001 v2) |
| ORCH-05 | Delegacni protokol (HTTP) | REST API na sidecar, validace, RBAC |
| ORCH-06 | Leader → Worker delegace | Docker exec orchestrace v ramci tymu |
| ORCH-07 | Delegacni timeline v UI | Vizualizace delegaci v chatu |
| ORCH-08 | DelegationLog tabulka | Auditovani vsech delegaci |
| ORCH-09 | Leader auto-routing | Uzivatel pise do tymu, leader rozhodne komu |
| ORCH-10 | Paralelni delegace | wait_group pattern pro vice workeru soucasne |
| ORCH-11 | Error handling + circuit breaker | Auto-retry 3x → eskalace na leadera (viz 5.8) |
| ORCH-12 | Leader summary/agregace | Leader shrnuje vysledky pred odeslani uzivatel |

### Phase 2B: Virtual Director + advanced features

| ID | Feature | Popis |
|---|---|---|
| ORCH-13 | Director agent role | Specialni agent na urovni organizace |
| ORCH-14 | Director lightweight execution | LLM call bez Docker kontejneru (ADR-007) |
| ORCH-15 | Director → Leader delegace | Cross-team orchestrace |
| ORCH-16 | Director auto-routing | Director rozhodne ktery tym oslovit |
| ORCH-17 | Cross-team agregace | Director sbira odpovedi od vice tymu |
| ORCH-18 | Director UI (dashboard card + chat) | Specialni karta na dashboardu |
| ORCH-19 | Leader modes (active/passive) | Dva mody leadera — viz 5.6 (ADR-004) |
| ORCH-20 | Worker output compression | Auto-sumarizace worker vystupu — viz 5.7 (ADR-005) |
| ORCH-21 | Cost estimation per crew operation | Odhad token nakladu pred spustenim crew |
| ORCH-22 | Per-crew budget limits | Max token/dollar budget per crew execution |
| ORCH-23 | crewship-agent binary (API-direct) | Vlastni Go runtime, primo LLM API (ADR-009) |
| ORCH-24 | Trace ID across delegaci | Korelace sessions/delegaci (ADR-012) |
| ORCH-25 | Meilisearch conversation search | Async JSONL → search index (ADR-011) |

### Phase 3: Pokrocila orchestrace + skalovani

| ID | Feature | Popis |
|---|---|---|
| ORCH-23 | Leader ↔ Leader primo | Primo cross-team komunikace bez directora |
| ORCH-24 | Director s tools | Director dostane vlastni kontejner a tools |
| ORCH-25 | Director long-term memory | Strategicke cile, KPIs, trendy |
| ORCH-26 | Orchestracni vizualizace | Graf delegaci v realtime (OrchVis pattern) |
| ORCH-27 | Auto-leader election | AI doporuci ktery agent by mel byt leader |
| ORCH-28 | Director → Director (multi-org) | Spoluprace mezi organizacemi pres webhooky |
| ORCH-29 | NATS JetStream integrace | Message broker pro multi-node cluster (ADR-002) |
| ORCH-30 | gVisor runtime | Optional container runtime pro multi-tenant (ADR-003) |
| ORCH-31 | Delegation replay/debug | Prehrat celou crew execution krok po kroku |
| ORCH-32 | Landlock per-agent izolace | Filesystem izolace uvnitr kontejneru (ADR-010) |
| ORCH-33 | API-direct jako default | CLI tools jako legacy adapter |

---

## 11. SROVNANI S KONKURENCI

| Aspekt | CrewAI | LangGraph | AutoGen | **Crewship** |
|---|---|---|---|---|
| Orchestracni vzor | Hierarchical Crew | Graph/DAG | Conversation | **3-urovnova hierarchie** |
| Cilova skupina | Developeri (Python) | Developeri (Python) | Developeri (Python) | **Byznys uzivatele (UI)** |
| Leader koncept | Manager Agent | Supervisor node | GroupChat manager | **Crew Leader (lidsky)** |
| Cross-team | Nested crews | Sub-graphs | Nested groups | **Virtual Director** |
| Konfigurace | Kod (Python) | Kod (Python) | Kod (Python) | **Web UI + auto-prompty** |
| Deployment | Library (pip) | Library (pip) | Library (pip) | **Platform (Docker/K8s)** |
| Security | Zadna izolace | Zadna izolace | Zadna izolace | **Container + RBAC + audit** |
| Delegacni vizualizace | Terminal logy | LangSmith | Terminal logy | **Real-time UI timeline** |

---

## 12. OTEVRENE OTAZKY

### Uzavrene (rozhodnuto)

4. ~~**Delegacni format:**~~ → **Named pipe + JSON** (ADR-001). Cisty kanal, zadne kolize se stdout.
5. ~~**Context window:**~~ → **Worker output compression** (ADR-005). Auto-sumarizace na 2k tokens.
6. ~~**Naklady:**~~ → **Cost estimation** feature v Phase 2B (ORCH-21). UI ukazuje odhad pred spustenim.
7. ~~**Deadlock:**~~ → **Circuit breaker + backpressure** (ADR-006). Max queue 10, 3 retries, cooldown.

### Stale otevrene

1. **Director kontejner (Phase 3):** Kdyz director dostane tools, jaky kontejner? Dedicovany "org kontejner"?
2. **Leader volba:** Muze uzivatel zmenit leadera tymu za behu? Co se stane s probihajicimi delegacemi?
3. **Multi-leader:** Muze mit tym vice leaderu (napr. den/noc smena)? Pravdepodobne ne v MVP.
8. **Metriky:** Jak merit efektivitu leadera? (cas odpovedi, pocet delegaci, uspesnost)
9. **Sumarizacni model:** Jaky LLM pouzit pro worker output compression? Haiku/GPT-4o-mini? Konfigurovatelne?
10. ~~**Pipe security:**~~ → Nahrazeno sidecar HTTP (localhost only, session auth). Viz ADR-001 v2.
11. **Sidecar port conflict:** Co kdyz agent proces pouziva port 9119? Konfigurovatelny port?
12. **crewship-agent tool coverage:** Pokryva crewship-agent vsechny tools co Claude Code? LSP?

---

## 13. PRIKLADY PRO RUZNE ODVETVI

### IT firma
```
Director "Max"
├── Development (Leader: "Tomas")
│   ├── Worker "Alice" (Backend Dev)
│   ├── Worker "Charlie" (Frontend Dev)
│   └── Worker "Diana" (QA)
├── DevOps (Leader: "Viktor")
│   ├── Worker "SRE-bot" (Monitoring)
│   └── Worker "Deploy-bot" (CI/CD)
└── Support (Leader: "Petra")
    ├── Worker "Help-bot" (Ticketing)
    └── Worker "Docs-bot" (Knowledge Base)
```

### Marketing agentura
```
Director "Jana"
├── Kreativa (Leader: "Anna")
│   ├── Worker "Copywriter"
│   ├── Worker "Designer"
│   └── Worker "Video Editor"
├── Analytics (Leader: "Martin")
│   ├── Worker "Data Analyst"
│   └── Worker "SEO Specialist"
└── Social Media (Leader: "Lucie")
    ├── Worker "Community Manager"
    └── Worker "Influencer Scout"
```

### Zubni ordinace (maly byznys)
```
Director (neni — mala firma)
├── Recepce (Leader: "Recepční")
│   └── Worker "Objednávkový asistent"
└── Admin (Leader: "Účetní")
    └── Worker "Fakturant"
```

---

*Tento dokument je zivý — bude se aktualizovat s kazdou novou iteraci implementace.*
