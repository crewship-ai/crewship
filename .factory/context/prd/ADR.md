# Crewship -- Architecture Decision Records (ADR)

**Datum:** 2026-02-17
**Status:** Zivý dokument — nové ADR se přidávají průběžně.

---

## ADR-001 v2: Loopback HTTP Sidecar pro delegační protokol

**Status:** Accepted (v2 — nahrazuje původní named pipe rozhodnutí)
**Datum:** 2026-02-15 (v2 update)
**Kontext:** Lead/Coordinator agenti potřebují posílat delegační příkazy
orchestratoru. V1 navrhoval named pipe, ale analýza odhalila zásadní problém:
CLI tools (Claude Code, Codex, OpenCode) NATIVNĚ NEPODPORUJÍ zápis do named pipe.
Agent by musel být instruován přes system prompt → závislost na spolehlivosti LLM.

**Problém s named pipe (v1):**
- CLI tools neví o pipe — musíte spoléhat na LLM instrukce
- LLM může ignorovat instrukci, zapomenout formát, hallucinovat JSON
- Bidirectional pipe (2 FIFO) = komplexní asynchronní multiplexing
- Docker Desktop na macOS mountuje pipes přes FUSE/gRPC — nespolehlivé

**Rozhodnutí (v2):** Loopback HTTP sidecar (`crewship-sidecar`).
- Go binary (~5MB) běžící v každém crew kontejneru na `localhost:9119`
- Standardní REST API: POST /assign, POST /ask, GET /results
- CLI tools delegují přes `curl` (built-in bash tool, standardní)
- API-direct runtime prirazuje přes nativní HTTP call
- Sidecar drží persistentní spojení s crewshipd (gRPC/WebSocket)

**Proč HTTP a ne pipe:**
- CLI tools UMÍ curl — je to standardní tool v každém coding agentu
- HTTP je debuggovatelný (curl, Postman, tcpdump)
- Funguje IDENTICKY pro CLI tools i vlastní crewship-agent runtime
- V K8s: sidecar je nativní pattern (sdílený Pod, localhost network)
- Žádná závislost na LLM spolehlivosti pro machine protocol

**Důsledky:**
- (+) Spolehlivější než pipe (standardní HTTP, žádná LLM závislost)
- (+) Unified interface pro CLI i API-direct mode
- (+) Sidecar validuje příkazy lokálně (RBAC, circuit breaker)
- (+) K8s-native pattern (sidecar container v Pod)
- (-) Extra proces v kontejneru (~5MB RAM)
- (-) Port 9119 musí být volný (konfigurovatelný)
- (-) Sidecar lifecycle management (healthcheck, restart)

**Inspirace:**
- Docker cagent (2026): multi-agent orchestrace přes HTTP/gRPC
- Dapr sidecar: application-level networking přes localhost
- K8s native sidecars (stable v1.33): restartable init containers

---

## ADR-002: NATS JetStream odložen na Phase 3

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** V diskuzi o event-driven orchestraci byl navržen NATS JetStream
jako message broker pro assignments a crew orchestraci.

**Problém:**
- crewshipd je single-process Go service s goroutinami
- Delegace Lead→Agent je Docker exec ve stejném kontejneru
- bbolt slouží jako WAL pro state persistence
- Go channels + goroutiny = in-process message passing
- NATS přidává: extra service dependency, failure modes, latence (NATS roundtrip)

**Rozhodnutí:** Go channels pro MVP. NATS JetStream až při multi-node (Phase 3).

**Kdy přidat NATS:**
1. crewshipd potřebuje horizontální škálování (víc instancí)
2. Multi-node cluster (agenti na různých strojích)
3. Enterprise zákazník vyžaduje HA (high availability)

**Důsledky:**
- (+) Jednodušší MVP — žádná extra dependency
- (+) Nižší latence (in-process vs network roundtrip)
- (+) Méně failure modes (méně co se může rozbít)
- (-) Single-node limit — nelze škálovat horizontálně
- (-) Při přechodu na NATS bude nutné refactorovat messaging layer

**Alternativy zvažované:**
1. NATS embedded v crewshipd (zamítnuto — stále zbytečná komplexita pro MVP)
2. Redis Pub/Sub (zamítnuto — další dependency, NATS je lepší pro JetStream persistence)
3. PostgreSQL LISTEN/NOTIFY (zváženo pro Phase 2 WS broadcast, ne pro assignments)

---

## ADR-003: gVisor jako optional runtime

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Pro bezpečnost agentních kontejnerů byla diskutována
volba container runtime: runc (standard), runsc (gVisor), Firecracker, Kata.

**Analýza:**

| Runtime | Izolace | Performance overhead | Setup |
|---|---|---|---|
| runc | namespaces + cgroups | 0% | default |
| runsc (gVisor) | syscall interception | 5-50% (I/O heavy) | instalace na host |
| Firecracker | microVM | ~5%, boot 125ms | komplexní |
| Kata Containers | QEMU VM | ~10%, boot 500ms | komplexní |

**Rozhodnutí:** Default `runc`, optional `runsc` přes env var `CREWSHIP_RUNTIME`.

**Zdůvodnění:**
- gVisor na macOS nepodporován (vývojáři na Mac)
- Pro self-hosted (důvěryhodní agenti, vlastní klíče) stačí standard Docker isolation
- gVisor overhead 5-50% je problém pro I/O-heavy CLI nástroje (git, npm, Claude Code)
- Multi-tenant SaaS s nedůvěryhodnými agenty = gVisor oprávněný

**Důsledky:**
- (+) MVP bez overhead — standard Docker výkon
- (+) Enterprise option pro multi-tenant bezpečnost
- (-) Dva testovací matrixy (runc + runsc)
- (-) gVisor nepodporuje všechny syscally — některé CLI tools mohou selhat

**Konfigurace:**
```bash
CREWSHIP_RUNTIME=runc    # default
CREWSHIP_RUNTIME=runsc   # optional, multi-tenant SaaS
```

---

## ADR-004: Lead modes (active/passive)

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Lead běží po celou dobu Mission (long-running).
To je flexibilní ale drahé — Lead konzumuje LLM tokeny po celou dobu.

**Problém:**
- Active lead: 5 delegací = minimálně 6 LLM calls (1 init + 5 responses)
- Každý call = plný context (system prompt + konverzace + agent výsledky)
- Pro rutinní úkoly (denní report) je to zbytečně drahé

**Rozhodnutí:** Dva lead modes — uživatel volí per-lead konfigurací.

**Active mode (default):**
- Lead běží celou dobu, rozhoduje v real-time
- Může měnit strategii za běhu (jiný agent, jiný přístup)
- Dražší, flexibilnější
- Use case: složité úkoly, debugging, iterativní práce

**Passive mode:**
- Lead se spustí 2x: init (task breakdown → task plan) + finalize (agregace)
- Mezi tím crewshipd orchestruje agenty deterministicky dle task plan
- Levnější (2 LLM calls místo N), předvídatelné
- Use case: rutinní úkoly, reporty, sběr dat

**Důsledky:**
- (+) Uživatel má kontrolu nad náklady
- (+) Passive mode je deterministický — snazší debugging
- (-) Passive mode nemůže reagovat na neočekávané výsledky
- (-) Dva mody = víc kódu, víc testů

---

## ADR-005: Agent output compression

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Agent vrací výsledek Leadovi. Pokud agent output = 50k tokens,
Lead musí celý output přečíst v LLM context window → drahé.

**Problém:**
- 5 agentů × 50k tokens = 250k tokens v Lead contextu
- Claude 200k context: potenciální overflow
- Náklady: 250k input tokens = ~$0.75 per Lead turn

**Rozhodnutí:** crewshipd automaticky sumarizuje agent output před předáním Leadovi.

**Mechanismus:**
1. Agent output > `agent_output_max_tokens` (default 2000)?
2. Ano → crewshipd zavolá levný LLM (Haiku/GPT-4o-mini) pro sumarizaci
3. Lead dostane: summary (2k tokens) + file reference na plný output
4. Ne → Lead dostane plný output přímo

**Trade-off:**
- Sumarizace 1 agent výsledku: ~$0.002 (Haiku)
- Bez sumarizace, 50k tokens v Lead contextu: ~$0.15 per call
- 5 agentů: $0.01 sumarizace vs $0.75 plný context → 75x levnější

**Důsledky:**
- (+) Dramatická úspora tokenů (Lead context zůstává malý)
- (+) Lead nepotřebuje 200k context window model
- (-) Ztráta detailu při sumarizaci (mitigováno file referencí)
- (-) Extra LLM call per agent (mitigováno levným modelem)
- (-) Konfigurační otázka: jaký model pro sumarizaci?

---

## ADR-006: Circuit breaker pro assignments

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Agent může opakovaně selhávat (bug, špatný prompt, nedostupná služba).
Bez circuit breakeru Lead pořád zkouší prirazovat → plýtvá tokeny a časem.

**Rozhodnutí:** Circuit breaker pattern pro každého agenta.

**Flow:**
```
Agent fail → increment counter → counter >= 3?
  NO → auto-retry s exponential backoff (1s, 2s, 4s)
  YES → circuit OPEN → eskalace na Leada
        → po 5 min cooldown → circuit HALF → 1 zkušební assignments
        → úspěch? → circuit CLOSED
        → fail? → circuit OPEN → další cooldown
```

**Backpressure:**
- Max queue depth per Lead: 10 čekajících delegací
- Queue full → Lead dostane error → může čekat nebo informovat uživatele

**Důsledky:**
- (+) Zabraňuje plýtvání tokeny na mrtvého agenta
- (+) Lead může reagovat (jiný agent, jiný přístup)
- (+) Standardní distributed systems pattern
- (-) Trochu víc komplexity v AssignmentEngine
- (-) Cooldown = čekání (5 min default, konfigurovatelné)

---

## ADR-007: Coordinator bez kontejneru (MVP)

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Virtual Coordinator koordinuje cross-crew úkoly. Otázka:
potřebuje vlastní Docker kontejner?

**Rozhodnutí:** MVP Coordinator běží jako přímý LLM API call v crewshipd (Go).
Žádný Docker kontejner.

**Zdůvodnění:**
- Coordinator jen přemýšlí a prirazuje — nepíše kód, nepotřebuje tools
- Menší latence (žádný Docker exec overhead)
- Jednodušší credentials (org-level LLM key)

**Rizika a mitigace:**
- LLM API timeout blokuje goroutinu → `context.WithTimeout` (30s)
- Coordinator panic → `recover()` v goroutině, neshodí celý process
- Bez tools → postačující pro MVP (assignments + agregace)

**Phase 3:** Coordinator dostane vlastní lightweight kontejner pokud bude
potřebovat tools (web search, file access, code execution).

**Důsledky:**
- (+) Jednodušší implementace (žádný container management)
- (+) Nižší resource overhead
- (-) Coordinator nemůže používat tools (mitigováno: prirazuje na leady co tools mají)
- (-) Žádná izolace — Coordinator goroutina běží v crewshipd procesu

---

## ADR-008: Cost visibility pro Crew operace

**Status:** Proposed (Phase 2B)
**Datum:** 2026-02-15
**Kontext:** Crew operace (Lead + Agents) konzumují víc LLM tokenů
než přímý chat s agentem. Uživatel (BYOK) potřebuje vědět kolik to stojí.

**Problém:**
- Lead + 5 Agents = 6+ LLM calls per Crew operace
- Active Lead = N LLM calls (kde N = počet agent výsledků + init)
- Uživatel netuší, že Lead konzumuje 3-5x víc tokenů než přímý chat

**Rozhodnutí:** Implementovat cost estimation a tracking.

**Plánované features:**
1. **Pre-execution estimate:** "Tato Crew operace bude stát přibližně $2.50"
   - Na základě: počet agentů × average output size × model pricing
2. **Real-time tracking:** Zobrazit aktuální token consumption v delegační timeline
3. **Per-crew budget:** Max $/tokens per mission (hard limit, crew se zastaví)
4. **Post-execution report:** Kolik stál každý agent, kolik lead, celkem

**Důsledky:**
- (+) Transparentnost nákladů pro uživatele
- (+) Budget limits zabraňují runaway costs
- (-) Pricing data se mění (nutné aktualizovat)
- (-) Odhad může být nepřesný (agent output variabilní)

---

---

## ADR-009: Dual Runtime Architecture (CLI + API-direct)

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** CLI-first architektura (Claude Code, Codex) má zásadní omezení:
žádná kontrola lifecycle, nepřesný token tracking, závislost na cizím stdout formátu,
300MB per agent (Node.js), nemožný přesný cost estimation.

**Výzkum (2026):**
- OpenCode (100k+ stars): Go-based, provider-agnostic, 75+ LLM providerů, MIT
- Anthropic Go SDK (`anthropics/anthropic-sdk-go`): oficiální, plný tool use + streaming
- Docker cagent: agent builder/runtime, YAML-driven, přímé LLM API volání

**Rozhodnutí:** Dual runtime — CLI mode + API-direct mode.

- **CLI mode:** Docker exec → CLI tool (Claude Code, OpenCode, Codex)
  - Pro: silný tool use, coding-heavy tasks, established tools
  - Delegace přes `curl localhost:9119/assign`

- **API-direct mode:** Docker exec → `crewship-agent` (vlastní Go binary, ~5MB)
  - Volá LLM API přímo přes oficiální SDK
  - Nativní tool use (file_read, file_write, bash, grep, web_search)
  - Delegace je LLM TOOL (function calling) → nativní HTTP na sidecar
  - Přesný token tracking z API response
  - 10MB vs 300MB memory footprint

**Fázování:**
- Phase 1: CLI_CLAUDE_CODE, CLI_OPENCODE jako default
- Phase 2: API_DIRECT jako alternativa
- Phase 3: API_DIRECT jako default, CLI jako "power adapter"

**Důsledky:**
- (+) Přesný cost tracking, lifecycle control, menší footprint
- (+) Delegace je nativní tool (ne LLM instrukce pro curl)
- (+) Jeden binary pro všechny LLM providery
- (-) Nutné implementovat tool use engine (~2000 řádků Go)
- (-) Dva runtime mody = víc kódu, víc testů

---

## ADR-010: Landlock per-agent filesystem izolace

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** 1 kontejner = 1 tým. Agenti sdílí filesystem. Agent pod
prompt injection může číst/mazat soubory jiných agentů v /workspace/.

**Výzkum:**
- Landlock LSM: Linux kernel >=5.13, per-process filesystem izolace, zero overhead
- `landrun` (github.com/Zouuup/landrun): user-friendly CLI wrapper
- Anthropic `sandbox-runtime`: používá `bubblewrap` na Linuxu
  - CVE-2025-66479: prázdný allowlist = žádná izolace → poučení: default DENY

**Rozhodnutí:** Landlock per-agent izolace uvnitř sdíleného kontejneru.

```
Agent "bob" vidí:
  /workspace/bob        → read-write (vlastní workspace)
  /output/bob           → read-write (vlastní output)
  /output               → read-only (sdílený, čtení výsledků)
  /workspace/alice      → DENY (jiný agent)
  /workspace/charlie    → DENY (jiný agent)
```

**Proč Landlock a ne:**
- gVisor: overkill (celý user-space kernel, 5-50% overhead)
- bubblewrap: extra dependency, Anthropic měl CVE
- Separate containers: resource waste, networking complexity

**Důsledky:**
- (+) Zero performance overhead (kernel-native)
- (+) Agent A nemůže sabotovat agenta B
- (+) Nevyžaduje root (unprivileged sandboxing)
- (-) Linux-only (macOS dev: feature flag CREWSHIP_LANDLOCK=false)
- (-) Vyžaduje kernel >=5.13 (Ubuntu 22.04+, ok pro produkci)

---

## ADR-011: Meilisearch pro conversation search

**Status:** Proposed (Phase 2)
**Datum:** 2026-02-15
**Kontext:** Konverzace jsou JSONL soubory na filesystému. Hledání
v konverzacích = grep přes soubory. Pro 10k+ sessions nescaluje.

**Rozhodnutí:** Meilisearch jako async search index.

```
JSONL append → crewshipd → async indexer → Meilisearch
                                              ↓
                                  UI: instant search (<10ms)
                                  across all conversations
```

**Proč Meilisearch:**
- Rust-based, <10ms latency, single binary (~50MB)
- Nativní JSONL import, typo tolerance, faceted search
- MIT licence, aktivní vývoj (2026)
- Conversational search (AI-powered, embeddings)

**Proč ne PostgreSQL FTS:**
- PostgreSQL FTS vyžaduje kopírování dat z JSONL do PG
- Meilisearch je purpose-built pro full-text search

**Důsledky:**
- (+) Instant search across všech konverzací
- (+) Typo tolerance, faceted search per tým/agent
- (-) Extra service (docker-compose, K8s Deployment)
- (-) Async indexing = mírné zpoždění oproti real-time

---

## ADR-012: Trace ID across delegací

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Mission zahrnuje Lead + N Agents. Debugging vyžaduje
korelaci sessions, delegačních logů, a JSONL konverzací across agentů.

**Rozhodnutí:** Každá mission dostane unikátní `trace_id`.
- Přidán do: AssignmentLog, JSONL metadata, Meilisearch index
- UI: timeline view celé mission s proklikem do session

---

## ADR-013: Credential decryption v Go service

**Status:** Proposed
**Datum:** 2026-02-15
**Kontext:** Aktuálně Next.js dešifruje credentials a posílá plaintext
přes Unix socket do Go. Plaintext existuje v paměti DVOU procesů.
Node.js GC negarantuje okamžité smazání z paměti.

**Rozhodnutí:** Go service dostane vlastní `ENCRYPTION_KEY` a dešifruje sám.
Next.js posílá jen encrypted blob + credential ID přes Unix socket.
Plaintext existuje jen v Go paměti po dobu Docker exec.

**Důsledky:**
- (+) Menší attack surface (1 proces místo 2 s plaintextem)
- (+) Go má deterministické mazání paměti (zeroing)
- (-) Go service potřebuje ENCRYPTION_KEY (extra env var)
- (-) Duplikace decrypt logiky (TypeScript + Go)

---

---

## ADR-014: Sidecar jako MCP Gateway uvnitr kontejneru

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Skill system potrebuje zpusob jak agent pouziva externi nastroje
(GitHub, Slack, DB, web search). MCP (Model Context Protocol) je standard
od Anthropic (spec 2025-11-25) pro tool/resource sharing. Otazka: kde bezi
MCP servery a jak se k nim agent pripojuje?

**Vyzkum:**
- Docker MCP Gateway (leden 2026): centralizovany proxy pro MCP servery,
  spravuje credentials, loguje cally, RBAC. Bezi jako externi kontejner.
- Docker MCP Catalog: predpripravene MCP servery v Docker images.
- Anthropic engineering blog: tool definition bloat problem (50+ tools = 100k tokenu).
- arxiv CA-MCP paper: shared context store pro multi-agent MCP.

**Rozhodnuti:** crewship-sidecar se rozsiruije o MCP Gateway roli.

Sidecar uz bezi v kazdem crew kontejneru (ADR-001 v2). Pridame:
1. Sidecar SPOUSTI MCP servery (stdio) pro skills prirazene agentu
2. Sidecar INJEKTUJE credentials do MCP serveru (ne do agenta!)
3. Sidecar PROXY MCP tool cally (RBAC check, audit log, rate limit)
4. Sidecar vystavuje search_tools meta-tool pro on-demand discovery (ADR-016)

**Proc uvnitr kontejneru (ne externi Docker MCP Gateway):**
- Latence: stdio = ~0ms, HTTP k externimu = 5-50ms per tool call
- Izolace: MCP server nemuze pristupovat k jinym tymum
- Jednoduchost: jeden kontejner = vsechno
- K8s-ready: sidecar je nativni pattern

**Proc ne kazdy MCP server jako externi kontejner:**
- Overhead: 50MB+ per MCP kontejner × 10 skills = 500MB extra
- Networking: inter-container latence, DNS resolution
- Phase 3: externi MCP servery pro sdilene use cases (org-wide DB)

**Dsledky:**
- (+) Skill = MCP Server wrapper — standardni, kompatibilni s ekosystemem
- (+) Agent NEMA tool credentials — security (viz ADR-015)
- (+) Per-tool-call audit trail s credential_id
- (+) Sidecar uz existuje — rozsirujeme, ne pridavame novy komponent
- (-) Sidecar complexity roste (assignments + MCP gateway)
- (-) MCP servery konzumuji pamet v kontejneru (~2MB per server)
- (-) stdio transport neumoznuje sdileni MCP serveru across kontejnery

---

## ADR-015: Credential-less Agent (agent nema API klice)

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** V aktualni architekture agent dostava API klice jako env vars
(GITHUB_TOKEN, SERP_API_KEY). Prompt injection muze extrahovat klice
z agentova kontextu/pameti. Gravitee "State of AI Agent Security 2026"
ukazuje ze 80.9% firem deployuje agenty ale jen 14.4% ma plny security approval.

**Vyzkum (stav prumslu 2026):**
- Aembit: "secretless" agent auth — ephemeral tokens, per-request injection
- Cerbos: MCP + Zero Trust — agent = independent identity, on-behalf-of tokens
- OASIS AAM Framework: agent identity, least privilege, credential rotation
- Docker MCP Gateway: gateway drzi secrets, MCP server je dostane az pri runtime

**Rozhodnuti:** Rozdeleni credentials na DVE kategorie:

1. **LLM API klice** (ANTHROPIC_API_KEY):
   - Phase 1 (CLI mode): MUSI byt env var (CLI tools vyzaduji)
   - Phase 2 (API-direct): sidecar drzi, crewship-agent ziska per-request
   - Klic existuje v pameti agenta JEN po dobu LLM API callu

2. **Tool credentials** (GITHUB_TOKEN, SLACK_TOKEN, DB_URL):
   - Phase 1 i 2: agent je NIKDY nedostane
   - Sidecar injektuje do MCP serveru pri jeho spusteni
   - Agent vola MCP tool → sidecar proxy → MCP server (s credentials) → API

**Dsledky:**
- (+) Prompt injection NEMUZE extrahovat tool credentials
- (+) Credential rotace bez restartu agenta
- (+) Per-call audit trail (kdo, co, s jakym klicem)
- (+) Multi-key failover v sidecar (transparentni pro agenta)
- (-) CLI tools stale vyzaduji LLM klic jako env var (Phase 1 limit)
- (-) Sidecar musi drzet desifrovane klice v pameti (Go zeroing mitiguje)

---

## ADR-016: Tool Search pro on-demand tool discovery

**Status:** Accepted
**Datum:** 2026-02-15
**Kontext:** Agent s 5 skills × 10 toolu = 50 tool definic. Kazda definice
~500 tokenu. 50 toolu = 25k tokenu PRED prvni otazkou. S 10+ skills
context window exploduje a agent zpomaluje.

**Vyzkum:**
- Anthropic "Tool Search Tool" beta (leden 2026): meta-tool search_tools(query),
  tool definice se nacitaji on-demand, `defer_loading: true` flag
- Anthropic "Programmatic Tool Calling": agent generuje KOD ktery vola
  tooly, 1 code block = N tool callu, vyrazne mene tokenu
- Docker MCP Gateway: `mcp-find` primordial tool pro dynamic discovery

**Rozhodnuti:** Sidecar vystavuje search_tools meta-tool.

Agent pri startu dostane JEN:
- search_tools (meta-tool, vzdy)
- delegate (pokud lead)
- tools ze skills s defer_loading=false (kriticke, vzdy nactene)

Vsechny ostatni tools se nacitaji on-demand pres search_tools(query).
Sidecar ma cached index vsech tool definic ze vsech MCP serveru.

**Dsledky:**
- (+) Konstantni pocet tokenu pri startu (3-5 tools misto 50+)
- (+) Agent nacita jen tools ktere potrebuje
- (+) Skalovatelne na 100+ toolu bez context window problemu
- (-) Extra LLM call pro search pred prvnim pouzitim nastroje
- (-) LLM musi umet pouzit search_tools (system prompt instrukce)

**Inspirace:** Anthropic defer_loading, Docker mcp-find, CrewAI tool routing.

---

## ADR-017: Sandbox Runtime (srt) pro MCP servery uvnitr kontejneru

**Status:** Accepted
**Datum:** 2026-02-16
**Kontext:** Sidecar spousti MCP servery jako stdio procesy UVNITR crew
kontejneru (ADR-014). MCP server pro GitHub bezi ve stejnem FS namespace
jako agent a ostatni MCP servery. Pokud je MCP server bugged nebo exploited,
muze cist /output/ jinych agentu, pristupovat k jinym MCP serverum, nebo
volat libovolne externi API.

OWASP MCP Top 10 (unor 2026): tool poisoning, command injection, privilege
escalation. 53% MCP serveru uklada klice v plaintextu (safepasswordgenerator.net
studie). Anthropic vydal sandbox-runtime (`srt`, @anthropic-ai/sandbox-runtime)
— lightweight OS-level sandboxing bez kontejnerizace, pouziva bubblewrap (Linux)
a sandbox-exec (macOS).

**Vyzkum:**
- Anthropic sandbox-runtime (v0.0.37, unor 2026): FS deny-read + allow-write
  patterny, network allow-only domeny pres HTTP/SOCKS5 proxy, Unix socket
  blocking pres seccomp. Primo navrzeno pro sandboxovani MCP serveru.
- Pouziti: `srt npx @modelcontextprotocol/server-github` s konfiguraci
  v srt-settings.json.
- Funguje uvnitr Docker kontejneru s `enableWeakerNestedSandbox: true`
  (bubblewrap nepotrebuje plne namespaces kdyz uz je v Docker).

**Rozhodnuti:** Sidecar wrapne kazdy MCP server spusteni pres `srt`.

Per-skill srt-settings.json (generovany sidecar z Skill definice):
```json
{
  "filesystem": {
    "denyRead": ["/output", "/workspace/other-agents"],
    "allowWrite": ["/tmp"],
    "denyWrite": ["/workspace", "/output", "/etc"]
  },
  "network": {
    "allowedDomains": ["api.github.com", "*.github.com"]
  }
}
```

Sidecar generuje config z:
- `Skill.allowed_domains` → network.allowedDomains
- Hardcoded deny patterns → denyRead /output, denyWrite /workspace
- Skill.dependencies → povoleni pro instalacni cesty

`srt` se nainstaluje do agent-runtime Docker image:
`RUN npm install -g @anthropic-ai/sandbox-runtime`

**Dsledky:**
- (+) MCP server pro GitHub NEMUZE volat Slack API
- (+) MCP server NEMUZE cist /output/ (agent vysledky)
- (+) Double sandboxing: Docker (kontejner) + srt (MCP server proces)
- (+) Kompatibilni s Anthropic ekosystemem (srt je jejich tool)
- (-) `enableWeakerNestedSandbox: true` oslabuje bubblewrap izolaci
  (ale Docker uz izoluje na vyssi urovni — defense in depth)
- (-) Extra overhead pri spusteni MCP serveru (~50ms)
- (-) srt je beta (v0.0.37) — API se muze zmenit

---

## ADR-018: Claude Agent Teams jako optional mission mode

**Status:** Proposed
**Datum:** 2026-02-16
**Kontext:** Anthropic vydal Claude Code Agent Teams (Opus 4.6, 5.2.2026).
Experimentalni feature ktera umoznuje vice Claude Code instanci spolupracovat
na sdilenem codebase. Lead session koordinuje, teammates pracuji nezavisle,
sdili task list a komunikuji pres mailbox (peer-to-peer).

Crewship ma vlastni orchestracni system (Lead/Agent assignments pres sidecar,
ADR-001 v2). Otazka: pouzit nativni Agent Teams MISTO nasi orchestrace,
nebo jako alternativu?

**Vyzkum:**
- Agent Teams: lead + N teammates, kazdy = samostatny Claude Code proces
- Koordinace pres shared task list (~/.claude/tasks/), mailbox messaging
- Display: in-process nebo tmux split-pane
- Omezeni: no session resumption, no nested teams, lead je fixni
- Experimental: CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1

**Rozhodnuti:** Agent Teams jako ALTERNATIVNI mission mode, ne nahrada.

Novy `MissionMode`:
- `CREWSHIP_ORCHESTRATED` (default): nase Lead/Agent assignments pres sidecar
- `CLAUDE_AGENT_TEAMS` (optional): nativni Claude Code Agent Teams

Pouzitelny JEN kdyz vsichni agenti v crew pouzivaji CLI_CLAUDE_CODE runtime.
crewshipd spusti 1 Docker exec s Agent Teams flagy misto N separatnich exec callu.

```
docker exec -e ANTHROPIC_API_KEY=... \
  -e CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 \
  crew-container claude \
  --teammate-mode in-process \
  "Proved X, pouzij 3 teammates: ..."
```

Bezpecnost v kontejneru:
- Sit: vsichni teammates sdili Docker --internal network. BEZPECNE.
- FS: sdili /workspace/ a /output/ (jsou ve stejnem tymu, to je OK).
- LLM klice: zdedi env var z Docker exec (Phase 1 ADR-015).
- MCP: lead session vidi sidecar, muze prirazovat MCP tool cally.

**Dsledky:**
- (+) Nativni Claude lateral communication (teammates si mohou psat)
- (+) Mene overhead nez nase assignments pro Claude-only crew
- (+) Anthropic optimalizuje Agent Teams — budoucnost CLI mode
- (-) Funguje JEN s Claude Code (ne Codex, OpenCode, crewship-agent)
- (-) Experimental, API se muze zmenit
- (-) crewshipd ma mensi kontrolu nad task assignments
- (-) Cost tracking je slozitejsi (N procesu, kazdy vlastni context)

---

## ADR-019: Crewship Skill Hub (MCP Marketplace)

**Status:** Accepted
**Datum:** 2026-02-16
**Kontext:** MCP ekosystem (2026) ma 20,000+ serveru, ale bezpecnost je
katastrofalni. Existujici katalogy (Docker MCP Catalog 270+, GitHub MCP Registry,
LobeHub, mcpmarket.com) resi discovery, ale NE hlubsi bezpecnostni verifikaci.
Uzivatel nema jak rozlisit bezpecny MCP server od potencialne skodliveho.

Crewship potrebuje skill system kde uzivatel muze snadno najit, nainstalovat
a pouzivat MCP servery s jistotou ze prosly bezpecnostnim auditem.

**Vyzkum (stav prumyslu 2026):**
- Docker MCP Catalog: publisher verification, SBOM, containerized. Ale:
  zadne security scoring, zadne rating/reviews, zadna monetizace.
- GitHub MCP Registry: one-click install pro VS Code. Ale: zadny security
  audit, zadne rating, zavisle na GitHub stars jako proxy pro kvalitu.
- LobeHub MCP Marketplace: ma rating, ale vetsina serveru ma spatne hodnoceni,
  zadny security audit.
- OWASP MCP Top 10 (02/2026): tool poisoning je #1 hrozba.
- Stacklok: Sigstore + GitHub Attestations pro source verifikaci.
- Endor Labs: AppSec pro MCP — command injection, path traversal, SSRF.

**Rozhodnuti:** Crewship Skill Hub — vlastni kuratorovany marketplace s 3 tiers:

1. **Official Skills**: Crewship vytvari, udrzuje, verifikuje. Badge "Official".
2. **Community Skills**: Kdokoliv submitne (GitHub repo URL), projde review
   procesem (automated pipeline + manual review). Badge "Verified" nebo "Unverified".
3. **Private Skills**: Org-specific, interni MCP servery. Badge "Private".
   Neprochazeji marketplace pipeline (org si resi bezpecnost sam).

Integrace s existujicim Skill modelem (rozsireni, ne nahrada):
- Nova pole: verification, security_score, downloads, rating_avg, tags,
  oci_image, allowed_domains, pricing_tier, author_id
- Novy model: SkillReview (rating + recenze)
- Nove enumy: VerificationStatus, SkillPricing

Docker MCP Catalog jako **upstream zdroj**: importujeme verified Docker MCP
images a re-scanujeme nasim pipeline.

**Dsledky:**
- (+) Uzivatel vi ze kazdy VERIFIED skill prosel 6-krokovym auditem
- (+) Plug-and-play: browse → install → credentials → hotovo
- (+) Revenue share model motivuje autory publikovat kvalitni skills
- (+) Private tier umoznuje enterprise custom tooling
- (+) Competitive advantage: zadna jina platforma nema takto hluboky audit
- (-) Provoz security pipeline vyzaduje CI/CD infrastrukturu
- (-) Manual review pro community submissions = bottleneck
- (-) OCI registry hosting = extra provozni naklady
- (-) Phase 3+ implementation (7-9 mesicu)

---

## ADR-020: Skill Security Pipeline

**Status:** Accepted
**Datum:** 2026-02-16
**Kontext:** MCP servery mohou obsahovat: tool poisoning (skryte instrukce
v tool descriptions), command injection (nezabezpecene vstupy), exfiltrace
dat (undeclared network calls), supply chain utoky (kompromitovane dependencies).

Pred tim nez skill ziska status VERIFIED, musi projit automatizovanym
bezpecnostnim pipeline.

**Rozhodnuti:** 6-krokovy security pipeline:

| Krok | Co | Automated? | Blocks? |
|---|---|---|---|
| 1. Source verification | Sigstore/GitHub attestations — overeni ze code pochazi od deklarovaneho autora | Ano | Ano |
| 2. Static analysis | Parsovani tool definic — hledani dangerous ops (raw SQL exec, rm -rf, shell exec bez omezeni, tool poisoning patterns) | Ano | Ano (critical) |
| 3. Dependency scan | SBOM generace (CycloneDX), CVE scan (Trivy/Grype), license check | Ano | Ano (critical CVE) |
| 4. Sandbox test run | Spusteni MCP serveru pres srt v izolaci, monitoring network + FS pristupu — overeni ze skill nepotrebuje vic nez deklaruje v allowed_domains | Ano | Ano (undeclared network) |
| 5. Manual review | Security tym zkontroluje tool definice, prompt fragmenty, flagged findings. Pro community povinne, pro official volitelne | Ne | Ano |
| 6. Continuous monitoring | Re-scan pri kazdem update verze. Auto-deprecation pri critical CVE v dependencies | Ano | Ne (notifikace) |

Vysledek: `security_score` (0-100) a `verification` status (VERIFIED/REJECTED).

security_score formula:
- Source verified: +20
- Zero critical CVEs: +20
- Zero high CVEs: +10
- Network matches declared domains: +20
- No dangerous tool patterns: +20
- License compatible: +10

**Dsledky:**
- (+) Systematicky audit pred kazdym VERIFIED statusem
- (+) Continuous monitoring zajisti ze skill zustane bezpecny
- (+) security_score dava uzivatelum jasny signal kvality
- (-) Pipeline bezi 5-15 minut (ne instant)
- (-) False positives mohou blokovat legitimni skills
- (-) Manual review je bottleneck pro community submissions
- (-) Vyžaduje CI/CD infrastrukturu (GitHub Actions / Coolify pipeline)

---

## ADR-021: Skill Distribution via OCI Images

**Status:** Accepted
**Datum:** 2026-02-16
**Kontext:** MCP servery se typicky instaluji pres `npx @scope/server-name`
nebo `pip install mcp-server-x`. To znamena: nestabilni dependencies, ruzne
verze runtime, nepredvidatelne build chyby. Kazdy crew kontejner by musel
mit vsechny runtime dependencies pred-instalovane.

Docker MCP Catalog (2026) uz distribuuje MCP servery jako container images.
OCI (Open Container Initiative) images jsou standard pro reprodukovatelnou
distribuci.

**Rozhodnuti:** OCI images jako preferovany distribution format pro marketplace
skills.

```
Official:    ghcr.io/crewship-ai/skills/{name}:{version}
Community:   ghcr.io/crewship-ai/community/{name}:{version}
Private:     {org-registry}/{name}:{version}
```

Image obsahuje: MCP server binary/script + vsechny dependencies.
Prebuildovany Crewship CI pipeline. Digest-verified pull (SHA256).

Sidecar stahne a spusti MCP server z OCI image. Image se cachuje
v crew kontejneru (Docker-in-Docker nebo bind-mounted socket).

**Fallback pro jednoduche skills:**
- `mcp_server_command` zustava pro Phase 1 a custom skills
- `oci_image` je preferovany pro marketplace skills
- Pokud oci_image existuje, pouzije se. Jinak mcp_server_command.

**Proc ne jen npm/pip command:**
- Reprodukovatelnost: dependencies prebaked, zadne "works on my machine"
- Integrita: SHA256 digest, tamper-proof
- Rychlost: cached image, zadna instalace pri kazdem spusteni
- Offline: image je v local cache po prvnim pull
- Kompatibilita: Docker MCP Catalog pouziva stejny format

**Dsledky:**
- (+) Reprodukovatelne prostredi (zadne build chyby v runtime)
- (+) Digest verifikace (SHA256, security pipeline overuje)
- (+) Kompatibilni s Docker MCP Catalog ekosystemem
- (+) Rychlejsi spusteni (cached image vs npm install)
- (-) Vetsi image size (~50-200MB per skill vs ~5MB npm package)
- (-) Nutnost buildovat a hostovat OCI images (CI/CD + registry)
- (-) Docker-in-Docker nebo bind-mounted socket v crew kontejneru
- (-) Phase 2B+ implementation

---

---

### ADR-022: SQLite jako default databaze
- **Status:** Proposed
- **Kontext:** Single binary distribuce vyzaduje zero-deps setup. PostgreSQL vyzaduje Docker nebo externi server.
- **Rozhodnuti:** SQLite jako default pro `crewship start`, PostgreSQL jako opt-in pro tymy/enterprise.
- **Prisma:** multi-provider (sqlite + postgresql), schema sdilene.
- **Dusledky:** Omezeni na SQLite typy (zadne UUID, JSONB), WAL mode pro concurrency, migracni tool.
- **Inspirace:** Gitea (50k+ stars) pouziva SQLite default, PostgreSQL opt-in.

### ADR-023: Single binary distribuce (Ollama model)
- **Status:** Proposed
- **Kontext:** OpenClaw ma slozitou instalaci (npm + config + messaging). Ollama ukazal, ze single binary je optimalni UX.
- **Rozhodnuti:** Go binary s embedded Next.js (embed.FS), SQLite, CLI (start/stop/status/logs).
- **Build:** GoReleaser (linux/darwin/windows, amd64/arm64), brew tap, curl installer.
- **Distribuce:** brew, curl, winget, scoop, Docker (fallback).
- **Dusledky:** Next.js musi byt static export (zadne SSR), API routes v Go serveru.

### ADR-024: Per-agent network control
- **Status:** Proposed
- **Kontext:** OpenClaw agenti maji full internet access bez omezeni. Bezpecnostni riziko.
- **Rozhodnuti:** Kazdy agent/tym ma konfigurovatelny sitovy pristup (internet on/off, whitelist, local network).
- **Implementace:** Docker network policies, UI klikaci konfigurace.
- **Dusledky:** Slozitejsi Docker network setup, nutnost custom bridge networks per agent.

### ADR-025: Skill sandbox enforcement
- **Status:** Proposed
- **Kontext:** OpenClaw ClawHub ma 20% malicious skills (zadny sandbox). Supply-chain utoky.
- **Rozhodnuti:** Kazdy skill deklaruje permissions (filesystem, network, secrets, shell). Docker vynucuje.
- **Format:** skill.yaml s permissions blokem.
- **Urovne:** OFFICIAL (rucne reviewed), VERIFIED (automated scan), COMMUNITY (sandbox-only).
- **Dusledky:** Skills bez permissions deklarace se nespusti. Zpetna kompatibilita s existujicimi skills.

### ADR-026: 3-tier monetizacni model
- **Status:** Proposed
- **Kontext:** Potrebujeme udrzitelny business model. GitLab (CE/EE), Gitea (free+paid) jako vzory.
- **Rozhodnuti:** Free (single binary, SQLite), Crew (cloud, $15-30/user), Enterprise (K8s, $50-100/user).
- **Implementace:** Feature flags v DB, env var CREWSHIP_TIER.
- **Dusledky:** Stejna codebase pro vsechny tiery, feature gating.

---

## Přehled všech ADR

| ID | Rozhodnutí | Status | Dopad |
|---|---|---|---|
| ADR-001 v2 | Loopback HTTP sidecar | Accepted | ORCHESTRATION.md 5.1-5.3, AGENT-RUNTIME.md, architecture.md |
| ADR-002 | NATS odložen na Phase 3 | Accepted | architecture.md (Messaging Architecture) |
| ADR-003 | gVisor optional runtime | Accepted | AGENT-RUNTIME.md 16, architecture.md |
| ADR-004 | Lead modes (active/passive) | Accepted | ORCHESTRATION.md 5.6 |
| ADR-005 | Agent output compression | Accepted | ORCHESTRATION.md 5.7 |
| ADR-006 | Circuit breaker | Accepted | ORCHESTRATION.md 5.8 |
| ADR-007 | Coordinator bez kontejneru | Accepted | ORCHESTRATION.md 5.2, AGENT-RUNTIME.md 15.2 |
| ADR-008 | Cost visibility | Proposed | ORCHESTRATION.md 10 (ORCH-21, ORCH-22) |
| ADR-009 | Dual runtime (CLI + API) | Accepted | ORCHESTRATION.md 5.9, AGENT-RUNTIME.md 1 |
| ADR-010 | Landlock per-agent | Accepted | AGENT-RUNTIME.md 16.2 |
| ADR-011 | Meilisearch search | Proposed | architecture.md (Conversation Search) |
| ADR-012 | Trace ID | Accepted | ORCHESTRATION.md 5.10, AssignmentLog |
| ADR-013 | Credential decrypt v Go | Proposed | SECURITY.md, architecture.md |
| ADR-014 | Sidecar = MCP Gateway | Accepted | AGENT-RUNTIME.md 6A, architecture.md, ORCHESTRATION.md 7.2 |
| ADR-015 | Credential-less agent | Accepted | AGENT-RUNTIME.md 3.3, 6A.6, architecture.md |
| ADR-016 | Tool search on-demand | Accepted | AGENT-RUNTIME.md 6A.4, architecture.md |
| ADR-017 | srt sandbox per-MCP-server | Accepted | AGENT-RUNTIME.md 6A, architecture.md |
| ADR-018 | Claude Agent Teams mode | Proposed | ORCHESTRATION.md, AGENT-RUNTIME.md |
| ADR-019 | Crewship Skill Hub (Marketplace) | Accepted | DATABASE.md, AGENT-RUNTIME.md 6A.10, architecture.md |
| ADR-020 | Skill Security Pipeline | Accepted | AGENT-RUNTIME.md 6A.10, architecture.md |
| ADR-021 | Skill Distribution via OCI | Accepted | AGENT-RUNTIME.md 6A.10, architecture.md |
| ADR-022 | SQLite jako default databaze | Proposed | DATABASE.md, architecture.md |
| ADR-023 | Single binary distribuce (Ollama model) | Proposed | architecture.md, DEPLOYMENT.md |
| ADR-024 | Per-agent network control | Proposed | SECURITY.md, AGENT-RUNTIME.md |
| ADR-025 | Skill sandbox enforcement | Proposed | SECURITY.md, AGENT-RUNTIME.md |
| ADR-026 | 3-tier monetizacni model | Proposed | business.md, DATABASE.md |

---

*Nová ADR se přidávají na konec tohoto dokumentu. Formát:
ADR-NNN, Status, Datum, Kontext, Rozhodnutí, Důsledky, Alternativy.*
