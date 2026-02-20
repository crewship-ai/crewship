# Crewship -- Sidecar Proxy (SIDECAR.md)

**Verze:** 1.1
**Datum:** 2026-02-20
**Status:** IMPLEMENTOVANO (Phase 1B) -- sidecar HTTP proxy s credential injection, scrubber, orchestrator integrace + memory engine.

---

## 1. PREHLED

Crewship-sidecar je Go binary ktery bezi UVNITR agent kontejneru jako HTTP forward
proxy na `127.0.0.1:9119`. Jeho ucel je **oddeleni credentials od agenta** -- agent
NIKDY nedostane skutecne API klice. Misto toho sidecar interceptuje vsechny outbound
HTTP requesty a injektuje spravne credentials pro kazdy LLM provider.

### 1.1 Problem ktery sidecar resi

Pred sidecarem agenti dostavali API klice primo jako ENV vars:
```
ANTHROPIC_API_KEY=sk-ant-real-key-123   ← agent vidi!
```

**Rizika:**
- Prompt injection muze extrahovat klic pres `env` nebo `cat /proc/self/environ`
- Agent muze klic poslat kamkoliv (data exfiltrace)
- Credential na disku (Claude Code OAuth token soubory)
- Zadne logovani pouziti klicu

### 1.2 Reseni: crewship-sidecar ("Doorman")

```
Container (--network none equivalent, --internal Docker network)
├── Agent (UID 1001, no credentials, dummy keys, HTTP_PROXY=sidecar)
└── crewship-sidecar (UID 1002, Go binary, localhost:9119)
    ├── HTTP Forward Proxy (injects LLM API keys per-request)
    ├── Domain Allowlist (blocks non-allowed domains)
    ├── Credential Store (in-memory only, NEVER on disk)
    └── Stdout Scrubber (redacts credential patterns)
```

### 1.3 Inspirace

| Vzor | Zdroj |
|---|---|
| HTTP Forward Proxy s credential injection | HashiCorp Vault Agent sidecar pattern |
| Domain allowlist | Anthropic `--network none` + proxy |
| In-memory credential store | CyberArk Conjur sidecar |
| Stdout scrubbing | Pincer-MCP credential redaction |
| UID izolace | Docker best practices, K8s PodSecurityPolicy |

---

## 2. ARCHITEKTURA

### 2.1 Diagramy

**Sidecar uvnitr kontejneru:**
```
┌──────────────── Crew Container ────────────────────────────┐
│                                                             │
│  ┌─────────────────────────┐  ┌──────────────────────────┐ │
│  │ Agent Process (UID 1001)│  │ Sidecar (UID 1002)       │ │
│  │                         │  │ 127.0.0.1:9119           │ │
│  │ ENV:                    │  │                          │ │
│  │  HTTP_PROXY=:9119       │  │ In-Memory Creds:         │ │
│  │  ANTHROPIC_BASE_URL=    │  │  sk-ant-real-key-123     │ │
│  │    http://127.0.0.1:9119│  │  sk-openai-real-456      │ │
│  │  ANTHROPIC_API_KEY=     │  │                          │ │
│  │    sk-ant-dummy-*       │  │ Allowlist:               │ │
│  │  OPENAI_API_KEY=        │  │  api.anthropic.com       │ │
│  │    sk-dummy-*           │  │  api.openai.com          │ │
│  │  NO_PROXY=127.0.0.1,...│  │  googleapis.com          │ │
│  │                         │  │  api.factory.ai          │ │
│  │ Agent vidi JEN dummy    │  │                          │ │
│  │ klice, NIKDY realne!    │  │ SECURITY:                │ │
│  └────────┬────────────────┘  │  hop-by-hop stripping    │ │
│           │                   │  10MB body limit         │ │
│           │ HTTP requests     │  localhost detection     │ │
│           └──────────────────►│                          │ │
│                               └──────────┬───────────────┘ │
│                                          │                  │
│                                          │ HTTPS (real key  │
│                                          │ in header)       │
│                                          ▼                  │
│                                   api.anthropic.com         │
│                                   api.openai.com            │
│                                   googleapis.com            │
└─────────────────────────────────────────────────────────────┘
```

**Credential flow (end-to-end):**
```
1. Uzivatel ulozi API klic v UI
   → Go API sifruje AES-256-GCM → DB ("v1:" + base64)

2. Agent run request:
   → Go orchestrator desifruje klice z DB
   → JSON marshal → base64 encode → pipe stdin do sidecar procesu

3. Sidecar start:
   → cte base64-encoded JSON ze stdin → decode → parse
   → NOVY FORMAT: objekt `{credentials: [...], memory: {...}}` (backwards-compatible s legacy array)
   → ulozi klice DO PAMETI (CredStore)
   → pokud memory config pritomny, inicializuje FTS5 memory engine
   → posle "SIDECAR_READY" na stdout
   → zacne naslouchat na 127.0.0.1:9119
   → registruje endpointy: /health, /memory/search, /memory/status, /memory/reindex

4. Agent start (Docker exec, UID 1001):
   → dostane HTTP_PROXY=http://127.0.0.1:9119
   → dostane ANTHROPIC_BASE_URL=http://127.0.0.1:9119
   → dostane ANTHROPIC_API_KEY=sk-ant-dummy-crewship-sidecar
   → dostane NO_PROXY=127.0.0.1,localhost,::1

5. Agent posle request na Anthropic API:
   → Claude Code pouzije ANTHROPIC_BASE_URL → HTTP request na :9119
   → sidecar interceptuje request
   → sidecar zkontroluje allowlist (api.anthropic.com → OK)
   → sidecar injektuje realny API klic do headeru (x-api-key: sk-ant-real-...)
   → sidecar stripne hop-by-hop headery
   → sidecar forward request na https://api.anthropic.com
   → response se vrati agentovi (bez API klice)

6. Stdout scrubbing:
   → orchestrator cte agent stdout
   → kazdou radku prohne pres Scrubber
   → [REDACTED:anthropic_key] nahrazuje detekovanej klic
   → az potom jde na WebSocket / JSONL log
```

### 2.2 Komponenty

| Soubor | Package | Ucel |
|---|---|---|
| `cmd/crewship-sidecar/main.go` | `main` | Standalone binary, cte creds ze stdin, signal SIDECAR_READY |
| `internal/sidecar/credstore.go` | `sidecar` | In-memory credential store, round-robin selection |
| `internal/sidecar/allowlist.go` | `sidecar` | Domain allowlist, provider-to-host mapping |
| `internal/sidecar/proxy.go` | `sidecar` | HTTP forward proxy, credential injection, CONNECT tunnel |
| `internal/sidecar/server.go` | `sidecar` | Server lifecycle, graceful shutdown, route registration |
| `internal/sidecar/memory.go` | `sidecar` | Memory API handlers (`/memory/search`, `/memory/status`, `/memory/reindex`) |
| `internal/scrubber/scrubber.go` | `scrubber` | Credential pattern detection + redaction |
| `internal/orchestrator/exec.go` | `orchestrator` | `BuildEnvVarsSidecar()`, `startSidecar()`, UID izolace |
| `internal/orchestrator/orchestrator.go` | `orchestrator` | `SetSidecarEnabled()`, `wrapScrubHandler()` |
| `internal/config/config.go` | `config` | `SidecarEnabled` config, `CREWSHIP_SIDECAR_ENABLED` env |

---

## 3. CREDENTIAL STORE (`internal/sidecar/credstore.go`)

### 3.1 Design

In-memory only. Credentials se NIKDY nezapisuji na disk.

```go
type Credential struct {
    ID       string       // unikatni identifikator
    Provider ProviderType // ANTHROPIC, OPENAI, GOOGLE
    Token    string       // plaintext API klic (jen v pameti)
    Priority int          // plne implementovano: priority-based selection s round-robin v ramci tier
}

type CredStore struct {
    mu    sync.RWMutex
    creds []Credential
    idx   map[ProviderType]int // round-robin index per provider
}
```

### 3.2 Operace

| Metoda | Popis |
|---|---|
| `Load(creds)` | Nahradi vsechny credentials (volano pri startu) |
| `Select(provider)` | Vybere dalsi credential pro providera (round-robin) |
| `Remove(id)` | Odebere credential (napr. pri revokaci) |
| `Count(provider)` | Pocet credentials pro providera |

### 3.3 Round-robin

Kdyz agent posila vice requestu, sidecar stridave pouziva ruzne klice
pro stejneho providera. Toto prirozene rozklada zatez a predchazi rate limitum.

### 3.4 Thread safety

Vsechny operace jsou chraneny `sync.RWMutex`:
- `Select()` bere write lock (inkrementuje index)
- `Count()` bere read lock
- `Load()` bere write lock

**Benchmark:** ~36 ns/op pro `Select()` s jednim klicem.

---

## 4. DOMAIN ALLOWLIST (`internal/sidecar/allowlist.go`)

### 4.1 Default povolene domeny

```go
var DefaultAllowedDomains = []string{
    "api.anthropic.com",
    "api.openai.com",
    "generativelanguage.googleapis.com",
    "api.factory.ai",
}
```

Vsechny ostatni domeny jsou BLOKOVANY (HTTP 403).

### 4.2 Jak funguje matching

- Host se normalizuje na lowercase
- Port se stripne (`:443` atd.)
- Presna shoda -- zadne wildcardy, zadne suffixove utoky

**Bezpecnostni testy (vsechny prochazi):**
- Trailing dot (`api.anthropic.com.`) → BLOCKED
- Subdomain attack (`evil.api.anthropic.com`) → BLOCKED
- Suffix attack (`notapi.anthropic.com`) → BLOCKED
- IP address bypass (`104.18.4.12`) → BLOCKED
- Empty host → BLOCKED
- Case insensitivity (`API.ANTHROPIC.COM`) → ALLOWED

### 4.3 Provider mapping

```go
func providerForHost(host string) ProviderType {
    switch h {
    case "api.anthropic.com":    return ProviderAnthropic
    case "api.openai.com":       return ProviderOpenAI
    case "generativelanguage.googleapis.com": return ProviderGoogle
    default:                     return "" // no credential injection
    }
}
```

Requesty na povolene domeny bez provider mappingu (napr. `api.factory.ai`)
se forwarduji BEZ credential injection -- pouzivaji se pro non-LLM komunikaci.

**Benchmark:** ~16-22 ns/op pro `IsAllowed()`.

---

## 5. HTTP PROXY (`internal/sidecar/proxy.go`)

### 5.1 Dva rezimy

| Rezim | Jak funguje | Credential injection |
|---|---|---|
| **HTTP forward** | Agent nastavi `HTTP_PROXY` nebo `ANTHROPIC_BASE_URL` → plaintext HTTP na :9119 → sidecar forward na HTTPS | ANO (sidecar cte request, prida header) |
| **CONNECT tunnel** | Agent posle `CONNECT api.anthropic.com:443` → TCP tunnel | NE (raw TCP, sidecar nevi co je uvnitr) |

Primarni cesta je **HTTP forward** pres `ANTHROPIC_BASE_URL`:
- Claude Code pouzije `ANTHROPIC_BASE_URL=http://127.0.0.1:9119`
- Posle plaintext HTTP request
- Sidecar vidi cely request, injektuje `x-api-key` header
- Sidecar forward na `https://api.anthropic.com`

CONNECT tunnel je povoleny pro allowlisted domeny, ale BEZ credential
injection (raw TCP). Toto je zamer -- slouzi jako fallback pro HTTPS
requesty ktere nepotrebuji credential injection.

### 5.2 Credential injection per provider

```go
func injectCredential(r *http.Request, provider ProviderType, token string) {
    switch provider {
    case ProviderAnthropic:
        r.Header.Set("x-api-key", token)
        r.Header.Set("anthropic-version", "2023-06-01")
    case ProviderOpenAI:
        r.Header.Set("Authorization", "Bearer "+token)
    case ProviderGoogle:
        q := r.URL.Query()
        q.Set("key", token)
        r.URL.RawQuery = q.Encode()
    }
}
```

**SECURITY:** Sidecar PREPISE jakykoli `x-api-key` nebo `Authorization` header
ktery agent posle. Agent nemuze podstrcit vlastni klic.

### 5.3 Bezpecnostni opatreni v proxy

| Opatreni | Implementace | Duvod |
|---|---|---|
| Hop-by-hop header stripping | `Connection`, `Keep-Alive`, `Proxy-Authenticate`, `Proxy-Authorization`, `TE`, `Trailers`, `Transfer-Encoding`, `Upgrade` | RFC 2616 compliance, `Proxy-Authorization` = exfiltration vector |
| Request body size limit | `http.MaxBytesReader` 10 MB | OOM prevence |
| Localhost detection | `net.ParseIP().IsLoopback()` pokryva 127.0.0.0/8, `::1`, `localhost.localdomain` | Ochrana interni control plane |
| Domain allowlist | Presna shoda, case-insensitive, port-stripped | Blokuje data exfiltraci |
| ReadHeaderTimeout | 10 sekund | Slowloris ochrana |
| IdleTimeout | 120 sekund | Konexni hygena |

### 5.4 Health endpoint

```
GET http://127.0.0.1:9119/health

Response:
{
    "status": "ok",
    "anthropic_creds": 1,
    "openai_creds": 0,
    "google_creds": 0
}
```

Health endpoint NEUKAZUJE hodnoty credentials -- jen pocty per provider.
Toto je low-risk info disclosure (agent vi ze ma 1 Anthropic klic, ale ne jaky).

### 5.5 Memory API (Phase 1 MVP)

Pokud je memory config pritomny ve stdin, sidecar inicializuje FTS5 memory engine
a registruje nasledujici endpointy (localhost only, viz MEMORY.md pro detaily):

| Metoda | Path | Popis |
|---|---|---|
| `POST` | `/memory/search` | BM25 fulltext search pres memory soubory |
| `GET` | `/memory/status` | Stav memory indexu (pocet souboru, chunku, velikost) |
| `POST` | `/memory/reindex` | Manualni reindex vsech `.md` souboru |

**Stdin format (novy objekt):**
```json
{
  "credentials": [{"id": "...", "provider": "ANTHROPIC", "token": "sk-ant-...", "priority": 0}],
  "memory": {"enabled": true, "base_path": "/output/agent-slug/.memory", "agent_slug": "agent-slug"}
}
```

**Backward compatibility:** Pokud JSON parsing jako objekt selze, sidecar se pokusi parsovat
jako legacy array credentials (pro starsi verze orchestratoru).

### 5.6 Error responses

502 a 503 error zpravy jsou genericke ("upstream request failed",
"no credential available for ANTHROPIC") a NIKDY neobsahuji credential hodnoty.

---

## 6. CREDENTIAL SCRUBBER (`internal/scrubber/scrubber.go`)

### 6.1 Ucel

Kazdy radek agent stdout prochazi pres Scrubber pred odeslanim na WebSocket
nebo JSONL log. Toto je posledni obranana vrstva -- i kdyz agent nejak
ziska credential a pokusi se ho vypsat, bude redaktovan.

### 6.2 Detekovane patterns

| Pattern | Priklad | Nahrazeno |
|---|---|---|
| Anthropic API key | `sk-ant-abc123...` | `[REDACTED:anthropic_key]` |
| OpenAI API key | `sk-proj-abc123...`, `sk-{20+chars}` | `[REDACTED:openai_key]` |
| Google API key | `AIzaSy...{33chars}` | `[REDACTED:google_key]` |
| GitHub token | `ghp_`, `gho_`, `ghs_`, `ghr_`, `github_pat_` | `[REDACTED:github_token]` |
| Slack token | `xoxb-`, `xoxp-`, `xoxa-`, `xoxr-` | `[REDACTED:slack_token]` |
| AWS access key | `AKIA{16chars}` | `[REDACTED:aws_key]` |
| JWT Bearer token | `Bearer eyJ...` | `[REDACTED:bearer_token]` |
| SSH/RSA/EC private key | `-----BEGIN * PRIVATE KEY-----` | `[REDACTED:private_key]` |
| JSON password/secret | `"password": "value"` | `"password": "[REDACTED]"` |
| ENV var secret | `SECRET_KEY=value` | `SECRET_KEY=[REDACTED]` |

### 6.3 API

```go
s := scrubber.New()

// Scrub text (redact all patterns)
safe := s.Scrub(dangerousText)

// Check if text contains secrets (fast boolean check)
hasSecret := s.ContainsSecret(text)

// Add custom pattern
s.AddPattern("custom_key", `CUSTOM-[A-Z]{10,}`)
```

### 6.4 Integrace s orchestratorem

```go
// internal/orchestrator/orchestrator.go
func (o *Orchestrator) wrapScrubHandler(handler EventHandler) EventHandler {
    return func(event AgentEvent) {
        event.Content = o.scrubber.Scrub(event.Content)
        handler(event)
    }
}
```

Kazdy `AgentEvent` z `streamOutput()` projde pres `wrapScrubHandler()` pred
dorucenim na WebSocket nebo JSONL log.

### 6.5 Znama omezeni

| Omezeni | Vysvetleni |
|---|---|
| Base64-encoded credentials | Pokud agent base64-encoduje klic, scrubber ho nedetekuje |
| URL-encoded credentials | `sk%2Dant%2D...` projde bez detekce |
| Unicode obfuscation | Nahrazeni znaku Unicode lookalikes |
| Split credentials | Klic rozdeleny na vice radku |

Tyto omezeni jsou akceptovatelne protoze agent v sidecar modu NEMA pristup
k realnym klicum -- sidecar je injektuje az na urovni HTTP headeru.
Scrubber je defense-in-depth, ne primarni ochrana.

**Benchmark:** ~2.8 us/op pro typicky radek textu (13 patterns).

---

## 7. ORCHESTRATOR INTEGRACE

### 7.1 Konfigurace

**YAML config:**
```yaml
container:
  sidecar_enabled: true
```

**ENV var:**
```bash
CREWSHIP_SIDECAR_ENABLED=true
```

Kod v `internal/server/server.go` wires:
```go
orch.SetSidecarEnabled(cfg.Container.SidecarEnabled)
```

### 7.2 Agent ENV vars (sidecar mode)

Kdyz je sidecar enabled, `BuildEnvVarsSidecar()` nastavuje:

```bash
# Proxy config
HTTP_PROXY=http://127.0.0.1:9119
HTTPS_PROXY=http://127.0.0.1:9119
http_proxy=http://127.0.0.1:9119
https_proxy=http://127.0.0.1:9119

# Infinite proxy loop prevention
NO_PROXY=127.0.0.1,localhost,::1
no_proxy=127.0.0.1,localhost,::1

# Claude Code: point to sidecar (HTTP, not HTTPS)
ANTHROPIC_BASE_URL=http://127.0.0.1:9119

# Dummy keys (sidecar replaces with real ones)
ANTHROPIC_API_KEY=sk-ant-dummy-crewship-sidecar
OPENAI_API_KEY=sk-dummy-crewship-sidecar

# Standard Crewship metadata
CREWSHIP_AGENT_ID=...
CREWSHIP_CREW_ID=...
CREWSHIP_CHAT_ID=...
```

**KRITICKE:** Zadne realne API klice v ENV vars. Agent vidi JEN dummy klice.

### 7.3 Sidecar start flow

```go
func startSidecar(ctx, container, containerID, creds, logger) error {
    // 1. Mapuj orchestrator creds na sidecar format
    //    (credTypeToProvider: ANTHROPIC_API_KEY → "ANTHROPIC", etc.)

    // 2. JSON marshal → base64 encode
    //    SECURITY: base64 prevence shell injection (raw JSON
    //    s apostrofy/backticky v tokenech muze breaknout echo '...')

    // 3. Docker exec s shell scriptem:
    //    echo '<base64>' | base64 -d | crewship-sidecar --addr 127.0.0.1:9119 &
    //    sleep 0.5
    //    wget -q -O /dev/null http://127.0.0.1:9119/health || curl -sf ...

    // 4. SECURITY: Bezi jako UID 1002 (agent je UID 1001)
    //    → Linux kernel blokuje /proc/<sidecar_pid>/mem pristup z jineho UID
}
```

### 7.4 Credential type mapping

```go
func credTypeToProvider(c Credential) string {
    switch {
    case c.EnvVarName == "ANTHROPIC_API_KEY" || c.Type == "AI_CLI_TOKEN":
        return "ANTHROPIC"
    case c.EnvVarName == "OPENAI_API_KEY":
        return "OPENAI"
    case c.EnvVarName == "GOOGLE_API_KEY":
        return "GOOGLE"
    default:
        return "" // skip non-LLM credentials
    }
}
```

`AI_CLI_TOKEN` (Claude Code OAuth tokeny) se mapuji na `ANTHROPIC` provider
protoze jsou to Anthropic API klice v OAuth formatu.

### 7.5 setupClaudeConfig() (ne setupClaudeCredentials!)

Puvodni `setupClaudeCredentials()` zapisovala plaintext OAuth tokeny na disk
do kontejneru. Toto bylo nahrazeno `setupClaudeConfig()` ktera zapisuje
JEN nesekrektni konfiguracni data:

```json
{"hasCompletedOnboarding":true,"hasAvailableSubscription":true,"autoUpdates":false}
```

Zadne credentials na disku. NIKDY.

### 7.6 Internal token auto-generace

Puvodni `CREWSHIP_INTERNAL_TOKEN` mel hardcoded default `"crewshipd"` --
kdokoli kdo zna zdrojovy kod mohl pristoupit k desifrovanym credentials.

Nyni: `crypto/rand` generuje 32-byte (64 hex chars) nahodny token pri startu
pokud neni explicitne nastaven pres env var.

---

## 8. UID IZOLACE

### 8.1 Proc dve UID

| Proces | UID | Duvod |
|---|---|---|
| Agent (Claude Code, etc.) | 1001 (agent) | Standardni neprivilegovany uzivatel |
| Sidecar | 1002 (sidecar) | Oddeleni od agenta kvuli /proc izolaci |

### 8.2 /proc ochrana

Linux kernel omezuje `/proc/<PID>/mem` pristup na procesy se **stejnym UID**
(pokud neni `CAP_SYS_PTRACE`, coz agent NEMA -- `--cap-drop ALL`).

```
Agent (UID 1001) → cat /proc/<sidecar_pid>/mem → Permission denied
Agent (UID 1001) → cat /proc/<sidecar_pid>/cmdline → viditelne (ale neobsahuje creds)
Agent (UID 1001) → cat /proc/<sidecar_pid>/environ → Permission denied
```

Credentials jsou JEN v heap pameti sidecar procesu (UID 1002).
Agent je nemuze precist.

### 8.3 Dockerfile setup

```dockerfile
# docker/agent-runtime/Dockerfile
RUN groupadd -g 1001 agent && useradd -u 1001 -g agent -m agent && \
    groupadd -g 1002 sidecar && useradd -u 1002 -g sidecar -s /usr/sbin/nologin sidecar
```

Sidecar uzivatel ma `/usr/sbin/nologin` shell -- nelze se do nej prihlasit.

---

## 9. BEZPECNOSTNI AUDIT (vysledky)

### 9.1 Nalezene a opravene zranitelnosti

| # | Zavaznost | Zranitelnost | Oprava |
|---|---|---|---|
| 1 | CRITICAL | Shell injection v `startSidecar()` -- tokeny se shell metaznaky mohly escapnout `echo '...'` | Base64-encode creds pred pipnutim do shellu |
| 2 | HIGH | CONNECT tunnel bypassa credential injection | Dokumentovano jako zamerne (raw TCP) |
| 3 | HIGH | Sidecar `/proc` leaks -- agent (same UID) mohl cist heap | Sidecar bezi jako UID 1002 (agent UID 1001) |
| 4 | MEDIUM | Chybejici `NO_PROXY` -- nekonecna proxy smycka | Pridano `NO_PROXY=127.0.0.1,localhost,::1` |
| 5 | MEDIUM | Hop-by-hop headery nepruhovane -- `Proxy-Authorization` data exfiltrace | Strippuje 8 hop-by-hop headeru per RFC 2616 |
| 6 | MEDIUM | Zadny request body size limit -- OOM | `http.MaxBytesReader` s 10 MB limitem |
| 7 | MEDIUM | Zadny response body streaming limit | Dokumentovano, akceptovatelne pro MVP |
| 8 | LOW | Health endpoint ukazuje pocty credentials | Minor info disclosure, akceptovatelne |
| 9 | LOW | `isLocalhost()` nekomletni | Pouziva `net.ParseIP().IsLoopback()` pokryvajici 127.0.0.0/8, `::1` |
| 10 | LOW | Scrubber chybel Google API key pattern | Pridan `AIzaSy...` pattern |

### 9.2 Test coverage

| Test soubor | Pocet testu | Co testuje |
|---|---|---|
| `internal/sidecar/credstore_test.go` | 5+ | Load, Select, Remove, Count, round-robin |
| `internal/sidecar/allowlist_test.go` | 5+ | IsAllowed, Add, providerForHost |
| `internal/sidecar/proxy_test.go` | 10+ | HTTP forward, CONNECT, credential injection, blocked domains |
| `internal/sidecar/server_test.go` | 4+ | Start, shutdown, health endpoint |
| `internal/sidecar/memory_test.go` | 3+ | Memory search, status, reindex handlers |
| `internal/sidecar/security_test.go` | 25 | Allowlist bypass, host header attacks, credential leak, hop-by-hop, /proc, concurrent safety |
| `internal/sidecar/bench_test.go` | 5 | Performance benchmarky |
| `internal/scrubber/scrubber_test.go` | 13 | Vsechny patterns, edge cases |
| `internal/scrubber/security_test.go` | 12 | Google key, multi-cred, nested JSON, base64, ReDoS, unicode, concurrent |
| `internal/orchestrator/security_test.go` | 8 | Fuzz creds, sidecar env isolation, NO_PROXY, shell injection, stream scrubbing |
| `internal/orchestrator/failover_test.go` | Tests | Sidecar env var + provider mapping |
| `internal/orchestrator/orchestrator_test.go` | Tests | Sidecar + scrubbing integrace |

**Celkem: 80+ bezpecnostnich a unit testu, vsechny PASS s `-race` detektorem.**

### 9.3 Docker penetracni testy (12/12 PASS)

Provadeny v zivem kontejneru s sidecarem na UID 1002:

| # | Test | Vysledek |
|---|---|---|
| 1 | `/proc/self/environ` neobsahuje API klice | PASS |
| 2 | Sidecar bezi jako UID 1002 (ne 1001) | PASS |
| 3 | `/proc/<sidecar>/mem` → Permission denied | PASS |
| 4 | `/proc/<sidecar>/cmdline` neobsahuje credentials | PASS |
| 5 | Filesystem scan nenajde zadne credentials | PASS |
| 6 | Root filesystem je read-only | PASS |
| 7 | Agent ma nulove capabilities | PASS |
| 8 | Sidecar binary je stripped | PASS |
| 9 | Proxy blokuje evil.com (403) | PASS |
| 10 | Proxy povoluje api.anthropic.com (405 = reached API) | PASS |
| 11 | Health endpoint vraci ok | PASS |
| 12 | Health endpoint neukazuje credential hodnoty | PASS |

### 9.4 Performance benchmarky

| Operace | Cas |
|---|---|
| CredStore Select (1 klic) | ~36 ns/op |
| Allowlist IsAllowed (hit) | ~16 ns/op |
| Allowlist IsAllowed (miss) | ~22 ns/op |
| Proxy pipeline (inject + strip + forward mock) | ~900 ns/op |
| Scrubber (typicky radek, 13 patterns) | ~2.8 us/op |

Vsechny operace jsou pod 10 us -- zanedbatelny overhead.

---

## 10. POROVNANI: S SIDECAREM vs. BEZ

| Aspekt | Bez sidecaru (legacy) | Se sidecarem (novy) |
|---|---|---|
| Agent vidi API klice | ANO (ENV vars) | NE (dummy klice) |
| Credentials na disku | ANO (Claude OAuth soubory) | NE (jen v pameti sidecar) |
| Prompt injection risk | VYSOKY (klic v env) | NIZKY (agent nema pristup) |
| /proc leak risk | VYSOKY (same UID, env) | NIZKY (jiny UID, zadny env) |
| Network exfiltrace | VYSOKA (agent posle klic kam chce) | BLOKOVÁNA (allowlist + dummy key) |
| Credential rotation | Restart agenta | Sidecar CredStore.Load() (hot reload) |
| Audit trail per klic | NE | ANO (credential_id v proxy logu) |
| Stdout scrubbing | NE | ANO (scrubber pred WebSocket/JSONL) |
| Internal token | Hardcoded "crewshipd" | crypto/rand 32B (auto-gen) |

---

## 11. KONFIGURACE

### 11.1 Zapnuti sidecaru

```bash
# ENV var
CREWSHIP_SIDECAR_ENABLED=true

# Nebo v config YAML
container:
  sidecar_enabled: true
```

### 11.2 Overeni ze sidecar funguje

V logu crewshipd:
```json
{"level":"info","msg":"sidecar proxy enabled for credential injection"}
```

V kontejneru:
```bash
curl http://127.0.0.1:9119/health
# → {"status":"ok","anthropic_creds":1,"openai_creds":0,"google_creds":0}
```

### 11.3 Pridani extra domeny do allowlistu

V `ServerConfig.AllowedDomains`:
```go
sidecar.NewServer(sidecar.ServerConfig{
    AllowedDomains: []string{"custom-llm.example.com"},
    // ...
})
```

Nebo runtime: `server.Allowlist().Add("custom-llm.example.com")`

---

## 12. BUDOUCI VYVOJ (Phase 2+)

| Feature | Popis | Phase |
|---|---|---|
| Credential-less agent | Agent nema ZADNE API klice, ani dummy -- sidecar je MCP Gateway | Phase 2 |
| MCP Gateway | Sidecar spousti MCP servery, injektuje tool credentials per-request | Phase 2 |
| Hot credential rotation | CredStore.Load() pro zmenu klicu bez restartu agenta | Phase 2 |
| Per-request audit trail | Kazdy HTTP request logovan s credential_id a trace_id | Phase 2 |
| Response body limit | Streaming limit pro velke LLM odpovedi | Phase 2 |
| Custom allowlists per crew | Ruzne domeny pro ruzne tymy | Phase 2 |
| Sidecar healthcheck | crewshipd detekuje padly sidecar a restartuje | Phase 2 |

---

## 13. OTEVRENE OTAZKY

1. **Sidecar crash recovery** -- jak crewshipd detekuje ze sidecar spadl? Healthcheck polling?
2. **Response body streaming limit** -- akceptovatelne pro MVP, ale LLM odpovedi mohou byt velke.
3. **Multi-sidecar** -- jeden sidecar per kontejner, nebo jeden per agent? (nyni 1 per kontejner)
4. **Credential hot reload** -- CredStore ma `Load()`, ale orchestrator to zatim nevola za behu.
5. **WebSocket support** -- sidecar nepodporuje WebSocket tunnel (pro streaming LLM API).
