# Crewship -- Agent Memory System (MEMORY.md)

**Verze:** 2.0
**Datum:** 2026-04-14
**Status:** Phase 1 MVP DONE + Phase 2B CREW MEMORY DONE + Phase 3 WORKSPACE FOUNDATION DONE (CRE-118, PR #155)
**Zavislosti:** AGENT-RUNTIME.md (sidecar, kontejnery, /output/ storage),
ORCHESTRATION.md (leader/crew/agent hierarchie, sidecar API),
SECURITY.md (izolace, encryption, RBAC),
DATABASE.md (SQLite, schema konvence),
CREW-EXECUTION.md (mission, progress tracking)

---

## 1. PREHLED A MOTIVACE

Crewship agenti potrebuji **persistentni pamet** across sessions — schopnost
zapamatovat si fakta, preference, rozhodnuti a ucit se z predchozich interakci.
Bez memory systemu agent zacina kazdy chat "od nuly" a uzivatel musi opakovane
vysvetlovat kontext.

### 1.1 Pozadavky

| Pozadavek | Popis |
|---|---|
| **Persistence** | Memory prezije restart kontejneru, restart crewshipd, reboot hostu |
| **Retrieval** | Agent musi umet efektivne hledat v pameti (ne jen cist cely soubor) |
| **Izolace** | Agent NEMUZE cist pamet jineho agenta (multi-tenant bezpecnost) |
| **Hierarchie** | Memory na 4 urovnich: Workspace, Crew, Agent, Session |
| **Security** | Validace vstupu, size limity, audit log, budouci encryption at rest |
| **Zero deps** | Zadne externi sluzby (Redis, Pinecone, Weaviate) — vse embedded |
| **Zero cost** | Phase 1 bez API callu na embeddingy — BM25 search zdarma |
| **Performance** | Sub-ms search na tisicich chunks (SQLite FTS5) |

### 1.2 Inspirace a benchmarking

Navrh vychazi z reserse OpenClaw memory systemu (180k+ GitHub stars, unor 2026),
akademickych publikaci (Hindsight architecture — arxiv 2512.12818), a industry
best practices pro AI agent memory (tiered memory, hybrid search, local-first RAG).

---

## 2. RESERSE: OPENCLAW MEMORY SYSTEM (stav unor 2026)

### 2.1 Zakladni architektura OpenClaw

OpenClaw pouziva **file-first** pristup — pamet je ulozena v plain Markdown souborech:

- **`MEMORY.md`** — dlouhodoba pamet (curated facts, preferences, project context)
- **`memory/YYYY-MM-DD.md`** — denni zaznamy (append-only), ctou se today + yesterday pri startu session
- Soubory ziji v workspace (`~/.openclaw/workspace`)
- **Zadna databaze jako primarni storage** — Markdown = source of truth

OpenClaw je single-user personal AI assistant. Nema RBAC, nema multi-tenant
izolaci, nema container boundary. Agenti bezi PRIMO NA HOSTU (ne v kontejneru).

### 2.2 Memory Flush (pre-compaction)

Kdyz se session blizi k auto-compaction (context window se plni):

1. OpenClaw spusti **tichy agentic turn** — silent system message
2. Agent dostane prompt: "Session nearing compaction. Store durable memories now."
3. Agent zapise dulezite veci do `memory/YYYY-MM-DD.md`
4. Teprve pak probehne compaction (sumarizace starych messages)
5. **Jeden flush per compaction cyklus** (trackuje se v `sessions.json`)

**Konfigurace OpenClaw memory flush:**
```json
{
  "agents": {
    "defaults": {
      "compaction": {
        "reserveTokensFloor": 20000,
        "memoryFlush": {
          "enabled": true,
          "softThresholdTokens": 4000,
          "systemPrompt": "Session nearing compaction. Store durable memories now.",
          "prompt": "Write any lasting notes to memory/YYYY-MM-DD.md; reply with NO_REPLY if nothing to store."
        }
      }
    }
  }
}
```

**Detaily:**
- **Soft threshold:** Flush se spusti kdyz session token estimate prekroci
  `contextWindow - reserveTokensFloor - softThresholdTokens`
- **Silent by default:** Prompty obsahuji `NO_REPLY` aby uzivatel nic nevidel
- **Dva prompty:** user prompt + system prompt
- **Jeden flush per compaction cycle** (trackováno v sessions.json)
- **Workspace musi byt writeable:** Pokud session bezi sandboxed s
  `workspaceAccess: "ro"` nebo `"none"`, flush se preskoci

### 2.3 Zname problemy s OpenClaw memory flush

Z GitHub issues:

| Issue | Problem | Dopad |
|---|---|---|
| **#4836** | memoryFlush enabled ale nespusti se behem compaction | Kriticka ztrata kontextu across sessions |
| **#17603** | Memory flush zapisuje soubory se spatnym datem (model hadá rok) | Denni logy pojmenovane spatne, degraduje retrieval |
| **#15218** | Compaction se nikdy nespusti (contextTokens < contextWindow) | Sessions se naplni na 100% bez triggeru compaction |
| **#15006** | Predcasna compaction kvuli spatnemu pocitani cache tokenu | 13/14 compaction eventu bylo false alarm |
| **#18023** | Post-compaction agenti preskakuji startup sekvence | Agent po compaction nevi co ma delat |
| **#19148** | Pozadavek na customizable compaction prompts | Community chce vic kontroly |
| **#13987** | Nova session nenacita memory soubory automaticky | AI ztraci identitu a kontext uzivatele |

**Pouceni pro Crewship:** Automaticky memory flush je komplexni a buggy.
Pro MVP je lepsi agent instruovat v system promptu at pise prubezne (jednodussi, spolehlivejsi).

### 2.4 Vector Memory Search

OpenClaw implementuje **hybrid search** kombinujici BM25 (keyword) a vector (semantic):

**Architektura:**
- **SQLite** jako index storage (`~/.openclaw/memory/<agentId>.sqlite`)
- **sqlite-vec** extension pro vector queries (optional, fallback = JS cosine similarity)
- **Chunking:** ~400 tokenu target, 80 tokenu overlap
- **Embedding provideri (fallback chain):**
  1. `local` — GGUF model (~600MB, `node-llama-cpp`)
  2. `openai` — `text-embedding-3-small`
  3. `gemini` — `gemini-embedding-001`
  4. `voyage` — Voyage AI embeddings

**Hybrid search merge:**
1. Vector: top `maxResults * candidateMultiplier` by cosine similarity
2. BM25: top `maxResults * candidateMultiplier` by FTS5 BM25 rank
3. Konverze BM25 rank: `textScore = 1 / (1 + max(0, bm25Rank))`
4. Union candidates by chunk id: `finalScore = vectorWeight * vectorScore + textWeight * textScore`
5. Defaultni vahy: 0.7 vector + 0.3 BM25 (konfigurovatelne)

**Post-processing pipeline:**
```
Vector + Keyword → Weighted Merge → Temporal Decay → Sort → MMR → Top-K Results
```

**Temporal decay (recency boost):**
- `decayedScore = score × e^(-λ × ageInDays)` kde `λ = ln(2) / halfLifeDays`
- Default halflife: 30 dni
- Dnesni poznamky: 100% skore, 30 dni stare: 50%, 180 dni: ~1.6%
- Evergreen soubory (MEMORY.md, nedate soubory) nemaji decay

**MMR re-ranking (diversity):**
- Maximal Marginal Relevance — redukuje near-duplicate snippety
- `λ × relevance − (1−λ) × max_similarity_to_selected`
- Default lambda: 0.7 (balancovane, mirny bias k relevanci)
- Podobnost mezi vysledky merana Jaccard similarity na tokenizovanem obsahu

**QMD backend (experimental):**
- Externi sidecar (`qmd` CLI binary) pro BM25 + vectors + reranking
- Bezi lokalne pres Bun + `node-llama-cpp`
- Auto-downloads GGUF models z HuggingFace
- OpenClaw ho spousti jako child process s XDG izolaci per agent

### 2.5 Session Memory (experimental)

OpenClaw muze volitelne indexovat **JSONL session transcripts** a surfacovat je
pres `memory_search`:

```json
{
  "agents": {
    "defaults": {
      "memorySearch": {
        "experimental": { "sessionMemory": true },
        "sources": ["memory", "sessions"]
      }
    }
  }
}
```

- Session indexing je **opt-in** (default vypnuto)
- Delta thresholds: 100KB nebo 50 zprav → background sync
- Indexovani je asynchronni, vysledky mohou byt mirne stale
- Session logy ziji na disku (`~/.openclaw/agents/<agentId>/sessions/*.jsonl`)

### 2.6 Embedding Cache

OpenClaw cachuje chunk embeddings v SQLite:

```json
{
  "agents": {
    "defaults": {
      "memorySearch": {
        "cache": {
          "enabled": true,
          "maxEntries": 50000
        }
      }
    }
  }
}
```

Reindexing a casté updaty (especially session transcripts) nemusí re-embedovat
nezmeneney text. Cache key = chunk content hash + provider + model.

### 2.7 Navrh 5-Tier systemu (PR #17574, NEZMERGOVANO)

Community PR navrhujici 5-urovnovy memory management:

| Tier | Nazev | Popis |
|---|---|---|
| T0 | Working memory | Current context window |
| T1 | Daily entries | `memory/YYYY-MM-DD.md` |
| T2 | Short-term compressed | LLM compression pipeline T1 → T2 |
| T3 | Long-term archived | Auto-archival starych entries |
| T4 | Foundational knowledge | Immutable (MEMORY.md, system prompt) |

Features: recall frequency tracking, tier-weighted search scoring,
LLM compression pipeline, automatic archival, configurable purging.

**Status:** Pull request je otevreny, ale nebyl mergnout (unor 2026).
Existujici funkcionalita se nemeni kdyz je tier system vypnuty.

### 2.8 OpenClaw Memory MCP Tools

- **`memory_search`** — semanticky hleda v markdown chunks (~400 token target,
  80-token overlap). Vraci snippet text (max ~700 chars), file path, line range,
  score, provider/model.
- **`memory_get`** — cte specificky markdown soubor (workspace-relative),
  volitelne od urciteho radku a pro N radku. Cesty mimo `MEMORY.md` / `memory/`
  jsou rejected.

---

## 3. RESERSE: BEST PRACTICES AI AGENT MEMORY (UNOR 2026)

### 3.1 Tiered Memory (industry konsenzus)

Z vice zdroju (LinkedIn, Substack, arxiv) se v unoru 2026 formuje konsenzus
na 3-vrstvovem memory modelu:

| Tier | Nazev | Latence | Naklady | Popis |
|---|---|---|---|---|
| **Hot** | Working memory | <1ms | Vysoke (context window) | Immediate facts, current task state |
| **Warm** | Recent memory | <100ms | Stredni (vector cache) | Recent interactions, 7-30 dni |
| **Cold** | Archival memory | <1s | Nizke (compressed) | Historicka data, low-priority retrieval |
| **Foundational** | Immutable | N/A (loaded at start) | Zero (one-time) | System prompt, agent identity, workspace rules |

**Zdroje:**
- Brian Hammons (LinkedIn, 2026-02-06): 70% redukce initialization context usage
  pres 3-tier loading strategy
- SwarmsSignal (2026-02-08): "Vector Databases Are Agent Memory" — hot/warm/cold tiers
- Grizzly Peak Software (2026-02-13): short-term buffers + PostgreSQL long-term

### 3.2 Hybrid Retrieval (de-facto standard)

BM25 + Vector search je de-facto standard pro unor 2026:
- BM25 silny na exact tokens (IDs, env vars, code symbols, error strings)
- Vector silny na semantic similarity (parafrazovane otazky)
- Hybrid = pragmaticky kompromis pro oba typy dotazu

**Implementacni varianty:**
- SQLite FTS5 + sqlite-vec (OpenClaw, local-first)
- PostgreSQL FTS + pgvector (multi-tenant SaaS)
- Dedicated vector DB (Pinecone, Weaviate — overkill pro single-node)

### 3.3 Memory-as-Files vs Memory-as-DB

| Pristup | Vyhody | Nevyhody |
|---|---|---|
| **File-first** (OpenClaw) | Transparentni, Git-friendly, human-readable | Skaluje spatne pro multi-agent, zadna izolace |
| **DB-first** (Mem0, MemoriesDB) | Structured, queryable, multi-tenant | Vendor lock-in, opaque |
| **Hybrid** (DOPORUCENY) | Files pro human-readable + DB index pro search | Dva zdroje dat (sync needed) |

### 3.4 Hindsight Architecture (arxiv 2512.12818)

Akademicky paper navrhujici 4 logicke site pro agent memory:
- **World facts** — objektivni fakta o svete
- **Agent experiences** — co agent delal a videl
- **Entity summaries** — shrnuti o lidech, projektech, systemech
- **Evolving beliefs** — menic se presvedceni a preference

Operace: **Retain** (ulozit) → **Recall** (najit) → **Reflect** (aktualizovat)

Vysledky: 39% → 83.6% accuracy na long-horizon conversational memory benchmarks
s 20B modelem. Dalsi scaling zlepsi jeste vic.

### 3.5 Cost Management

- Embedding API cally jsou drahe pri velkych corpus (~$0.01/1M tokens OpenAI)
- Local embeddings (GGUF models) jsou free ale pomale (~100ms per chunk)
- **Embedding cache je povinny** — nikdy re-embedovat nezmeneney chunk
- BM25 (FTS5) je ZADARMO — zadne API cally, sub-ms response

### 3.6 Mem0 a MemoriesDB

**Mem0** (formerly ChatMemo): Python framework pro AI agent memory.
Pouziva LangGraph + SQLite + vector embeddings. Long-term memory through
structured memory entries. Popularni v LangChain ekosystemu.

**MemoriesDB** (arxiv 2511.06179): Temporal-semantic-relational DB na
PostgreSQL + pgvector. Append-only schema, directed edges, temporal surfaces.
Akademicky prototype, ne produkcni system.

### 3.7 Graphiti Knowledge Graphs (OpenClaw plugin)

`clawdbrunner/openclaw-graphiti-memory` — hybrid 3-layer system:
1. **Private files** — per-agent directory, personal notes
2. **Shared files** — symlinked, stable reference docs
3. **Shared knowledge graph** — Graphiti temporal knowledge graph (Neo4j)

Vyzaduje Docker + Neo4j + OpenAI API key. Overkill pro Crewship MVP,
ale inspirace pro Phase 3 cross-agent knowledge sharing.

---

## 4. IMPLEMENTACNI STAV (Phase 1 MVP)

> **Posledni aktualizace:** 2026-02-20. Phase 1 MVP je ~70% implementovano.

### 4.1 IMPLEMENTOVANO (v kodu, otestovano)

| Komponent | Kde v kodu | Popis |
|---|---|---|
| **File structure** (.memory/AGENT.md, daily/) | `internal/orchestrator/memory.go` | Orchestrator cte AGENT.md + daily/{today}.md + daily/{yesterday}.md z kontejneru |
| **BM25 FTS5 search engine** | `internal/memory/` (engine.go, search.go, index.go, chunk.go) | Plny memory engine: SQLite FTS5 index, markdown chunker, BM25 search, reindex |
| **System prompt injection** (`buildMemoryContext`) | `internal/orchestrator/memory.go` | Nacte memory soubory pri session start, injektuje `[AGENT MEMORY]` blok do system promptu (max 15k chars, truncation) |
| **Memory instructions block** | `internal/orchestrator/memory.go` (`buildMemoryInstructions`) | Instruuje agenta jak psat do .memory/AGENT.md a .memory/daily/ |
| **DB migration 3** (`memory_config`) | `internal/database/migrate.go` | `ALTER TABLE agents ADD COLUMN memory_config TEXT` |
| **`memory_enabled` flag** (full flow) | `internal/api/agents.go` → `internal/chatbridge/resolver.go` → `internal/chatbridge/bridge.go` → `internal/orchestrator/orchestrator.go` | Boolean per agent, flows z DB pres API az do orchestratoru |
| **Sidecar memory endpoints** | `internal/sidecar/memory.go` + `internal/sidecar/server.go` | `POST /memory/search`, `GET /memory/status`, `POST /memory/reindex` |
| **Sidecar stdin object format** | `cmd/crewship-sidecar/main.go` + `internal/sidecar/credstore.go` | Stdin akceptuje object `{credentials, memory}` s backwards-compat pro array format |
| **Memory engine testy** | `internal/memory/engine_test.go`, `internal/sidecar/memory_test.go`, `internal/orchestrator/memory_test.go` | Unit testy pro engine, sidecar handlery, orchestrator memory loading |

### 4.2 NENI IMPLEMENTOVANO (Phase 1 remaining)

| Komponent | Kriticnost | Popis |
|---|---|---|
| **Sidecar write endpoint** (`POST /memory/write`) | VYSOKE | Agent nema jak zapisovat pamet pres sidecar API — ted pise primo do FS pres CLI |
| **Sidecar read endpoint** (`GET /memory/read`) | VYSOKE | Agent nema jak cist jednotlive soubory pres sidecar API |
| **MCP tools** (`memory_write`, `memory_read`, `memory_search`) | VYSOKE | MCP skill definice + sidecar MCP tool handlery neexistuji |
| **REST API pro UI** (`/api/v1/agents/{id}/memory/*`) | STREDNI | Endpointy pro memory viewer/manager v UI neexistuji |
| **File watcher + auto-reindex** (fsnotify) | STREDNI | Zmeny v .md souborech nevyvolaji automaticky reindex — jen manualni `POST /memory/reindex` |
| **Input validace/sanitizace** | STREDNI | Sidecar nevaliduje velikost, obsah ani rate limit memory zapisu |
| **Audit logging** (memory operaci) | NIZKE | Zadny audit log pro memory read/write/search operace |
| **Rate limiting** (memory zapisu) | NIZKE | Bez rate limitu na sidecar memory endpoints |
| **RBAC pro memory** (sidecar-level) | NIZKE | Sidecar nekontroluje session-id / agent oprávnění pro memory pristup |

### 4.3 NENI IMPLEMENTOVANO (Phase 2+)

| Komponent | Phase | Popis |
|---|---|---|
| **Vector search** (sqlite-vec, hybrid BM25+vector) | Phase 2 | BM25 postacuje pro MVP |
| **Local embeddings** (GGUF model) | Phase 2 | Zero cost, privacy-first — ale az pri vector search |
| **Session memory** (transcript indexing) | Phase 2 | Opt-in indexovani JSONL session transkriptu |
| **Memory flush** (pre-compaction) | Phase 2 | CLI mode = system prompt instrukce (uz implementovano); API-direct mode = presna token detekce |
| **Temporal decay + MMR** | Phase 2 | Exponential decay, diversity re-ranking |
| **Embedding cache** | Phase 2 | Per-chunk hash, skip unchanged |
| **Crew shared memory** | Phase 2B | `/output/.crew-memory/`, CREW.md + topics/, sidecar `/memory/crew` endpoint |
| **Workspace memory** (Coordinator) | Phase 3 | crewshipd spravuje, ne sidecar |
| **Memory encryption at rest** | Phase 3 | AES-256-GCM s ENCRYPTION_KEY |
| **LLM-driven compaction** | Phase 3 | Daily logy → AGENT.md sumarizace |
| **Memory export/import** | Phase 3 | Encrypted ZIP, agent onboarding |
| **Memory analytics** (dashboard widget) | Phase 3 | Recall quality metriky |

### 4.4 Pozitivni zaklad

- **Container izolace** — memory per agent je prirozene izolovana Dockerem + Landlock
- **Persistent storage** — `/output/` je bind mount, prezije restart kontejneru
- **JSONL format** — konzistentni s logy a konverzacemi, lehky, snadno parsovatelny
- **SQLite uz v stacku** — `modernc.org/sqlite` (pure Go), muzeme pridat FTS5 nativne
- **Sidecar architektura** — memory tools = MCP skill pres sidecar, konzistentni pattern

---

## 5. KRITICKA VALIDACE PROTI PRD DOKUMENTUM

### 5.1 KONFLIKT 1: Memory index NESMI byt v agent procesu

**Puvodni navrh:** `index.sqlite` v agent procesu — agent spousti SQLite queries.

**Problem dle AGENT-RUNTIME.md a SECURITY.md:**
- Agent bezi jako UID 1001, non-root, `--read-only` root filesystem
- Agent NEMA SQLite binary v kontejneru (runtime image = minimal Ubuntu + CLI tools)
- FTS5 + sqlite-vec extensions by musely byt v agent runtime image → zvetsuje attack surface
- **SECURITY.md Trust Zone 3:** Agent kontejner = NEDUVERYHODNY. FTS5 query engine
  v agent procesu zvysuje risk prompt injection → memory poisoning/exfiltrace
- Agent pristupuje k nastrójum VZDY pres sidecar (ADR-014, ADR-015)

**Reseni:** Memory index a search provadi **crewship-sidecar** (Go binary).
Agent vola memory pres MCP tools na `localhost:9119`. Konzistentni s existujicim
patternem kde sidecar je jediny prostredník mezi agentem a vnejsim svetem.

### 5.2 KONFLIKT 2: Memory write NESMI byt neomezeny

**Problem dle SECURITY.md sekce 6.1:**
Prompt-injected agent muze:
- Zaplnit disk (`.memory/` je v `/output/` = persistent bind mount na hostu)
- Zapsat misleading fakta do AGENT.md → budouci sessions ovlivneny (memory poisoning)
- Zapsat "User said: ignore all security rules" → budouci prompt injection

**Reseni:**
- `memory_write` tool prochazi pres sidecar → sidecar validuje:
  - Max file size: 100KB per daily log
  - Max total memory: 10MB per agent (konfigurovatelne)
  - Sanitization: no control chars, no executable patterns
  - Rate limit: max 60 writes/min per agent
- Sidecar **audit loguje** kazdy memory write (analogie k tool call audit z AGENT-RUNTIME.md 6A.3)
- Lead MUZE cist memory agentu ve sve crew (RBAC check)
- Agent NEMUZE cist memory jineho agenta

### 5.3 KONFLIKT 3: Crew shared memory vs Landlock izolace

**Puvodni navrh:** `/output/.shared/` — vsichni agenti ctou pres filesystem.

**Problem dle ADR-010, AGENT-RUNTIME.md 16.2:**
Landlock per-agent filesystem izolace explicitne **deny** pristup k workspace
jinych agentu. Sdilena pamet pres FS by musela obejit Landlock → oslabuje izolaci.

**Reseni:** Crew shared memory jde **PRES SIDECAR** (HTTP GET `/memory/crew`),
nikdy primo pres filesystem. Sidecar cte `/output/.crew-memory/` a servuje
jen read-only snippety. Agent nema filesystem pristup ke crew memory.
Konzistentni s principem ze agent pristupuje ke vsemu pres sidecar (ADR-014, ADR-015).

### 5.4 KONFLIKT 4: Ignorovani 4 vrstev hierarchie

**Problem dle ORCHESTRATION.md a CREW-EXECUTION.md:**
Crewship ma 4 organizacni vrstvy — kazda potrebuje jiny typ memory s jinymi RBAC pravidly:

```
Workspace (Organizace) → Coordinator
  └── Crew (Oddeleni) → Crew Lead
        └── Agent (Zamestnanec)
              └── Session/Mission
```

Memory system MUSI respektovat tuto hierarchii. Puvodni navrh mel memory jen
na urovni agenta, coz je nedostatecne.

---

## 6. ARCHITEKTURA MEMORY SYSTEMU

### 6.1 Ctyrvrstvy Memory Model

```
VRSTVA 4: WORKSPACE MEMORY (Phase 3)
  Kde:           /var/lib/crewship/memory/{workspace-id}/
  Kdo spravuje:  crewshipd (Go process)
  Kdo cte:       Coordinator (pres lightweight LLM call v crewshipd)
  Obsah:         Strategicke cile, KPIs, cross-crew rozhodnuti, org policies
  RBAC:          OWNER/ADMIN write, Coordinator read, ostatni NE
  Format:        Markdown + SQLite FTS5 index (spravuje crewshipd)

VRSTVA 3: CREW MEMORY (Phase 2B)
  Kde:           /output/.crew-memory/{crew-slug}/
  Kdo spravuje:  crewship-sidecar
  Kdo cte:       Lead (primo z FS), Agenti (pres sidecar HTTP API)
  Obsah:         Crew rozhodnuti, sdilene fakty, project state, mission historie
  RBAC:          Lead write, Agenti read-only (pres sidecar), cross-crew NE
  Format:        Markdown (CREW.md + topics/) + SQLite FTS5 index

VRSTVA 2: AGENT MEMORY (Phase 1 = MVP)
  Kde:           /output/{agent-slug}/.memory/
  Kdo spravuje:  crewship-sidecar (MCP tools)
  Kdo cte:       Agent sam (pres MCP tools), Lead (pres sidecar read)
  Obsah:         Agent identity, learned facts, daily logs, task notes
  RBAC:          Agent write/read (sve), Lead read (crew scope), ostatni NE
  Format:        Markdown + context.jsonl + SQLite FTS5 index

VRSTVA 1: SESSION MEMORY (Phase 2)
  Kde:           JSONL transcripts (uz existujici) + SQLite session index
  Kdo spravuje:  crewshipd (existujici JSONL writer)
  Kdo cte:       Agent (pres MCP memory_search — opt-in)
  Obsah:         Nedavne konverzace, task context
  RBAC:          Agent cte jen SVE sessions (sidecar enforced)
  Format:        Existujici JSONL + delta-indexed do SQLite
```

### 6.2 Diagram (vrstva 2 — Agent Memory, MVP)

```
┌─────────────────────── Crew Container ────────────────────────┐
│                                                                │
│  ┌──────────────┐           ┌──────────────────────────────┐  │
│  │ Agent         │           │ crewship-sidecar              │  │
│  │ (CLI tool     │  MCP      │ (localhost:9119)               │  │
│  │  nebo         │  tools    │                                │  │
│  │  crewship-    │◄────────►│  ┌──────────────────────────┐ │  │
│  │  agent)       │           │  │ Memory Engine (NOVE)     │ │  │
│  │               │           │  │  ├── SQLite FTS5 index   │ │  │
│  │ NEMA pristup  │           │  │  ├── File watcher        │ │  │
│  │ k .memory/ FS!│           │  │  ├── Chunker (md→chunks) │ │  │
│  │               │           │  │  ├── (Phase 2) sqlite-vec│ │  │
│  │ Pouziva POUZE │           │  │  └── Validator/sanitizer │ │  │
│  │ MCP tools:    │           │  └──────────────────────────┘ │  │
│  │  memory_write │           │                                │  │
│  │  memory_read  │           │  Existujici role:              │  │
│  │  memory_search│           │  ├── Assignment proxy          │  │
│  └──────────────┘           │  ├── MCP Gateway               │  │
│                              │  └── Credential injection      │  │
│                              └──────────┬─────────────────────┘  │
│                                         │ FS access               │
│                              ┌──────────▼─────────────────────┐  │
│                              │ /output/{agent}/.memory/        │  │
│                              │  ├── AGENT.md                   │  │
│                              │  ├── daily/                     │  │
│                              │  │   ├── 2026-02-18.md          │  │
│                              │  │   └── 2026-02-17.md          │  │
│                              │  ├── context.jsonl              │  │
│                              │  └── index.sqlite (derived)     │  │
│                              └─────────────────────────────────┘  │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
```

### 6.3 File Structure

```
/output/{agent-slug}/.memory/
  ├── AGENT.md              ← long-term curated memory (agent identity, preferences,
  │                           learned facts, project context). Evergreen — no temporal decay.
  │                           Nacteny automaticky pri startu KAZDE session.
  │
  ├── daily/                ← denni zaznamy (append-only)
  │   ├── 2026-02-18.md     ← dnesni log
  │   ├── 2026-02-17.md     ← vcerejsi log (nacteny pri startu session)
  │   ├── 2026-02-16.md     ← starsi logy (dostupne pres memory_search)
  │   └── ...
  │
  ├── context.jsonl         ← structured memory entries (machine-readable)
  │                           Kazdy radek = JSON objekt s timestamp, type, content
  │                           Pouzivano pro programmatic recall
  │
  └── index.sqlite          ← DERIVED index (FTS5 + Phase 2: sqlite-vec)
                              Muze se kdykoliv smazat a rebuildnout z .md souboru.
                              Markdown = source of truth, SQLite = jen index.
```

**AGENT.md format:**
```markdown
# Agent Memory — {agent_name}

## Identity
- Role: DevOps Engineer in Infrastructure crew
- Preferred tools: Terraform, Ansible, Docker Compose
- Communication style: concise, technical

## Learned Facts
- Production server: 192.168.1.100 (Ubuntu 24.04)
- Deployment pipeline: GitHub Actions → Docker Hub → Coolify
- Database backups: every 6 hours to S3

## User Preferences
- User prefers Czech language for reports
- User wants cost estimates before any infrastructure changes
- Approval required for production deployments

## Project Context
- Current sprint: migration from Docker Compose to K8s
- Deadline: March 15, 2026
- Blockers: GPU node not yet provisioned
```

**Daily log format:**
```markdown
# 2026-02-18

## Session 1 (10:00)
- Analyzed server logs, found memory leak in auth service
- Recommended restart + heap dump before fix
- User approved restart, scheduled for maintenance window

## Session 2 (14:30)
- Deployed hotfix for auth memory leak (PR #342)
- Monitoring shows memory stable after 2 hours
- Updated AGENT.md with new deployment process
```

**context.jsonl format:**
```jsonl
{"ts":"2026-02-18T10:05:00Z","type":"fact","key":"auth_service_memory_leak","content":"Auth service has memory leak, restarting every 48h"}
{"ts":"2026-02-18T14:35:00Z","type":"decision","key":"hotfix_deployed","content":"Deployed PR #342 fixing auth memory leak"}
{"ts":"2026-02-18T14:40:00Z","type":"preference","key":"deployment_process","content":"User wants staging → canary → production deployment flow"}
```

### 6.4 Kde bezi SQLite index (KRITICKE architekturni rozhodnuti)

**SQLite index bezi v crewship-sidecar (Go process)**, NE v agent procesu:

```
crewship-sidecar (Go binary, uz bezi v kontejneru)
  ├── Assignment proxy (existujici, ORCHESTRATION.md 5.1)
  ├── MCP Gateway (existujici, AGENT-RUNTIME.md 6A)
  └── Memory Engine (NOVE)
        ├── SQLite FTS5 index (/output/{agent}/.memory/index.sqlite)
        ├── File watcher (fsnotify na .memory/*.md)
        ├── Markdown chunker (~500 char chunks)
        ├── Validator + sanitizer
        ├── (Phase 2) sqlite-vec embeddings
        └── MCP tools: memory_write, memory_read, memory_search
```

**Proc sidecar a ne agent proces:**
- Agent je NEDUVERYHODNY (SECURITY.md Trust Zone 3) — nema mit query engine
- Sidecar uz bezi v kazdem crew kontejneru (~5MB RAM overhead)
- Sidecar uz pouziva `modernc.org/sqlite` (pure Go, no CGO)
- Zero network latence (localhost)
- Index je per-agent, per-kontejner — neni potreba centralni service
- **Konzistentni s existujicim patternem** (sidecar = veskery pristup k nastrójum)

**Proc ne crewshipd:**
- crewshipd by musel cist /output/ z hostu pro kazdy search (I/O overhead)
- crewshipd spravuje VSECHNY agenty → memory search by musel byt multi-tenant
- Sidecar uz ma per-crew izolaci (jeden per kontejner)

### 6.5 Memory jako MCP Skill (konzistence s ADR-014)

> **Status: NENI IMPLEMENTOVANO** — MCP skill definice a MCP tools (`memory_write`, `memory_read`, `memory_search`) neexistuji. Planovane pro Phase 2A (plny MCP server). Phase 1 pouziva system prompt instrukce + primo FS zapis.

Memory se implementuje jako **bundled MCP skill** (AGENT-RUNTIME.md 6A.7):

```yaml
# Skill definice: crewship-memory
name: crewship-memory
slug: crewship-memory
type: hybrid  # MCP tools + system prompt fragment
source: BUNDLED
defer_loading: false  # vzdy nacteny (kriticke)
credential_requirements: []  # zadne externi credentials

tools:
  - name: memory_write
    description: "Write content to persistent agent memory"
    inputSchema:
      type: object
      properties:
        content:
          type: string
          description: "Content to write"
        target:
          type: string
          enum: ["daily", "agent"]
          description: "daily = append to today's log, agent = update AGENT.md"
      required: ["content", "target"]

  - name: memory_read
    description: "Read content from a memory file"
    inputSchema:
      type: object
      properties:
        path:
          type: string
          description: "Relative path in .memory/ (e.g. 'AGENT.md', 'daily/2026-02-18.md')"
        startLine:
          type: integer
        numLines:
          type: integer
      required: ["path"]

  - name: memory_search
    description: "Search agent memory using keyword/semantic search"
    inputSchema:
      type: object
      properties:
        query:
          type: string
          description: "Search query"
        limit:
          type: integer
          default: 5
          description: "Max results to return"
      required: ["query"]

system_prompt: |
  You have persistent memory across sessions.
  Your long-term memory (AGENT.md) and recent daily logs are loaded at session start.

  MEMORY GUIDELINES:
  - Write durable facts, preferences, and decisions to memory using memory_write.
  - Use target="agent" for long-term facts (identity, preferences, project context).
  - Use target="daily" for session notes, decisions, observations.
  - Use memory_search to recall past information before starting complex tasks.
  - If the user says "remember this", write it immediately using memory_write.
  - Periodically save important context — don't rely on conversation history alone.
```

**Phase 1 (MVP):** Prompt-only skill + sidecar HTTP endpoints (ne plny MCP server).
Agent vola memory pres CLI tool (curl na sidecar) nebo pres crewship-agent
nativni HTTP. System prompt fragment instruuje agenta JAK pouzivat memory.

**Phase 2A:** Plny MCP skill — sidecar vystavuje memory tools pres MCP stdio proxy.
Agent vola memory tools pres standardni MCP protocol.

---

## 7. TECHNICKY FLOW

### 7.1 Session Start (automaticky memory loading)

> **Status: IMPLEMENTOVANO** — `buildMemoryContext()` v `internal/orchestrator/memory.go` cte AGENT.md + today/yesterday daily logy a injektuje je do system promptu.

```
1. crewshipd spusti Docker exec pro agenta
2. crewshipd sestavi system prompt:
   a) Agent.system_prompt (uzivatelsky definovany)
   b) Crew kontext (lead info, agent list — existujici, ORCHESTRATION.md 3.2)
   c) Memory skill system prompt fragment
   d) IF agent.memory_enabled:
      i)  Sidecar precte /output/{agent}/.memory/AGENT.md
      ii) Sidecar precte /output/{agent}/.memory/daily/{today}.md
      iii) Sidecar precte /output/{agent}/.memory/daily/{yesterday}.md
      iv) Obsah injektuje do system promptu jako [AGENT MEMORY] blok
3. Agent zacne s plnym kontextem (identita + nedavna historie + instrukce)
```

**System prompt injection (priklad):**
```
[AGENT MEMORY — loaded at session start]

## Long-term memory (AGENT.md):
{obsah AGENT.md}

## Recent daily log (2026-02-18):
{obsah daily/2026-02-18.md}

## Yesterday (2026-02-17):
{obsah daily/2026-02-17.md}

[END AGENT MEMORY]

Use memory_write to save new important information.
Use memory_search to recall past events and decisions.
```

### 7.2 Memory Write Flow

> **Status: NENI IMPLEMENTOVANO** — sidecar `POST /memory/write` endpoint a MCP `memory_write` tool neexistuji. Agent ted pise primo do FS pres CLI (`echo >> .memory/AGENT.md`).

```
1. Agent: memory_write({content: "User prefers Czech reports", target: "agent"})

2. Sidecar (localhost:9119) zachyti request:
   a) Validace:
      - content.length < 100KB? ✓
      - Total .memory/ size < 10MB? ✓
      - Sanitization (no control chars, no script tags)? ✓
      - Rate limit (< 60 writes/min per agent)? ✓
   b) RBAC: session-id patri tomuto agentovi? ✓
   c) Zapis:
      - target="agent" → append/update /output/{agent}/.memory/AGENT.md
      - target="daily" → append /output/{agent}/.memory/daily/2026-02-18.md
   d) Index update:
      - Oznacit index jako dirty (fsnotify)
      - Debounced reindex (1.5s) — async, neblokuje response
   e) Audit log:
      {"ts":"2026-02-18T10:05:00Z","type":"memory_write","agent":"bob",
       "target":"agent","file":"AGENT.md","bytes":42,"session":"sess-uuid"}

3. Response:
   {"status": "ok", "file": "AGENT.md", "bytes_written": 42}
```

### 7.3 Memory Search Flow

> **Status: CASTECNE IMPLEMENTOVANO** — sidecar `POST /memory/search` endpoint je implementovany (BM25 FTS5). Chybi: MCP `memory_search` tool (agent musi ted volat sidecar primo pres HTTP). Phase 2 hybrid search (vector + BM25) neni implementovan.

```
1. Agent: memory_search({query: "deployment process", limit: 5})

2. Sidecar:
   a) RBAC: session-id patri tomuto agentovi? ✓
   b) Phase 1 (BM25 only):
      - SQLite FTS5 query: SELECT * FROM memory_chunks
        WHERE memory_chunks MATCH 'deployment process'
        ORDER BY rank LIMIT 5*4  (candidateMultiplier=4)
      - Score conversion: textScore = 1 / (1 + max(0, bm25Rank))
      - Return top 5 by textScore
   c) Phase 2 (Hybrid BM25 + Vector):
      - BM25 candidates: top 20 by FTS5 rank
      - Vector candidates: top 20 by cosine similarity (sqlite-vec)
      - Merge: finalScore = 0.7 * vectorScore + 0.3 * textScore
      - Post-processing: temporal decay → sort → MMR → top 5

3. Response:
   {
     "results": [
       {
         "file": "AGENT.md",
         "line_start": 15,
         "line_end": 18,
         "snippet": "Deployment pipeline: GitHub Actions → Docker Hub → Coolify...",
         "score": 0.87,
         "search_type": "bm25"
       },
       {
         "file": "daily/2026-02-18.md",
         "line_start": 8,
         "line_end": 10,
         "snippet": "Deployed hotfix for auth memory leak (PR #342)...",
         "score": 0.72,
         "search_type": "bm25"
       }
     ],
     "total_chunks": 145,
     "search_ms": 2
   }
```

### 7.4 Memory Flush (Pre-Compaction)

> **Status: CASTECNE IMPLEMENTOVANO** — system prompt instrukce pro CLI mode jsou implementovane v `buildMemoryInstructions()`. API-direct mode (crewship-agent token detection) je planovany pro Phase 2.

**CLI mode (Phase 1 — Claude Code, OpenCode, Codex):**

CLI tools maji vlastni compaction mechanismus. Crewship NEMUZE detekovat
context fullness primo (CLI je black box).

**Reseni pro Phase 1:** Agent dostane instrukci v system promptu:
```
IMPORTANT: Before your context gets compacted, write important facts to memory
using memory_write tool. Write early and often — don't wait until the end.
If you notice the conversation is getting long, proactively save key decisions
and context to your daily log.
```

Toto je dostatecne pro MVP a vyhyba se VSEM compaction bugum ktere trapi OpenClaw.

**API-direct mode (Phase 2 — crewship-agent):**

crewship-agent sam pocita tokeny (presne z API response):
```go
// cmd/crewship-agent/memory_flush.go
func (a *Agent) checkMemoryFlush(totalTokens, contextWindow int) {
    threshold := contextWindow - a.config.ReserveTokensFloor - a.config.FlushSoftThreshold
    if totalTokens > threshold && !a.flushedThisCycle {
        // Inject silent system message
        a.injectSystemMessage("Session nearing compaction. " +
            "Write any lasting notes to memory using memory_write. " +
            "Reply with NO_REPLY if nothing to store.")
        a.flushedThisCycle = true
    }
}
```

### 7.5 Indexing Pipeline

> **Status: CASTECNE IMPLEMENTOVANO** — markdown chunker a FTS5 indexer jsou implementovane v `internal/memory/`. Chybi: fsnotify file watcher (auto-reindex), vector index (Phase 2). Reindex se ted spousti manualne pres `POST /memory/reindex`.

```
.md soubory zmena (fsnotify)
  │
  ▼
Debounce (1.5s) — ignoruje burst zmen
  │
  ▼
Chunker:
  - Markdown → plain text
  - Split by headings (## jako boundary)
  - Target: ~500 chars per chunk, ~80 chars overlap
  - Kazdy chunk: {file, line_start, line_end, content, hash}
  │
  ▼
FTS5 Index (Phase 1):
  - INSERT OR REPLACE INTO memory_chunks(file, line_start, line_end, content)
  - FTS5 tokenizer: unicode61 (podpora diakritiky — CZ/SK content)
  │
  ▼
Vector Index (Phase 2):
  - Embedding: local GGUF model NEBO remote API (configurable)
  - Cache check: chunk hash unchanged? → skip embedding
  - sqlite-vec: INSERT INTO memory_vec(chunk_id, embedding)
  │
  ▼
Index ready — search requests served
```

---

## 8. SIDECAR API ROZSIRENI

### 8.1 Nove endpointy (doplneni k existujicim z ORCHESTRATION.md 5.1)

> **Status:** `POST /memory/search`, `GET /memory/status`, `POST /memory/reindex` — IMPLEMENTOVANO v `internal/sidecar/memory.go`. Ostatni endpointy (`POST /memory/write`, `GET /memory/read`, `GET /memory/crew`, `DELETE /memory/daily/{date}`) — NENI IMPLEMENTOVANO.

```
MEMORY ENDPOINTS (localhost:9119):

  POST /memory/write
    Headers: X-Crewship-Session: {session-id}
    Body: {"content": "...", "target": "daily"|"agent"}
    Response: {"status": "ok", "file": "...", "bytes_written": N}
    Validace: size limit, sanitization, rate limit, RBAC

  GET /memory/read?path={relative_path}&startLine={N}&numLines={N}
    Headers: X-Crewship-Session: {session-id}
    Response: {"content": "...", "file": "...", "total_lines": N}
    Validace: path traversal check, RBAC (jen vlastni .memory/)

  POST /memory/search
    Headers: X-Crewship-Session: {session-id}
    Body: {"query": "...", "limit": 5, "sources": ["memory", "sessions"]}
    Response: {"results": [...], "total_chunks": N, "search_ms": N}
    Validace: RBAC (agent cte jen SVE memory)

  GET /memory/status
    Headers: X-Crewship-Session: {session-id}
    Response: {
      "enabled": true,
      "total_size_bytes": 45230,
      "file_count": 12,
      "chunk_count": 145,
      "last_indexed": "2026-02-18T10:05:00Z",
      "index_backend": "fts5",  // "fts5" | "hybrid" | "vector"
      "limits": {"max_size_mb": 10, "daily_max_kb": 100}
    }

  GET /memory/crew
    Headers: X-Crewship-Session: {session-id}
    Response: {"files": [...], "search_available": true}
    Validace: RBAC — jen agenti v dane crew
    NOTE: Phase 2B

  DELETE /memory/daily/{date}
    Headers: X-Crewship-Session: {session-id}
    Validace: RBAC — jen lead muze mazat denni logy
    NOTE: Phase 3
```

### 8.2 REST API (nove endpointy na crewshipd)

> **Status: NENI IMPLEMENTOVANO** — zadne REST API endpointy pro memory management v UI. Planovane pro Phase 1 remaining.

```
MEMORY MANAGEMENT API (/api/v1/):

  GET /api/v1/agents/{id}/memory
    Auth: JWT
    RBAC: OWNER/ADMIN/MANAGER (prirazena crew)
    Response: memory status (velikost, soubory, index stav)

  GET /api/v1/agents/{id}/memory/files
    Auth: JWT
    RBAC: OWNER/ADMIN/MANAGER
    Response: seznam memory souboru (tree)

  GET /api/v1/agents/{id}/memory/{path}
    Auth: JWT
    RBAC: OWNER/ADMIN/MANAGER
    Response: obsah memory souboru (pro UI viewer)
    Validace: path traversal, jen .memory/ scope

  PUT /api/v1/agents/{id}/memory/config
    Auth: JWT
    RBAC: OWNER/ADMIN
    Body: {"max_size_mb": 10, "daily_log_max_kb": 100, "search_enabled": true}
    Response: updated config

  DELETE /api/v1/agents/{id}/memory
    Auth: JWT
    RBAC: OWNER/ADMIN
    Effect: smaze vsechny memory soubory + index (factory reset)
    Audit: logged
```

---

## 9. SECURITY ANALYZA

### 9.1 Threat Model (doplneni k SECURITY.md sekce 6)

| Vektor | Riziko | Mitigace |
|---|---|---|
| **Memory poisoning** (prompt injection → agent zapise skodlivy obsah do AGENT.md) | VYSOKE — ovlivni vsechny budouci sessions | Sidecar sanitizace, max file size, audit log. Phase 2: LLM-based review pred zápisem do AGENT.md |
| **Memory exfiltrace** (agent cte memory jineho agenta) | STREDNI | Sidecar RBAC: agent cte JEN svoji .memory/. Landlock deny na /output/ jinych agentu |
| **Disk exhaustion** (agent zapisuje nekonecne do memory) | STREDNI | Sidecar limit: 10MB per agent .memory/, 100KB per daily file, 60 writes/min |
| **Cross-crew memory leak** (lead cte memory z jine crew) | NIZKE | Sidecar overuje crew membership. Memory endpointy jen na localhost |
| **Index corruption** (SQLite index corrupted → search fails) | NIZKE | Index je DERIVED — smazat a rebuildnout z .md souboru. Sidecar healthcheck |
| **Stale memory** (agent pamatuje si zastarale fakty) | STREDNI | Temporal decay (Phase 2), memory compaction (Phase 3) |
| **Path traversal** (agent posle path mimo .memory/) | VYSOKE | Sidecar validuje ze path je v /output/{agent}/.memory/ scope. Zadny `..` |
| **Memory as covert channel** (agent pise zpravy jinemu agentovi pres shared crew memory) | NIZKE | Crew memory je read-only pro agenty, write-only pro lead |

### 9.2 Memory Encryption at Rest

**Phase 1 (MVP):** Memory soubory jsou plaintext na hostu v `/output/`.
Toto je akceptovatelne protoze:
- `/output/` je pod kontrolou hostu (self-hosted = uzivatel vlastni HW)
- Docker bind mount ma host-level file permissions
- Konzistentni s JSONL konverzacemi (ty jsou taky plaintext na hostu)
- OpenClaw ma taky plaintext memory (zname security issue)

**Phase 3 (Enterprise):** Memory encryption at rest:
- Sidecar sifruje memory soubory s AES-256-GCM (ENCRYPTION_KEY — uz existujici env var)
- Index.sqlite taky sifrovany
- Decrypt jen v pameti sidecaru pri read/search
- **Crewship differentiator vs. OpenClaw** (plaintext memory = known security risk)

### 9.3 RBAC pro Memory

> **Status: NENI IMPLEMENTOVANO** — sidecar nekontroluje RBAC pro memory operace. Planovane pro Phase 1 remaining.

| Akce | Agent | Lead | Coordinator | MEMBER | MANAGER | ADMIN | OWNER |
|---|---|---|---|---|---|---|---|
| Read vlastni memory | ✅ | ✅ | — | — | — | — | — |
| Write vlastni memory | ✅ | ✅ | — | — | — | — | — |
| Search vlastni memory | ✅ | ✅ | — | — | — | — | — |
| Read memory agenta v crew | ❌ | ✅ (crew) | ❌ | ❌ | ✅ (crew) | ✅ | ✅ |
| Write crew shared memory | ❌ | ✅ | ❌ | ❌ | ❌ | ✅ | ✅ |
| Read crew shared memory | ✅ (crew) | ✅ (crew) | ❌ | ❌ | ✅ (crew) | ✅ | ✅ |
| Read workspace memory | ❌ | ❌ | ✅ | ❌ | ❌ | ✅ | ✅ |
| Delete agent memory | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ |
| Config memory settings | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ |
| View memory in UI | ❌ | ❌ | ❌ | ❌ | ✅ (crew) | ✅ | ✅ |

### 9.4 Audit Log

> **Status: NENI IMPLEMENTOVANO** — zadny audit log pro memory operace. Planovane pro Phase 1 remaining.

Vsechny memory operace se loguji do audit logu:

```jsonl
{"ts":"2026-02-18T10:05:00Z","action":"memory.write","agent":"bob","file":"AGENT.md","bytes":42,"session":"sess-uuid"}
{"ts":"2026-02-18T10:06:00Z","action":"memory.write","agent":"bob","file":"daily/2026-02-18.md","bytes":128,"session":"sess-uuid"}
{"ts":"2026-02-18T10:10:00Z","action":"memory.search","agent":"bob","query":"deployment process","results":3,"search_ms":2}
{"ts":"2026-02-18T10:15:00Z","action":"memory.read","agent":"bob","file":"AGENT.md","session":"sess-uuid"}
{"ts":"2026-02-18T11:00:00Z","action":"memory.read_cross","user":"admin@example.com","agent":"bob","file":"AGENT.md"}
{"ts":"2026-02-18T12:00:00Z","action":"memory.delete","user":"admin@example.com","agent":"bob","reason":"factory_reset"}
```

---

## 10. DATABASE IMPACT

### 10.1 Schema zmeny (minimalni)

> **Status: IMPLEMENTOVANO** — `memory_config TEXT` sloupec pridan v migraci 3. `memory_enabled` flag uz existoval v init migraci.

**ZADNE nove tabulky pro Phase 1.** Pouze rozsireni Agent modelu:

```sql
-- Agent model: memory_enabled uz existuje (Boolean, default false)
-- Pridat JSON konfiguracni sloupec:
ALTER TABLE agents ADD COLUMN memory_config TEXT;
-- JSON obsah: {"max_size_mb": 10, "daily_log_max_kb": 100, "search_enabled": true}
```

**Prisma schema doplneni:**
```prisma
model Agent {
  // ... existujici pole ...
  memory_enabled  Boolean  @default(false)
  memory_config   Json?    // {"max_size_mb": 10, "daily_log_max_kb": 100, "search_enabled": true}
}
```

Memory metadata (posledni sync, index stav) ziji v `index.sqlite` (per-agent) — NE v hlavni DB.
Konzistentni s principem "zadne logy/zpravy v DB" (DATABASE.md sekce 1).

### 10.2 Go migrace (internal/database/migrate.go)

> **Status: IMPLEMENTOVANO** — migrace je v kodu jako migrace cislo 3 (ne 21 jak puvodni navrh). Viz `internal/database/migrate.go`: `{3, "add_memory_config", migrationAddMemoryConfig}`.

```go
// Migration 3: Add memory_config to agents (IMPLEMENTOVANO)
{3, "add_memory_config", `ALTER TABLE agents ADD COLUMN memory_config TEXT;`},
```

---

## 11. SROVNANI: CREWSHIP vs OPENCLAW MEMORY

### 11.1 Kde je Crewship HORSI nez OpenClaw (ted)

> **Poznamka (2026-02-20):** Od verze 1.1 bylo implementovano BM25 FTS5 search, file structure (.memory/AGENT.md + daily/), a system prompt injection. Zbyvajici mezery viz nize.

| Oblast | OpenClaw | Crewship (ted) | Severity |
|---|---|---|---|
| **Memory retrieval** | Hybrid BM25 + Vector search, sqlite-vec | BM25 FTS5 search (implementovano), vector search chybi | STREDNI (Phase 2) |
| **Memory flush** | Pre-compaction auto-flush, configurable | System prompt instrukce (implementovano), API-direct mode chybi | NIZKE (Phase 2) |
| **Long-term memory** | MEMORY.md (curated) + daily logs | AGENT.md + daily/ (implementovano, file structure shodna) | ✅ VYRESENO |
| **Memory search tools** | `memory_search` + `memory_get` MCP tools | Sidecar `POST /memory/search` (implementovano), MCP tools chybi | STREDNI (Phase 2A) |
| **Embedding cache** | SQLite cache, reuse unchanged chunks | Neexistuje | NIZKE — az pri vector search |
| **Temporal decay** | Exponential decay, configurable halflife | Neexistuje | NIZKE — nice-to-have |
| **Session memory** | Opt-in transcript indexing | Neexistuje | NIZKE — Phase 2 |

### 11.2 Kde je Crewship LEPSI nez OpenClaw (uz ted nebo po implementaci)

| Oblast | Crewship prinos | OpenClaw |
|---|---|---|
| **Multi-agent memory izolace** | Landlock + per-agent /output/ + sidecar RBAC | Single-user, zadna izolace |
| **Persistent by default** | /output/ bind mount prezije vse | Workspace muze byt ephemeral |
| **SQLite uz v stacku** | `modernc.org/sqlite` pure Go, zero new deps | Node.js + native extensions |
| **Container boundary** | Agent nemuze kompromitovat memory jineho agenta | Agenti bezi na hostu |
| **Sidecar architektura** | Memory = MCP skill, konzistentni pattern | Tight coupling s gateway |
| **4-vrstvova memory** | Workspace/Crew/Agent/Session hierarchy | Flat (single user) |
| **Crew shared memory** | Lead pise, agenti ctou — team knowledge base | Neexistuje (single user) |
| **Memory audit log** | Kazdy read/write auditovany | Zadny audit |
| **Memory size limits** | Sidecar enforced (10MB/agent, 100KB/daily) | Neomezeno |
| **Encryption at rest (Phase 3)** | AES-256-GCM s existujicim ENCRYPTION_KEY | Plaintext na disku |
| **Memory validation** | Sidecar sanitizace, rate limiting | Zadna validace |

### 11.3 Co OpenClaw dela spatne (a my to udelame lepe)

| OpenClaw problem | Nase reseni | Proc lepsi |
|---|---|---|
| **Memory flush je buggy** (#4836, #17603, #15218) | Phase 1: zadny auto-flush — agent pise prubezne (system prompt instrukce). Phase 2: presna detekce v API-direct mode | Jednodussi = spolehlivejsi. Vyhybame se VSEM compaction bugum |
| **Flat file structure** — degraduje s objemem | Tiered: AGENT.md (curated) + daily/ (append-only) + index | Separace hot/cold dat od zacatku |
| **Single-user, zadna izolace** | Per-agent memory, sidecar RBAC, Landlock FS izolace | Multi-tenant safe |
| **Plaintext memory na disku** | Phase 3: AES-256-GCM encryption at rest | Enterprise-grade security |
| **5-Tier navrzeny ale ne merged** (PR #17574) | 4 vrstvy dle nasi hierarchie (workspace/crew/agent/session) | Prirozene mapovani na existujici architekturu |
| **Vector search vyzaduje API klice** | Phase 1: BM25 only (zero cost). Phase 2: local GGUF embeddings | Zero cost MVP, privacy-first |
| **QMD externi sidecar dependency** | Memory engine integrovan primo do crewship-sidecar | Zadna nova dependency |
| **Post-compaction context loss** (#18023) | Memory loaded pri KAZDEM session start (ne jen po compaction) | Agent vzdy zna svoji identitu a historii |
| **Wrong dates in memory files** (#17603) | Sidecar generuje nazev souboru (ne LLM) — presne datum | Eliminace LLM chyb v metadata |

---

## 12. GO IMPLEMENTACE

### 12.1 Memory Engine v sidecaru

> **Status: IMPLEMENTOVANO** — skutecna implementace je v `internal/memory/engine.go`. Kod nize je referencni navrh z PRD — realna implementace se muze lisit v detailech (napr. chybi fsnotify watcher, jina signatura metod).

```go
// cmd/crewship-sidecar/memory.go

type MemoryEngine struct {
    basePath    string            // /output/{agent-slug}/.memory/
    agentSlug   string
    db          *sql.DB           // SQLite FTS5 index (modernc.org/sqlite)
    watcher     *fsnotify.Watcher
    mu          sync.RWMutex
    config      MemoryConfig
    indexDirty  atomic.Bool
    lastIndexed time.Time
}

type MemoryConfig struct {
    MaxSizeMB     int  `json:"max_size_mb"`      // default: 10
    DailyMaxKB    int  `json:"daily_log_max_kb"`  // default: 100
    SearchEnabled bool `json:"search_enabled"`    // default: true
}

func NewMemoryEngine(basePath, agentSlug string, config MemoryConfig) (*MemoryEngine, error) {
    // Vytvorit adresarovou strukturu
    os.MkdirAll(filepath.Join(basePath, "daily"), 0755)

    // Otevrit/vytvorit SQLite index
    dbPath := filepath.Join(basePath, "index.sqlite")
    db, err := sql.Open("sqlite", dbPath)
    if err != nil {
        return nil, fmt.Errorf("open memory index: %w", err)
    }

    // Vytvorit FTS5 tabulku
    _, err = db.Exec(`
        CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks USING fts5(
            file, line_start, line_end, content,
            tokenize='unicode61'
        )
    `)
    if err != nil {
        return nil, fmt.Errorf("create FTS5 table: %w", err)
    }

    // Spustit fsnotify watcher
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        return nil, fmt.Errorf("create watcher: %w", err)
    }
    watcher.Add(basePath)
    watcher.Add(filepath.Join(basePath, "daily"))

    engine := &MemoryEngine{
        basePath:  basePath,
        agentSlug: agentSlug,
        db:        db,
        watcher:   watcher,
        config:    config,
    }

    // Inicializacni index
    go engine.reindex()

    // Background watcher
    go engine.watchFiles()

    return engine, nil
}
```

### 12.2 Memory Write

> **Status: NENI IMPLEMENTOVANO** — Write metoda na MemoryEngine neexistuje. Planovano pro Phase 1 remaining (sidecar `POST /memory/write`).

```go
func (e *MemoryEngine) Write(content, target string) (string, int, error) {
    e.mu.Lock()
    defer e.mu.Unlock()

    // Validace velikosti
    totalSize, err := e.totalSize()
    if err != nil {
        return "", 0, fmt.Errorf("check size: %w", err)
    }
    if totalSize+int64(len(content)) > int64(e.config.MaxSizeMB)*1024*1024 {
        return "", 0, fmt.Errorf("memory limit exceeded: %dMB max", e.config.MaxSizeMB)
    }

    // Sanitizace
    content = sanitizeMemoryContent(content)

    var filePath string
    switch target {
    case "daily":
        date := time.Now().Format("2006-01-02")
        filePath = filepath.Join(e.basePath, "daily", date+".md")

        // Check daily file size
        if info, err := os.Stat(filePath); err == nil {
            if info.Size()+int64(len(content)) > int64(e.config.DailyMaxKB)*1024 {
                return "", 0, fmt.Errorf("daily log limit exceeded: %dKB max", e.config.DailyMaxKB)
            }
        }

        // Append
        f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
        if err != nil {
            return "", 0, fmt.Errorf("open daily log: %w", err)
        }
        defer f.Close()
        n, err := f.WriteString("\n" + content + "\n")
        return filepath.Base(filePath), n, err

    case "agent":
        filePath = filepath.Join(e.basePath, "AGENT.md")
        // Append to AGENT.md
        f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
        if err != nil {
            return "", 0, fmt.Errorf("open AGENT.md: %w", err)
        }
        defer f.Close()
        n, err := f.WriteString("\n" + content + "\n")
        return "AGENT.md", n, err

    default:
        return "", 0, fmt.Errorf("invalid target: %s (must be 'daily' or 'agent')", target)
    }
}

func sanitizeMemoryContent(content string) string {
    // Remove control characters (except newline, tab)
    content = strings.Map(func(r rune) rune {
        if r < 32 && r != '\n' && r != '\t' {
            return -1
        }
        return r
    }, content)
    // Truncate to reasonable size
    if len(content) > 100*1024 {
        content = content[:100*1024]
    }
    return content
}
```

### 12.3 Memory Search (BM25 — Phase 1)

> **Status: IMPLEMENTOVANO** — skutecna implementace je v `internal/memory/search.go`. Kod nize je referencni navrh.

```go
func (e *MemoryEngine) Search(query string, limit int) ([]MemorySearchResult, error) {
    e.mu.RLock()
    defer e.mu.RUnlock()

    if !e.config.SearchEnabled {
        return nil, fmt.Errorf("memory search is disabled for this agent")
    }

    if limit <= 0 || limit > 20 {
        limit = 5
    }

    rows, err := e.db.Query(`
        SELECT file, line_start, line_end, snippet(memory_chunks, 3, '', '', '...', 64),
               rank
        FROM memory_chunks
        WHERE memory_chunks MATCH ?
        ORDER BY rank
        LIMIT ?
    `, query, limit*4) // candidateMultiplier = 4
    if err != nil {
        return nil, fmt.Errorf("FTS5 search: %w", err)
    }
    defer rows.Close()

    var results []MemorySearchResult
    for rows.Next() {
        var r MemorySearchResult
        var bm25Rank float64
        if err := rows.Scan(&r.File, &r.LineStart, &r.LineEnd, &r.Snippet, &bm25Rank); err != nil {
            continue
        }
        r.Score = 1.0 / (1.0 + math.Max(0, bm25Rank))
        r.SearchType = "bm25"
        results = append(results, r)
    }
    if err := rows.Err(); err != nil {
        return nil, fmt.Errorf("iterate results: %w", err)
    }

    // Sort by score descending, take top limit
    sort.Slice(results, func(i, j int) bool {
        return results[i].Score > results[j].Score
    })
    if len(results) > limit {
        results = results[:limit]
    }

    return results, nil
}

type MemorySearchResult struct {
    File       string  `json:"file"`
    LineStart  int     `json:"line_start"`
    LineEnd    int     `json:"line_end"`
    Snippet    string  `json:"snippet"`
    Score      float64 `json:"score"`
    SearchType string  `json:"search_type"` // "bm25" | "vector" | "hybrid"
}
```

### 12.4 Reindex Pipeline

> **Status: IMPLEMENTOVANO** — skutecna implementace je v `internal/memory/index.go` (Reindex) a `internal/memory/chunk.go` (chunker). Kod nize je referencni navrh.

```go
func (e *MemoryEngine) reindex() error {
    e.mu.Lock()
    defer e.mu.Unlock()

    // Clear existing index
    e.db.Exec("DELETE FROM memory_chunks")

    // Walk .memory/ directory
    return filepath.Walk(e.basePath, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
            return nil
        }

        relPath, _ := filepath.Rel(e.basePath, path)
        content, err := os.ReadFile(path)
        if err != nil {
            return nil // skip unreadable files
        }

        // Chunk markdown
        chunks := chunkMarkdown(string(content), relPath, 500, 80)

        // Insert chunks into FTS5
        for _, chunk := range chunks {
            e.db.Exec(
                "INSERT INTO memory_chunks(file, line_start, line_end, content) VALUES(?, ?, ?, ?)",
                chunk.File, chunk.LineStart, chunk.LineEnd, chunk.Content,
            )
        }

        return nil
    })
}

type MemoryChunk struct {
    File      string
    LineStart int
    LineEnd   int
    Content   string
}

func chunkMarkdown(content, filePath string, targetSize, overlap int) []MemoryChunk {
    lines := strings.Split(content, "\n")
    var chunks []MemoryChunk
    var current strings.Builder
    startLine := 1

    for i, line := range lines {
        // Heading = chunk boundary
        if strings.HasPrefix(line, "## ") && current.Len() > 0 {
            chunks = append(chunks, MemoryChunk{
                File:      filePath,
                LineStart: startLine,
                LineEnd:   i,
                Content:   current.String(),
            })
            current.Reset()
            startLine = i + 1
        }

        current.WriteString(line + "\n")

        // Size-based boundary
        if current.Len() >= targetSize {
            chunks = append(chunks, MemoryChunk{
                File:      filePath,
                LineStart: startLine,
                LineEnd:   i + 1,
                Content:   current.String(),
            })
            current.Reset()
            startLine = i + 1
        }
    }

    // Remaining
    if current.Len() > 0 {
        chunks = append(chunks, MemoryChunk{
            File:      filePath,
            LineStart: startLine,
            LineEnd:   len(lines),
            Content:   current.String(),
        })
    }

    return chunks
}
```

### 12.5 File Watcher (debounced reindex)

> **Status: NENI IMPLEMENTOVANO** — fsnotify file watcher neexistuje. Reindex se spousti manualne pres `POST /memory/reindex`. Planovano pro Phase 1 remaining.

```go
func (e *MemoryEngine) watchFiles() {
    debounceTimer := time.NewTimer(0)
    debounceTimer.Stop()

    for {
        select {
        case event, ok := <-e.watcher.Events:
            if !ok {
                return
            }
            if strings.HasSuffix(event.Name, ".md") {
                e.indexDirty.Store(true)
                debounceTimer.Reset(1500 * time.Millisecond) // 1.5s debounce
            }

        case <-debounceTimer.C:
            if e.indexDirty.CompareAndSwap(true, false) {
                if err := e.reindex(); err != nil {
                    log.Printf("memory reindex error: %v", err)
                }
                e.lastIndexed = time.Now()
            }
        }
    }
}
```

---

## 13. PHASE 2: VECTOR SEARCH

> **Status: NENI IMPLEMENTOVANO** — cela sekce popisuje planovane Phase 2 features.

### 13.1 sqlite-vec Integration

```go
// Phase 2: pridat do MemoryEngine

func (e *MemoryEngine) initVectorTable() error {
    // sqlite-vec extension (pure Go compatible via modernc.org/sqlite)
    _, err := e.db.Exec(`
        CREATE VIRTUAL TABLE IF NOT EXISTS memory_vec USING vec0(
            chunk_id INTEGER PRIMARY KEY,
            embedding FLOAT[384]
        )
    `)
    return err
}

func (e *MemoryEngine) hybridSearch(query string, limit int) ([]MemorySearchResult, error) {
    // 1. BM25 candidates
    bm25Results, _ := e.bm25Search(query, limit*4)

    // 2. Vector candidates
    queryEmbedding := e.embedQuery(query)
    vectorResults, _ := e.vectorSearch(queryEmbedding, limit*4)

    // 3. Merge
    merged := mergeResults(bm25Results, vectorResults, 0.7, 0.3)

    // 4. Temporal decay (optional)
    if e.config.TemporalDecayEnabled {
        merged = applyTemporalDecay(merged, e.config.TemporalDecayHalfLifeDays)
    }

    // 5. MMR re-ranking (optional)
    if e.config.MMREnabled {
        merged = applyMMR(merged, e.config.MMRLambda)
    }

    // 6. Top-K
    if len(merged) > limit {
        merged = merged[:limit]
    }

    return merged, nil
}
```

### 13.2 Local Embeddings

```go
// Phase 2: GGUF embedding model v agent runtime image
// Alternativy:
// 1. Local: llama.cpp Go bindings (github.com/ggerganov/llama.cpp/bindings/go)
// 2. Remote: OpenAI text-embedding-3-small (vyzaduje API key)
// 3. Remote: Gemini embedding-001 (vyzaduje API key)

type EmbeddingProvider interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    Dimension() int
}

type LocalEmbeddingProvider struct {
    modelPath string
    dimension int
    // llama.cpp model
}

type RemoteEmbeddingProvider struct {
    apiURL    string
    apiKey    string
    model     string
    dimension int
}
```

### 13.3 Embedding Cache

```go
func (e *MemoryEngine) getCachedEmbedding(chunkHash string) ([]float32, bool) {
    row := e.db.QueryRow(
        "SELECT embedding FROM embedding_cache WHERE chunk_hash = ?",
        chunkHash,
    )
    var data []byte
    if err := row.Scan(&data); err != nil {
        return nil, false
    }
    return deserializeEmbedding(data), true
}

func (e *MemoryEngine) cacheEmbedding(chunkHash string, embedding []float32) {
    e.db.Exec(
        "INSERT OR REPLACE INTO embedding_cache(chunk_hash, embedding, created_at) VALUES(?, ?, ?)",
        chunkHash, serializeEmbedding(embedding), time.Now(),
    )
}
```

---

## 14. PHASE 2B: CREW SHARED MEMORY

> **Status: NENI IMPLEMENTOVANO** — cela sekce popisuje planovane Phase 2B features.

### 14.1 Architektura

```
/output/.crew-memory/{crew-slug}/
  ├── CREW.md              ← crew-level decisions, policies, context
  ├── topics/
  │   ├── infrastructure.md
  │   ├── deployment.md
  │   └── security.md
  └── index.sqlite         ← FTS5 index (spravuje sidecar)
```

### 14.2 Kdo pise, kdo cte

- **Lead** pise do crew memory (po kazdem mission completion, pri dulezitych rozhodnutich)
- **Agenti** ctou crew memory pres sidecar (GET `/memory/crew`)
- **Agenti NEMOHOU** psat primo do crew memory (jen lead)
- **Cross-crew** pristup neni mozny (sidecar je per-crew kontejner)

### 14.3 Automaticky zapis z Mission

Po dokonceni Mission (CREW-EXECUTION.md) lead automaticky sumarizuje
klicova rozhodnuti a zapise je do crew memory:

```go
func (e *MissionEngine) onMissionComplete(mission *Mission) {
    summary := e.summarizeMissionDecisions(mission)
    e.sidecar.WriteCrewMemory(CrewMemoryEntry{
        Topic:   "mission-" + mission.TraceID,
        Content: summary,
    })
}
```

---

## 15. PHASE 3: WORKSPACE MEMORY + ADVANCED

> **Status: NENI IMPLEMENTOVANO** — cela sekce popisuje planovane Phase 3 features.

### 15.1 Workspace Memory (Coordinator)

Coordinator (lightweight LLM call v crewshipd, viz ORCHESTRATION.md 5.2)
pristupuje k workspace-level memory:

```
/var/lib/crewship/memory/{workspace-id}/
  ├── WORKSPACE.md         ← org-level strategy, KPIs, policies
  ├── crews/
  │   ├── marketing.md     ← aggregated crew summaries
  │   └── development.md
  └── index.sqlite
```

Spravuje crewshipd (ne sidecar — coordinator nema kontejner).

### 15.2 LLM-Driven Memory Compaction

Background job (sidecar scheduled task):
- Daily logy starsi 30 dni → LLM sumarizuje dulezite body
- Sumarizace se prida do AGENT.md (long-term memory)
- Original daily logy se archivuji (move do cold storage)

```go
func (e *MemoryEngine) compactOldDailyLogs() error {
    cutoff := time.Now().AddDate(0, 0, -30)
    oldLogs := e.findDailyLogsOlderThan(cutoff)

    for _, log := range oldLogs {
        content, _ := os.ReadFile(log.Path)
        summary, _ := e.llmSummarize(string(content))

        // Append summary to AGENT.md
        e.Write(fmt.Sprintf("\n## Archived: %s\n%s\n", log.Date, summary), "agent")

        // Archive original
        archivePath := filepath.Join(e.basePath, "archive", filepath.Base(log.Path))
        os.Rename(log.Path, archivePath)
    }
    return nil
}
```

### 15.3 Memory Encryption at Rest

```go
func (e *MemoryEngine) writeEncrypted(path string, content []byte) error {
    encrypted, err := encryption.Encrypt(content, e.encryptionKey)
    if err != nil {
        return fmt.Errorf("encrypt memory: %w", err)
    }
    // "v1:" prefix (konzistentni s credential encryption pattern)
    return os.WriteFile(path, []byte("v1:"+base64.StdEncoding.EncodeToString(encrypted)), 0644)
}
```

### 15.4 Memory Export/Import

Pro agent onboarding, knowledge transfer:

```
POST /api/v1/agents/{id}/memory/export
  → encrypted ZIP soubor s vsemi memory soubory

POST /api/v1/agents/{id}/memory/import
  Body: multipart/form-data (encrypted ZIP)
  → naimportuje memory do /output/{agent}/.memory/
```

---

## 16. FAZOVANI IMPLEMENTACE

### Phase 1: MVP Memory (3-5 dnu effort)

| # | Feature | Effort | Stav | Popis |
|---|---|---|---|---|
| 1 | Memory file structure | 0.5d | ✅ HOTOVO | `.memory/AGENT.md` + `daily/`, orchestrator cte z kontejneru |
| 2 | Sidecar memory endpoints (search/status/reindex) | 1d | ✅ HOTOVO | `POST /memory/search`, `GET /memory/status`, `POST /memory/reindex` |
| 3 | SQLite FTS5 index + chunker | 1.5d | ✅ HOTOVO | `internal/memory/` — engine, chunker, index, BM25 search |
| 4 | System prompt injection | 0.5d | ✅ HOTOVO | `buildMemoryContext` + `buildMemoryInstructions` v orchestratoru |
| 5 | Agent model: memory_config | 0.5d | ✅ HOTOVO | DB migration 3, `memory_enabled` full flow |
| 6 | Sidecar stdin object format | 0.5d | ✅ HOTOVO | `{credentials, memory}` s backwards-compat pro array |
| 7 | Sidecar write/read endpoints | 1d | ❌ TODO | `POST /memory/write`, `GET /memory/read` |
| 8 | MCP tools (memory_write/read/search) | 1d | ❌ TODO | MCP skill definice + sidecar tool handlery |
| 9 | REST API: memory endpoints | 0.5d | ❌ TODO | `/api/v1/agents/{id}/memory/*` pro UI |
| 10 | File watcher + auto-reindex | 0.5d | ❌ TODO | fsnotify, debounced reindex |
| 11 | Input validace/sanitizace/rate limiting | 0.5d | ❌ TODO | Size limity, sanitization, rate limit na sidecar |

**Vysledek po dokonceni:** Agent si pamatuje across sessions. BM25 search. Zero external deps.
Zero cost. Secure by default.

### Phase 2: Vector Search + Session Memory (5-7 dnu)

| # | Feature | Effort | Popis |
|---|---|---|---|
| 7 | sqlite-vec extension | 2d | Vector table, cosine similarity queries |
| 8 | Hybrid BM25 + vector search | 1.5d | Weighted merge, configurable weights |
| 9 | Local GGUF embedding model | 1d | V agent runtime image (~600MB increase) |
| 10 | Embedding cache | 0.5d | Per-chunk hash, skip unchanged |
| 11 | Temporal decay + MMR | 1d | Configurable halflife, diversity re-ranking |
| 12 | Session transcript indexing | 1d | Opt-in, delta thresholds, async |
| 13 | Memory flush (API-direct mode) | 1d | Presna token detekce, silent flush turn |

### Phase 2B: Crew Memory (3-5 dnu)

| # | Feature | Effort | Popis |
|---|---|---|---|
| 14 | Crew shared memory structure | 1.5d | `/output/.crew-memory/`, CREW.md + topics/ |
| 15 | Sidecar `/memory/crew` endpoint | 1d | Read-only pro agenty, write pro lead |
| 16 | Auto-write mission decisions | 1d | Lead sumarizuje po mission completion |
| 17 | Cross-agent memory search (lead) | 1d | Sidecar aggregates across agents |

### Phase 3: Enterprise Memory (5-10 dnu)

| # | Feature | Effort | Popis |
|---|---|---|---|
| 18 | Memory encryption at rest | 2d | AES-256-GCM, reuse ENCRYPTION_KEY |
| 19 | Workspace memory (Coordinator) | 2d | crewshipd manages, not sidecar |
| 20 | LLM-driven compaction | 2d | Daily logs → AGENT.md sumarizace |
| 21 | Memory export/import | 1d | Encrypted ZIP, agent onboarding |
| 22 | Memory analytics | 1d | Dashboard widget, recall quality |

---

## 17. KONFIGURACE

### 17.1 Agent-level (memory_config JSON)

```json
{
  "max_size_mb": 10,
  "daily_log_max_kb": 100,
  "search_enabled": true,
  "search_backend": "fts5",
  "vector_enabled": false,
  "vector_provider": "local",
  "vector_model": "embeddinggemma-300m-qat-Q8_0.gguf",
  "temporal_decay_enabled": false,
  "temporal_decay_halflife_days": 30,
  "mmr_enabled": false,
  "mmr_lambda": 0.7,
  "session_memory_enabled": false,
  "auto_load_days": 2,
  "compaction_enabled": false,
  "compaction_after_days": 30
}
```

### 17.2 Crew-level (v Crew modelu)

```json
{
  "shared_memory_enabled": false,
  "shared_memory_max_mb": 50,
  "lead_can_read_agent_memory": true
}
```

### 17.3 Workspace-level (v Workspace modelu)

```json
{
  "workspace_memory_enabled": false,
  "memory_encryption_at_rest": false
}
```

---

## 18. OTEVRENE OTAZKY

### Rozhodnute v teto verzi

1. **Kde bezi memory engine?** → crewship-sidecar (konzistentni s ADR-014)
2. **File format?** → Markdown (source of truth) + SQLite FTS5 (derived index)
3. **Phase 1 search?** → BM25 only (zero cost, zero deps)
4. **Memory flush MVP?** → System prompt instrukce (ne auto-flush — vyhnuti se OpenClaw bugum)
5. **Crew shared memory pristup?** → Pres sidecar HTTP API (ne filesystem — Landlock compatible)
6. **Memory structure?** → 4 vrstvy mapovane na 4 organizacni vrstvy

### Stale otevrene

1. **Memory size limity** — 10MB per agent dostatecne? Enterprise muze potrebovat vic.
2. **Memory compaction model** — Jaky LLM pouzit pro sumarizaci starych logu? Haiku/GPT-4o-mini?
3. **Embedding model** — Jaky GGUF model pro local embeddings? embeddinggemma-300m?
4. **Memory backup** — Automaticky backup memory souboru pred compaction?
5. **Memory versioning** — Git-like history pro AGENT.md? (rollback po memory poisoning)
6. **Memory sharing across workspaces** — Phase 3+ pro Crewship Connect?
7. **Memory UI** — Jak zobrazit memory v UI? Read-only viewer? Editovatelne?
8. **Memory migration** — Co pri upgrade agent runtime image? Index rebuild automaticky?
9. **sqlite-vec pure Go** — Je sqlite-vec kompatibilni s `modernc.org/sqlite`? Overit.
10. **Chunk overlap** — 80 chars dostatecne? OpenClaw pouziva 80 tokenu (~320 chars).

---

## 19. REFERENCE

### Externi zdroje

| Zdroj | URL | Relevance |
|---|---|---|
| OpenClaw Memory docs | https://docs.openclaw.ai/concepts/memory | Primarni inspirace |
| OpenClaw Session Management | https://docs.openclaw.ai/reference/session-management-compaction | Compaction lifecycle |
| OpenClaw 5-Tier Memory PR | https://github.com/openclaw/openclaw/pull/17574 | Tiered memory navrh |
| OpenClaw Compaction Fix PR | https://github.com/openclaw/openclaw/pull/19291 | Post-compaction sync |
| Hindsight Architecture | https://arxiv.org/abs/2512.12818 | 4-network memory model |
| MemoriesDB | https://arxiv.org/abs/2511.06179 | Temporal-semantic-relational DB |
| Local-First RAG with SQLite | https://www.pingcap.com/blog/local-first-rag-using-sqlite-ai-agent-memory-openclaw | SQLite + FTS5 + sqlite-vec |
| Tiered Knowledge Management | LinkedIn (Brian Hammons, 2026-02-06) | 70% context reduction |
| Compact Memory Framework | https://github.com/scottfalconer/compact-memory | Context compression strategies |
| OpenClaw Graphiti Memory | https://github.com/clawdbrunner/openclaw-graphiti-memory | Knowledge graphs for multi-agent |
| SochDB | https://sochdb.dev/docs/ARCHITECTURE/ | Token-efficient vector DB |

### Interni docs (Crewship)

| Dokument | Relevantni sekce |
|---|---|
| AGENT-RUNTIME.md | 6.1 (storage model), 6.4 (memory persistence), 6A (MCP skills) |
| ORCHESTRATION.md | 3 (3-urovnova hierarchie), 5.1 (sidecar API), 5.7 (output compression) |
| SECURITY.md | 1.4 (trust zones), 3 (credentials), 6 (agent threats) |
| DATABASE.md | 1 (co v DB a co ne), 4 (Agent model), SQLite compatibility |
| CREW-EXECUTION.md | 2 (Mission), 4 (JSONL mirror), 6 (dev-test loop) |
| ADR.md | ADR-010 (Landlock), ADR-014 (sidecar=MCP gateway), ADR-015 (credential-less) |

---

*Tento dokument je zivy — bude se aktualizovat s kazdou novou iteraci implementace.
Viz AGENTS.md sekce "Change Documentation" pro pravidla aktualizace.*
