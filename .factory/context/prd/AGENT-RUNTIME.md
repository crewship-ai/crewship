# Crewship -- Agent Runtime (AGENT-RUNTIME.md)

**Verze:** 2.0
**Datum:** 2026-02-11

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
5. CLI adapter — Claude Code / Codex CLI / Gemini CLI / OpenCode
6. LLM provider — Anthropic / OpenAI / Google / Ollama
```

> **KRITICKE:** crewshipd NIKDY nepristupuje k Docker/filesystem/bbolt primo.
> Vse jde pres provider interface. Viz `K8S-READINESS.md` pro kompletni specifikaci.

### Flow: uzivatel posle zpravu agentovi

```
1. Uzivatel napise v chat UI: "Vytvor report"
2. Browser → WebSocket → crewshipd (Go)
3. crewshipd overí ze kontejner tymu bezi (pokud ne, spusti)
4. crewshipd zavola Docker exec v kontejneru:
   docker exec -e OPENAI_API_KEY=... -w /workspace/anna team-container \
     claude-code --print "Vytvor report"
5. crewshipd attachne na stdout/stderr (Docker attach API)
6. Agent stdout → crewshipd parsuje → WebSocket → Browser (real-time)
7. Agent pise soubory do /output/ → fsnotify → WebSocket → Browser
8. Kazdy radek stdout → append do JSONL log souboru
9. Po dokonceni: crewshipd updatne AgentRun status v DB (pres IPC)
```

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
    resp, err := m.client.ContainerCreate(ctx, &container.Config{
        Image: "ghcr.io/crewship-ai/agent-runtime:latest",
        User:  "1001:1001",  // non-root
        Env: []string{
            "CREWSHIP_TEAM_ID=" + team.ID,
        },
    }, &container.HostConfig{
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

    return m.client.ContainerStart(ctx, resp.ID, container.StartOptions{})
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

### 3.3 Credential injection

```go
func (o *Orchestrator) buildEnvVars(req AgentRunRequest) []string {
    env := []string{
        "CREWSHIP_AGENT_ID=" + req.AgentID,
        "CREWSHIP_TEAM_ID=" + req.TeamID,
        "CREWSHIP_SESSION_ID=" + req.SessionID,
    }

    // Credentials — desifrovane Next.js posle pres IPC
    for _, cred := range req.Credentials {
        env = append(env, cred.EnvVarName+"="+cred.PlaintextValue)
    }

    return env
}
```

> **KRITICKE:** Credentials se NIKDY neukladaji na disk. Existuji jen jako ENV vars
> po dobu behu exec procesu. Po skonceni procesu zmizi.

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
  ├── .claude/               ← Claude Code state
  ├── scratch/               ← docasne soubory
  └── ...

/output/{agent-slug}/        ← PERSISTENT (bind mount on host)
  ├── reports/
  │   └── january-2026.pdf
  ├── code/
  │   └── fix-memory-leak.patch
  └── data/
      └── analysis.csv
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
    # Gemini CLI a OpenCode — instalace dle jejich dokumentace

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

## 15. OTEVRENE OTAZKY

1. **Multi-agent v jednom kontejneru** — jak izolovat /workspace/ per agent? Diky Docker exec s `--workdir` — ok?
2. **Agent-to-agent komunikace** — Phase 2: sdileny adresar? Message passing? gRPC?
3. **GPU podpora** — Phase 3: `--gpus` flag pro ML agenty?
4. **Custom agent images** — uzivatel si muze prinest vlastni Docker image? Bezpecnostni implikace?
5. **Streaming format** — Claude Code ma jiny stdout format nez Codex. Univerzalni parser?
6. **Memory across sessions** — `memory_enabled` flag: kde ukladat agent memory? /workspace/.memory/?
