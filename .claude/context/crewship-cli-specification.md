# Crewship CLI — Specifikace (IMPLEMENTED)

> **Status**: Fully implemented as of 2026-02-24. All commands below are live and tested.
> See Linear project "Crewship CLI" for future roadmap items.

## Kontext

Crewship je AI agent orchestration platforma. CLI umožňuje plnou správu agentů, crews, missions, skills a logů přímo z terminálu.

**Cíl**: CLI jako first-class citizen vedle web UI. Uživatel může kompletně řídit Crewship z příkazové řádky — spouštět agenty, sledovat průběh, importovat skills, prohlížet logy.

**Implementační jazyk**: Go s knihovnou `cobra` (github.com/spf13/cobra)

**Architektura**: CLI je HTTP klient, který volá existující REST API Crewship serveru. Server MUSÍ běžet (`crewship start`). CLI se připojuje na `http://localhost:8080` (konfigurovatelné).

---

## Architektonické rozhodnutí (IMPLEMENTED)

### CLI Framework: cobra ✅

Používá `github.com/spf13/cobra` s nested subcommands, persistent flags, auto-generated help/completion.

### Autentizace CLI → Server ✅

CLI token system (`crewship_cli_` prefix + 40 hex chars, SHA-256 hashed v DB):
```bash
crewship login                        # Interaktivní: email + heslo → CSRF flow → CLI token
crewship login --token <api-token>    # Neinteraktivní: přímé nastavení
```
- Token uložen v `~/.crewship/cli-config.yaml`
- Posílán jako `Authorization: Bearer <token>` header
- Server endpoints: `POST/GET/DELETE /api/v1/auth/cli-token*`

### Výstupní formáty

Všechny příkazy podporují:
```
--format table    # Výchozí: lidsky čitelná tabulka (tabwriter)
--format json     # Strojově čitelný JSON
--format yaml     # YAML (pro config export)
--format quiet    # Jen ID (pro scriptování: crewship agent list -q | xargs ...)
```

### Globální flagy (persistent na root command)

```
--server, -s      # Server URL (default: http://localhost:8080, env: CREWSHIP_SERVER)
--workspace, -w   # Workspace ID nebo slug (default: z config, env: CREWSHIP_WORKSPACE)
--format, -f      # Výstupní formát: table|json|yaml|quiet (default: table)
--verbose, -v     # Verbose output (debug logging)
--no-color        # Disable ANSI colors (auto-detected z NO_COLOR env)
```

### Config soubor

`~/.crewship/cli-config.yaml`:
```yaml
server: http://localhost:8080
workspace: my-workspace          # default workspace slug
token: crewship_cli_xxxxxxxxxxxx # auth token
format: table                    # default output format
```

Priorita: CLI flag > env var > config file > default

---

## Příkazy — Kompletní reference

### 1. `crewship login`

Autentizace CLI vůči serveru.

```bash
crewship login                           # Interaktivní (email + heslo prompt)
crewship login --token <token>           # Nastavit API token přímo
crewship logout                          # Smazat uložený token
crewship whoami                          # Zobrazit aktuální uživatel + workspace
```

**Implementace**:
- `login` interaktivní: `POST /api/auth/callback/credentials` s email/password → získá session token
- `login --token`: Uloží token do config souboru, validuje `GET /api/v1/workspaces`
- `whoami`: `GET /api/v1/workspaces` + zobrazí user info z session
- Token uložit do `~/.crewship/cli-config.yaml`

**API endpointy**:
- `POST /api/auth/callback/credentials` (existující)
- `GET /api/v1/workspaces` (existující)
- **NOVÝ**: `POST /api/v1/auth/cli-token` — vygeneruje long-lived API token (nikdy neexpiruje, revokatelný)

**Výstup whoami**:
```
User:      pavel@example.com
Workspace: my-workspace (cuid_xxx)
Role:      OWNER
Server:    http://localhost:8080
```

---

### 2. `crewship agent` — Správa agentů

```bash
crewship agent list                      # Seznam všech agentů ve workspace
crewship agent get <slug-or-id>          # Detail jednoho agenta
crewship agent create --name "..." ...   # Vytvořit agenta
crewship agent update <slug> --name "..."# Aktualizovat agenta
crewship agent delete <slug>             # Smazat agenta (soft delete)
crewship agent runs <slug>               # Seznam běhů agenta
```

**API endpointy**:
- `GET /api/v1/agents?workspace_id=X` (existující)
- `GET /api/v1/agents/{agentId}` (existující)
- `POST /api/v1/agents` (existující)
- `PATCH /api/v1/agents/{agentId}` (existující)
- `DELETE /api/v1/agents/{agentId}` (existující)
- `GET /api/v1/agents/{agentId}/runs` (existující)

**Flags pro `agent create`**:
```
--name            # Název agenta (povinný)
--slug            # Slug (auto-generovaný z name pokud chybí)
--crew            # Crew slug nebo ID (volitelný)
--role            # agent_role: AGENT|LEAD|COORDINATOR (default: AGENT)
--role-title      # Lidský popis role ("Backend Developer")
--cli-adapter     # CLAUDE_CODE|CODEX_CLI|GEMINI_CLI|OPENCODE (default: CLAUDE_CODE)
--system-prompt   # System prompt text nebo @soubor.txt pro načtení ze souboru
--tool-profile    # MINIMAL|CODING|MESSAGING|FULL (default: CODING)
--memory          # Zapnout memory (default: false)
--lead-mode       # active|passive (jen pro LEAD)
--timeout         # Timeout v sekundách (default: 300)
```

**Příklad**:
```bash
crewship agent create \
  --name "React Developer" \
  --crew backend-crew \
  --role AGENT \
  --role-title "Frontend Specialist" \
  --cli-adapter CLAUDE_CODE \
  --system-prompt @prompts/react-dev.txt \
  --tool-profile CODING \
  --memory
```

**Výstup `agent list` (table)**:
```
SLUG              ROLE   CREW           STATUS   ADAPTER      MEMORY
react-developer   AGENT  backend-crew   IDLE     CLAUDE_CODE  on
viktor-backend    AGENT  backend-crew   BUSY     CLAUDE_CODE  on
lead-architect    LEAD   backend-crew   IDLE     CLAUDE_CODE  on
```

**Výstup `agent get` (table)**:
```
Name:           React Developer
Slug:           react-developer
ID:             cuid_xxxxxxxxx
Role:           AGENT
Role Title:     Frontend Specialist
Crew:           backend-crew
Status:         IDLE
CLI Adapter:    CLAUDE_CODE
Tool Profile:   CODING
Memory:         on
Timeout:        300s
Created:        2026-02-15 14:30:00
Last Run:       2026-02-20 09:15:22
Skills:         react-patterns, css-advanced (2)
Credentials:    ANTHROPIC_API_KEY (1)
```

---

### 3. `crewship crew` — Správa crews

```bash
crewship crew list                       # Seznam crews
crewship crew get <slug>                 # Detail crew
crewship crew create --name "..." ...    # Vytvořit crew
crewship crew update <slug> ...          # Aktualizovat
crewship crew delete <slug>              # Smazat
crewship crew status [<slug>]            # Živý status crew (agenti, běhy, assignments)
```

**API endpointy**:
- `GET /api/v1/crews?workspace_id=X` (existující)
- `GET /api/v1/crews/{crewId}` (existující)
- `POST /api/v1/crews` (existující)
- `PATCH /api/v1/crews/{crewId}` (existující)
- `DELETE /api/v1/crews/{crewId}` (existující)
- `GET /api/v1/crews/{crewId}/assignments` (existující)
- `GET /api/v1/crews/{crewId}/peer-conversations` (existující)
- `GET /api/v1/crews/{crewId}/escalations` (existující)
- `GET /api/v1/crews/{crewId}/standup` (existující)

**Flags pro `crew create`**:
```
--name            # Název (povinný)
--slug            # Slug (auto z name)
--description     # Popis
--color           # Hex barva (#3B82F6)
--icon            # Emoji
--memory-mb       # Container memory limit (default: 512)
--cpus            # Container CPU limit (default: 1.0)
```

**`crew status` — speciální příkaz (compound view)**:

Tento příkaz agreguje data z více endpointů a zobrazí kompletní přehled:

```
Crew: backend-crew (Backend Development Team)
Container: crewship-crew-backend-crew [RUNNING] (256MB / 1.0 CPU)

AGENTS (3):
  SLUG              ROLE   STATUS   LAST RUN
  lead-architect    LEAD   IDLE     5m ago
  viktor-backend    AGENT  BUSY     running now
  react-developer   AGENT  IDLE     2h ago

RECENT ASSIGNMENTS (last 5):
  #  TASK                        FROM → TO            STATUS     DURATION
  1  "Build REST API"            lead → viktor        COMPLETED  12m
  2  "Write Dockerfile"          lead → viktor        RUNNING    3m
  3  "Create React components"   lead → react-dev     PENDING    -

ESCALATIONS (open):
  None

STANDUP SUMMARY:
  Viktor: Working on Dockerfile, completed REST API endpoints.
  React Dev: Idle, waiting for assignment.
```

Implementace: Paralelně volat 4+ endpointů, agregovat do jednoho view.

---

### 4. `crewship run` — Spuštění agenta

```bash
crewship run <agent-slug> "prompt text"              # Jednorázový běh
crewship run <agent-slug> --prompt @task.txt         # Prompt ze souboru
crewship run <agent-slug> --interactive              # Interaktivní chat (streaming)
crewship run <agent-slug> --chat <chatId> "follow-up" # Pokračovat v existujícím chatu
```

**Toto je nejsložitější příkaz.** Vyžaduje WebSocket pro streaming výstupu.

**Implementace**:
1. Vytvořit chat: `POST /api/v1/agents/{agentId}/chats`
2. Získat WS token: `GET /api/v1/ws-token`
3. Připojit se na WebSocket: `ws://localhost:8080/ws?token=X`
4. Subscribe na kanál `agent:{agentId}`
5. Poslat zprávu přes WS: `{"type": "send_message", "channel": "agent:{id}", "payload": {"content": "..."}}`
6. Streamovat výstup do terminálu (NDJSON eventy)
7. Po dokončení odpojit

**Event typy ze serveru** (zobrazit v terminálu):
- `text` → plain text výstup agenta
- `thinking` → myšlenkový proces (zobrazit šedě/dimmed)
- `tool_call` → volání nástroje (zobrazit jako `[tool: read_file] path/to/file`)
- `tool_result` → výsledek nástroje (summary)
- `done` → konec běhu
- `error` → chyba

**Výstup streamingu (terminal)**:
```
$ crewship run viktor-backend "Create a REST API for calculator"

[agent: viktor-backend] Starting run...
[thinking] Let me analyze the requirements for a calculator REST API...
[tool: write_file] internal/calculator/handler.go
[tool: write_file] internal/calculator/calculator.go
[tool: write_file] internal/calculator/calculator_test.go

I've created the calculator REST API with the following endpoints:
- POST /api/calculate — performs calculation
- GET /api/history — returns calculation history

All tests pass. The implementation uses...

[done] Run completed in 45s (tokens: 12,340)
```

**Interaktivní mód (`--interactive`)**:
- Po `done` eventu zobrazit prompt `> ` pro další zprávu
- Ctrl+C pro ukončení
- Ctrl+D pro ukončení chatu
- Historie chatu persistuje na serveru

**Flags**:
```
--prompt, -p     # Prompt text nebo @soubor.txt
--interactive    # Interaktivní chat mód
--chat           # Pokračovat v existujícím chatu (chat ID)
--no-stream      # Počkat na dokončení, zobrazit jen výsledek
--timeout        # Override agent timeout (sekundy)
--quiet, -q      # Jen výsledný text, bez meta informací
```

---

### 5. `crewship logs` — Prohlížení logů

```bash
crewship logs <agent-slug>               # Poslední logy agenta
crewship logs <agent-slug> --follow      # Stream logů (tail -f styl)
crewship logs <agent-slug> --run <runId> # Logy konkrétního běhu
crewship logs <agent-slug> --lines 50    # Počet řádků (default: 100)
```

**API endpointy**:
- `GET /api/v1/agents/{agentId}/logs` (existující — proxy k crewshipd)
- WebSocket subscribe na `agent:{id}` kanál pro `--follow`

**Výstup**:
```
2026-02-20 09:15:22 [RUN cuid_xxx] Started
2026-02-20 09:15:23 [THINK] Analyzing calculator requirements...
2026-02-20 09:15:25 [TOOL] write_file: internal/calculator/handler.go
2026-02-20 09:15:30 [TEXT] I've created the calculator REST API...
2026-02-20 09:16:07 [RUN cuid_xxx] Completed (45s, 12340 tokens)
```

---

### 6. `crewship skill` — Správa skills

```bash
crewship skill list                      # Seznam skills ve workspace
crewship skill import <url>              # Import skill z URL
crewship skill import --file skill.md    # Import z lokálního souboru
crewship skill get <slug>                # Detail skill
crewship skill assign <skill-slug> <agent-slug>   # Přiřadit skill agentovi
crewship skill unassign <skill-slug> <agent-slug> # Odebrat skill
```

**API endpointy**:
- `GET /api/v1/skills?workspace_id=X` (existující)
- `POST /api/v1/workspaces/{workspaceId}/skills/import` (existující)
- `POST /api/v1/agents/{agentId}/skills` (existující)
- `DELETE /api/v1/agents/{agentId}/skills/{skillId}` (existující)

**Flags pro `skill import`**:
```
--file, -f       # Cesta k lokálnímu SKILL.md souboru
--url            # URL pro import (GitHub shorthand: owner/repo/path.md)
```

**Výstup `skill list` (table)**:
```
SLUG              CATEGORY   VERSION   SOURCE       AGENTS
react-patterns    CODING     1.0.0     MARKETPLACE  2
css-advanced      CODING     2.1.0     CUSTOM       1
seo-optimizer     WRITING    1.0.0     OFFICIAL     0
data-analysis     ANALYSIS   1.2.0     BUILT_IN     3
```

---

### 7. `crewship credential` — Správa credentials

```bash
crewship credential list                 # Seznam credentials
crewship credential create --name "..." --type ANTHROPIC --value "sk-..."
crewship credential get <id>             # Detail (NIKDY nezobrazuje hodnotu)
crewship credential update <id> --value "sk-new-..."
crewship credential delete <id>
crewship credential assign <cred-id> <agent-slug>   # Přiřadit agentovi
crewship credential unassign <cred-id> <agent-slug> # Odebrat
```

**API endpointy**:
- `GET /api/v1/credentials?workspace_id=X` (existující)
- `POST /api/v1/credentials` (existující)
- `GET /api/v1/credentials/{credentialId}` (existující)
- `PATCH /api/v1/credentials/{credentialId}` (existující)
- `DELETE /api/v1/credentials/{credentialId}` (existující)
- `POST /api/v1/agents/{agentId}/credentials` (existující)
- `DELETE /api/v1/agents/{agentId}/credentials/{assignmentId}` (existující)

**Bezpečnost**: CLI NIKDY nezobrazuje hodnotu credentials v output. Při `create` přijímá hodnotu přes `--value` flag nebo `--value-stdin` (pipe).

```bash
# Bezpečné zadání credential (bez historie v shellu):
echo "sk-ant-xxxx" | crewship credential create --name "Anthropic" --type ANTHROPIC --value-stdin
```

**Flags pro `credential create`**:
```
--name           # Název (povinný)
--type           # Typ: ANTHROPIC|OPENAI|GOOGLE|GITHUB|CUSTOM (povinný)
--value          # Hodnota (povinná, alternativa: --value-stdin)
--value-stdin    # Číst hodnotu ze stdin
--env-var-name   # Název env var (default: auto z typu, např. ANTHROPIC_API_KEY)
--priority       # Priorita 1-10 (default: 5)
```

---

### 8. `crewship activity` — Activity Feed / Ship's Log

```bash
crewship activity                        # Poslední aktivita across all crews
crewship activity --crew <slug>          # Aktivita konkrétní crew
crewship activity --follow               # Stream živé aktivity
crewship activity --lines 20             # Počet záznamů (default: 50)
```

**API endpointy**:
- `GET /api/v1/activity?workspace_id=X` (existující)
- `GET /api/v1/crews/{crewId}/peer-conversations` (existující)
- `GET /api/v1/crews/{crewId}/escalations` (existující)

**Výstup**:
```
2026-02-20 09:15:22  [backend-crew]  ASSIGNMENT   lead → viktor: "Build REST API"
2026-02-20 09:16:07  [backend-crew]  COMPLETED    viktor: "Build REST API" (45s)
2026-02-20 09:16:10  [backend-crew]  QUERY        lead → react-dev: "Can you review the API?"
2026-02-20 09:16:15  [backend-crew]  RESPONSE     react-dev → lead: "Looks good, ..."
2026-02-20 09:17:00  [backend-crew]  ESCALATION   react-dev: "Need CSS framework decision"
```

---

### 9. `crewship workspace` — Správa workspace

```bash
crewship workspace list                  # Seznam workspaces
crewship workspace use <slug>            # Nastavit default workspace
crewship workspace get                   # Detail aktuálního workspace
```

**API endpointy**:
- `GET /api/v1/workspaces` (existující)
- `GET /api/v1/workspaces/{workspaceId}` (existující)

`workspace use` uloží slug do `~/.crewship/cli-config.yaml` jako default.

---

### 10. `crewship audit` — Audit logy

```bash
crewship audit                           # Poslední audit záznamy
crewship audit --action create           # Filtr podle akce
crewship audit --lines 100              # Počet záznamů
```

**API endpointy**:
- `GET /api/v1/audit?workspace_id=X` (existující)

---

## Příkazy zachovat z aktuálního CLI

Tyto příkazy zůstanou beze změny:

```bash
crewship start [flags]                   # Spustit server (existující)
crewship version                         # Verze (existující)
crewship doctor                          # Health check (existující)
```

**Změna**: Přesunout do cobra subcommands, ale zachovat zpětnou kompatibilitu.

---

## Implementační plán (ALL PHASES COMPLETE ✅)

### Fáze 1: Základ ✅
Cobra root command, config file, HTTP client, formatters, colors.

### Fáze 2: CRUD příkazy ✅
workspace, agent, crew, skill, credential, mission — all CRUD + assign/unassign.

### Fáze 3: Runtime příkazy ✅
run (streaming/interactive/no-stream), logs (--follow), activity, audit.

### Fáze 4: Server-side changes ✅
CLI token auth (migration #12, 4 endpoints, middleware update, WS token for CLI).

### Additional commands added post-spec ✅
- `agent stop` — stop running agent
- `token list/create/revoke` — CLI token management
- `mission create/update/delete` — full mission CRUD
- `run list` — global run history
- `workspace create` — create new workspace
- `config show/set` — CLI configuration management
- Delete confirmation (`--yes`/`-y`) on all destructive commands
- `credential create` type/provider separation fix

---

## Struktura souborů (CURRENT)

```
cmd/crewship/
  main.go                    # Cobra root command + helpers (confirmAction, requireAuth, etc.)
  cmd_start.go               # crewship start
  cmd_version.go             # crewship version
  cmd_doctor.go              # crewship doctor
  cmd_login.go               # crewship login/logout/whoami
  cmd_agent.go               # crewship agent list/get/create/update/delete/runs/stop
  cmd_crew.go                # crewship crew list/get/create/update/delete/status
  cmd_run.go                 # crewship run (streaming/interactive/no-stream) + run list
  cmd_logs.go                # crewship logs (--follow)
  cmd_skill.go               # crewship skill list/get/import/assign/unassign
  cmd_credential.go          # crewship credential list/get/create/update/delete/assign/unassign
  cmd_workspace.go           # crewship workspace list/use/get/create
  cmd_mission.go             # crewship mission list/get/create/update/delete
  cmd_activity.go            # crewship activity
  cmd_audit.go               # crewship audit
  cmd_token.go               # crewship token list/create/revoke
  cmd_config.go              # crewship config show/set
  cmd_completion.go          # crewship completion bash/zsh/fish

internal/cli/
  client.go                  # HTTP klient (auth, workspace injection, slug→ID cache)
  client_test.go             # 12 tests
  config.go                  # Config file (~/.crewship/cli-config.yaml)
  config_test.go             # 10 tests
  formatter.go               # Output formatters (table, json, yaml, quiet, detail)
  formatter_test.go          # 10 tests
  websocket.go               # WebSocket klient pro streaming (run, logs)
  colors.go                  # ANSI color helpers (NO_COLOR support)
  colors_test.go             # 2 tests

internal/api/
  cli_token.go               # CLI token CRUD + validation handlers
  cli_token_test.go          # 8 tests
```

---

## Klíčové soubory

| Soubor | Popis |
|--------|-------|
| `cmd/crewship/main.go` | Cobra root command, global flags, helpers |
| `cmd/crewship/cmd_*.go` | 17 command files (agent, crew, run, etc.) |
| `internal/cli/` | Config, HTTP client, formatters, WS client, colors |
| `internal/api/cli_token.go` | CLI token server-side handlers |
| `internal/api/router.go` | All API endpoints (57+) |
| `internal/api/middleware.go` | Auth middleware (RequireAuth supports CLI tokens) |
| `internal/database/migrate.go` | Migration #12 = cli_tokens table |

---

## Testy (CURRENT STATE)

### Implemented ✅ (44 tests total)
- `internal/cli/` — 34 unit tests (config, client, formatter, colors)
- `internal/api/cli_token_test.go` — 8+ tests (create, validate, list, revoke)
- Full E2E manual test suite (12 categories, all passing)

### Not yet implemented (see Linear backlog)
- `cmd/crewship/` has no `_test.go` files — needs httptest mock-based unit tests
- `internal/cli/websocket.go` has no unit tests — needs WS mock server
- No tagged `//go:build integration` test suite

### Příkazy pro ověření
```bash
go test ./... -count=1                   # All tests
go test ./internal/cli/... -count=1      # CLI package tests
go test ./internal/api/... -count=1      # API tests (includes cli_token)
go vet ./...                             # Static analysis
```

---

## Příklady použití (end-to-end)

### Základní workflow
```bash
# 1. Login
crewship login --token crewship_cli_xxxxxx

# 2. Nastavit workspace
crewship workspace use my-workspace

# 3. Vytvořit crew
crewship crew create --name "Backend Team" --description "Backend development"

# 4. Vytvořit agenta
crewship agent create \
  --name "Viktor" \
  --crew backend-team \
  --role AGENT \
  --role-title "Go Developer" \
  --system-prompt @prompts/go-dev.txt \
  --memory

# 5. Přiřadit credential
crewship credential assign cuid_xxx viktor

# 6. Spustit agenta
crewship run viktor "Create a REST API for user management"

# 7. Sledovat logy
crewship logs viktor --follow

# 8. Zkontrolovat status crew
crewship crew status backend-team
```

### Scriptování
```bash
# Spustit všechny agenty v crew sekvečně
for agent in $(crewship agent list --crew backend-team -q); do
  crewship run "$agent" "Run tests and report status" --no-stream
done

# Export agent konfigurace
crewship agent get viktor -f yaml > viktor-backup.yaml

# Import skills hromadně
cat skill-urls.txt | while read url; do
  crewship skill import "$url"
done
```

### Interaktivní chat
```bash
$ crewship run viktor --interactive

[agent: viktor] Ready. Type your message (Ctrl+D to exit):

> Create a calculator REST API

[thinking] Let me design the API endpoints...
[tool: write_file] internal/calculator/handler.go
Done. I've created the calculator API with add, subtract, multiply, divide.

> Add tests for edge cases (division by zero)

[tool: write_file] internal/calculator/calculator_test.go
Done. Added tests for division by zero, overflow, and invalid input.

> ^D
[session ended]
```

---

## Omezení a poznámky

1. **Server musí běžet**: CLI je čistě klient. Bez `crewship start` nic nefunguje.
2. **WebSocket pro streaming**: `run` a `logs --follow` vyžadují WS spojení.
3. **Credential values**: NIKDY se nezobrazují v outputu. Ani v JSON formátu.
4. **Zpětná kompatibilita**: `crewship start`, `version`, `doctor` fungují identicky jako před refactorem.
5. **Shell completion**: `crewship completion bash/zsh/fish` generuje completion skripty.
6. **Slug→ID resolution**: CLI client automaticky resolvuje slug na CUID ID s in-memory cache.

## API Coverage (47/57 public endpoints)

Covered: All core CRUD (agents, crews, missions, skills, credentials, workspaces, runs, audit, activity, CLI tokens, chats create, agent stop/logs/runs, crew assignments/escalations/standup).

Not covered (see Linear backlog): workspace update/members/invitations, crew members, mission tasks, agent chat history/debug/files, admin endpoints, onboarding, system/runtime. Internal endpoints (`/api/v1/internal/*`) are sidecar-only and intentionally excluded.
