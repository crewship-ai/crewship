# Crewship -- Architecture Diagrams (Mermaid)

## 1. System Overview (Two-Process Architecture)

```mermaid
graph TB
    subgraph Internet
        Browser["Browser<br/>(React UI)"]
        ExtSystem["External Systems<br/>(Grafana, n8n, Make, CI/CD)"]
    end

    subgraph Platform["Crewship Platform (same host)"]
        subgraph NextJS["Next.js :3000 (~300 MB RAM)"]
            UI["React UI<br/>shadcn/ui + Tailwind v4"]
            Auth["NextAuth.js<br/>(Auth.js v5)"]
            API["REST API<br/>/api/v1/*"]
            Prisma["Prisma ORM"]
        end

        subgraph Go["crewshipd :8080 (~50 MB RAM)"]
            WS["WebSocket<br/>Gateway"]
            Orch["Orchestrator<br/>+ Credential Pool"]
            DockerMgr["Docker<br/>Manager"]
            LogCol["Log<br/>Collector"]
            FileSrv["File Server<br/>+ fsnotify"]
            WebhookIn["Webhook<br/>Ingress"]
            WAL["bbolt<br/>WAL"]
            Metrics["Prometheus<br/>/metrics"]
            RateLim["Rate<br/>Limiter"]
        end

        Socket["Unix Socket<br/>/tmp/crewship.sock"]

        DB[("PostgreSQL 16<br/>Structured data only<br/>19 tables")]

        subgraph Storage["Host Filesystem"]
            Logs["/var/log/crewship/<br/>JSONL + logrotate"]
            Convos["/var/lib/crewship/conversations/<br/>JSONL per session"]
            Output["/var/lib/crewship/output/<br/>Agent deliverables"]
        end
    end

    subgraph Containers["Docker Network: crewship-agents (--internal)"]
        TeamA["Team A Container<br/>UID 1001, non-root<br/>cap-drop ALL"]
        TeamB["Team B Container<br/>UID 1001, non-root<br/>cap-drop ALL"]
    end

    subgraph LLM["LLM APIs (allowlisted)"]
        Anthropic["Anthropic API"]
        OpenAI["OpenAI API"]
        Google["Google AI API"]
        Ollama["Ollama (local)"]
    end

    Browser -->|"HTTPS"| UI
    Browser <-->|"WSS"| WS
    ExtSystem -->|"POST webhook"| WebhookIn

    UI --> Auth
    UI --> API
    API --> Prisma
    Prisma --> DB

    NextJS <-->|"HTTP over Unix socket"| Socket
    Socket <-->|"IPC"| Go

    Orch --> DockerMgr
    DockerMgr -->|"Docker SDK"| TeamA
    DockerMgr -->|"Docker SDK"| TeamB
    Orch --> WAL

    LogCol -->|"append"| Logs
    LogCol -->|"append"| Convos
    FileSrv -->|"watch + serve"| Output

    TeamA -->|"stdout/stderr"| LogCol
    TeamB -->|"stdout/stderr"| LogCol
    TeamA -->|"write files"| Output
    TeamB -->|"write files"| Output

    TeamA -->|"HTTPS (allowlist)"| LLM
    TeamB -->|"HTTPS (allowlist)"| LLM

    style NextJS fill:#2563eb,color:#fff
    style Go fill:#00add8,color:#fff
    style DB fill:#336791,color:#fff
    style TeamA fill:#f59e0b,color:#000
    style TeamB fill:#f59e0b,color:#000
```

## 2. Data Model (Entity Relationships)

```mermaid
erDiagram
    User ||--o{ OrganizationMember : "belongs to"
    User ||--o{ TeamMember : "belongs to"
    User ||--o{ ConversationSession : "creates"
    User ||--o{ AgentRun : "triggers"
    User ||--o{ AuditLog : "performs"

    Organization ||--o{ OrganizationMember : "has"
    Organization ||--o{ OrganizationInvitation : "has"
    Organization ||--o{ Team : "has"
    Organization ||--o{ Agent : "has"
    Organization ||--o{ Credential : "has"
    Organization ||--o{ AuditLog : "has"
    Organization ||--o| Subscription : "has"
    Organization ||--o{ FeatureFlagOverride : "has"

    Team ||--o{ TeamMember : "has"
    Team ||--o{ Agent : "has"

    Agent ||--o{ AgentSkill : "has"
    Agent ||--o{ AgentCredential : "has"
    Agent ||--o{ ConversationSession : "has"
    Agent ||--o{ AgentRun : "has"
    Agent ||--o{ AgentConfigHistory : "has"

    Skill ||--o{ AgentSkill : "assigned to"
    Credential ||--o{ AgentCredential : "assigned to"

    ConversationSession ||--o{ AgentRun : "contains"

    Subscription }o--|| Plan : "uses"
    FeatureFlag ||--o{ FeatureFlagOverride : "overridden by"

    User {
        uuid id PK
        string email UK
        string full_name
        string hashed_password
        datetime email_verified
    }

    Organization {
        uuid id PK
        string name
        string slug UK
        datetime deleted_at
    }

    Team {
        uuid id PK
        uuid org_id FK
        string name
        string slug
        int container_memory_mb
        float container_cpus
        datetime deleted_at
    }

    Agent {
        uuid id PK
        uuid team_id FK
        uuid org_id FK
        string name
        enum status "IDLE|RUNNING|ERROR|STOPPED"
        enum cli_adapter "CLAUDE_CODE|CODEX_CLI|GEMINI_CLI|OPENCODE"
        enum tool_profile "MINIMAL|CODING|MESSAGING|FULL"
        string webhook_secret
        datetime deleted_at
    }

    Credential {
        uuid id PK
        uuid org_id FK
        string name
        string encrypted_value "v1:AES-256-GCM"
        enum scope "ORGANIZATION|TEAM"
        datetime deleted_at
    }

    AgentCredential {
        uuid id PK
        uuid agent_id FK
        uuid credential_id FK
        string env_var_name
        int priority "0=primary 1+=fallback"
    }

    Skill {
        uuid id PK
        string name UK
        enum category "CODING|DATA|DEVOPS|etc"
        enum source "BUNDLED|MANAGED|MARKETPLACE|CUSTOM"
    }

    ConversationSession {
        uuid id PK
        uuid agent_id FK
        enum mode "CHAT|TASK"
        enum status "ACTIVE|COMPLETED|ERROR"
        int message_count
        string jsonl_path "messages in JSONL not DB"
    }

    AgentRun {
        uuid id PK
        uuid agent_id FK
        uuid session_id FK
        enum status "PENDING|RUNNING|COMPLETED|FAILED|CANCELLED|TIMEOUT"
        enum trigger_type "USER|WEBHOOK|CRON|AGENT|SYSTEM"
        uuid triggered_by "nullable"
    }

    AuditLog {
        uuid id PK
        uuid org_id FK
        string action
        string entity_type
        json metadata "immutable append-only"
    }
```

## 3. Request Flow: User Chat Message

```mermaid
sequenceDiagram
    actor User
    participant Browser
    participant NextJS as Next.js
    participant Socket as Unix Socket
    participant Go as crewshipd
    participant Docker as Docker Engine
    participant Agent as Agent Container
    participant LLM as LLM API

    User->>Browser: Types message in chat UI
    Browser->>Go: WebSocket send (WSS)
    Go->>Go: Validate JWT token
    Go->>Go: Check RBAC (team membership via IPC)

    Go->>Go: Select credential from pool (priority order)
    Go->>Go: Write RunState to bbolt WAL

    Go->>Socket: IPC: Create AgentRun + ConversationSession
    Socket->>NextJS: HTTP POST
    NextJS->>NextJS: Prisma → PostgreSQL

    Go->>Docker: ContainerExecCreate<br/>(ENV vars with credentials)
    Docker->>Agent: Exec: claude --print "user message"
    Agent->>LLM: HTTPS API call (ANTHROPIC_API_KEY)
    LLM-->>Agent: Streaming response

    loop Stdout streaming
        Agent-->>Go: stdout line
        Go-->>Browser: WebSocket event (real-time)
        Go->>Go: Append to JSONL log
        Go->>Go: Append to conversation JSONL
    end

    Agent->>Agent: Write file to /output/
    Go->>Go: fsnotify detects new file
    Go-->>Browser: WebSocket: "file created"

    Agent-->>Docker: Exit code 0
    Go->>Go: Update bbolt WAL (COMPLETED)
    Go->>Socket: IPC: Update AgentRun status
    Socket->>NextJS: HTTP PATCH
    Go-->>Browser: WebSocket: "run completed"
```

## 4. Request Flow: Webhook Trigger

```mermaid
sequenceDiagram
    actor Grafana as External System
    participant Go as crewshipd
    participant Socket as Unix Socket
    participant NextJS as Next.js
    participant Docker as Docker Engine
    participant Agent as Agent Container

    Grafana->>Go: POST /api/v1/webhooks/{team}/{agent}/trigger<br/>X-Webhook-Secret: xxx

    Go->>Go: Validate webhook_secret
    alt Invalid secret
        Go-->>Grafana: 401 Unauthorized
        Go->>Go: Audit log: webhook.auth_failed
    end

    Go->>Socket: IPC: Get agent config + credentials
    Socket->>NextJS: HTTP GET
    NextJS-->>Socket: Agent config + decrypted credentials
    Socket-->>Go: Response

    Go->>Go: Select credential from pool
    Go->>Go: Write RunState (trigger_type=WEBHOOK)

    Go->>Socket: IPC: Create Session + Run
    Socket->>NextJS: Prisma → PostgreSQL

    Go->>Docker: ContainerExecCreate
    Docker->>Agent: Exec with webhook payload as input

    Agent->>Agent: Process event, write to /output/

    Go-->>Grafana: 202 Accepted {run_id}
```

## 5. Credential Pool & Failover

```mermaid
flowchart TD
    Start["Agent Run Requested"] --> GetPool["Get credential pool<br/>for env_var_name<br/>(sorted by priority ASC)"]

    GetPool --> Loop{"Next credential<br/>in pool?"}

    Loop -->|Yes| Cooldown{"In cooldown?"}
    Cooldown -->|Yes| Loop
    Cooldown -->|No| Select["Select this credential"]

    Loop -->|No more| Exhausted["ERROR: All API keys exhausted<br/>Notify user via WebSocket"]

    Select --> Inject["Inject as ENV var<br/>into Docker exec"]
    Inject --> Run["Agent runs"]

    Run --> Success{"Exit code?"}
    Success -->|0| Done["Run COMPLETED<br/>Update WAL + DB"]

    Success -->|1| CheckErr{"stderr contains<br/>rate limit / 429?"}
    CheckErr -->|No| Failed["Run FAILED<br/>Report error"]
    CheckErr -->|Yes| MarkCooldown["Mark credential<br/>cooldown 5 min"]

    MarkCooldown --> Preserve{"CLI adapter?"}
    Preserve -->|"Claude Code"| Resume["--resume flag<br/>(native context)"]
    Preserve -->|"Other CLI"| CatchUp["JSONL catch-up prompt<br/>(last 10 messages)"]

    Resume --> GetPool
    CatchUp --> GetPool

    style Start fill:#2563eb,color:#fff
    style Done fill:#16a34a,color:#fff
    style Exhausted fill:#dc2626,color:#fff
    style Failed fill:#dc2626,color:#fff
    style MarkCooldown fill:#f59e0b,color:#000
```

## 6. Container Isolation & Security

```mermaid
graph TB
    subgraph TrustZone1["TRUST ZONE 1: Platform (fully trusted)"]
        NextJS["Next.js<br/>Port 3000"]
        Crewshipd["crewshipd<br/>Port 8080"]
        PG[("PostgreSQL<br/>Port 5432")]
        Socket["Unix Socket<br/>/run/crewship/crewship.sock<br/>chmod 0660"]
    end

    subgraph TrustZone2["TRUST ZONE 2: Authenticated User"]
        Browser["Browser<br/>JWT + HttpOnly cookie"]
    end

    subgraph TrustZone3["TRUST ZONE 3: Agent Container (UNTRUSTED)"]
        direction TB
        Agent["Agent Process<br/>UID 1001 (non-root)"]
        Workspace["/workspace/<br/>(ephemeral volume)"]
        OutputMount["/output/<br/>(bind mount)"]

        subgraph Restrictions["Container Restrictions"]
            R1["--read-only root fs"]
            R2["--cap-drop ALL"]
            R3["--no-new-privileges"]
            R4["--pids-limit 200"]
            R5["--memory 4g"]
            R6["--network internal"]
        end
    end

    subgraph TrustZone4["TRUST ZONE 4: External"]
        LLMAPI["LLM APIs<br/>(allowlisted only)"]
        Webhooks["Webhook senders"]
    end

    Browser -->|"HTTPS + WSS"| NextJS
    Browser -->|"WSS"| Crewshipd
    NextJS <-->|"Unix socket"| Socket <-->|"IPC"| Crewshipd
    NextJS -->|"Prisma"| PG
    Crewshipd -->|"Docker SDK"| Agent

    Agent -->|"stdout"| Crewshipd
    Agent -->|"HTTPS (allowlist)"| LLMAPI
    Agent -.->|"BLOCKED"| PG
    Agent -.->|"BLOCKED"| NextJS
    Agent -.->|"BLOCKED"| Socket

    Webhooks -->|"POST + X-Webhook-Secret"| Crewshipd

    style TrustZone1 fill:#dcfce7,stroke:#16a34a
    style TrustZone2 fill:#dbeafe,stroke:#2563eb
    style TrustZone3 fill:#fef3c7,stroke:#f59e0b
    style TrustZone4 fill:#fee2e2,stroke:#dc2626
```

## 7. Storage Architecture

```mermaid
graph LR
    subgraph PostgreSQL["PostgreSQL (structured data)"]
        Users["Users, Orgs,<br/>Members"]
        Teams["Teams,<br/>TeamMembers"]
        Agents["Agents, Skills,<br/>Credentials"]
        Sessions["ConversationSession<br/>(metadata only)"]
        Runs["AgentRun"]
        Audit["AuditLog<br/>(immutable)"]
        Billing["Subscription,<br/>Plan, FeatureFlags"]
    end

    subgraph Filesystem["Host Filesystem"]
        subgraph LogDir["/var/log/crewship/"]
            SvcLog["service.jsonl"]
            AgentLog["teams/{id}/agents/{id}/current.jsonl"]
            AuditJSONL["audit.jsonl (chattr +a)"]
        end

        subgraph ConvoDir["/var/lib/crewship/conversations/"]
            ConvoFile["{org}/{agent}/{session}.jsonl<br/>All messages per session"]
        end

        subgraph OutputDir["/var/lib/crewship/output/"]
            OrgDir["{org}/{team}/{agent}/<br/>reports, code, data"]
            Archived["_archived/<br/>deleted teams"]
        end
    end

    subgraph GoMemory["crewshipd Memory"]
        WAL["bbolt WAL<br/>(RunState, job queue)"]
        Cooldowns["Credential cooldowns<br/>(in-memory map)"]
        WSConns["WebSocket connections<br/>(goroutine per conn)"]
        RateLimit["Rate limit buckets<br/>(token bucket)"]
    end

    subgraph Container["Docker Volume (ephemeral)"]
        WS["/workspace/<br/>Agent scratch space<br/>Dies with container"]
    end

    Sessions -.->|"jsonl_path"| ConvoFile
    Runs -.->|"log location"| AgentLog

    style PostgreSQL fill:#336791,color:#fff
    style Filesystem fill:#f3f4f6,color:#000
    style GoMemory fill:#00add8,color:#fff
    style Container fill:#fef3c7,color:#000
```

## 8. RBAC Flow

```mermaid
flowchart TD
    Request["API Request"] --> Auth{"Authenticated?<br/>(NextAuth JWT)"}
    Auth -->|No| R401["401 Unauthorized"]
    Auth -->|Yes| LoadUser["Load user + org membership"]

    LoadUser --> CASL["Build CASL abilities<br/>based on OrgRole"]

    CASL --> Check{"can(action, subject)?"}
    Check -->|No| R403["403 Forbidden<br/>+ Audit log"]
    Check -->|Yes| TeamCheck{"Team-scoped<br/>resource?"}

    TeamCheck -->|No| Execute["Execute request"]
    TeamCheck -->|Yes| MemberCheck{"User is member<br/>of target team?"}

    MemberCheck -->|No| R403
    MemberCheck -->|Yes| Execute

    Execute --> AuditWrite["Write to AuditLog<br/>(append-only)"]

    subgraph Roles["Role Hierarchy"]
        OWNER["OWNER<br/>manage all"]
        ADMIN["ADMIN<br/>manage all<br/>except delete org"]
        MANAGER["MANAGER<br/>assigned teams<br/>create agents + creds"]
        MEMBER["MEMBER<br/>assigned teams<br/>use agents only"]
        VIEWER["VIEWER<br/>assigned teams<br/>read-only"]
    end

    style R401 fill:#dc2626,color:#fff
    style R403 fill:#dc2626,color:#fff
    style Execute fill:#16a34a,color:#fff
```

## 9. Provider Pattern (K8s Readiness)

```mermaid
graph TB
    subgraph Crewshipd["crewshipd (business logic)"]
        Orch["Orchestrator"]
        LogCol["Log Collector"]
        FileSrv["File Server"]
        WALMgr["State Manager"]
    end

    subgraph Interfaces["Provider Interfaces"]
        CP["ContainerProvider"]
        SP["StorageProvider"]
        StP["StateProvider"]
    end

    subgraph MVP["MVP Implementation"]
        Docker["DockerProvider<br/>Docker SDK<br/>docker exec"]
        LocalFS["LocalFSProvider<br/>os.OpenFile<br/>fsnotify"]
        Bbolt["BboltProvider<br/>embedded KV<br/>WAL"]
    end

    subgraph Enterprise["Enterprise Implementation"]
        K8s["K8sProvider<br/>client-go<br/>Pods / Jobs"]
        S3["S3Provider<br/>MinIO / AWS S3<br/>event notifications"]
        PgState["PgStateProvider<br/>PostgreSQL table<br/>shared across instances"]
    end

    Orch --> CP
    LogCol --> SP
    FileSrv --> SP
    WALMgr --> StP

    CP --> Docker
    CP --> K8s
    SP --> LocalFS
    SP --> S3
    StP --> Bbolt
    StP --> PgState

    subgraph Config["Configuration (env vars)"]
        E1["CREWSHIP_CONTAINER_PROVIDER=docker|k8s"]
        E2["CREWSHIP_STORAGE_PROVIDER=localfs|s3"]
        E3["CREWSHIP_STATE_PROVIDER=bbolt|postgres"]
    end

    Config -.->|"selects"| Interfaces

    style Crewshipd fill:#2563eb,color:#fff
    style Interfaces fill:#7c3aed,color:#fff
    style MVP fill:#16a34a,color:#fff
    style Enterprise fill:#f59e0b,color:#000
```

## 10. K8s Deployment Architecture (Enterprise)

```mermaid
graph TB
    subgraph Internet
        Users["Users"]
    end

    subgraph K8s["Kubernetes Cluster"]
        Ingress["Ingress Controller<br/>TLS + sticky sessions (WS)"]

        subgraph Platform["Namespace: crewship"]
            NextDeploy["Deployment: nextjs<br/>N replicas (stateless)"]
            GoDeploy["Deployment: crewshipd<br/>N replicas"]
            PG[("StatefulSet: postgresql<br/>1 replica + PVC")]
            S3Store[("MinIO / S3<br/>Logs, Convos, Output")]

            SvcNext["Service: nextjs"]
            SvcGo["Service: crewshipd"]
            SvcPG["Service: postgresql"]

            HPA["HPA: autoscaler<br/>CPU/memory based"]
            ConfigMap["ConfigMap:<br/>crewship-config"]
            Secret["Secret:<br/>ENCRYPTION_KEY<br/>NEXTAUTH_SECRET"]
        end

        subgraph AgentNS["Namespace: crewship-agents"]
            TeamA["Pod: team-alpha<br/>agent-runtime image<br/>UID 1001"]
            TeamB["Pod: team-beta<br/>agent-runtime image<br/>UID 1001"]
            NetPol["NetworkPolicy:<br/>deny all ingress<br/>allow LLM egress only"]
        end
    end

    subgraph LLM["LLM APIs"]
        APIs["Anthropic / OpenAI / Google"]
    end

    Users -->|"HTTPS/WSS"| Ingress
    Ingress --> SvcNext
    Ingress --> SvcGo
    SvcNext --> NextDeploy
    SvcGo --> GoDeploy
    NextDeploy -->|"HTTP"| SvcGo
    NextDeploy -->|"Prisma"| SvcPG
    SvcPG --> PG
    GoDeploy -->|"client-go"| TeamA
    GoDeploy -->|"client-go"| TeamB
    GoDeploy -->|"S3 API"| S3Store
    GoDeploy -->|"PG state"| SvcPG
    GoDeploy -->|"LISTEN/NOTIFY"| PG

    TeamA -->|"allowlisted"| APIs
    TeamB -->|"allowlisted"| APIs

    HPA -.->|"scales"| GoDeploy

    style Platform fill:#dbeafe,stroke:#2563eb
    style AgentNS fill:#fef3c7,stroke:#f59e0b
    style NetPol fill:#fee2e2,stroke:#dc2626
```

## 11. Deployment Architecture (Coolify / MVP)

```mermaid
graph TB
    subgraph Internet
        CDN["Cloudflare / CDN<br/>(TLS termination)"]
    end

    subgraph Proxmox["Proxmox Host (128GB RAM, i7-12700)"]
        subgraph Coolify["Coolify (self-hosted PaaS)"]
            NextJSSvc["Next.js Service<br/>Docker container<br/>Port 3000"]
            GoSvc["crewshipd Service<br/>Docker container<br/>Port 8080"]
            PGSvc[("PostgreSQL 16<br/>Managed by Coolify")]
        end

        subgraph AgentPool["Agent Containers"]
            T1["Team 1 Container"]
            T2["Team 2 Container"]
            TN["Team N Container"]
        end

        subgraph Volumes["Persistent Volumes"]
            PGData["PostgreSQL data"]
            OutputVol["/output/ storage"]
            LogVol["/var/log/crewship/"]
            ConvoVol["/var/lib/crewship/"]
        end

        cAdvisor["cAdvisor<br/>Container metrics"]
    end

    CDN -->|"HTTPS"| NextJSSvc
    CDN -->|"WSS"| GoSvc
    NextJSSvc <-->|"Unix socket"| GoSvc
    NextJSSvc --> PGSvc
    GoSvc -->|"Docker SDK"| T1
    GoSvc -->|"Docker SDK"| T2
    GoSvc -->|"Docker SDK"| TN
    GoSvc --> LogVol
    GoSvc --> ConvoVol
    GoSvc --> OutputVol
    PGSvc --> PGData
    cAdvisor -.->|"scrape"| T1
    cAdvisor -.->|"scrape"| T2

    style Coolify fill:#7c3aed,color:#fff
    style AgentPool fill:#f59e0b,color:#000
```
