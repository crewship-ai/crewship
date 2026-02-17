# Crewship -- Naming Convention (Názvosloví)

**Verze:** 1.0
**Datum:** 2026-02-17
**Status:** Schváleno -- aplikovat na celý codebase, dokumentaci, UI, API, DB schema

---

## Cíl

Sjednotit názvosloví tak, aby rezonovalo s primární cílovou skupinou:
- Solo vývojáři a hobby technici
- Freelanceři
- Malé firmy a startupy (do ~50 lidí)

Enterprise přijde později. Názvosloví má být **hravé, přístupné, srozumitelné bez vysvětlování**
a konzistentní s brandem Crewship (posádka, loď, mise).

---

## 1. STRUKTURÁLNÍ POJMY

### Organization → Workspace

| Aspect | Staré | Nové |
|---|---|---|
| Název | Organization | **Workspace** |
| DB tabulka | `organizations` | `workspaces` |
| Prisma model | `Organization` | `Workspace` |
| API endpoint | `/api/v1/organizations` | `/api/v1/workspaces` |
| FK sloupec | `org_id` | `workspace_id` |
| Enum | `ORGANIZATION` (scope) | `WORKSPACE` |
| UI label | "Organization" | "Workspace" |

**Odvozené přejmenování:**
- `OrganizationMember` → `WorkspaceMember`
- `OrganizationInvitation` → `WorkspaceInvitation`
- `organization_members` → `workspace_members`
- `organization_invitations` → `workspace_invitations`
- `CredentialScope.ORGANIZATION` → `CredentialScope.WORKSPACE`
- `AuditScope.ORGANIZATION` → `AuditScope.WORKSPACE`
- `currentOrgId` (session) → `currentWorkspaceId`
- `org_memberships` (User relation) → `workspace_memberships`

### Team → Crew

| Aspect | Staré | Nové |
|---|---|---|
| Název | Team | **Crew** |
| DB tabulka | `teams` | `crews` |
| Prisma model | `Team` | `Crew` |
| API endpoint | `/api/v1/teams` | `/api/v1/crews` |
| FK sloupec | `team_id` | `crew_id` |
| Enum | `TEAM` (scope) | `CREW` |
| UI label | "Team" | "Crew" |

**Odvozené přejmenování:**
- `TeamMember` → `CrewMember`
- `team_members` → `crew_members`
- `CredentialScope.TEAM` → `CredentialScope.CREW`
- `team_memberships` (User relation) → `crew_memberships`
- `uq_team_slug` → `uq_crew_slug`
- `idx_team_org` → `idx_crew_workspace`
- `team_slug` (v delegačních příkazech) → `crew_slug`

---

## 2. AGENT ROLES

### Worker → Agent

| Aspect | Staré | Nové |
|---|---|---|
| Role název | Worker | **Agent** |
| Enum hodnota | `WORKER` | `AGENT` |
| UI label | "Worker" | "Agent" |
| Popis (UI) | "Default role. Specialized agent that performs tasks." | "Default role. Specialized crew member that performs tasks assigned by a Lead or directly by users." |

### Crew Leader → Lead

| Aspect | Staré | Nové |
|---|---|---|
| Role název | Crew Leader | **Lead** |
| Enum hodnota | `LEADER` | `LEAD` |
| UI label | "Team Leader" | "Lead" |
| Popis (UI) | "Orchestrates workers in this team." | "Coordinates agents in this crew. Assigns work, aggregates results, controls quality. Max 1 per crew." |

### Virtual Director → Coordinator

| Aspect | Staré | Nové |
|---|---|---|
| Role název | Virtual Director | **Coordinator** |
| Enum hodnota | `DIRECTOR` | `COORDINATOR` |
| UI label | "Virtual Director" | "Coordinator" |
| Popis (UI) | "Coordinates across all teams via Crew Leaders." | "Connects all crews in the workspace. Routes cross-crew questions, aggregates results. Max 1 per workspace. Opt-in." |

### Enum (Prisma):

```prisma
// STARÉ:
enum AgentRole {
  WORKER
  LEADER
  DIRECTOR
}

// NOVÉ:
enum AgentRole {
  AGENT
  LEAD
  COORDINATOR
}
```

---

## 3. OPERAČNÍ POJMY

### Crew Execution → Mission

| Aspect | Staré | Nové |
|---|---|---|
| Název | Crew Execution | **Mission** |
| DB tabulka | `crew_executions` | `missions` |
| Prisma model | `CrewExecution` | `Mission` |
| API endpoint | `/api/v1/executions` | `/api/v1/missions` |
| UI label | "Execution" | "Mission" |
| Status texty | "Execution completed" | "Mission completed" |

### Delegation → Assignment

| Aspect | Staré | Nové |
|---|---|---|
| Název | Delegation | **Assignment** |
| DB tabulka | `delegation_logs` | `assignments` |
| Prisma model | `DelegationLog` | `Assignment` |
| API endpoint | `/api/v1/delegations` | `/api/v1/assignments` |
| FK sloupec | `source_agent_id` / `target_agent_id` | `assigned_by_id` / `assigned_to_id` |
| Enum | `DelegationStatus` | `AssignmentStatus` |
| UI label | "Delegation" | "Assignment" |
| Sidecar endpoint | `POST /delegate` | `POST /assign` |
| Agent příkaz (stdout) | `@delegate(agent, "task")` | `@assign(agent, "task")` |
| Agent příkaz (cross-crew) | `@delegate_team(team, "task")` | `@assign_crew(crew, "task")` |
| Agent příkaz (ask) | `@ask_leader(team, "q")` | `@ask_lead(crew, "q")` |

### Delegation Timeline → Activity Feed

| Aspect | Staré | Nové |
|---|---|---|
| Název | Delegation Timeline | **Activity Feed** |
| UI component | `DelegationTimeline` | `ActivityFeed` |

### Conversation Session → Chat

| Aspect | Staré | Nové |
|---|---|---|
| Název (UI) | Conversation Session | **Chat** |
| DB tabulka | `conversation_sessions` (zachovat) | `chats` |
| Prisma model | `ConversationSession` | `Chat` |
| API endpoint | `/api/v1/sessions` | `/api/v1/chats` |
| UI label | "Conversation" / "Session" | "Chat" |

---

## 4. ZACHOVANÉ POJMY (beze změny)

| Pojem | Důvod |
|---|---|
| **Agent** (model/entita) | Zastřešující model pro všechny AI agenty (Agent, Lead, Coordinator jsou role v rámci Agent modelu) |
| **Skill** | Univerzální, srozumitelné |
| **Credential** | Přesnější než alternativy, credential pool pattern |
| **Webhook** | Technický standard |
| **Audit Log** | Standard |
| **Agent Run** | Technický pojem pro jednotlivý běh agenta |
| **RBAC role** | OWNER, ADMIN, MANAGER, MEMBER, VIEWER -- beze změny (lidské role) |

---

## 5. SIDECAR API PŘEJMENOVÁNÍ

```
STARÉ:                              NOVÉ:
POST /delegate                  →   POST /assign
POST /ask                       →   POST /ask (zachováno)
POST /broadcast                 →   POST /broadcast (zachováno)
GET  /results/:group            →   GET  /results/:group (zachováno)
GET  /status                    →   GET  /status (zachováno)

Agent stdout příkazy:
@delegate(slug, "task")         →   @assign(slug, "task")
@ask(slug, "question")          →   @ask(slug, "question") (zachováno)
@delegate_team(slug, "task")    →   @assign_crew(slug, "task")
@ask_leader(slug, "q")          →   @ask_lead(slug, "q")
@broadcast(...)                 →   @broadcast(...) (zachováno)
```

---

## 6. GO KÓDOVÉ PŘEJMENOVÁNÍ

```
STARÉ:                              NOVÉ:
internal/orchestrator/              internal/orchestrator/ (zachováno)
  delegation.go                 →     assignment.go
  DelegationEngine              →     AssignmentEngine
  DelegationRequest             →     AssignmentRequest
  DelegationResult              →     AssignmentResult
  director.go                   →     coordinator.go
  RunDirector()                 →     RunCoordinator()
  DirectorRequest               →     CoordinatorRequest
  circuit_breaker.go            →     circuit_breaker.go (zachováno)
  execution.go                  →     mission.go
  
cmd/crewship-sidecar/           →   cmd/crewship-sidecar/ (zachováno)
  HandleDelegate()              →     HandleAssign()
```

---

## 7. UI TEXTY -- PŘÍKLADY

### Dashboard
```
STARÉ: "1 director · 4 leaders · 8 workers"
NOVÉ:  "1 coordinator · 4 leads · 8 agents"

STARÉ: "Showing 13 of 13 agents (1 director, 4 leaders, 8 workers)"
NOVÉ:  "Showing 13 agents (1 coordinator, 4 leads, 8 agents)"
```

### Agent Settings (role selector)
```
STARÉ:
  ○ Worker -- Default role. Specialized agent that performs tasks.
  ● Team Leader -- Orchestrates workers in this team.
  ○ Virtual Director -- Coordinates across all teams.

NOVÉ:
  ○ Agent -- Specialized crew member. Performs tasks assigned by a Lead or directly by users.
  ● Lead -- Coordinates agents in this crew. Assigns work, aggregates results. Max 1 per crew.
  ○ Coordinator -- Connects all crews. Routes cross-crew work. Max 1 per workspace.
```

### Chat
```
STARÉ: "Anna delegated 'Pull Twitter data' to Bob"
NOVÉ:  "Anna assigned 'Pull Twitter data' to Bob"

STARÉ: "Delegation completed in 2m 34s"
NOVÉ:  "Assignment completed in 2m 34s"
```

### Crew view
```
STARÉ:
  Team: Marketing
  ├─ 👑 Anna (Team Leader) — RUNNING
  ├─ Bob (Data Analyst) — IDLE
  └─ Claudia (Copywriter) — IDLE

NOVÉ:
  Crew: Marketing
  ├─ ★ Anna (Lead) — RUNNING
  ├─ Bob (Data Analyst) — IDLE
  └─ Claudia (Copywriter) — IDLE
```

### Workspace dashboard
```
STARÉ:
  🏢 Virtual Director: Max
     Teams under management: 4

NOVÉ:
  ◈ Coordinator: Max
    Crews: 4 (Marketing, Development, Finance, Support)
    [Chat with Coordinator]
```

### Mission
```
STARÉ: "Crew Execution: January Social Media Report"
NOVÉ:  "Mission: January Social Media Report"

STARÉ: "Execution status: IN_PROGRESS (3/5 workers done)"
NOVÉ:  "Mission status: IN_PROGRESS (3/5 agents done)"
```

---

## 8. SEARCH & REPLACE CHECKLIST

Při aplikování změn proveď v tomto pořadí (závislosti):

1. **Prisma schema** (`prisma/schema.prisma`)
   - Modely: Organization→Workspace, Team→Crew, ConversationSession→Chat, DelegationLog→Assignment, CrewExecution→Mission
   - Enumy: AgentRole (WORKER→AGENT, LEADER→LEAD, DIRECTOR→COORDINATOR), DelegationStatus→AssignmentStatus, CredentialScope, AuditScope
   - Relace a FK sloupce: org_id→workspace_id, team_id→crew_id, source_agent_id→assigned_by_id, target_agent_id→assigned_to_id
   - Indexy a constrainty: přejmenovat dle nových názvů

2. **DB migrace** -- nová Prisma migrace s `RENAME TABLE`, `RENAME COLUMN`, `ALTER TYPE`

3. **TypeScript kód** (lib/, app/, components/)
   - Typy a importy
   - API routes
   - Service layer
   - Components

4. **Go kód** (cmd/, internal/)
   - Struktury, funkce, soubory
   - Sidecar API endpointy
   - Agent příkazy (stdout parsing)

5. **Dokumentace** (.factory/context/)
   - AGENTS.md, architecture.md, business.md
   - prd/PRD.md, prd/ORCHESTRATION.md, prd/DATABASE.md, prd/AGENT-RUNTIME.md, prd/API.md
   - prd/CREW-EXECUTION.md, prd/ADR.md
   - TODO.md, MVP.md, STRATEGY-2026.md

6. **Wireframes** (.factory/context/wireframes/)
   - HTML wireframy s UI texty

7. **Config soubory**
   - AGENTS.md (coding guidelines)
   - Environment variables: CREWSHIP_CONTAINER_PROVIDER etc. (beze změny)

---

## 9. SLOVNÍČEK (rychlá reference)

| EN (nové) | CZ ekvivalent | Popis |
|---|---|---|
| Workspace | Pracovní prostor | Hlavní organizační jednotka |
| Crew | Posádka / Tým | Skupina agentů se společným cílem |
| Agent | Agent | Řadový AI člen crew (default role) |
| Lead | Vedoucí | Koordinátor crew, přiřazuje práci (1 per crew) |
| Coordinator | Koordinátor | Propojuje crews v rámci workspace (1 per workspace) |
| Mission | Mise | Komplexní úkol pro crew (multi-agent execution) |
| Assignment | Přiřazení | Lead přiřadí práci agentovi |
| Chat | Chat | Konverzace s agentem |
| Activity Feed | Přehled aktivit | Timeline událostí v rámci mission |
| Skill | Dovednost | Schopnost agenta (MCP tool) |
| Credential | Přístupový údaj | API klíč, token (šifrovaný) |

---

*Tento dokument slouží jako single source of truth pro názvosloví.
Veškerá dokumentace, kód, UI a API se řídí tímto mapováním.*
