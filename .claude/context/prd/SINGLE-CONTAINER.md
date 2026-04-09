# Crewship — Single-Container Mode (SINGLE-CONTAINER.md)

**Verze:** 1.0
**Datum:** 2026-04-09
**Status:** Architekturni navrh (neni implementovano)
**Zavislosti:** ORCHESTRATION.md, SECURITY.md, AGENT-RUNTIME.md

---

## 1. MOTIVACE

Crewship dnes bezi v **multi-container mode** (1 kontejner = 1 crew). To je spravne pro enterprise deployments, ale pro single-user/hobby pouziti je to overhead:

- Kazda crew = novy Docker kontejner (startup 5-15s, RAM 200MB+)
- Pro solo usera s 2-3 crews = 3 kontejnery, 600MB+ RAM
- Docker Desktop na macOS/Windows je tezky a pomaly
- Novy uzivatel chce "crewship start" a za 5s mit behajici agenty

**Reseni:** Dual-mode architektura — per-workspace nastaveni.

---

## 2. DVA REZIMY

### 2.1 Single-Container Mode ("hobby")

```
crewship binary (Go server, port 8080)
    |
    v
JEDEN kontejner: crewship-workspace-{slug}
    |
    +-- Sidecar (UID 1002, port 9119)
    |     +-- Multi-crew CredStore (crew_id -> credentials)
    |     +-- Multi-crew network policy (crew_id -> allowed_domains)
    |     +-- Shared MCP gateway
    |
    +-- Agent A (crew: engineering, UID 1001)
    |     +-- /crew/agents/alice/
    |     +-- /output/alice/
    |
    +-- Agent B (crew: devops, UID 1001)
    |     +-- /crew/agents/bob/
    |     +-- /output/bob/
    |
    +-- /crew/shared/          (shared across ALL crews)
    +-- /secrets/{agent-slug}/ (per-agent credential files)
```

**Klicove vlastnosti:**
- Jeden kontejner pro cely workspace
- Jeden sidecar proxy pro vsechny crews
- Sdileny UID 1001 (vsichni agenti)
- Namespace izolace pres adresarovou strukturu (/crew/agents/{slug}/)
- Sidecar rozlisuje crew_id v request headers pro credential injection

### 2.2 Multi-Container Mode ("enterprise") — dnesni stav

```
crewship binary (Go server, port 8080)
    |
    +-- Kontejner: crewship-team-engineering
    |     +-- Sidecar (UID 1002)
    |     +-- Agent A (UID 1001)
    |     +-- Agent B (UID 1001)
    |
    +-- Kontejner: crewship-team-devops
    |     +-- Sidecar (UID 1002)
    |     +-- Agent C (UID 1001)
    |     +-- Agent D (UID 1001)
```

**Klicove vlastnosti:**
- Jeden kontejner per crew (silna izolace)
- Samostatny sidecar per crew (izolace credentials)
- Agenti z jedne crew nevidi data druhe crew

---

## 3. PER-WORKSPACE NASTAVENI

Mode se nastavuje na urovni workspace (ne globalne):

```sql
ALTER TABLE workspaces ADD COLUMN container_mode TEXT DEFAULT 'multi';
-- Hodnoty: 'single' | 'multi'
```

### API

```
PATCH /api/v1/workspaces/{id}
{ "container_mode": "single" }
```

### CLI

```bash
crewship workspace update --container-mode single
```

### UI

Settings → Workspace → Container Mode → Single / Multi

### Zmena modu

Zmena z single → multi (nebo naopak):
1. Zastavit vsechny bezici agenty
2. Odstranit existujici kontejnery
3. Zmenit mode v DB
4. Dalsi agent run vytvori kontejner v novem modu

---

## 4. ARCHITEKTURNI ZMENY

### 4.1 ContainerProvider Interface

```go
// Dnesni interface — beze zmeny
type ContainerProvider interface {
    EnsureCrewRuntime(ctx context.Context, cfg CrewConfig) (string, error)
    StopCrewRuntime(ctx context.Context, containerID string) error
    RemoveCrewRuntime(ctx context.Context, containerID string) error
    // ...
}
```

**Nova abstrakce:**

```go
// ContainerResolver vybira container pro agenta
type ContainerResolver interface {
    // ResolveContainer vraci containerID pro dany crew v danem workspace
    ResolveContainer(ctx context.Context, workspaceID, crewID string) (string, error)
}

// SingleContainerResolver — vsechny crews → jeden kontejner
type SingleContainerResolver struct{}
func (r *SingleContainerResolver) ResolveContainer(ctx, wsID, crewID) (string, error) {
    return "crewship-workspace-" + wsSlug, nil
}

// MultiContainerResolver — kazda crew → vlastni kontejner (dnesni chovani)
type MultiContainerResolver struct{}
func (r *MultiContainerResolver) ResolveContainer(ctx, wsID, crewID) (string, error) {
    return "crewship-team-" + crewSlug, nil
}
```

### 4.2 Sidecar Multi-Crew Support

Dnesni sidecar ma **jeden CredStore** a **jednu SidecarIPCConfig** per instance.

**Zmena pro single-container:**

```go
// SidecarConfig — rozsireni o multi-crew
type SidecarConfig struct {
    Mode        string                    // "single" | "multi"
    Crews       map[string]CrewSidecarCfg // crew_id -> config (single mode)
    // ... existujici fields pro multi mode
}

type CrewSidecarCfg struct {
    CrewID         string
    CrewSlug       string
    Credentials    []Credential
    NetworkMode    string
    AllowedDomains []string
    MCPConfig      string
}
```

**CredStore zmena:**

```go
// Dnes: credStore.Select(provider) — vybere globalne
// Nove: credStore.SelectForCrew(crewID, provider) — vybere per-crew

type MultiCrewCredStore struct {
    stores map[string]*CredStore // crew_id -> CredStore
}
```

**Proxy zmena:**

```go
// Dnes: proxy injectuje credentials podle destination host
// Nove: proxy musi vedet KTERY agent (= ktera crew) posila request

// Reseni: Agent nastavi X-Crewship-Agent header (sidecar ho stripne pred forwardovanim)
// Nebo: kazdy agent ma unikatni local port range (complex)
// Nebo: HTTP_PROXY env var per agent ukazuje na crew-specific sidecar endpoint
```

### 4.3 Filesystem Layout (Single-Container)

```
/crew/
  agents/
    alice/          # crew: engineering
      .memory/
      .mcp.json
    bob/            # crew: devops
      .memory/
      .mcp.json
  shared/           # sdileny vsemi crews
/output/
  alice/
  bob/
/secrets/
  alice/            # jen Alice credentials
    .env
    ANTHROPIC_API_KEY
  bob/              # jen Bob credentials
    .env
    GH_TOKEN
/workspace/         # docasny scratch
```

### 4.4 Container Naming

| Mode | Container Name | Pocet |
|------|---------------|-------|
| single | `crewship-workspace-{ws-slug}` | 1 per workspace |
| multi | `crewship-team-{crew-slug}` | 1 per crew |

### 4.5 Volume Strategy

| Mode | Volumes |
|------|---------|
| single | `crewship-ws-{slug}-home`, `crewship-ws-{slug}-tools` |
| multi | `crewship-home-{crew-slug}`, `crewship-tools-{crew-slug}` (beze zmeny) |

---

## 5. CREDENTIAL IZOLACE V SINGLE-CONTAINER

### Problem

V single-container mode sdileji vsichni agenti jeden sidecar. Pokud crew A ma jine credentials nez crew B, sidecar musi rozlisovat.

### Reseni: Per-Agent Credential Resolution

```
Agent Alice (crew: engineering) → HTTP request → api.anthropic.com
    |
    v
Sidecar proxy
    |
    | 1. Zjisti agent slug z X-Crewship-Agent headeru
    | 2. Lookup: alice → crew: engineering
    | 3. CredStore.SelectForCrew("engineering", "anthropic")
    | 4. Inject x-api-key header
    |
    v
api.anthropic.com
```

**Agent identifikace v proxy:**
- Environment variable `CREWSHIP_AGENT_SLUG` nastavena per exec
- Agent's HTTP_PROXY ukazuje na `http://127.0.0.1:9119`
- Sidecar rozpozna agenta z source IP:port → PID → agent slug mapping
- ALTERNATIVA: Proxy header `X-Crewship-Agent: alice` (jednodussi, sidecar ho stripne)

### Credential File Izolace

Credential files v `/secrets/{agent-slug}/` uz JSOU per-agent (ne per-crew). Toto funguje bez zmeny v obou modech.

---

## 6. NETWORK POLICY V SINGLE-CONTAINER

### Problem

V multi-container mode ma kazda crew svuj network mode (free/restricted). V single-container sdileji vsichni agenti jeden network stack.

### Reseni: Agent-Aware Network Policy

```go
// Dnes: DomainAllowlist je globalni per sidecar
// Nove: Allowlist je per-crew

type MultiCrewAllowlist struct {
    mode       map[string]string         // crew_id -> "free"|"restricted"
    allowlists map[string]*DomainAllowlist // crew_id -> allowlist
}

func (a *MultiCrewAllowlist) IsAllowed(crewID, host string) bool {
    if a.mode[crewID] == "free" {
        return true
    }
    return a.allowlists[crewID].Contains(host)
}
```

### Omezeni

V single-container mode nelze mit **uplnou** network izolaci mezi crews (sdileji network namespace). Agent z crew A technicky MUZE pristupovat k domainum povolenim pro crew B na urovni TCP. Sidecar proxy to blokuje na HTTP urovni, ale HTTPS CONNECT tunely je tezke rozlisit.

**Doporuceni:** Single-container mode pouzivat jen pro hobby/solo deployments kde crew izolace neni kriticka. Pro enterprise pouzit multi-container.

---

## 7. MIGRACE MEZI MODY

### Single → Multi

1. Stop all agents
2. Stop workspace container
3. Pro kazdou crew:
   - Vytvor crew kontejner
   - Zkopiruj /crew/agents/{crew-agents}/ do crew kontejneru
   - Zkopiruj /output/{crew-agents}/ do crew kontejneru
4. Zmena mode v DB
5. Odstran workspace kontejner

### Multi → Single

1. Stop all agents
2. Stop all crew containers
3. Vytvor workspace kontejner
4. Pro kazdou crew:
   - Zkopiruj data z crew kontejneru do workspace kontejneru
5. Zmena mode v DB
6. Odstran crew kontejnery

---

## 8. IMPLEMENTACNI FAZE

### Phase 1: ContainerResolver abstrakce
- Zavest ContainerResolver interface
- MultiContainerResolver (dnesni chovani, beze zmeny)
- Refactor orchestrator.go: pouzit resolver misto primo crew slug

### Phase 2: Single-Container Provider
- SingleContainerResolver implementace
- Workspace container naming: `crewship-workspace-{slug}`
- Volume naming: `crewship-ws-{slug}-home`

### Phase 3: Multi-Crew Sidecar
- MultiCrewCredStore (crew_id → CredStore)
- Agent identification v proxy (X-Crewship-Agent header)
- Per-crew network policy v sidecar

### Phase 4: Workspace Setting
- DB migrace: workspaces.container_mode
- API endpoint: PATCH workspace
- CLI: `crewship workspace update --container-mode`
- UI: Settings → Container Mode

### Phase 5: Migrace nastroj
- `crewship workspace migrate-mode` CLI command
- Data kopie mezi single/multi kontejnery

---

## 9. BEZPECNOSTNI SROVNANI

| Aspekt | Single-Container | Multi-Container |
|--------|-----------------|-----------------|
| Agent ↔ Agent izolace (same crew) | Zadna (sdileny UID) | Zadna (sdileny UID) |
| Agent ↔ Agent izolace (diff crew) | Jen filesystem namespace | Uplna (jiny kontejner) |
| Credential izolace | Per-agent via sidecar | Per-crew via sidecar |
| Network izolace | Per-crew na HTTP urovni | Per-crew na Docker urovni |
| Container escape | Vidi data vsech crews | Vidi data jen sve crew |
| RAM overhead | ~200MB celkem | ~200MB per crew |
| Startup time | ~5s (jednou) | ~5-15s per crew |

---

## 10. DOPORUCENI

- **Hobby/Solo:** single-container mode (1-3 crews, 1-10 agents)
- **Team:** multi-container mode (3+ crews, 10+ agents)
- **Enterprise:** multi-container + runsc/kata runtime

Default pro nove workspaces: **multi** (bezpecnejsi). Single mode je opt-in.
