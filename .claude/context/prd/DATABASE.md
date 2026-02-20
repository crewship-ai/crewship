# Crewship -- Database Schema (DATABASE.md)

**Verze:** 3.1
**Datum:** 2026-02-20
**DB pristup:** Go `database/sql` (primy pristup, NO ORM at runtime)
**Prisma:** Schema pouzivano POUZE pro TypeScript type generation (`pnpm db:generate`)
**Databaze:** SQLite (default, embedded) nebo PostgreSQL 16+ (opt-in)
**Auth:** Go (NextAuth-compatible JWE endpoints v `internal/api/`)
**Autorizace (MVP):** Go RBAC middleware (`internal/api/middleware.go`) -- zadne RLS v MVP
**Autorizace (Phase 2):** Go middleware + RLS jako defense-in-depth (`current_setting()` pattern)
**Multi-tenancy:** workspace_id sloupec + Go middleware (MVP), + RLS (Phase 2)
**Mody:** Community (1 workspace, free) | Enterprise (multi-workspace, placeny)

---

## 1. CO JE V POSTGRESQL A CO NE

| Data | Kde | Duvod |
|---|---|---|
| Uzivatele, workspaces, crews, agenti | SQLite/PostgreSQL | Strukturovana data, relace, RBAC |
| Credentials (sifrovane) | SQLite/PostgreSQL | AES-256-GCM, pristup pres Go database/sql |
| Skills, plany, feature flags | SQLite/PostgreSQL | Konfigurace platformy |
| Audit log | SQLite/PostgreSQL | Immutable, queryable, GDPR |
| Subscription (Stripe) | SQLite/PostgreSQL | Billing stav |
| **Session metadata** | **SQLite/PostgreSQL** | ID, agent, title, status, cas — queryable |
| **Konverzacni zpravy** | **JSONL soubory** | /var/lib/crewship/conversations/{workspace}/{agent}/{session}.jsonl |
| **Logy agentu** | **JSONL soubory** | /var/log/crewship/crews/{crew}/agents/{agent}/current.jsonl |
| **Agent live status** | **Go pamet + bbolt** | crewshipd drzi v pameti, persistuje do bbolt WAL |
| **WebSocket sessions** | **Go pamet** | goroutine per connection, zadna DB |
| **Container status** | **Go pamet + Docker API** | crewshipd se pta Docker SDK |
| **Rate limiting** | **Go pamet** | In-memory token bucket (MVP), per-process |

> **PRAVIDLO:** PostgreSQL = strukturovana data s relacemi. Vsechno ostatni (logy, zpravy, live stav) je mimo DB.

---

## SQLite kompatibilita (single binary mode)

V single binary mode (`crewship start`) pouzivame SQLite jako default databazi.

### Prisma multi-provider
- `DB_PROVIDER=sqlite` + `DATABASE_URL=file:./crewship.db` (default)
- `DB_PROVIDER=postgresql` + `DATABASE_URL=postgresql://...` (opt-in)

### SQLite omezeni
- Zadne `@db.Uuid` -- pouzit `String` s nanoid/cuid
- Zadne `@db.JsonB` -- pouzit `String` s JSON serializaci
- Zadne `gen_random_uuid()` -- generovat v aplikacni vrstve
- WAL mode pro lepsi concurrent reads
- Vhodne pro: solo dev, mala crew (1-10 lidi)
- Pro vetsi crews: `crewship start --db postgres://...`

### Migracni strategie
- SQLite → PostgreSQL: export/import tool (`crewship migrate --to postgres://...`)
- Schema je STEJNE pro oba providery (Prisma abstrahuje)

---

## 2. PREHLED ENTIT

**24 tabulek** rozdelenychdo 7 domen:

| Domena | Tabulky | Popis |
|---|---|---|
| **NextAuth** | Account, Session, VerificationToken | OAuth adapter, session tokens, email verifikace |
| **Uzivatele & Workspace** | User, Workspace, WorkspaceMember, WorkspaceInvitation | Multi-tenant zaklad |
| **Crews** | Crew, CrewMember | Izolacni boundary (1 kontejner = 1 crew) |
| **Agenti** | Agent, AgentSkill, AgentCredential, AgentConfigHistory, Assignment | Virtualni zamestnanci + orchestrace |
| **Skills & Credentials** | Skill, SkillReview, Credential | Dovednosti, marketplace recenze, opravneni |
| **Konverzace & Behy** | Chat (metadata only), AgentRun | Session metadata, behy |
| **System** | AuditLog, Subscription, Plan, FeatureFlag, FeatureFlagOverride | Billing, audit, flags |

> **Pozor:** Chat je **metadata-only** model. Samotne zpravy jsou v JSONL souborech, NE v PostgreSQL.

### Entity Relationship Diagram (textovy)
```
                                                   User
                                                    │
                                              ┌─────┼──────┐
                                          (*) Account  (*) Session
                                              VerificationToken (standalone)

Workspace (1) ──── (*) WorkspaceMember (*) ──── (1) User
     │                         │
     │                    WorkspaceInvitation
     │
     ├── (*) Crew (1) ──── (*) CrewMember (*) ──── (1) User
     │        │
     │        ├── (*) Agent (agent_role: AGENT | LEAD | COORDINATOR)
     │        │       ├── (*) AgentSkill (*) ──── (1) Skill ──── (*) SkillReview (*) ──── (1) User
     │        │       ├── (*) AgentCredential (*) ──── (1) Credential
     │        │       ├── (*) AgentConfigHistory
     │        │       ├── (*) Chat (metadata only, zpravy v JSONL)
     │        │       ├── (*) AgentRun
     │        │       └── (*) Assignment (assigned_by/assigned_to — lead↔agent, coordinator↔lead)
     │        │
     │
     ├── (*) Credential
     ├── (*) AuditLog
     ├── (1) Subscription ──── (1) Plan
     └── (*) FeatureFlagOverride (*) ──── (1) FeatureFlag
```

---

## 3. SPOLECNE KONVENCE

### Vsechny tabulky maji:
- `id` -- UUID v4, primarni klic, generovany databazi (`gen_random_uuid()`)
- `created_at` -- TIMESTAMPTZ, default `now()`
- `updated_at` -- TIMESTAMPTZ, default `now()`, automaticky updatovany triggerem

### Soft delete:
- Hlavni entity (Workspace, Crew, Agent, Credential) maji `deleted_at` (TIMESTAMPTZ, nullable)
- Dotazy VZDY filtruje `WHERE deleted_at IS NULL` (Prisma middleware)
- Hard delete az po GDPR grace period (30 dni)

### Multi-tenancy:
- Vsechny tabulky krome User, Skill, Plan, FeatureFlag maji `workspace_id` pro RLS
- MVP: CASL na aplikacni urovni
- Phase 2: + RLS jako defense-in-depth

### Pojmenovani:
- Tabulky: `snake_case`, mnozne cislo (workspaces, crews, agents)
- Sloupce: `snake_case`
- Indexy: `idx_{tabulka}_{sloupce}`
- Unique constraints: `uq_{tabulka}_{sloupce}`
- Foreign keys: `fk_{tabulka}_{reference}`

---

## 4. PRISMA SCHEMA

```prisma
// ============================================================
// GENERATORY A DATASOURCE
// ============================================================

generator client {
  provider = "prisma-client-js"
}

datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

// ============================================================
// ENUMY
// ============================================================

enum OrgRole {
  OWNER
  ADMIN
  MANAGER
  MEMBER
  VIEWER
}

enum AgentStatus {
  IDLE
  RUNNING
  ERROR
  STOPPED
}

enum LLMProvider {
  OPENAI
  ANTHROPIC
  GOOGLE
  OLLAMA
}

enum CLIAdapter {
  CLAUDE_CODE
  OPENCODE
  CODEX_CLI
  GEMINI_CLI
}

enum ToolProfile {
  MINIMAL
  CODING
  MESSAGING
  FULL
}

enum AgentRole {
  AGENT        // default — radovy agent, specializovany na konkretni ukoly
  LEAD         // 1 per crew — vedouci crew, orchestruje agenty, primarni kontakt pro uzivatele
  COORDINATOR  // 1 per workspace — koordinator, orchestruje cross-crew, prirazuje leadum
}

enum AssignmentStatus {
  PENDING
  RUNNING
  COMPLETED
  FAILED
  TIMEOUT
  CANCELLED
}

enum SkillSource {
  BUNDLED
  MANAGED
  MARKETPLACE
  CUSTOM
}

enum SkillCategory {
  CODING
  MESSAGING
  AUTOMATION
  DATA
  DEVOPS
  SUPPORT
  SALES
  CUSTOM
}

enum VerificationStatus {
  UNVERIFIED       // cerstve submitnuty, neprosel pipeline
  PENDING_REVIEW   // v security pipeline / manual review
  VERIFIED         // prosel 6-krokovym auditem (ADR-020)
  REJECTED         // zamitnut (security issue)
  DEPRECATED       // starsi verze, existuje novejsi
}

enum SkillPricing {
  FREE             // zdarma pro vsechny plan tiers
  PREMIUM          // placeny, revenue share s autorem (ADR-019)
}

enum SessionMode {
  CHAT
  TASK
}

enum SessionStatus {
  ACTIVE
  COMPLETED
  ERROR
}

enum RunStatus {
  PENDING
  RUNNING
  COMPLETED
  FAILED
  CANCELLED
  TIMEOUT
}

enum RunTrigger {
  USER
  WEBHOOK
  CRON
  AGENT
  SYSTEM
}

enum SubscriptionStatus {
  ACTIVE
  PAST_DUE
  CANCELLED
  TRIALING
  INCOMPLETE
}

enum PlanTier {
  FREE
  PRO
  TEAM
  ENTERPRISE
}

enum CredentialScope {
  WORKSPACE
  CREW
}

// ============================================================
// 1. USER
// ============================================================
// Spravovany pres NextAuth.js Prisma adapter.
// hashed_password: pro CredentialsProvider (email+password login)
// email_verified: pro NextAuth email verification flow

model User {
  id                    String    @id @db.Uuid
  email                 String    @unique
  full_name             String?
  avatar_url            String?
  hashed_password       String?
  email_verified        DateTime? @db.Timestamptz
  onboarding_completed  Boolean   @default(false)  // migration 2: onboarding wizard done
  created_at            DateTime  @default(now()) @db.Timestamptz
  updated_at            DateTime  @default(now()) @updatedAt @db.Timestamptz

  accounts              Account[]
  sessions              Session[]
  workspace_memberships WorkspaceMember[]
  crew_memberships      CrewMember[]
  sent_invitations      WorkspaceInvitation[] @relation("InvitedBy")
  created_credentials   Credential[]          @relation("CreatedBy")
  created_chats         Chat[]                @relation("CreatedBy")
  triggered_runs        AgentRun[]            @relation("TriggeredBy")
  config_changes        AgentConfigHistory[]  @relation("ChangedBy")
  audit_logs            AuditLog[]
  authored_skills       Skill[]               @relation("SkillAuthor")
  skill_reviews         SkillReview[]

  @@map("users")
}

// ============================================================
// 1a. NEXTAUTH ADAPTER TABLES
// ============================================================
// Tyto tabulky jsou vyzadovany NextAuth.js Prisma adapterem.
// V Go runtime slouzi pro OAuth provider storage a session management.

model Account {
  id                String  @id @db.Uuid
  userId            String  @db.Uuid
  type              String
  provider          String
  providerAccountId String
  refresh_token     String?
  access_token      String?
  expires_at        Int?
  token_type        String?
  scope             String?
  id_token          String?
  session_state     String?

  user User @relation(fields: [userId], references: [id], onDelete: Cascade)

  @@unique([provider, providerAccountId])
  @@map("accounts")
}

model Session {
  id           String   @id @db.Uuid
  sessionToken String   @unique
  userId       String   @db.Uuid
  expires      DateTime

  user User @relation(fields: [userId], references: [id], onDelete: Cascade)

  @@map("sessions")
}

model VerificationToken {
  identifier String
  token      String @unique
  expires    DateTime

  @@unique([identifier, token])
  @@map("verification_tokens")
}

// ============================================================
// 2. WORKSPACE (Firma)
// ============================================================

model Workspace {
  id         String    @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  name       String
  slug       String    @unique
  logo_url   String?
  created_at DateTime  @default(now()) @db.Timestamptz
  updated_at DateTime  @default(now()) @updatedAt @db.Timestamptz
  deleted_at DateTime? @db.Timestamptz

  default_container_ttl_hours Int?  // null = kontejnery bezi porad

  members      WorkspaceMember[]
  invitations  WorkspaceInvitation[]
  crews        Crew[]
  agents       Agent[]
  credentials  Credential[]
  chats        Chat[]
  runs         AgentRun[]
  audit_logs   AuditLog[]
  subscription Subscription?
  flag_overrides FeatureFlagOverride[]
  assignments  Assignment[]

  @@map("workspaces")
}

// ============================================================
// 3. WORKSPACE MEMBER
// ============================================================

model WorkspaceMember {
  id           String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id String   @db.Uuid
  user_id      String   @db.Uuid
  role         OrgRole  @default(MEMBER)
  created_at   DateTime @default(now()) @db.Timestamptz
  updated_at   DateTime @default(now()) @updatedAt @db.Timestamptz

  workspace Workspace @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  user      User      @relation(fields: [user_id], references: [id], onDelete: Cascade)

  @@unique([workspace_id, user_id], name: "uq_workspace_member")
  @@index([workspace_id], name: "idx_workspace_member_workspace")
  @@index([user_id], name: "idx_workspace_member_user")
  @@map("workspace_members")
}

// ============================================================
// 4. WORKSPACE INVITATION
// ============================================================

model WorkspaceInvitation {
  id           String    @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id String    @db.Uuid
  email        String
  role         OrgRole   @default(MEMBER)
  invited_by   String    @db.Uuid
  token        String    @unique @default(dbgenerated("gen_random_uuid()"))
  expires_at   DateTime  @db.Timestamptz
  accepted_at  DateTime? @db.Timestamptz
  created_at   DateTime  @default(now()) @db.Timestamptz

  workspace Workspace @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  inviter   User      @relation("InvitedBy", fields: [invited_by], references: [id])

  @@index([workspace_id], name: "idx_invitation_workspace")
  @@index([token], name: "idx_invitation_token")
  @@index([email, workspace_id], name: "idx_invitation_email_workspace")
  @@map("workspace_invitations")
}

// ============================================================
// 5. CREW (Posadka / Oddeleni = 1 Docker kontejner)
// ============================================================

model Crew {
  id           String    @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id String    @db.Uuid
  name         String
  slug         String
  description  String?
  color        String?   @db.VarChar(7)
  icon         String?   @db.VarChar(10)
  created_at   DateTime  @default(now()) @db.Timestamptz
  updated_at   DateTime  @default(now()) @updatedAt @db.Timestamptz
  deleted_at   DateTime? @db.Timestamptz

  container_ttl_hours Int?
  container_memory_mb Int     @default(4096)
  container_cpus      Float   @default(2.0)

  workspace Workspace    @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  members   CrewMember[]
  agents    Agent[]

  @@unique([workspace_id, slug], name: "uq_crew_slug")
  @@index([workspace_id], name: "idx_crew_workspace")
  @@map("crews")
}

// ============================================================
// 6. CREW MEMBER
// ============================================================

model CrewMember {
  id         String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  crew_id    String   @db.Uuid
  user_id    String   @db.Uuid
  created_at DateTime @default(now()) @db.Timestamptz

  crew Crew @relation(fields: [crew_id], references: [id], onDelete: Cascade)
  user User @relation(fields: [user_id], references: [id], onDelete: Cascade)

  @@unique([crew_id, user_id], name: "uq_crew_member")
  @@index([crew_id], name: "idx_crew_member_crew")
  @@index([user_id], name: "idx_crew_member_user")
  @@map("crew_members")
}

// ============================================================
// 7. AGENT (Virtualni zamestnanec)
// ============================================================

model Agent {
  id              String          @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  crew_id         String?         @db.Uuid  // NULLABLE: Coordinator nema crew (patri workspace)
  workspace_id    String          @db.Uuid
  name            String
  slug            String
  description     String?
  role_title      String?          // "DevOps Engineer", "Sales Rep"
  agent_role      AgentRole       @default(AGENT)  // AGENT | LEAD | COORDINATOR
  status          AgentStatus     @default(IDLE)
  cli_adapter     CLIAdapter      @default(CLAUDE_CODE)
  llm_provider    LLMProvider?
  llm_model       String?
  system_prompt   String?         @db.Text
  temperature     Float           @default(0.7)
  max_tokens      Int?
  timeout_seconds Int             @default(1800)
  tool_profile    ToolProfile     @default(CODING)
  memory_enabled  Boolean         @default(false)
  memory_config   String?         @db.Text  // migration 3: JSON config for agent memory system
  webhook_secret  String?         // per-agent webhook auth token (generated, stored encrypted)
  created_at      DateTime        @default(now()) @db.Timestamptz
  updated_at      DateTime        @default(now()) @updatedAt @db.Timestamptz
  deleted_at      DateTime?       @db.Timestamptz

  // Orchestrace — lead/coordinator specificke
  // POZN: DB sloupce pouzivaji "delegation" naming (delegation_timeout_s, max_delegation_depth, max_parallel_delegates)
  delegation_timeout_s   Int?     // override timeout pro delegations (default: 2x agent timeout)
  max_delegation_depth   Int?     @default(3)   // max hloubka delegations (coordinator→lead→agent)
  max_parallel_delegates Int?     @default(5)   // max paralelne bezicich delegations

  crew             Crew?                 @relation(fields: [crew_id], references: [id], onDelete: Cascade)
  workspace        Workspace             @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  skills           AgentSkill[]
  credentials      AgentCredential[]
  chats            Chat[]
  runs             AgentRun[]
  config_history   AgentConfigHistory[]
  assignments_sent Assignment[]          @relation("AssignedBy")
  assignments_recv Assignment[]          @relation("AssignedTo")

  @@unique([workspace_id, slug], name: "uq_agent_slug")  // POZN: unique per workspace, NE per crew
  @@index([workspace_id], name: "idx_agent_workspace")
  @@index([crew_id], name: "idx_agent_crew")
  @@index([status], name: "idx_agent_status")
  @@index([agent_role], name: "idx_agent_role")
  @@map("agents")
}

// ============================================================
// 7a. COST TRACKING (planovane)
// ============================================================
// Agent.budget_limit_usd -- maximalni mesicni budget per agent (nullable)
// Agent.budget_alert_threshold -- prah pro alerting (0-100%, default 80%)
// AgentRun.estimated_cost_usd -- odhadovane naklady per run
// AgentRun.token_count_input -- pocet input tokenu
// AgentRun.token_count_output -- pocet output tokenu

// ============================================================
// 7b. ASSIGNMENT (Phase 2 — orchestracni audit)
// ============================================================
// Zaznamenava vsechny assignments mezi agenty (lead→agent, coordinator→lead).
// Umoznuje vizualizaci assignment stromu a audit kdo komu co priradil.
// Viz prd/ORCHESTRATION.md pro kompletni specifikaci.

model Assignment {
  id              String           @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id    String           @db.Uuid
  chat_id         String           @db.Uuid
  assigned_by_id  String           @db.Uuid  // kdo priradil (lead/coordinator)
  assigned_to_id  String           @db.Uuid  // komu (agent/lead)
  task            String           @db.Text  // co bylo prirazeno
  status          AssignmentStatus @default(PENDING)
  started_at      DateTime?        @db.Timestamptz
  finished_at     DateTime?        @db.Timestamptz
  result_summary  String?          @db.Text  // shrnuti vysledku od target agenta
  error_message   String?          @db.Text
  group_id        String?          // pro paralelni assignments (wait_group)
  created_at      DateTime         @default(now()) @db.Timestamptz

  workspace    Workspace @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  chat         Chat      @relation(fields: [chat_id], references: [id])
  assigned_by  Agent     @relation("AssignedBy", fields: [assigned_by_id], references: [id])
  assigned_to  Agent     @relation("AssignedTo", fields: [assigned_to_id], references: [id])

  @@index([chat_id], name: "idx_assignment_chat")
  @@index([assigned_by_id], name: "idx_assignment_by")
  @@index([assigned_to_id], name: "idx_assignment_to")
  @@index([group_id], name: "idx_assignment_group")
  @@map("assignments")
}

// ============================================================
// 8. SKILL
// ============================================================

model Skill {
  id            String        @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  name          String        @unique
  slug          String        @unique
  display_name  String
  description   String?       @db.Text
  version       String        @default("1.0.0")
  author        String?
  license       String?       @default("MIT")
  category      SkillCategory @default(CUSTOM)
  source        SkillSource   @default(CUSTOM)
  config_schema Json?
  tool_definitions Json?       // cached MCP tool schemas (for UI display)
  content       String?       @db.Text  // system prompt fragment
  icon          String?       @db.VarChar(10)
  created_at    DateTime      @default(now()) @db.Timestamptz
  updated_at    DateTime      @default(now()) @updatedAt @db.Timestamptz

  // MCP Server definice (Phase 2, viz AGENT-RUNTIME.md 6A)
  mcp_server_command  String?           // command pro spusteni MCP serveru ("npx @modelcontextprotocol/server-github")
  mcp_server_image    String?           // Docker image MCP serveru (pro marketplace skills)
  mcp_transport       String? @default("stdio")  // "stdio" | "sse" | "streamable-http"
  credential_requirements Json?         // [{"env_var": "GITHUB_TOKEN", "description": "GitHub PAT", "required": true}]
  dependencies        Json?             // {"apt": ["git"], "pip": [], "npm": ["@octokit/rest"]}
  tool_count          Int?              // pocet toolu v MCP serveru (pro UI display)
  defer_loading       Boolean @default(false)  // true = on-demand via tool search (ADR-016)

  // === MARKETPLACE / SKILL HUB (Phase 3, viz ADR-019, ADR-020, ADR-021) ===
  verification      VerificationStatus @default(UNVERIFIED) // status v security pipeline
  security_score    Int?               // 0-100, vysledek automated pipeline (ADR-020)
  security_report   Json?              // vysledky security scanu {steps: [...], score: N, scanned_at: "..."}
  downloads         Int                @default(0)           // pocet instalaci
  rating_avg        Float?             // 1.0-5.0 prumerne hodnoceni
  rating_count      Int                @default(0)           // pocet hodnoceni
  tags              String[]           // ["github", "devops", "ci-cd", "code-review"]
  featured          Boolean            @default(false)       // featured na marketplace homepage
  oci_image         String?            // "ghcr.io/crewship-ai/skills/github:1.2.0" (ADR-021)
  oci_digest        String?            // SHA256 digest pro integritu (ADR-021)
  sbom_url          String?            // link na SBOM (CycloneDX format)
  allowed_domains   String[]           // ["api.github.com", "*.github.com"] pro srt sandbox (ADR-017)
  pricing_tier      SkillPricing       @default(FREE)        // FREE | PREMIUM
  price_monthly     Int?               // cena v centech (pro premium, revenue share)
  author_id         String?            @db.Uuid              // autor skillu (pro revenue share)
  revenue_share_pct Int?               @default(70)          // % z price_monthly pro autora
  changelog         Json?              // [{"version": "1.2.0", "date": "...", "changes": "..."}]

  agent_skills AgentSkill[]
  reviews      SkillReview[]
  author_user  User?         @relation("SkillAuthor", fields: [author_id], references: [id])

  @@index([category], name: "idx_skill_category")
  @@index([source], name: "idx_skill_source")
  @@index([verification], name: "idx_skill_verification")
  @@index([featured], name: "idx_skill_featured")
  @@map("skills")
}

// ============================================================
// 8a. SKILL PERMISSIONS (planovane)
// ============================================================
// Skill.permissions_json -- JSON s deklarovanymi permissions (filesystem, network, secrets, shell)
// Skill.badge -- enum: OFFICIAL, VERIFIED, COMMUNITY
// Skill.install_count -- pocet instalaci
// Skill.rating_avg -- prumerne hodnoceni

// ============================================================
// 8B. SKILL REVIEW (marketplace recenze, ADR-019)
// ============================================================
// Uzivatel muze ohodnotit skill 1-5 a napsat recenzi.
// Jeden uzivatel = maximalne jedna recenze na skill.

model SkillReview {
  id         String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  skill_id   String   @db.Uuid
  user_id    String   @db.Uuid
  rating     Int                      // 1-5
  title      String?
  body       String?  @db.Text
  created_at DateTime @default(now()) @db.Timestamptz
  updated_at DateTime @default(now()) @updatedAt @db.Timestamptz

  skill Skill @relation(fields: [skill_id], references: [id], onDelete: Cascade)
  user  User  @relation(fields: [user_id], references: [id])

  @@unique([skill_id, user_id], name: "uq_skill_review_user")
  @@index([skill_id], name: "idx_skill_review_skill")
  @@map("skill_reviews")
}

// ============================================================
// 9. AGENT SKILL (M:N)
// ============================================================

model AgentSkill {
  id         String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  agent_id   String   @db.Uuid
  skill_id   String   @db.Uuid
  config     Json?              // per-agent skill configuration
  enabled    Boolean  @default(true)  // lze deaktivovat bez odebrani
  created_at DateTime @default(now()) @db.Timestamptz

  agent Agent @relation(fields: [agent_id], references: [id], onDelete: Cascade)
  skill Skill @relation(fields: [skill_id], references: [id], onDelete: Cascade)

  @@unique([agent_id, skill_id], name: "uq_agent_skill")
  @@map("agent_skills")
}

// ============================================================
// 10. CREDENTIAL (Sifrovane opravneni)
// ============================================================
// encrypted_value obsahuje key version prefix: "v1:base64data"
// To umoznuje budouci key rotation bez ztraceni starych credentials.

model Credential {
  id                      String          @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id            String          @db.Uuid
  crew_id                 String?         @db.Uuid  // null = workspace-wide
  name                    String
  description             String?
  encrypted_value         String          @db.Text  // "v1:" + AES-256-GCM sifrovana hodnota
  scope                   CredentialScope @default(WORKSPACE)
  type                    String          @default("SECRET")   // SECRET | API_KEY | OAUTH_TOKEN | AI_CLI_TOKEN
  provider                String          @default("NONE")     // NONE | ANTHROPIC | OPENAI | GOOGLE | GITHUB | CUSTOM
  status                  String          @default("ACTIVE")   // ACTIVE | EXPIRED | REVOKED | ERROR
  encrypted_refresh_token String?         @db.Text  // OAuth refresh token (encrypted)
  token_expires_at        DateTime?       @db.Timestamptz      // OAuth token expiry
  account_label           String?         // user-friendly label ("Personal Anthropic")
  account_email           String?         // associated account email
  last_checked_at         DateTime?       @db.Timestamptz      // last health check
  last_error              String?         @db.Text             // last error message from health check
  created_by              String          @db.Uuid
  created_at              DateTime        @default(now()) @db.Timestamptz
  updated_at              DateTime        @default(now()) @updatedAt @db.Timestamptz
  deleted_at              DateTime?       @db.Timestamptz

  workspace        Workspace        @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  creator          User             @relation("CreatedBy", fields: [created_by], references: [id])
  agent_credentials AgentCredential[]

  @@unique([workspace_id, name], name: "uq_credential_name")
  @@index([workspace_id], name: "idx_credential_workspace")
  @@index([crew_id], name: "idx_credential_crew")
  @@index([workspace_id, type, provider], name: "idx_credential_type_provider")
  @@map("credentials")
}

// ============================================================
// 11. AGENT CREDENTIAL (M:N)
// ============================================================
// CONSTRAINT: Pokud credential.scope = CREW, agent musi byt ve stejne crew.
// Toto se kontroluje na aplikacni urovni (CASL/service layer), ne DB constraint.

model AgentCredential {
  id            String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  agent_id      String   @db.Uuid
  credential_id String   @db.Uuid
  env_var_name  String   // nazev ENV promenne pri injekci ("OPENAI_API_KEY")
  priority      Int      @default(0)  // 0=primary, 1+=fallback (umoznuje credential pool)
  created_at    DateTime @default(now()) @db.Timestamptz

  agent      Agent      @relation(fields: [agent_id], references: [id], onDelete: Cascade)
  credential Credential @relation(fields: [credential_id], references: [id], onDelete: Cascade)

  @@unique([agent_id, credential_id], name: "uq_agent_credential")
  // POZNAMKA: Zadny @@unique na [agent_id, env_var_name] — agent muze mit VIC
  // credentials pro stejny env_var_name (credential pool pro round-robin/failover).
  // Viz sekce 5.1 "Credential Pool Pattern".
  @@index([agent_id, env_var_name], name: "idx_agent_credential_env")
  @@map("agent_credentials")
}

// ============================================================
// 12. CHAT (METADATA ONLY)
// ============================================================
// Samotne zpravy jsou v JSONL souboru:
//   /var/lib/crewship/conversations/{workspace_id}/{agent_id}/{chat_id}.jsonl
// PostgreSQL drzi jen metadata pro querying (seznam chats, filtering, pagination).
// Obsah zprav se cte primo z JSONL souboru pres Go service (crewshipd).

model Chat {
  id            String        @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  agent_id      String        @db.Uuid
  workspace_id  String        @db.Uuid
  created_by    String?       @db.Uuid  // null pro webhook/cron triggered chats
  title         String?
  mode          SessionMode   @default(CHAT)
  status        SessionStatus @default(ACTIVE)
  message_count Int           @default(0)  // pocet zprav v JSONL souboru
  jsonl_path    String?       // relativni cesta k JSONL souboru
  started_at    DateTime      @default(now()) @db.Timestamptz
  ended_at      DateTime?     @db.Timestamptz
  created_at    DateTime      @default(now()) @db.Timestamptz
  updated_at    DateTime      @default(now()) @updatedAt @db.Timestamptz

  agent       Agent     @relation(fields: [agent_id], references: [id], onDelete: Cascade)
  workspace   Workspace @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  creator     User?     @relation("CreatedBy", fields: [created_by], references: [id])
  runs        AgentRun[]
  assignments Assignment[]

  @@index([agent_id], name: "idx_chat_agent")
  @@index([workspace_id], name: "idx_chat_workspace")
  @@index([created_at], name: "idx_chat_created")
  @@map("chats")
}

// ============================================================
// 13. AGENT RUN (Jednotlivy beh agenta)
// ============================================================

model AgentRun {
  id            String      @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  agent_id      String      @db.Uuid
  chat_id       String?     @db.Uuid  // null pro standalone runs
  workspace_id  String      @db.Uuid
  triggered_by  String?     @db.Uuid  // null pro webhook/cron/system triggery
  trigger_type  RunTrigger  @default(USER)  // kdo spustil run
  status        RunStatus   @default(PENDING)
  started_at    DateTime?   @db.Timestamptz
  finished_at   DateTime?   @db.Timestamptz
  error_message String?     @db.Text
  exit_code     Int?
  metadata      Json?       // cli adapter, model, duration, token count
  created_at    DateTime    @default(now()) @db.Timestamptz

  agent     Agent      @relation(fields: [agent_id], references: [id], onDelete: Cascade)
  chat      Chat?      @relation(fields: [chat_id], references: [id])
  workspace Workspace  @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  triggerer User?      @relation("TriggeredBy", fields: [triggered_by], references: [id])

  @@index([agent_id, created_at], name: "idx_run_agent_time")
  @@index([workspace_id], name: "idx_run_workspace")
  @@index([status], name: "idx_run_status")
  @@map("agent_runs")
}

// ============================================================
// 14. AUDIT LOG
// ============================================================
// IMMUTABLE — zadny updated_at, zadny deleted_at, nikdy se nemaze.
// Append-only tabulka (v produkci chattr +a na tablespace).

model AuditLog {
  id          String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id String  @db.Uuid
  user_id     String?  @db.Uuid  // null pro system akce
  action      String   // "agent.created", "credential.added", "crew.member.invited"
  entity_type String   // "Agent", "Crew", "Credential"
  entity_id   String?  @db.Uuid
  metadata    Json?    // old/new values, request info
  ip_address  String?  @db.VarChar(45)
  user_agent  String?
  created_at  DateTime @default(now()) @db.Timestamptz

  workspace Workspace @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  user      User?     @relation(fields: [user_id], references: [id])

  @@index([workspace_id, created_at], name: "idx_audit_workspace_time")
  @@index([entity_type, entity_id], name: "idx_audit_entity")
  @@index([user_id], name: "idx_audit_user")
  @@index([action], name: "idx_audit_action")
  @@map("audit_logs")
}

// ============================================================
// 15. SUBSCRIPTION (Stripe)
// ============================================================

model Subscription {
  id                     String             @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id           String             @unique @db.Uuid
  plan_id                String             @db.Uuid
  stripe_customer_id     String?            @unique
  stripe_subscription_id String?            @unique
  status                 SubscriptionStatus @default(ACTIVE)
  current_period_start   DateTime?          @db.Timestamptz
  current_period_end     DateTime?          @db.Timestamptz
  cancel_at              DateTime?          @db.Timestamptz
  created_at             DateTime           @default(now()) @db.Timestamptz
  updated_at             DateTime           @default(now()) @updatedAt @db.Timestamptz

  workspace Workspace @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  plan      Plan      @relation(fields: [plan_id], references: [id])

  @@map("subscriptions")
}

// ============================================================
// 16. PLAN
// ============================================================

model Plan {
  id               String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  tier             PlanTier @unique
  display_name     String
  stripe_price_id  String?  @unique
  max_agents       Int      // -1 = unlimited
  max_crews        Int
  max_skills       Int      // per agent
  max_credentials  Int      // per workspace
  max_members      Int      // per workspace
  features         Json?
  price_monthly    Int      @default(0)  // v centech (2900 = $29)
  created_at       DateTime @default(now()) @db.Timestamptz
  updated_at       DateTime @default(now()) @updatedAt @db.Timestamptz

  subscriptions Subscription[]

  @@map("plans")
}

// ============================================================
// 17. FEATURE FLAG
// ============================================================

model FeatureFlag {
  id          String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  key         String   @unique
  description String?
  enabled     Boolean  @default(false)
  percentage  Int      @default(0)  // 0-100, gradual rollout
  created_at  DateTime @default(now()) @db.Timestamptz
  updated_at  DateTime @default(now()) @updatedAt @db.Timestamptz

  overrides FeatureFlagOverride[]

  @@map("feature_flags")
}

// ============================================================
// 18. FEATURE FLAG OVERRIDE (per workspace)
// ============================================================

model FeatureFlagOverride {
  id           String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  flag_id      String   @db.Uuid
  workspace_id String   @db.Uuid
  enabled      Boolean
  created_at   DateTime @default(now()) @db.Timestamptz

  flag      FeatureFlag @relation(fields: [flag_id], references: [id], onDelete: Cascade)
  workspace Workspace   @relation(fields: [workspace_id], references: [id], onDelete: Cascade)

  @@unique([flag_id, workspace_id], name: "uq_flag_override")
  @@map("feature_flag_overrides")
}

// ============================================================
// 19. AGENT CONFIG HISTORY (Phase 2)
// ============================================================

model AgentConfigHistory {
  id         String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  agent_id   String   @db.Uuid
  changed_by String   @db.Uuid
  version    Int
  changes    Json     // diff
  snapshot   Json     // plna konfigurace
  created_at DateTime @default(now()) @db.Timestamptz

  agent   Agent @relation(fields: [agent_id], references: [id], onDelete: Cascade)
  changer User  @relation("ChangedBy", fields: [changed_by], references: [id])

  @@unique([agent_id, version], name: "uq_config_version")
  @@index([agent_id, created_at], name: "idx_config_history_agent_time")
  @@map("agent_config_history")
}
```

---

## 5. CREDENTIAL POOL PATTERN

### 5.1 Koncept

Agent muze mit VIC credentials prirazeny ke stejnemu `env_var_name`. To umoznuje:

1. **Round-robin** — strida klice pri kazdem spusteni (rozlozeni zateze)
2. **Failover** — pri rate limit (429) automaticky prepne na dalsi klic
3. **Least-used** — pouzije klic s nejmensim poctem pouziti dnes

### 5.2 Priklad

```
Agent "Senior Dev" ma prirazeno:
  ANTHROPIC_API_KEY → Credential "Anthropic Key 1" (priority=0, primary)
  ANTHROPIC_API_KEY → Credential "Anthropic Key 2" (priority=1, fallback)
  ANTHROPIC_API_KEY → Credential "Anthropic Key 3" (priority=2, fallback)
  OPENAI_API_KEY    → Credential "OpenAI Key 1"    (priority=0, jediny)
```

### 5.3 Vyber klice pri spusteni agenta

```go
// crewshipd vybira credential z poolu:
// 1. Nacte vsechny credentials pro dany env_var_name (serazene dle priority)
// 2. Vynecha credentials v cooldown stavu (po 429 erroru)
// 3. Vybere podle strategie (MVP: priority order, Phase 2: round-robin/least-used)

func (o *Orchestrator) selectCredential(agentID, envVarName string) (*Credential, error) {
    pool := o.getCredentialPool(agentID, envVarName) // sorted by priority ASC
    for _, cred := range pool {
        if !o.isInCooldown(cred.ID) {
            return cred, nil
        }
    }
    return nil, fmt.Errorf("all credentials for %s are in cooldown", envVarName)
}
```

### 5.4 Failover pri rate limit (429)

```
1. Agent bezi s klicem priority=0
2. LLM API vrati 429 (rate limit exceeded)
3. Agent run selze s exit code 1, stderr obsahuje "rate limit" nebo "429"
4. crewshipd detekuje rate limit error
5. Oznaci klic priority=0 jako "cooldown" (5 minut)
6. Vybere dalsi klic z poolu (priority=1)
7. Restartuje agenta s novym klicem (context preservation — viz AGENT-RUNTIME.md)
8. Uzivatel vidi plynuly prechod v chat UI
```

### 5.5 MVP vs Phase 2

| Aspekt | MVP | Phase 2 |
|---|---|---|
| Multi-key pool | ✅ priority sloupec, crewshipd vybira | ✅ Plna implementace |
| Automatic failover | ❌ Manualni (uzivatel zmeni klic) | ✅ Detekce 429, auto-switch |
| Context preservation | ❌ Jen Claude Code --resume | ✅ JSONL catch-up pro vsechny CLI |
| Usage tracking | ❌ Zadne | ✅ Token counting per key per day |
| Cooldown management | ❌ Zadne | ✅ 429 → 5min cooldown → dalsi klic |
| Pool strategie | Priority order only | Round-robin, least-used, failover |

### 5.6 Cross-provider fallback (Phase 2+)

Agent muze mit credentials pro RUZNE LLM providery. Pokud Anthropic klic vycerpa,
crewshipd muze prepnout na OpenAI klic — vyzaduje zmenu CLI adapteru a modelu:

```
Agent "Senior Dev":
  ANTHROPIC_API_KEY → Claude Code adapter (primary)
  OPENAI_API_KEY    → Codex CLI adapter (fallback)
```

Toto vyzaduje zmenu `cli_adapter` a `llm_model` za behu — slozitejsi, odlozeno na Phase 2+.

---

## 6. KONVERZACE — JSONL FORMAT

Kazdy chat ma jeden JSONL soubor. Kazdy radek = jedna zprava:

```jsonl
{"id":"msg-uuid","role":"user","type":"text","content":"Vytvor report o socialnich sitich","user_id":"user-uuid","ts":"2026-02-11T10:00:00Z"}
{"id":"msg-uuid","role":"assistant","type":"thinking","content":"Analyzing social media data...","ts":"2026-02-11T10:00:01Z"}
{"id":"msg-uuid","role":"assistant","type":"tool_call","content":"{\"tool\":\"web-search\",\"args\":{\"query\":\"social media stats 2026\"}}","ts":"2026-02-11T10:00:02Z"}
{"id":"msg-uuid","role":"assistant","type":"tool_result","content":"{\"results\":[...]}","metadata":{"tokens":150},"ts":"2026-02-11T10:00:05Z"}
{"id":"msg-uuid","role":"assistant","type":"text","content":"Here is your social media report...","metadata":{"tokens":500,"model":"claude-sonnet-4"},"ts":"2026-02-11T10:00:10Z"}
```

### Jak se ctou zpravy:
1. UI pozada o zpravy chat X
2. Next.js posle request na Go service (pres Unix socket)
3. Go service precte JSONL soubor, parsuje, vrati jako JSON array
4. Podporuje pagination: `?offset=0&limit=50` (radky v JSONL)

### Proc JSONL a ne PostgreSQL:
- Konverzace mohou mit tisice zprav (LLM thinking, tool calls) — zbytecna zátez na DB
- JSONL je append-only — nativne rychle pro zapis
- Konzistentni s logy (stejny format, stejny storage model)
- Snizuje PostgreSQL load — DB resi jen strukturovana data

---

## 7. RLS POLITIKY (Phase 2)

> **MVP: Zadne RLS.** Autorizace pres CASL na aplikacni urovni.
> RLS se prida v Phase 2 jako defense-in-depth.

### Pattern: PostgreSQL session variables (univerzalni, bez vendor lock-in)
```sql
-- Pred kazdym dotazem Prisma middleware nastavi:
SELECT set_config('app.current_user_id', $userId, true);
SELECT set_config('app.current_workspace_id', $workspaceId, true);

-- RLS politika:
CREATE POLICY "workspace_isolation" ON agents FOR ALL
  USING (workspace_id = current_setting('app.current_workspace_id', true)::uuid);
```

Funguje na jakomkoli PostgreSQL (plain, RDS, Cloud SQL, Supabase).

### RLS matice (Phase 2)

| Tabulka | SELECT | INSERT | UPDATE | DELETE |
|---|---|---|---|---|
| workspaces | Member of workspace | Authenticated | OWNER/ADMIN | OWNER only |
| workspace_members | Member of workspace | OWNER/ADMIN | OWNER/ADMIN | OWNER/ADMIN |
| crews | Member of workspace (crew-scoped pro MGR/MBR) | OWNER/ADMIN/MANAGER | OWNER/ADMIN/MANAGER | OWNER/ADMIN |
| agents | Member of crew | OWNER/ADMIN/MANAGER | OWNER/ADMIN/MANAGER | OWNER/ADMIN |
| credentials | OWNER/ADMIN/MANAGER (masked) | OWNER/ADMIN/MANAGER | OWNER/ADMIN/MANAGER | OWNER/ADMIN |
| chats | Member of crew | Any member | - | - |
| agent_runs | Member of crew | System/Any member | System only | - |
| audit_logs | OWNER/ADMIN (all), MGR (crew) | System only | Never | Never |

---

## 8. INDEXY (SOUHRN)

```sql
-- Nejcastejsi dotazy
CREATE INDEX idx_agent_crew ON agents(crew_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_workspace ON agents(workspace_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_chat_agent ON chats(agent_id);
CREATE INDEX idx_audit_workspace_time ON audit_logs(workspace_id, created_at DESC);
CREATE INDEX idx_run_status ON agent_runs(status) WHERE status IN ('PENDING', 'RUNNING');

-- Partial indexy (soft delete)
CREATE INDEX idx_workspace_active ON workspaces(id) WHERE deleted_at IS NULL;
CREATE INDEX idx_crew_active ON crews(workspace_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_active ON agents(crew_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_credential_active ON credentials(workspace_id) WHERE deleted_at IS NULL;
```

---

## 9. SEED DATA

```sql
-- Plan tiers
INSERT INTO plans (tier, display_name, max_agents, max_crews, max_skills, max_credentials, max_members, price_monthly) VALUES
  ('FREE',       'Community',  5,   2,  5,  10,  5,     0),
  ('PRO',        'Pro',        20,  5,  20, 50,  10,  2900),
  ('TEAM',       'Team',       100, -1, -1, 200, 50,  7900),
  ('ENTERPRISE', 'Enterprise', -1,  -1, -1, -1,  -1,     0);

-- Built-in skills
INSERT INTO skills (name, slug, display_name, description, category, source) VALUES
  ('coding-assistant',  'coding-assistant',  'Coding Assistant',  'General coding, debugging, code review', 'CODING',  'BUNDLED'),
  ('web-researcher',    'web-researcher',    'Web Researcher',    'Web search, content extraction, summarization', 'DATA', 'BUNDLED'),
  ('customer-support',  'customer-support',  'Customer Support',  'Email handling, ticket management, FAQ', 'SUPPORT', 'BUNDLED');

-- Feature flags
INSERT INTO feature_flags (key, description, enabled) VALUES
  ('billing_enabled',    'Enable Stripe billing (enterprise only)',  false),
  ('marketplace_enabled','Enable skills marketplace',                false),
  ('orchestration',      'Enable multi-agent orchestration',         false),
  ('task_mode',          'Enable async task mode',                   false),
  ('config_history',     'Enable agent config versioning',           false),
  ('advanced_audit',     'Enable advanced audit log UI + export',    false);
```

---

## 10. AUTENTIZACE (Go -- NextAuth-compatible)

### MVP: Go auth endpoints (NextAuth-compatible JWE)

- Email + heslo (bcrypt hashing v Go, `internal/api/auth.go`)
- OAuth (Google, GitHub -- planovano)
- Session management (JWT/JWE, `internal/auth/`)
- NextAuth-compatible endpoints: `/api/auth/csrf`, `/api/auth/session`, `/api/auth/signin`, `/api/auth/signout`
- Funguje se SQLite (default) i PostgreSQL

### Pouziti v kodu
```go
// internal/api/middleware.go
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        token, err := s.auth.ValidateRequest(r)
        if err != nil {
            http.Error(w, "Unauthorized", http.StatusUnauthorized)
            return
        }
        ctx := context.WithValue(r.Context(), userKey, token.UserID)
        next(w, r.WithContext(ctx))
    }
}
```

---

## 11. COMMUNITY vs ENTERPRISE MODE

Stejna schema, dva mody:

| Aspekt | Community | Enterprise |
|---|---|---|
| Seed data | Auto-create 1 workspace + admin | Prazdna DB, registrace |
| Plan | FREE (5 agents, 2 crews) | Plny tier system |
| Feature flags | billing=false, audit=false | Dle planu |
| Subscription | Existuje ale prazdna | Aktivni (Stripe) |

---

## 12. DOCKER COMPOSE (Lokalni dev)

Viz `docker/docker-compose.yml` — jen PostgreSQL 16 (zadny Redis).

```bash
docker compose -f docker/docker-compose.yml up -d
```

---

## 13. MIGRACNI STRATEGIE

- **Go migration system** (`internal/database/migrate.go`) = manages actual DB schema (24 tables, 3 migrations)
- **Prisma schema** = used ONLY for TypeScript type generation, NOT for migrations
- Go migration system handles both SQLite and PostgreSQL
- **SQL skripty** pro RLS politiky a triggers (Phase 2)
- **Triggers:** `update_updated_at()` na vsech tabulkach s `updated_at`

```sql
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
```

---

## 14. OTEVRENE OTAZKY

1. **pgvector extension** — pripravit pro budouci RAG (Phase 3)?
2. **Full-text search** — PostgreSQL `tsvector` pro chat titles/metadata?
3. **Partitioning** — audit_logs muze rust rychle. Partitioning po mesicich az bude potreba?
4. **Archivace** — stare audit logy do cold storage (S3) po X mesicich?
