# Crewship -- Agent Runtime (AGENT-RUNTIME.md)

**Verze:** 4.0
**Datum:** 2026-02-16
**Zmeny v4.0:** Loopback HTTP sidecar (nahrazuje named pipe), dual runtime
(CLI + API-direct crewship-agent), Landlock per-agent filesystem izolace,
optional gVisor runtime, leader modes (active/passive).
**Zmeny v5.0:** Sidecar = MCP Gateway uvnitr kontejneru (ADR-014).
Skill = MCP Server wrapper (tools + resources + credential requirements).
Credential-less agent pattern: agent NEMA API klice, sidecar injektuje
per-request (ADR-015). Tool search pro on-demand tool discovery (ADR-016).
Agent memory presunuta do /output/.memory/ (persistent).
**Zmeny v6.0:** Skill Hub / MCP Marketplace (ADR-019). 6-krokovy security
pipeline pro skill verifikaci (ADR-020). OCI image distribuce (ADR-021).
Anthropic Sandbox Runtime (srt) per-MCP-server (ADR-017). Claude Agent
Teams jako optional crew execution mode (ADR-018).
Viz `ADR.md` pro zduvodneni rozhodnuti.

---

## 1. PREHLED

Agent runtime je system ktery spravuje zivotni cyklus AI agentu v Docker kontejnerech.
Klicovy princip: **1 kontejner = 1 tym. Agenti v tymu sdili kontejner.**

### Vrstvy

```
1. crewshipd (Go) — orchestrator, spravuje kontejnery a agent sessions
2. ContainerProvider — Docker (MVP) nebo K8s (Enterprise)
3. StorageProvider — LocalFS (MVP) nebo S3/MinIO (Enterprise)
4. StateProvider — bbolt (MVP) nebo PostgreSQL (Enterprise)
5. crewship-sidecar — MCP Gateway + delegacni proxy uvnitr kontejneru
6. MCP Servers — stdio procesy spustene sidecar (1 per skill)
7. CLI adapter — Claude Code / Codex CLI / Gemini CLI / OpenCode
8. LLM provider — Anthropic / OpenAI / Google / Ollama
```

> **KRITICKE:** crewshipd NIKDY nepristupuje k Docker/filesystem/bbolt primo.
> Vse jde pres provider interface. Viz `K8S-READINESS.md` pro kompletni specifikaci.

### Flow: uzivatel posle zpravu agentovi

```
1. Uzivatel napise v chat UI: "Vytvor report"
2. Browser → WebSocket → crewshipd (Go)
3. crewshipd overí ze kontejner tymu bezi (pokud ne, spusti)
   - Kontejner obsahuje crewship-sidecar (localhost:9119, startuje s kontejnerem)
4. crewshipd zavola Docker exec v kontejneru:
   docker exec -e ANTHROPIC_API_KEY=... \
     -e CREWSHIP_SESSION_ID={session-id} \
     -e CREWSHIP_SIDECAR=http://localhost:9119 \
     -w /workspace/anna team-container \
     {agent_command}  # CLI tool NEBO crewship-agent (dle runtime mode)
5. crewshipd cte stdout/stderr (Docker attach API) → WebSocket + JSONL log
6. Agent komunikuje s crewship-sidecar pres localhost:9119 (HTTP):
   - Delegace: POST /delegate
   - Vysledky: GET /results/{group}
7. Agent pise soubory do /output/ → fsnotify → WebSocket → Browser
8. Kazdy radek stdout → append do JSONL log souboru
9. Po dokonceni: crewshipd updatne AgentRun status v DB (pres IPC)
```

**Dual runtime — agent_command se vybere dle AgentRuntime:**
```
CLI_CLAUDE_CODE → claude --print "Vytvor report"
CLI_OPENCODE    → opencode run "Vytvor report"
CLI_CODEX       → codex "Vytvor report"
API_DIRECT      → crewship-agent --session={id} --model=claude-opus-4.6 "Vytvor report"
```

> Viz `ORCHESTRATION.md` sekce 5.1 a 5.9 pro detaily sidecar a dual runtime.

### Flow: webhook trigger

```
1. Grafana posle POST /api/v1/webhooks/{team}/{agent}/trigger
2. crewshipd validuje X-Webhook-Secret
3. crewshipd vytvori ConversationSession + AgentRun (trigger_type=WEBHOOK)
4. crewshipd spusti Docker exec s webhook payload jako vstup
5. Stejny flow jako vyse (stdout → JSONL, /output/ → fsnotify)
```

---

## 2. CONTAINER LIFECYCLE

### 2.1 Vytvoreni kontejneru (pri vytvoreni tymu)

> **Poznamka:** Nasledujici kod ukazuje Docker implementaci ContainerProvider.
> K8s implementace pouziva client-go (Deployment + Pod misto Container).
> Orchestrator vola `provider.EnsureTeamRuntime()` — nevidi implementaci.

```go
// internal/provider/docker/docker.go (Docker implementace ContainerProvider)
func (m *DockerProvider) EnsureTeamRuntime(ctx context.Context, team TeamConfig) error {
    // Runtime: runc (default) nebo runsc (gVisor, viz ADR-003)
    runtime := os.Getenv("CREWSHIP_RUNTIME") // "runc" | "runsc"
    if runtime == "" {
        runtime = "runc"
    }

    resp, err := m.client.ContainerCreate(ctx, &container.Config{
        Image: "ghcr.io/crewship-ai/agent-runtime:latest",
        User:  "1001:1001",  // non-root
        Env: []string{
            "CREWSHIP_TEAM_ID=" + team.ID,
        },
    }, &container.HostConfig{
        Runtime:        runtime,  // "runc" (default) nebo "runsc" (gVisor)
        ReadonlyRootfs: true,
        SecurityOpt:    []string{"no-new-privileges"},
        CapDrop:        []string{"ALL"},
        CapAdd:         []string{"NET_RAW"},
        Resources: container.Resources{
            Memory:   int64(team.MemoryMB) * 1024 * 1024,
            NanoCPUs: int64(team.CPUs * 1e9),
            PidsLimit: ptr(int64(200)),
        },
        Mounts: []mount.Mount{
            {Type: mount.TypeVolume, Source: "workspace-" + team.ID, Target: "/workspace"},
            {Type: mount.TypeBind, Source: outputPath(team), Target: "/output"},
        },
        Tmpfs: map[string]string{"/tmp": "rw,size=500m"},
        NetworkMode: "crewship-agents",
    }, nil, nil, "crewship-team-"+team.Slug)

    if err := m.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
        return fmt.Errorf("container start: %w", err)
    }

    // Start crewship-sidecar inside container (long-running background process)
    sidecarExec, _ := m.client.ContainerExecCreate(ctx, resp.ID, container.ExecOptions{
        Cmd:  []string{"/usr/local/bin/crewship-sidecar", "--team-id=" + team.ID},
        User: "1001:1001",
    })
    return m.client.ContainerExecStart(ctx, sidecarExec.ID, container.ExecStartOptions{Detach: true})
}
```

### 2.2 Stavy kontejneru

```
CREATING → RUNNING → IDLE → STOPPED
                  ↗         ↓
              ERROR ←───────┘
```

| Stav | Popis |
|---|---|
| CREATING | Docker image se stahuje, kontejner se vytvari |
| RUNNING | Kontejner bezi, alespon 1 agent je aktivni |
| IDLE | Kontejner bezi, zadny agent neni aktivni |
| STOPPED | Kontejner zastaven (TTL expiroval nebo manualne) |
| ERROR | Kontejner crashnul nebo se nepodarilo vytvorit |

### 2.3 TTL (auto-stop)

Pokud zadny agent v tymu nebezi po dobu `container_ttl_hours`, kontejner se automaticky zastavi.
`container_ttl_hours = null` → kontejner bezi navzdy (default).

### 2.4 Resource check pred vytvorenim

```go
// internal/docker/resources.go
func (m *Manager) CheckResources(ctx context.Context, requiredMemMB int) error {
    info, _ := m.client.Info(ctx)
    availableMB := (info.MemTotal - info.MemUsed) / (1024 * 1024)
    if availableMB < int64(requiredMemMB) + 512 { // 512 MB reserve
        return fmt.Errorf("insufficient memory: need %dMB, available %dMB", requiredMemMB, availableMB)
    }
    return nil
}
```

---

## 3. AGENT EXECUTION

### 3.1 Docker exec

```go
// internal/orchestrator/exec.go
func (o *Orchestrator) RunAgent(ctx context.Context, req AgentRunRequest) error {
    execConfig := container.ExecOptions{
        Cmd:          o.buildCLICommand(req),
        Env:          o.buildEnvVars(req),  // credentials jako ENV vars
        WorkingDir:   "/workspace/" + req.AgentSlug,
        AttachStdout: true,
        AttachStderr: true,
        User:         "1001:1001",
    }

    execID, err := o.docker.ContainerExecCreate(ctx, req.ContainerID, execConfig)
    if err != nil {
        return fmt.Errorf("exec create: %w", err)
    }

    resp, err := o.docker.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
    if err != nil {
        return fmt.Errorf("exec attach: %w", err)
    }
    defer resp.Close()

    // Stream stdout → WebSocket + JSONL log
    go o.streamOutput(ctx, resp.Reader, req)

    return nil
}
```

### 3.2 CLI command per adapter

```go
func (o *Orchestrator) buildCLICommand(req AgentRunRequest) []string {
    switch req.CLIAdapter {
    case "CLAUDE_CODE":
        cmd := []string{"claude", "--print"}
        if req.SystemPrompt != "" {
            cmd = append(cmd, "--system-prompt", req.SystemPrompt)
        }
        if req.ToolProfile == "MINIMAL" {
            cmd = append(cmd, "--tools", "Read,Search,Grep")
        }
        cmd = append(cmd, req.UserMessage)
        return cmd

    case "CODEX_CLI":
        cmd := []string{"codex", "--quiet"}
        if req.ToolProfile == "CODING" {
            cmd = append(cmd, "--sandbox")
        }
        cmd = append(cmd, req.UserMessage)
        return cmd

    case "GEMINI_CLI":
        return []string{"gemini", "-p", req.UserMessage}

    case "OPENCODE":
        return []string{"opencode", "run", req.UserMessage}

    default:
        return []string{"claude", "--print", req.UserMessage}
    }
}
```

### 3.3 Credential injection (Phase 1 → Phase 2 evoluce)

**Phase 1 (MVP) — ENV var injection:**

```go
func (o *Orchestrator) buildEnvVars(req AgentRunRequest) []string {
    env := []string{
        "CREWSHIP_AGENT_ID=" + req.AgentID,
        "CREWSHIP_TEAM_ID=" + req.TeamID,
        "CREWSHIP_SESSION_ID=" + req.SessionID,
        "CREWSHIP_SIDECAR=http://localhost:9119",
    }

    // Phase 1: LLM API klice jako env vars (CLI tools je MUSI mit)
    for _, cred := range req.LLMCredentials {
        env = append(env, cred.EnvVarName+"="+cred.PlaintextValue)
    }

    // Phase 1: Tool credentials NEdava agentovi — sidecar je drzi
    // Agent vola tools pres MCP → sidecar prida credentials

    return env
}
```

> **KRITICKE:** Credentials se NIKDY neukladaji na disk. Existuji jen jako ENV vars
> po dobu behu exec procesu. Po skonceni procesu zmizi.

**Phase 2 (credential-less agent, ADR-015):**

```
Phase 1:  Agent dostane ANTHROPIC_API_KEY jako env var (nutne pro CLI tools)
Phase 2:  Agent NEDOSTANE zadne API klice
          → crewship-agent (API-direct) vola sidecar: GET /credentials/{env_var}
          → sidecar vrati klic per-request, agent ho drzi JEN po dobu API callu
          → sidecar muze klic zrotovat mid-session bez restartu agenta
          → prompt injection NEMUZE extrahovat klic (agent ho nema v env)
```

**Rozdeleni credentials dle typu:**

| Typ credentials | Phase 1 | Phase 2 |
|---|---|---|
| LLM API klice (ANTHROPIC_API_KEY) | ENV var (CLI tools vyzaduji) | Sidecar per-request (API-direct only) |
| Tool credentials (GITHUB_TOKEN) | Sidecar drzi, agent nevidi | Sidecar drzi, agent nevidi |
| MCP server credentials | Sidecar injektuje do MCP serveru | Sidecar injektuje do MCP serveru |

> Viz sekce 6A (MCP a Skills) pro detailni credential flow.

---

## 4. STDOUT STREAMING

### 4.1 Real-time pipeline

```
Docker exec stdout → crewshipd goroutine → parser → broadcast
                                              ├── WebSocket (real-time k browseru)
                                              ├── JSONL log soubor (append)
                                              └── bbolt WAL (job state update)
```

### 4.2 Parsovani stdout

CLI nastroje (Claude Code, Codex) produkuji strukturovany output.
crewshipd parsuje kazdy radek a klasifikuje:

```go
type AgentEvent struct {
    Type      string `json:"type"`       // "thinking", "text", "tool_call", "tool_result", "status"
    Content   string `json:"content"`
    Metadata  any    `json:"metadata,omitempty"`
    Timestamp time.Time `json:"ts"`
}
```

### 4.3 WebSocket broadcast

```go
func (o *Orchestrator) streamOutput(ctx context.Context, reader io.Reader, req AgentRunRequest) {
    scanner := bufio.NewScanner(reader)
    channel := "agent:" + req.AgentID

    for scanner.Scan() {
        line := scanner.Text()
        event := o.parseLine(line)

        // 1. WebSocket broadcast (real-time)
        o.ws.Broadcast(channel, event)

        // 2. JSONL log (append)
        o.logCollector.Append(req.TeamID, req.AgentID, event)

        // 3. JSONL conversation (append)
        o.conversationWriter.Append(req.SessionID, event)
    }
}
```

---

## 5. LOG COLLECTION

### 5.1 JSONL format

Kazdy radek = jeden JSON objekt:

```jsonl
{"ts":"2026-02-11T10:00:01Z","level":"info","agent":"anna","event":"thinking","content":"Analyzing..."}
{"ts":"2026-02-11T10:00:02Z","level":"info","agent":"anna","event":"tool_call","tool":"web-search","args":{"query":"..."}}
{"ts":"2026-02-11T10:00:05Z","level":"info","agent":"anna","event":"text","content":"Here is the report...","tokens":500}
```

### 5.2 Soubory

```
/var/log/crewship/teams/{team-id}/agents/{agent-id}/current.jsonl
```

Rotace: logrotate (hodinova, gzip, 30 dni retence).
Zero custom code — Linux nativni nastroj.

### 5.3 Konverzacni JSONL

```
/var/lib/crewship/conversations/{org-id}/{agent-id}/{session-id}.jsonl
```

Kazda session = jeden soubor. Obsah viz DATABASE.md sekce 5.

---

## 6. FILE MANAGEMENT

### 6.1 Storage model

```
/workspace/{agent-slug}/     ← EPHEMERAL (Docker volume, dies with container)
  ├── .claude/               ← Claude Code state (session resume)
  ├── .mcp/                  ← MCP server cache (API responses, temp data)
  ├── scratch/               ← docasne soubory
  └── ...

/output/{agent-slug}/        ← PERSISTENT (bind mount on host)
  ├── reports/
  │   └── january-2026.pdf
  ├── code/
  │   └── fix-memory-leak.patch
  ├── data/
  │   └── analysis.csv
  ├── .memory/               ← agent long-term memory (persistent!)
  │   └── context.jsonl
  └── .skills/               ← persistent skill data/state
      └── {skill-slug}/
```

### 6.2 fsnotify (real-time)

```go
// internal/fileserver/watcher.go
func (w *Watcher) Watch(ctx context.Context, teamID string, outputDir string) {
    watcher, _ := fsnotify.NewWatcher()
    defer watcher.Close()
    watcher.Add(outputDir)

    for {
        select {
        case event := <-watcher.Events:
            w.ws.Broadcast("files:"+teamID, FileEvent{
                Event:     event.Op.String(),
                Path:      relativePath(event.Name, outputDir),
                Agent:     extractAgentSlug(event.Name),
                Size:      fileSize(event.Name),
                Timestamp: time.Now(),
            })
        case <-ctx.Done():
            return
        }
    }
}
```

### 6.3 File API (pres IPC)

Next.js posila requesty na crewshipd pres Unix socket:

```
GET /teams/{id}/files                    → seznam souboru (tree)
GET /teams/{id}/files/{path}             → stahnout soubor
GET /teams/{id}/files/{path}?preview=1   → preview (PDF/MD/image)
```

### 6.4 Agent memory persistence

```
SPATNE (puvodni navrh):
  /workspace/{agent}/.memory/   ← EPHEMERAL! Zmizi s kontejnerem.

SPRAVNE (v5.0):
  /output/{agent}/.memory/      ← PERSISTENT. Prezije restart kontejneru.
  /output/{agent}/.memory/context.jsonl   ← long-term memory entries
  /output/{agent}/.memory/embeddings/     ← optional vector cache (Phase 3)
```

Agent memory (`memory_enabled=true`) se uklada do `/output/` (persistent bind mount).
Claude Code state (`.claude/`) zustava v `/workspace/` (session-local, ok).

---

## 6A. SKILLS A MCP (Model Context Protocol)

> **Klicovy princip (ADR-014):** Skill = MCP Server wrapper.
> crewship-sidecar = MCP Gateway uvnitr kontejneru.
> Agent NEMA API klice pro nastroje — sidecar je injektuje per-request.

### 6A.1 Co je Skill v Crewship

Skill je balicek ktery dava agentovi SCHOPNOST. Technicky je to wrapper
kolem MCP serveru s doplnkovymi metadaty:

```
Skill = {
  MCP Server (tools + resources)       → CO umi delat
  System prompt fragment               → JAK to dela (instrukce pro LLM)
  Credential requirements              → JAKE klice potrebuje
  Konfigurace (per-agent)              → S jakymi parametry
  Dependencies (apt, pip, npm)         → CO je treba nainstalovat v image
}
```

**Priklady skills:**

| Skill | MCP Server | Tools | Credentials |
|---|---|---|---|
| GitHub Integration | @modelcontextprotocol/server-github | create_issue, list_prs, merge_pr... | GITHUB_TOKEN |
| Web Research | custom web-search MCP | web_search, scrape_url, summarize | SERP_API_KEY |
| Database Access | @modelcontextprotocol/server-postgres | query, list_tables, describe | DATABASE_URL |
| Slack Messaging | @modelcontextprotocol/server-slack | send_message, read_channel | SLACK_BOT_TOKEN |
| File Converter | custom file-convert MCP | pdf_to_text, csv_to_json | (zadne) |

### 6A.2 MCP architektura v kontejneru

```
┌──────────── Team Container ────────────────────────────────┐
│                                                             │
│  ┌─────────────┐        ┌────────────────────────────────┐ │
│  │ Agent        │        │ crewship-sidecar               │ │
│  │ (CLI tool    │        │ (localhost:9119)                │ │
│  │  nebo        │        │                                │ │
│  │  crewship-   │  MCP   │ Role 1: Delegacni proxy        │ │
│  │  agent)      │ stdio  │ Role 2: MCP Gateway (NOVA!)    │ │
│  │              │◄──────►│   - spousti MCP servery         │ │
│  │ NEMA tool    │        │   - injektuje credentials      │ │
│  │ credentials! │        │   - proxy MCP tool cally       │ │
│  │              │  HTTP   │   - tool search (on-demand)    │ │
│  │ Delegace:    │───────►│   - RBAC per tool              │ │
│  │ curl :9119   │        │   - audit log per tool call    │ │
│  └─────────────┘        │   - rate limiting              │ │
│                          └──────────┬─────────────────────┘ │
│                                     │ stdio                  │
│              ┌──────────────────────┼───────────────┐       │
│              │                      │               │       │
│         ┌────▼─────┐          ┌─────▼────┐    ┌─────▼────┐ │
│         │ github   │          │ web-     │    │ postgres │ │
│         │ MCP srv  │          │ search   │    │ MCP srv  │ │
│         │ (stdio)  │          │ MCP srv  │    │ (stdio)  │ │
│         │          │          │ (stdio)  │    │          │ │
│         │ GITHUB_  │          │ SERP_    │    │ DB_URL   │ │
│         │ TOKEN    │          │ API_KEY  │    │ injected │ │
│         │ injected │          │ injected │    │ by       │ │
│         │ by       │          │ by       │    │ sidecar  │ │
│         │ sidecar  │          │ sidecar  │    │          │ │
│         └──────────┘          └──────────┘    └──────────┘ │
│                                                             │
└─────────────────────────────────────────────────────────────┘
```

### 6A.3 MCP tool call flow

```
1. LLM rozhodne volat tool "github_create_issue"
2. Agent posle MCP tools/call pres stdio
3. crewship-sidecar ZACHYTI call (je MCP proxy):
   a) RBAC check: ma tento agent pravo na tool "github_create_issue"?
   b) Credential injection: prida GITHUB_TOKEN k MCP server env
   c) Rate limit check (per agent, per tool)
   d) Audit log: zapise {tool, agent, credential_id, timestamp}
4. Sidecar forward call do github MCP serveru (stdio)
5. GitHub MCP server provede API call (s injektovanym tokenem)
6. Vysledek se vrati pres sidecar zpet k agentovi
7. Agent dostane jen tool result, NIKDY credential
```

### 6A.4 Tool Search (on-demand discovery, ADR-016)

**Problem (identifikoval Anthropic, leden 2026):**
Agent s 5 skills × 10 toolu = 50 tool definic = ~25k tokenu PRED prvni otazkou.
S 10+ skills context window exploduje.

**Reseni:** Sidecar vystavuje meta-tool `search_tools`:

```json
{
  "name": "search_tools",
  "description": "Search available tools by capability. Use this instead of browsing all tools.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "query": {"type": "string", "description": "What capability you need (e.g. 'create github issue')"}
    },
    "required": ["query"]
  }
}
```

**Flow:**
```
1. Agent se spusti → dostane JEN 2 tool definice:
   - search_tools (meta-tool, vzdy)
   - delegate (delegacni tool, pokud leader)
   + tools ze skills s defer_loading=false (vzdy nactene)

2. LLM chce vytvorit GitHub issue → zavola search_tools("create github issue")

3. Sidecar vraci matching tool definice:
   [{"name": "github_create_issue", "description": "...", "inputSchema": {...}}]

4. LLM ted vi jak volat github_create_issue → posle MCP tools/call

5. Sidecar proxy call na github MCP server (viz 6A.3)
```

**Konfigurace per skill:**
```prisma
model Skill {
  defer_loading  Boolean @default(false)  // true = on-demand via tool search
}
```

- `defer_loading=false`: tool definice nacteny VZDY (pro kriticke tooly)
- `defer_loading=true`: tool definice nacteny az po search_tools callu

**Inspirace:**
- Anthropic "Tool Search Tool" beta (leden 2026)
- Anthropic "Programmatic Tool Calling" beta
- Docker MCP Gateway dynamic tool discovery (`mcp-find`)

### 6A.5 Sidecar spousti MCP servery

Kdyz se kontejner spusti, sidecar precte seznam skills pro tento tym
a spusti prislusne MCP servery:

```go
// cmd/crewship-sidecar/mcp_manager.go

type MCPServerProcess struct {
    SkillID   string
    Command   string          // "npx @modelcontextprotocol/server-github"
    Transport string          // "stdio"
    Process   *os.Process
    Stdin     io.WriteCloser
    Stdout    io.ReadCloser
    Tools     []MCPToolDef    // cached tool definitions
}

func (s *Sidecar) StartMCPServers(skills []SkillConfig) error {
    for _, skill := range skills {
        if skill.MCPServerCommand == "" {
            continue // skill bez MCP serveru (jen system prompt)
        }

        // Injektovat credentials do MCP server env
        env := os.Environ()
        for _, cred := range skill.Credentials {
            decrypted := s.decryptCredential(cred)
            env = append(env, cred.EnvVar+"="+decrypted)
        }

        cmd := exec.CommandContext(s.ctx, "sh", "-c", skill.MCPServerCommand)
        cmd.Env = env

        stdin, _ := cmd.StdinPipe()
        stdout, _ := cmd.StdoutPipe()

        if err := cmd.Start(); err != nil {
            return fmt.Errorf("MCP server %s start: %w", skill.Name, err)
        }

        // Cache tool definitions (tools/list call)
        tools := s.discoverTools(stdin, stdout)

        s.mcpServers[skill.ID] = &MCPServerProcess{
            SkillID:   skill.ID,
            Command:   skill.MCPServerCommand,
            Transport: skill.Transport,
            Process:   cmd.Process,
            Stdin:     stdin,
            Stdout:    stdout,
            Tools:     tools,
        }
    }
    return nil
}
```

### 6A.6 Credential flow pro MCP servery

```
1. Uzivatel priradi Skill "GitHub Integration" agentovi (UI)
2. Skill ma credential_requirements: [{"env_var": "GITHUB_TOKEN", "required": true}]
3. UI zobrazi: "Tento skill vyzaduje GITHUB_TOKEN" → uzivatel priradi credential
4. Credential ulozena v DB (AES-256-GCM sifrovana)

5. Kontejner se spusti:
   a) crewshipd posle sidecar seznam skills + encrypted credentials
   b) Sidecar desifruje credentials (Go, ENCRYPTION_KEY env var, ADR-013)
   c) Sidecar spusti MCP server s credentials jako env vars
   d) MCP server bezi s pristupem ke GitHub API
   e) Agent o GITHUB_TOKEN NEVI — vola jen MCP tools

6. Agent chce "github_create_issue":
   a) Agent → sidecar: tools/call
   b) Sidecar → github MCP server: tools/call (server uz ma GITHUB_TOKEN)
   c) MCP server → GitHub API (s tokenem)
   d) GitHub API → MCP server → sidecar → agent (jen result)
```

**Per-call audit trail:**
```jsonl
{"ts":"2026-02-15T10:00:01Z","type":"tool_call","agent":"anna","tool":"github_create_issue","skill":"github-integration","credential_id":"cred-uuid","duration_ms":450,"status":"ok"}
{"ts":"2026-02-15T10:00:02Z","type":"tool_call","agent":"anna","tool":"web_search","skill":"web-research","credential_id":"cred-uuid-2","duration_ms":1200,"status":"ok"}
```

### 6A.7 Skill typy

| Typ | MCP Server | System Prompt | Credentials | Priklad |
|---|---|---|---|---|
| **MCP Skill** | Ano (tools/resources) | Volitelny | Dle serveru | GitHub, Slack, DB |
| **Prompt Skill** | Ne | Ano (povinny) | Ne | "Write SEO content", "Code reviewer" |
| **Hybrid Skill** | Ano | Ano | Dle serveru | "Web Researcher" (tools + instrukce) |

Prompt-only skills nemaji MCP server — jsou jen system prompt fragment
ktery se prida do agentova system promptu. Toto je MVP format
(skills bez MCP, jen instrukce).

### 6A.8 Skill lifecycle

```
1. INSTALL:   Skill definice v DB (Skill tabulka), MCP server command
2. ASSIGN:    AgentSkill relace — agent ziskava skill
3. CONFIGURE: AgentSkill.config — per-agent parametry
4. ACTIVATE:  Sidecar spusti MCP server pri startu kontejneru
5. USE:       Agent vola MCP tools pres sidecar
6. DISABLE:   AgentSkill.enabled=false — MCP server se zastavi
7. UNASSIGN:  AgentSkill relace smazana, MCP server zastaven
```

### 6A.9 Fazovani MCP implementace

**Phase 1 (MVP):** Skills = jen system prompt fragmenty + credential requirements.
Zadne MCP servery. Tools jsou built-in v CLI toolu (Claude Code, OpenCode).

**Phase 2A:** Sidecar spousti MCP servery (stdio). Tool search. Credential injection.
Agent pouziva MCP tools pres sidecar. RBAC per tool. Audit trail per call.

**Phase 2B:** Credential-less agent (API-direct mode). Sidecar drzi vsechny
credentials — agent nema env vars s klici. Per-request credential injection.

**Phase 2B+:** srt sandbox per-MCP-server (ADR-017). OCI images (ADR-021).
Security pipeline pro marketplace skills (ADR-020).

**Phase 3:** Externi MCP servery (Streamable HTTP). Sdilene MCP servery
across tymu (napr. org-wide PostgreSQL). MCP Gateway pattern jako Docker.
**Skill Hub (Marketplace)** — browse, search, install, rate, review (ADR-019).

**Phase 3B:** Premium skills, revenue share, author portal, private skill
registry.

### 6A.10 Skill Hub (Marketplace)

> Viz ADR-019 (Skill Hub), ADR-020 (Security Pipeline), ADR-021 (OCI Images)
> pro zduvodneni rozhodnuti.

#### 6A.10.1 Prehled

Skill Hub je kuratorovany marketplace MCP serveru s bezpecnostnim auditem.
Kazdy skill projde 6-krokovym security pipeline pred tim nez ziska status
VERIFIED. Uzivatel browse, instaluje, a hodnotí skills primo z Crewship UI.

**3 tiers:**

| Tier | Kdo publishuje | Review | Badge | Priklad |
|---|---|---|---|---|
| **Official** | Crewship tym | Automated pipeline (manual volitelny) | ✓ Official | GitHub, Slack, Web Search |
| **Community** | Kdokoliv (submit GitHub repo) | Automated pipeline + manual review | ✓ Verified / Unverified | Jira Advanced, Notion Sync |
| **Private** | Organizace | Zadny (org odpovida) | 🔒 Private | Internal Salesforce MCP |

#### 6A.10.2 Security Pipeline (ADR-020)

Kazdy skill (official i community) projde pred VERIFIED statusem:

```
1. SOURCE VERIFICATION
   Sigstore / GitHub Attestations
   → overeni ze code pochazi od deklarovaneho autora

2. STATIC ANALYSIS
   Parsovani tool definic
   → hledani: raw SQL exec, rm -rf, shell exec bez omezeni,
     tool poisoning patterns (skryte instrukce v descriptions)

3. DEPENDENCY SCAN
   SBOM generace (CycloneDX), CVE scan (Trivy/Grype), license check
   → blokuje critical CVE, nekompatibilni licence

4. SANDBOX TEST RUN
   Spusteni MCP serveru pres srt v izolaci
   → monitoring network callu a FS pristupu
   → overeni ze skill nepotrebuje vic nez deklaruje v allowed_domains

5. MANUAL REVIEW
   Security tym (pro community submissions povinne)
   → kontrola tool definic, prompt fragmentu, flagged findings

6. CONTINUOUS MONITORING
   Re-scan pri kazdem update verze
   → auto-deprecation pri critical CVE v dependencies
```

Vysledek: `security_score` (0-100) a `verification` status.

#### 6A.10.3 Distribution — OCI Images (ADR-021)

Marketplace skills se distribuuji jako OCI (Docker) images:

```
Official:    ghcr.io/crewship-ai/skills/{name}:{version}
Community:   ghcr.io/crewship-ai/community/{name}:{version}
Private:     {org-registry}/{name}:{version}
```

Image obsahuje MCP server + vsechny dependencies. Prebuildovany Crewship
CI pipeline. Digest-verified pull (SHA256).

Fallback: `mcp_server_command` zustava pro Phase 1 a custom skills.
Pokud `oci_image` existuje, pouzije se. Jinak `mcp_server_command`.

#### 6A.10.4 srt Sandbox per-MCP-server (ADR-017)

Sidecar wrapne kazdy MCP server spusteni pres Anthropic Sandbox Runtime (`srt`):

```go
// sidecar spusti MCP server pres srt
func (s *Sidecar) startMCPServer(skill SkillConfig) (*MCPServer, error) {
    srtConfig := generateSrtConfig(skill) // z Skill.allowed_domains
    configPath := writeSrtConfig(srtConfig)

    cmd := exec.Command("srt",
        "--settings", configPath,
        skill.MCPServerCommand, // nebo OCI image entrypoint
    )
    cmd.Env = append(os.Environ(), skill.CredentialEnvVars...)
    // ...
}

// srt-settings.json (generovany z Skill definice):
// {
//   "filesystem": {
//     "denyRead": ["/output", "/workspace/other-agents"],
//     "allowWrite": ["/tmp"],
//     "denyWrite": ["/workspace", "/output", "/etc"]
//   },
//   "network": {
//     "allowedDomains": ["api.github.com", "*.github.com"]
//   }
// }
```

Efekt: MCP server pro GitHub NEMUZE volat Slack API. NEMUZE cist /output/.
Double sandboxing: Docker (kontejner) + srt (MCP server proces).

`srt` instalace v agent-runtime Dockerfile:
```dockerfile
RUN npm install -g @anthropic-ai/sandbox-runtime
```

#### 6A.10.5 End-to-end flow

```
1. PUBLISH
   Autor submitne skill (GitHub repo URL + metadata)
   → Security pipeline bezi (5-15 min)
   → Crewship prebuilduje OCI image
   → Skill v marketplace: VERIFIED nebo REJECTED

2. DISCOVER
   Uzivatel browse marketplace v Crewship UI
   → Filtruje: kategorie, rating, security_score, tagy, free/premium
   → Vidi: tool list, credential requirements, allowed_domains,
     downloads, recenze

3. INSTALL
   Uzivatel klikne "Add to Agent"
   → AgentSkill relace
   → UI zobrazi credential requirements (pokud nesplnene)
   → OCI image se pre-pullne do team kontejneru

4. RUN
   Agent pouzije skill
   → Sidecar spusti MCP server (z OCI image, pres srt sandbox)
   → Credentials injektovane sidecar (agent nevidi, ADR-015)
   → Tool call audit trail

5. UPDATE
   Nova verze skillu
   → Re-run security pipeline
   → Notifikace uzivateli
   → Auto-update (minor) nebo manual approve (major)
```

#### 6A.10.6 Vztah k Docker MCP Catalog

Docker MCP Catalog (2026, 270+ serveru) pouzijeme jako **upstream zdroj**:
importujeme verified Docker MCP images a re-scanujeme nasim pipeline.

| Aspect | Docker MCP Gateway | Crewship Skill Hub |
|---|---|---|
| Kde bezi | Externi kontejner na hostu | Sidecar UVNITR team kontejneru |
| Credentials | Docker secrets (host-level) | Sidecar inject (ADR-015) |
| Multi-tenant | Ne | Ano (per-team izolace) |
| RBAC | Per-server | Per-tool, per-agent |
| Audit | Basic logging | Per-call s credential_id, trace_id |
| Security | Publisher verification, SBOM | 6-krokovy pipeline + sandbox test |
| Monetizace | Ne | Free + Premium (revenue share) |
| Rating/reviews | Ne | Ano |

---

## 7. JOB STATE (bbolt WAL)

### 7.1 Proc bbolt

- Embedded KV store (zadny externi service)
- Write-ahead log — prezije crash Go service
- Singleproces — zadny concurrency problem
- ~50 MB RAM overhead

### 7.2 Co se uklada

```go
// Bucket: "agent_runs"
// Key: run_id (UUID)
// Value: JSON

type RunState struct {
    ID          string    `json:"id"`
    AgentID     string    `json:"agent_id"`
    SessionID   string    `json:"session_id"`
    Status      string    `json:"status"`
    StartedAt   time.Time `json:"started_at"`
    ContainerID string    `json:"container_id"`
    ExecID      string    `json:"exec_id"`
    LastActivity time.Time `json:"last_activity"`
}
```

### 7.3 Recovery po crashu

```go
func (o *Orchestrator) RecoverFromCrash(ctx context.Context) error {
    // 1. Nacist vsechny RUNNING runy z bbolt
    runs, _ := o.wal.GetByStatus("RUNNING")

    for _, run := range runs {
        // 2. Zkontrolovat jestli Docker exec jeste bezi
        inspect, err := o.docker.ContainerExecInspect(ctx, run.ExecID)
        if err != nil || !inspect.Running {
            // 3. Exec skoncil — updatovat stav
            o.wal.UpdateStatus(run.ID, "COMPLETED")
            o.notifyDB(run.ID, "COMPLETED")
            continue
        }
        // 4. Exec jeste bezi — znovu attachnout stdout
        o.reattachOutput(ctx, run)
    }
    return nil
}
```

---

## 8. GRACEFUL SHUTDOWN

```go
// cmd/crewshipd/main.go
func main() {
    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

    // ... start services ...

    <-sig
    log.Info("shutting down...")

    // 1. Stop accepting new WebSocket connections
    wsServer.Close()

    // 2. Stop accepting new agent runs
    orchestrator.StopAccepting()

    // 3. Wait for running agents to finish (max 30s)
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    orchestrator.WaitForRunning(ctx)

    // 4. Flush JSONL logs
    logCollector.Flush()

    // 5. Close bbolt
    wal.Close()

    // 6. Close Unix socket
    ipcServer.Close()

    log.Info("shutdown complete")
}
```

---

## 9. AGENT RUNTIME IMAGE

### 9.1 Dockerfile

```dockerfile
# docker/agent-runtime/Dockerfile
FROM ubuntu:24.04

# System deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl git jq openssh-client python3 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Node.js (pro CLI tools)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# CLI tools (VZDY pinovat konkretni verze!)
RUN npm install -g \
    @anthropic-ai/claude-code@1.x.x \
    @openai/codex@0.x.x
    # OpenCode: go install github.com/sst/opencode@latest

# Crewship binaries (sidecar + API-direct agent runtime)
COPY --from=builder /crewship-sidecar /usr/local/bin/crewship-sidecar
COPY --from=builder /crewship-agent /usr/local/bin/crewship-agent

# Landlock (per-agent filesystem izolace, Linux >=5.13)
COPY --from=builder /landrun /usr/local/bin/landrun

# Non-root user
RUN groupadd -g 1001 agent && useradd -u 1001 -g agent -m agent

# Directories
RUN mkdir -p /workspace /output && \
    chown agent:agent /workspace /output

USER agent
WORKDIR /workspace

HEALTHCHECK --interval=30s --timeout=5s CMD echo "alive"
```

### 9.2 Security hardening (pri vytvoreni kontejneru)

```
--read-only                          Read-only root filesystem
--security-opt no-new-privileges     Zadna eskalace privilegii
--cap-drop ALL --cap-add NET_RAW     Minimal capabilities
--memory 4g --cpus 2.0               Resource limits
--pids-limit 200                     Fork bomb protection
--tmpfs /tmp:rw,size=500m            Writeable /tmp s limitem
--network crewship-agents            Izolovana sit (--internal)
USER 1001:1001                       Non-root
```

---

## 10. TOOL PROFILES

| Profil | Claude Code flags | Codex CLI flags | Gemini CLI | OpenCode |
|---|---|---|---|---|
| MINIMAL | `--tools Read,Search,Grep` | `--sandbox` | prompt-based | agent config |
| CODING | (default) | (default) | prompt-based | agent config |
| MESSAGING | `--tools Read,Search,Web` | N/A | prompt-based | agent config |
| FULL | `--dangerously-skip-permissions` | `--full-auto` | prompt-based | agent config |

> **Pozor:** Tool profiles jsou enforcovane na urovni CLI nastroje.
> Docker kontejner je VZDY posledni obrana — i kdyz CLI nastroj selze v enforcovani.

---

## 11. TIMEOUTY A LIMITY

| Limit | Hodnota | Konfigurovatelne |
|---|---|---|
| Agent run timeout | 30 min (default) | Ano (per agent, `timeout_seconds`) |
| Container TTL | neomezene (default) | Ano (per team/org) |
| Max concurrent agents per team | 5 | Ano (per plan) |
| Max message length | 50,000 chars | Ne |
| Docker exec timeout | = agent timeout | Ne |
| WebSocket idle timeout | 60s (heartbeat) | Ne |

---

## 12. MONITORING (built-in)

| Nastroj | Co monitoruje | Jak |
|---|---|---|
| cAdvisor | CPU, RAM, disk, sit per kontejner | Separatni container, Prometheus scrape |
| Prometheus /metrics | Go service metriky | crewshipd nativne |
| fsnotify | Zmeny souboru v /output/ | Go → WebSocket real-time |
| Web terminal | SSH-like pristup do kontejneru | xterm.js + Docker exec API |
| Activity stream | Agent stdout real-time | Docker attach → WebSocket |
| Health checks | Je kontejner nazivu? | Docker healthcheck + Go ping |

---

## 13. API KEY FAILOVER (Credential Pool)

### 13.1 Problem

Agent bezi, pouziva Anthropic API klic. Klic se vycerpa (429 rate limit).
Bez failoveru agent selze a uzivatel musi rucne zmenit klic a restartovat.

### 13.2 Detekce rate limit erroru

```go
// internal/orchestrator/failover.go
func isRateLimitError(exitCode int, stderr string) bool {
    if exitCode != 1 {
        return false
    }
    patterns := []string{
        "rate limit", "rate_limit", "429",
        "too many requests", "quota exceeded",
        "insufficient_quota", "billing_hard_limit",
    }
    lower := strings.ToLower(stderr)
    for _, p := range patterns {
        if strings.Contains(lower, p) {
            return true
        }
    }
    return false
}
```

### 13.3 Failover flow

```
1. Agent run skonci s exit code 1
2. crewshipd precte stderr (posledni radky)
3. isRateLimitError() vrati true
4. crewshipd oznaci aktualni klic jako "cooldown" (5 min)
5. crewshipd vybere dalsi klic z poolu (viz DATABASE.md sekce 5)
6. Pokud zadny klic neni dostupny → run selze s "All API keys exhausted"
7. Pokud klic nalezen → restartuje agenta s novym klicem
8. Context preservation (viz 13.4)
```

### 13.4 Context preservation pri prepnuti klice

Kdyz se klic prepne, agent musi pokracovat kde skoncil. Jak se kontext zachova
zavisi na CLI adapteru:

| CLI adapter | Jak drzi kontext | Strategie pri restartu |
|---|---|---|
| Claude Code | `/workspace/.claude/` soubory | `claude --resume` — nativni persistence, kontext prezije |
| Codex CLI | JSON-RPC session | Session ztracena — poslat JSONL historii jako catch-up prompt |
| Gemini CLI | Stdin pipe | Session ztracena — poslat JSONL historii jako catch-up prompt |
| OpenCode | REST API session | Zavisí na `opencode serve` — TBD |

**Claude Code** ma nejlepsi podporu — soubory v `/workspace/.claude/` obsahuji
kompletni historii a `--resume` flag ji nacte automaticky.

**Pro ostatni CLI:** JSONL konverzacni historie slouzi jako backup:

```go
func (o *Orchestrator) buildCatchUpPrompt(sessionID string) string {
    history := o.readConversationJSONL(sessionID)
    lastMessages := history[max(0, len(history)-10):] // poslednich 10 zprav

    var summary strings.Builder
    summary.WriteString("Previous conversation context (key was rotated due to rate limit):\n\n")
    for _, msg := range lastMessages {
        summary.WriteString(fmt.Sprintf("[%s]: %s\n", msg.Role, truncate(msg.Content, 500)))
    }
    summary.WriteString("\nContinue where you left off.")
    return summary.String()
}
```

### 13.5 Cooldown management

```go
type CooldownManager struct {
    cooldowns map[string]time.Time // credentialID → cooldown until
    mu        sync.RWMutex
}

func (cm *CooldownManager) MarkCooldown(credID string, duration time.Duration) {
    cm.mu.Lock()
    defer cm.mu.Unlock()
    cm.cooldowns[credID] = time.Now().Add(duration)
}

func (cm *CooldownManager) IsInCooldown(credID string) bool {
    cm.mu.RLock()
    defer cm.mu.RUnlock()
    until, ok := cm.cooldowns[credID]
    return ok && time.Now().Before(until)
}
```

### 13.6 UI zobrazeni

Uzivatel vidi transparentni prechod v chat UI:

```
[10:00] Agent: Analyzing your codebase...
[10:01] Agent: Found 3 issues in auth module...
[10:02] ⚠️ API rate limit reached. Switching to backup key...
[10:02] Agent: Continuing analysis... (context preserved)
[10:03] Agent: Here's my report...
```

---

## 14. AGENT LOOP MODES (Phase 2)

Inspirovano mechanismem **Ralph Loop** (iterativni smycka v Claude Code).
Agent muze bezet v ruznych modech — ne jen jednorazove.

### 14.1 Mody

| Mod | Popis | Use case | Phase |
|---|---|---|---|
| `once` | Jednorazovy run (default) | Chat zprava, webhook trigger | MVP |
| `loop` | Opakovany run s intervalem | Monitoring, periodic reporting | Phase 2 |
| `until` | Opakovany run dokud podminka neni splnena | "Opravuj testy dokud neprochazi" | Phase 2 |

### 14.2 Loop mode (`loop`)

Agent se spusti opakovane s definovanym intervalem:

```
Agent "Monitor": loop mode, interval=1h
  [10:00] Run 1: "Zkontroluj stav serveru" → report
  [11:00] Run 2: "Zkontroluj stav serveru" → report
  [12:00] Run 3: "Zkontroluj stav serveru" → report
  ...
```

Implementace: crewshipd scheduler (Go ticker), NE cron.
Kazdy run = novy Docker exec se stejnym promptem.
Vysledky se kumuluji v /output/.

### 14.3 Until mode (`until`)

Agent bezi opakovane dokud neni splnena podminka (completion criteria):

```
Agent "QA": until mode, condition="all tests passing"
  [10:00] Run 1: "Oprav failing testy" → opravil 2/5
  [10:05] Run 2: "Oprav failing testy" → opravil 4/5
  [10:10] Run 3: "Oprav failing testy" → 5/5 passing ✅ DONE
```

Completion criteria mohou byt:
- **file_exists** — `file_exists:/output/reports/q1.pdf`
- **exit_code** — agent run skonci s exit code 0
- **custom** — agent sam vyhodnoti ("Tests passing") a vraci specialni marker
- **max_iterations** — bezpecnostni limit (default: 20)
- **max_duration** — casovy limit (default: 2h)

### 14.4 Kontext mezi iteracemi

Kazda iterace cte JSONL historii predchozi iterace:

```go
func (o *Orchestrator) runLoopIteration(agent Agent, iteration int) {
    prompt := agent.SystemPrompt
    if iteration > 1 {
        // Nacti JSONL z predchozi iterace
        prevHistory := o.readPreviousIteration(agent.ID, iteration-1)
        prompt += "\n\n[Previous iteration result]:\n" + summarize(prevHistory)
    }
    o.RunAgent(ctx, AgentRunRequest{
        ...
        UserMessage: prompt,
        Metadata: map[string]any{
            "loop_iteration": iteration,
            "loop_mode":      agent.LoopMode,
        },
    })
}
```

### 14.5 DB zmeny pro loop modes (Phase 2)

```prisma
// Pridat do Agent modelu:
  loop_mode        String?  @default("once")  // "once" | "loop" | "until"
  loop_interval_s  Int?     // interval v sekundach (pro loop mode)
  loop_max_iter    Int?     @default(20)  // max iteraci (bezpecnostni limit)
  loop_condition   String?  // completion criteria (pro until mode)
```

---

## 15. ORCHESTRACNI RUNTIME (Phase 2)

> Plna specifikace: `prd/ORCHESTRATION.md`
> Tato sekce popisuje runtime aspekty — jak crewshipd technicky provadi delegace.

### 15.1 Crew Leader execution

Leader bezi jako normalni agent (Docker exec) se specialnim system promptem.
Rozdil je v **delegacich pres crewship-sidecar** (localhost:9119 HTTP API).
Viz ADR-001 v2.

```
Leader execution flow:
  1. crewshipd spusti Docker exec pro leadera (sidecar uz bezi v kontejneru)
  2. crewshipd cte stdout → streaming k uzivatel pres WebSocket + JSONL log
  3. Leader deleguje pres HTTP na sidecar:
     - CLI mode: `curl -X POST localhost:9119/delegate -d '...'`
     - API-direct mode: nativni HTTP call (DelegateTool)
  4. Sidecar validuje (RBAC, circuit breaker) a posila do crewshipd
  5. crewshipd spusti worker Docker exec ve STEJNEM kontejneru
  6. Worker dokonci → crewshipd posle vysledek do sidecar
  7. Leader polluje GET localhost:9119/results/{group}
  8. Leader agreguje a odpovida uzivatel (stdout → WebSocket)
```

**Leader modes (ADR-004):**
- **active** (default): Leader bezi celou dobu, rozhoduje v real-time
- **passive**: Leader se spusti 2x (init task plan + finalize agregace)
- Viz ORCHESTRATION.md sekce 5.6 pro detaily

**Paralelni delegace pres sidecar:**
```bash
# Leader posle 2 delegace:
curl -X POST localhost:9119/delegate -d '{"target":"bob","task":"Data z Twitteru","group":"data"}'
curl -X POST localhost:9119/delegate -d '{"target":"eve","task":"Data z LinkedInu","group":"data"}'
# Ceka na vysledky:
curl localhost:9119/results/data  # → {"status":"completed","results":[...]}
```
crewshipd spusti oba Docker exec paralelne, ceka az oba skonci.

### 15.2 Virtual Director execution (lightweight)

Director **nepouziva Docker kontejner**. Bezi jako cisty LLM API call v crewshipd:

```go
// internal/orchestrator/director.go
func (o *Orchestrator) RunDirector(ctx context.Context, req DirectorRequest) error {
    // 1. Sestav system prompt s informacemi o tymech
    systemPrompt := o.buildDirectorSystemPrompt(req.OrgID)
    
    // 2. LLM API call primo (ne Docker exec)
    response, err := o.llmClient.Chat(ctx, LLMRequest{
        Model:        req.Model,
        SystemPrompt: systemPrompt,
        Messages:     req.Messages,
    })
    
    // 3. Parsuj delegacni prikazy
    commands, text := ParseDelegationCommands(response.Content)
    
    // 4. Stream text k uzivatel, proved delegace na leadery
    o.ws.Send(req.SessionID, AgentEvent{Type: "text", Content: text})
    for _, cmd := range commands {
        go o.executeDelegation(ctx, cmd, req.SessionID)
    }
    
    return nil
}
```

**Proc bez kontejneru:**
- Director jen premysli a deleguje — nepise kod, nepouziva tools
- Mensi latence (zadny Docker exec overhead)
- Jednodussi credentials (pouziva org-level LLM key)
- Phase 3: Director muze dostat vlastni kontejner pokud bude potrebovat tools

### 15.3 Delegacni protokol (Sidecar HTTP API)

> Plna specifikace: ORCHESTRATION.md sekce 5.3

Agenti (leader/director) posilaji delegace na crewship-sidecar (localhost:9119):

```
POST /delegate    — delegovat ukol na workera
POST /ask         — polozit otazku
POST /broadcast   — fire-and-forget zprava
GET  /results/:id — vyzvednout vysledky (polling)
GET  /status      — stav aktivnich delegaci
```

Stdout zustava cisty pro user-facing output. Agent vi o sidecar
pres env var `CREWSHIP_SIDECAR=http://localhost:9119`.

Viz ORCHESTRATION.md sekce 5.3 pro kompletni API specifikaci.

### 15.4 Timeouty

| Scenario | Timeout |
|---|---|
| Worker vykonava delegovany ukol | worker.timeout_seconds (default 1800s) |
| Leader ceka na workera | delegation_timeout_s NEBO 2x worker timeout |
| Director ceka na leadera | 2x leader delegation timeout |
| Max delegacni hloubka | max_delegation_depth (default 3) |
| Max paralelni delegace | max_parallel_delegates (default 5) |
| Max turns per delegace | 10 (hardcoded safety limit) |

### 15.5 Error handling

```
Worker selze → leader dostane error zpravu, muze:
  a) Zkusit jineho workera v tymu
  b) Zkusit sam
  c) Reportovat uzivatel

Leader selze → director dostane error, muze:
  a) Zkusit jiny tym
  b) Informovat uzivatel a navrhnout alternativu

Deadlock prevence:
  - Agent nemuze delegovat sam na sebe
  - Circular delegace detekovana (A→B→A = error)
  - Max depth limit (default 3)
```

---

## 16. CONTAINER RUNTIME SECURITY (ADR-003)

### 16.1 Runtime volba

Crewship podporuje dva container runtime:

| | runc (default) | runsc (gVisor) |
|---|---|---|
| **Izolace** | Linux namespaces + cgroups | Syscall interception (user-space kernel) |
| **Performance** | Nativni (0% overhead) | 5-50% overhead (I/O heavy = vice) |
| **Kernel exploit** | Mozny (sdileny kernel) | Blokovany (gVisor kernel) |
| **Setup** | Zadny (soucast Docker) | Nutna instalace runsc na host |
| **Use case** | Self-hosted, duveryhodne agenty | Multi-tenant SaaS, neduveryhodne agenty |
| **Doporuceeni** | **MVP + self-hosted enterprise** | **Multi-tenant SaaS (Phase 3+)** |

**Konfigurace:**
```bash
CREWSHIP_RUNTIME=runc    # default — standard Docker runtime
CREWSHIP_RUNTIME=runsc   # optional — gVisor (syscall filtering)
```

### 16.2 Landlock per-agent filesystem izolace (ADR-010)

**Problem:** 1 kontejner = 1 tym. Agenti sdili filesystem. Agent A muze
cist/mazat soubory agenta B v /workspace/. Prompt injection = sabotaz.

**Reseni:** Landlock LSM (Linux kernel >=5.13) — per-process filesystem izolace.

```bash
# Kazdy Docker exec pro agenta "bob" se spusti s Landlock:
docker exec team-container \
  landrun \
    --ro /workspace/bob \         # read-only: vlastni workspace
    --rw /workspace/bob/scratch \ # read-write: scratch prostor
    --ro /output \                # read-only: sdileny output (cteni vysledku)
    --rw /output/bob \            # read-write: vlastni output
    --deny /workspace/alice \     # deny: workspace jineho agenta
    --deny /workspace/charlie \   # deny: workspace jineho agenta
    -- claude --print "..."
```

**Proc Landlock a ne gVisor:**
- gVisor = cely user-space kernel (overkill pro inter-agent izolaci)
- Landlock = kernel-native, ZERO performance overhead
- Landlock nevyzaduje root/privilegia — agent sam omeji sve prava
- V Linux kernel od 5.13 (2021), stabilni, pouziva se v produkcich

**Inspirace:** Anthropic `sandbox-runtime` (`@anthropic-ai/sandbox-runtime`)
pouziva `bubblewrap` na Linuxu pro filesystem + network izolaci agentů.
Lekce z CVE-2025-66479: default DENY, explicitni ALLOW. Nikdy naopak.

**Implementace v crewshipd:**
```go
func (o *Orchestrator) buildExecCommand(req AgentRunRequest) []string {
    if req.EnableLandlock {
        return append([]string{
            "landrun",
            "--ro", "/workspace/" + req.AgentSlug,
            "--rw", "/workspace/" + req.AgentSlug + "/scratch",
            "--ro", "/output",
            "--rw", "/output/" + req.AgentSlug,
            "--",
        }, o.buildAgentCommand(req)...)
    }
    return o.buildAgentCommand(req)
}
```

**Fazovani:** Phase 2 (Landlock vyzaduje Linux >=5.13, macOS nepodporovan).
Pro dev na Macu: Landlock se preskoci (feature flag `CREWSHIP_LANDLOCK=true|false`).

### 16.3 Security layers (souhrn)

```
Layer 1: Container runtime   runc (default) nebo runsc (gVisor, optional Phase 3)
Layer 2: Non-root user       --user=1001:1001, zadny sudo, zadny setuid
Layer 3: Capabilities        --cap-drop=ALL (+ NET_RAW pro DNS)
Layer 4: Privilege escalation --security-opt=no-new-privileges
Layer 5: Filesystem (kontejner) --read-only root, tmpfs /tmp, /workspace volume
Layer 6: Filesystem (per-agent) Landlock LSM — agent vidi jen svuj workspace (Phase 2)
Layer 7: Network              --network=crewship-agents (--internal)
Layer 8: Network allowlist    nftables: jen LLM API IPs (api.anthropic.com, etc.)
Layer 9: PID limit            --pids-limit=200 (fork bomb protection)
Layer 10: Resource limits      --memory, --cpus (per team konfigurovatelne)
Layer 11: RBAC                sidecar + crewshipd validuji kazdy prikaz
Layer 12: Sidecar auth        localhost-only (127.0.0.1:9119), session token
Layer 13: Credential isolation Go service desifruje (ne Next.js), ENV var per-exec
```

### 16.3 gVisor instalace (pro ty co to potrebuji)

```bash
# Na hostu (Linux only — gVisor na macOS nepodporovan):
curl -fsSL https://gvisor.dev/archive.key | sudo apt-key add -
sudo add-apt-repository "deb https://storage.googleapis.com/gvisor/releases release main"
sudo apt-get install -y runsc

# Registrace v Docker:
sudo runsc install
sudo systemctl restart docker

# Overeni:
docker run --runtime=runsc hello-world
```

> **Pozn:** gVisor NEPODPORUJE macOS. Pro lokalni vyvoj na Macu pouzijte runc.
> gVisor je urcen pro Linux produkcni servery.

### 16.4 Alternativni runtime (future consideration)

| Runtime | Boot time | Izolace | Poznamka |
|---|---|---|---|
| **Firecracker** | ~125ms | MicroVM (plny kernel) | Pouziva AWS Lambda, Fly.io |
| **Kata Containers** | ~500ms | QEMU/Cloud Hypervisor | OCI-kompatibilni |
| **gVisor** | ~0ms (v kontejneru) | Syscall interception | Pouziva GCP Cloud Run |

Pro Phase 3+ zvazit Firecracker pro scenare kde gVisor nestaci
(napr. kernel-level izolace pro enterprise multi-tenant).

---

## 17. OTEVRENE OTAZKY

### Uzavrene (rozhodnuto)

2. ~~**Agent-to-agent komunikace:**~~ → Loopback HTTP sidecar (ADR-001 v2). Viz ORCHESTRATION.md 5.1.
8. ~~**Delegacni format:**~~ → HTTP JSON API na sidecar (ADR-001 v2).
9. ~~**Context window:**~~ → Worker output compression (ADR-005). Viz ORCHESTRATION.md 5.7.
6. ~~**Memory across sessions:**~~ → /output/{agent}/.memory/ (persistent). Viz 6.4.
15. ~~**Skill architektura:**~~ → Skill = MCP Server wrapper (ADR-014). Viz 6A.
16. ~~**Tool credentials:**~~ → Sidecar drzi, agent nevidi (ADR-015). Viz 6A.6.
17. ~~**Tool discovery scaling:**~~ → Tool search on-demand (ADR-016). Viz 6A.4.

### Stale otevrene

1. **Multi-agent v jednom kontejneru** — jak izolovat /workspace/ per agent? Docker exec s `--workdir` — ok?
3. **GPU podpora** — Phase 3: `--gpus` flag pro ML agenty?
4. **Custom agent images** — uzivatel si muze prinest vlastni Docker image? Bezpecnostni implikace?
5. **Streaming format** — Claude Code ma jiny stdout format nez Codex. Univerzalni parser?
7. **Director kontejner** — Phase 3: kdyz director dostane tools, jaky kontejner? Dedicovany "org kontejner"?
10. **Leader volba** — Muze uzivatel zmenit leadera za behu? Co s probihajicimi delegacemi?
11. **Landlock na macOS** — Landlock je Linux-only. Pro macOS dev: feature flag `CREWSHIP_LANDLOCK=false`.
12. **Sidecar healthcheck** — Jak crewshipd detekuje ze sidecar spadl a restartuje ho?
13. **crewship-agent tool coverage** — Pokryva crewship-agent vsechny use cases co Claude Code? LSP integrace?
14. **Image size** — S CLI tools + crewship binaries + MCP servery: ~700MB. Zvazit lazy install.
18. **MCP server crash** — Co kdyz MCP server spadne? Sidecar restart? Agent retry?
19. **Externi MCP servery** — Phase 3: sdilene MCP servery across tymu. Streamable HTTP transport.
20. **MCP server resource limits** — Max MCP serveru per kontejner? Memory/CPU limity per server?
