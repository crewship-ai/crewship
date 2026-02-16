# Crewship -- Database Schema (DATABASE.md)

**Verze:** 2.0
**Datum:** 2026-02-11
**ORM:** Prisma (JEDINY zpusob pristupu k DB z Next.js)
**Databaze:** PostgreSQL 16+ (plain PostgreSQL, RDS, Cloud SQL — jakykoli provider)
**Auth:** NextAuth.js (Auth.js v5) s Prisma adapterem
**Autorizace (MVP):** CASL (aplikacni uroven) -- zadne RLS v MVP
**Autorizace (Phase 2):** CASL + RLS jako defense-in-depth (`current_setting()` pattern)
**Multi-tenancy:** org_id sloupec + CASL (MVP), + RLS (Phase 2)
**Mody:** Community (1 org, free) | Enterprise (multi-org, placeny)

---

## 1. CO JE V POSTGRESQL A CO NE

| Data | Kde | Duvod |
|---|---|---|
| Uzivatele, organizace, tymy, agenti | PostgreSQL | Strukturovana data, relace, RBAC |
| Credentials (sifrovane) | PostgreSQL | AES-256-GCM, pristup pres Prisma |
| Skills, plany, feature flags | PostgreSQL | Konfigurace platformy |
| Audit log | PostgreSQL | Immutable, queryable, GDPR |
| Subscription (Stripe) | PostgreSQL | Billing stav |
| **Session metadata** | **PostgreSQL** | ID, agent, title, status, cas — queryable |
| **Konverzacni zpravy** | **JSONL soubory** | /var/lib/crewship/conversations/{org}/{agent}/{session}.jsonl |
| **Logy agentu** | **JSONL soubory** | /var/log/crewship/teams/{team}/agents/{agent}/current.jsonl |
| **Agent live status** | **Go pamet + bbolt** | crewshipd drzi v pameti, persistuje do bbolt WAL |
| **WebSocket sessions** | **Go pamet** | goroutine per connection, zadna DB |
| **Container status** | **Go pamet + Docker API** | crewshipd se pta Docker SDK |
| **Rate limiting** | **Go pamet** | In-memory token bucket (MVP), per-process |

> **PRAVIDLO:** PostgreSQL = strukturovana data s relacemi. Vsechno ostatni (logy, zpravy, live stav) je mimo DB.

---

## 2. PREHLED ENTIT

**20 tabulek** rozdelenychdo 6 domen:

| Domena | Tabulky | Popis |
|---|---|---|
| **Uzivatele & Org** | User, Organization, OrganizationMember, OrganizationInvitation | Multi-tenant zaklad |
| **Tymy** | Team, TeamMember | Izolacni boundary (1 kontejner = 1 tym) |
| **Agenti** | Agent, AgentSkill, AgentCredential, AgentConfigHistory, DelegationLog | Virtualni zamestnanci + orchestrace |
| **Skills & Credentials** | Skill, SkillReview, Credential | Dovednosti, marketplace recenze, opravneni |
| **Konverzace & Behy** | ConversationSession (metadata only), AgentRun | Session metadata, behy |
| **System** | AuditLog, Subscription, Plan, FeatureFlag, FeatureFlagOverride | Billing, audit, flags |

> **Pozor:** ConversationSession je **metadata-only** model. Samotne zpravy jsou v JSONL souborech, NE v PostgreSQL.

### Entity Relationship Diagram (textovy)
```
Organization (1) ──── (*) OrganizationMember (*) ──── (1) User
     │                         │
     │                    OrganizationInvitation
     │
     ├── (*) Team (1) ──── (*) TeamMember (*) ──── (1) User
     │        │
     │        ├── (*) Agent (agent_role: WORKER | LEADER | DIRECTOR)
     │        │       ├── (*) AgentSkill (*) ──── (1) Skill ──── (*) SkillReview (*) ──── (1) User
     │        │       ├── (*) AgentCredential (*) ──── (1) Credential
     │        │       ├── (*) AgentConfigHistory
     │        │       ├── (*) ConversationSession (metadata only, zpravy v JSONL)
     │        │       ├── (*) AgentRun
     │        │       └── (*) DelegationLog (source/target — leader↔worker, director↔leader)
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
- Hlavni entity (Organization, Team, Agent, Credential) maji `deleted_at` (TIMESTAMPTZ, nullable)
- Dotazy VZDY filtruje `WHERE deleted_at IS NULL` (Prisma middleware)
- Hard delete az po GDPR grace period (30 dni)

### Multi-tenancy:
- Vsechny tabulky krome User, Skill, Plan, FeatureFlag maji `org_id` pro RLS
- MVP: CASL na aplikacni urovni
- Phase 2: + RLS jako defense-in-depth

### Pojmenovani:
- Tabulky: `snake_case`, mnozne cislo (organizations, teams, agents)
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
  WORKER       // default — radovy agent, specializovany na konkretni ukoly
  LEADER       // 1 per team — sef tymu, orchestruje workery, primarni kontakt pro uzivatele
  DIRECTOR     // 1 per org — reditel, orchestruje cross-team, deleguje na leadery
}

enum DelegationStatus {
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
  ORGANIZATION
  TEAM
}

// ============================================================
// 1. USER
// ============================================================
// Spravovany pres NextAuth.js Prisma adapter.
// hashed_password: pro CredentialsProvider (email+password login)
// email_verified: pro NextAuth email verification flow

model User {
  id              String    @id @db.Uuid
  email           String    @unique
  full_name       String?
  avatar_url      String?
  hashed_password String?
  email_verified  DateTime? @db.Timestamptz
  created_at      DateTime  @default(now()) @db.Timestamptz
  updated_at      DateTime  @default(now()) @updatedAt @db.Timestamptz

  org_memberships     OrganizationMember[]
  team_memberships    TeamMember[]
  sent_invitations    OrganizationInvitation[] @relation("InvitedBy")
  created_credentials Credential[]             @relation("CreatedBy")
  created_sessions    ConversationSession[]    @relation("CreatedBy")
  triggered_runs      AgentRun[]               @relation("TriggeredBy")
  config_changes      AgentConfigHistory[]     @relation("ChangedBy")
  audit_logs          AuditLog[]
  authored_skills     Skill[]                  @relation("SkillAuthor")
  skill_reviews       SkillReview[]

  @@map("users")
}

// ============================================================
// 2. ORGANIZATION (Firma)
// ============================================================

model Organization {
  id         String    @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  name       String
  slug       String    @unique
  logo_url   String?
  created_at DateTime  @default(now()) @db.Timestamptz
  updated_at DateTime  @default(now()) @updatedAt @db.Timestamptz
  deleted_at DateTime? @db.Timestamptz

  default_container_ttl_hours Int?  // null = kontejnery bezi porad

  members      OrganizationMember[]
  invitations  OrganizationInvitation[]
  teams        Team[]
  agents       Agent[]
  credentials  Credential[]
  sessions     ConversationSession[]
  runs         AgentRun[]
  audit_logs   AuditLog[]
  subscription Subscription?
  flag_overrides FeatureFlagOverride[]
  delegation_logs DelegationLog[]

  @@map("organizations")
}

// ============================================================
// 3. ORGANIZATION MEMBER
// ============================================================

model OrganizationMember {
  id         String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  org_id     String   @db.Uuid
  user_id    String   @db.Uuid
  role       OrgRole  @default(MEMBER)
  created_at DateTime @default(now()) @db.Timestamptz
  updated_at DateTime @default(now()) @updatedAt @db.Timestamptz

  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)
  user         User         @relation(fields: [user_id], references: [id], onDelete: Cascade)

  @@unique([org_id, user_id], name: "uq_org_member")
  @@index([org_id], name: "idx_org_member_org")
  @@index([user_id], name: "idx_org_member_user")
  @@map("organization_members")
}

// ============================================================
// 4. ORGANIZATION INVITATION
// ============================================================

model OrganizationInvitation {
  id          String    @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  org_id      String    @db.Uuid
  email       String
  role        OrgRole   @default(MEMBER)
  invited_by  String    @db.Uuid
  token       String    @unique @default(dbgenerated("gen_random_uuid()"))
  expires_at  DateTime  @db.Timestamptz
  accepted_at DateTime? @db.Timestamptz
  created_at  DateTime  @default(now()) @db.Timestamptz

  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)
  inviter      User         @relation("InvitedBy", fields: [invited_by], references: [id])

  @@index([org_id], name: "idx_invitation_org")
  @@index([token], name: "idx_invitation_token")
  @@index([email, org_id], name: "idx_invitation_email_org")
  @@map("organization_invitations")
}

// ============================================================
// 5. TEAM (Tym / Oddeleni = 1 Docker kontejner)
// ============================================================

model Team {
  id          String    @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  org_id      String    @db.Uuid
  name        String
  slug        String
  description String?
  color       String?   @db.VarChar(7)
  icon        String?   @db.VarChar(10)
  created_at  DateTime  @default(now()) @db.Timestamptz
  updated_at  DateTime  @default(now()) @updatedAt @db.Timestamptz
  deleted_at  DateTime? @db.Timestamptz

  container_ttl_hours Int?
  container_memory_mb Int     @default(4096)
  container_cpus      Float   @default(2.0)

  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)
  members      TeamMember[]
  agents       Agent[]

  @@unique([org_id, slug], name: "uq_team_slug")
  @@index([org_id], name: "idx_team_org")
  @@map("teams")
}

// ============================================================
// 6. TEAM MEMBER
// ============================================================

model TeamMember {
  id         String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  team_id    String   @db.Uuid
  user_id    String   @db.Uuid
  created_at DateTime @default(now()) @db.Timestamptz

  team Team @relation(fields: [team_id], references: [id], onDelete: Cascade)
  user User @relation(fields: [user_id], references: [id], onDelete: Cascade)

  @@unique([team_id, user_id], name: "uq_team_member")
  @@index([team_id], name: "idx_team_member_team")
  @@index([user_id], name: "idx_team_member_user")
  @@map("team_members")
}

// ============================================================
// 7. AGENT (Virtualni zamestnanec)
// ============================================================

model Agent {
  id              String          @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  team_id         String?         @db.Uuid  // NULLABLE: Director nema team (patri org)
  org_id          String          @db.Uuid
  name            String
  slug            String
  description     String?
  role_title      String?          // "DevOps Engineer", "Sales Rep"
  agent_role      AgentRole       @default(WORKER)  // WORKER | LEADER | DIRECTOR
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
  webhook_secret  String?         // per-agent webhook auth token (generated, stored encrypted)
  created_at      DateTime        @default(now()) @db.Timestamptz
  updated_at      DateTime        @default(now()) @updatedAt @db.Timestamptz
  deleted_at      DateTime?       @db.Timestamptz

  // Orchestrace — leader/director specificke
  delegation_timeout_s   Int?     // override timeout pro delegace (default: 2x agent timeout)
  max_delegation_depth   Int?     @default(3)   // max hloubka delegace (director→leader→worker)
  max_parallel_delegates Int?     @default(5)   // max paralelne bezicich delegaci

  team             Team?                 @relation(fields: [team_id], references: [id], onDelete: Cascade)
  organization     Organization          @relation(fields: [org_id], references: [id], onDelete: Cascade)
  skills           AgentSkill[]
  credentials      AgentCredential[]
  sessions         ConversationSession[]
  runs             AgentRun[]
  config_history   AgentConfigHistory[]
  delegations_sent DelegationLog[]       @relation("DelegatedBy")
  delegations_recv DelegationLog[]       @relation("DelegatedTo")

  @@unique([team_id, slug], name: "uq_agent_slug")
  @@index([org_id], name: "idx_agent_org")
  @@index([team_id], name: "idx_agent_team")
  @@index([status], name: "idx_agent_status")
  @@index([agent_role], name: "idx_agent_role")
  @@map("agents")
}

// ============================================================
// 7b. DELEGATION LOG (Phase 2 — orchestracni audit)
// ============================================================
// Zaznamenava vsechny delegace mezi agenty (leader→worker, director→leader).
// Umoznuje vizualizaci delegacniho stromu a audit kdo komu co delegoval.
// Viz prd/ORCHESTRATION.md pro kompletni specifikaci.

model DelegationLog {
  id              String           @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  org_id          String           @db.Uuid
  session_id      String           @db.Uuid
  source_agent_id String           @db.Uuid  // kdo delegoval (leader/director)
  target_agent_id String           @db.Uuid  // komu (worker/leader)
  task            String           @db.Text  // co bylo delegovano
  status          DelegationStatus @default(PENDING)
  started_at      DateTime?        @db.Timestamptz
  finished_at     DateTime?        @db.Timestamptz
  result_summary  String?          @db.Text  // shrnuti vysledku od target agenta
  error_message   String?          @db.Text
  group_id        String?          // pro paralelni delegace (wait_group)
  created_at      DateTime         @default(now()) @db.Timestamptz

  organization Organization        @relation(fields: [org_id], references: [id], onDelete: Cascade)
  session      ConversationSession @relation(fields: [session_id], references: [id])
  source_agent Agent               @relation("DelegatedBy", fields: [source_agent_id], references: [id])
  target_agent Agent               @relation("DelegatedTo", fields: [target_agent_id], references: [id])

  @@index([session_id], name: "idx_delegation_session")
  @@index([source_agent_id], name: "idx_delegation_source")
  @@index([target_agent_id], name: "idx_delegation_target")
  @@index([group_id], name: "idx_delegation_group")
  @@map("delegation_logs")
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
  id              String          @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  org_id          String          @db.Uuid
  team_id         String?         @db.Uuid  // null = org-wide
  name            String
  description     String?
  encrypted_value String          @db.Text  // "v1:" + AES-256-GCM sifrovana hodnota
  scope           CredentialScope @default(ORGANIZATION)
  created_by      String          @db.Uuid
  created_at      DateTime        @default(now()) @db.Timestamptz
  updated_at      DateTime        @default(now()) @updatedAt @db.Timestamptz
  deleted_at      DateTime?       @db.Timestamptz

  organization     Organization     @relation(fields: [org_id], references: [id], onDelete: Cascade)
  creator          User             @relation("CreatedBy", fields: [created_by], references: [id])
  agent_credentials AgentCredential[]

  @@unique([org_id, name], name: "uq_credential_name")
  @@index([org_id], name: "idx_credential_org")
  @@index([team_id], name: "idx_credential_team")
  @@map("credentials")
}

// ============================================================
// 11. AGENT CREDENTIAL (M:N)
// ============================================================
// CONSTRAINT: Pokud credential.scope = TEAM, agent musi byt ve stejnem tymu.
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
// 12. CONVERSATION SESSION (METADATA ONLY)
// ============================================================
// Samotne zpravy jsou v JSONL souboru:
//   /var/lib/crewship/conversations/{org_id}/{agent_id}/{session_id}.jsonl
// PostgreSQL drzi jen metadata pro querying (seznam sessions, filtering, pagination).
// Obsah zprav se cte primo z JSONL souboru pres Go service (crewshipd).

model ConversationSession {
  id            String        @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  agent_id      String        @db.Uuid
  org_id        String        @db.Uuid
  created_by    String?       @db.Uuid  // null pro webhook/cron triggered sessions
  title         String?
  mode          SessionMode   @default(CHAT)
  status        SessionStatus @default(ACTIVE)
  message_count Int           @default(0)  // pocet zprav v JSONL souboru
  jsonl_path    String?       // relativni cesta k JSONL souboru
  started_at    DateTime      @default(now()) @db.Timestamptz
  ended_at      DateTime?     @db.Timestamptz
  created_at    DateTime      @default(now()) @db.Timestamptz
  updated_at    DateTime      @default(now()) @updatedAt @db.Timestamptz

  agent        Agent        @relation(fields: [agent_id], references: [id], onDelete: Cascade)
  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)
  creator      User?        @relation("CreatedBy", fields: [created_by], references: [id])
  runs         AgentRun[]
  delegations  DelegationLog[]

  @@index([agent_id], name: "idx_session_agent")
  @@index([org_id], name: "idx_session_org")
  @@index([created_at], name: "idx_session_created")
  @@map("conversation_sessions")
}

// ============================================================
// 13. AGENT RUN (Jednotlivy beh agenta)
// ============================================================

model AgentRun {
  id            String      @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  agent_id      String      @db.Uuid
  session_id    String?     @db.Uuid  // null pro standalone runs
  org_id        String      @db.Uuid
  triggered_by  String?     @db.Uuid  // null pro webhook/cron/system triggery
  trigger_type  RunTrigger  @default(USER)  // kdo spustil run
  status        RunStatus   @default(PENDING)
  started_at    DateTime?   @db.Timestamptz
  finished_at   DateTime?   @db.Timestamptz
  error_message String?     @db.Text
  exit_code     Int?
  metadata      Json?       // cli adapter, model, duration, token count
  created_at    DateTime    @default(now()) @db.Timestamptz

  agent        Agent                @relation(fields: [agent_id], references: [id], onDelete: Cascade)
  session      ConversationSession? @relation(fields: [session_id], references: [id])
  organization Organization         @relation(fields: [org_id], references: [id], onDelete: Cascade)
  triggerer    User?                @relation("TriggeredBy", fields: [triggered_by], references: [id])

  @@index([agent_id, created_at], name: "idx_run_agent_time")
  @@index([org_id], name: "idx_run_org")
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
  org_id      String   @db.Uuid
  user_id     String?  @db.Uuid  // null pro system akce
  action      String   // "agent.created", "credential.added", "team.member.invited"
  entity_type String   // "Agent", "Team", "Credential"
  entity_id   String?  @db.Uuid
  metadata    Json?    // old/new values, request info
  ip_address  String?  @db.VarChar(45)
  user_agent  String?
  created_at  DateTime @default(now()) @db.Timestamptz

  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)
  user         User?        @relation(fields: [user_id], references: [id])

  @@index([org_id, created_at], name: "idx_audit_org_time")
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
  org_id                 String             @unique @db.Uuid
  plan_id                String             @db.Uuid
  stripe_customer_id     String?            @unique
  stripe_subscription_id String?            @unique
  status                 SubscriptionStatus @default(ACTIVE)
  current_period_start   DateTime?          @db.Timestamptz
  current_period_end     DateTime?          @db.Timestamptz
  cancel_at              DateTime?          @db.Timestamptz
  created_at             DateTime           @default(now()) @db.Timestamptz
  updated_at             DateTime           @default(now()) @updatedAt @db.Timestamptz

  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)
  plan         Plan         @relation(fields: [plan_id], references: [id])

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
  max_teams        Int
  max_skills       Int      // per agent
  max_credentials  Int      // per org
  max_members      Int      // per org
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
// 18. FEATURE FLAG OVERRIDE (per org)
// ============================================================

model FeatureFlagOverride {
  id         String   @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  flag_id    String   @db.Uuid
  org_id     String   @db.Uuid
  enabled    Boolean
  created_at DateTime @default(now()) @db.Timestamptz

  flag         FeatureFlag  @relation(fields: [flag_id], references: [id], onDelete: Cascade)
  organization Organization @relation(fields: [org_id], references: [id], onDelete: Cascade)

  @@unique([flag_id, org_id], name: "uq_flag_override")
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

Kazda session ma jeden JSONL soubor. Kazdy radek = jedna zprava:

```jsonl
{"id":"msg-uuid","role":"user","type":"text","content":"Vytvor report o socialnich sitich","user_id":"user-uuid","ts":"2026-02-11T10:00:00Z"}
{"id":"msg-uuid","role":"assistant","type":"thinking","content":"Analyzing social media data...","ts":"2026-02-11T10:00:01Z"}
{"id":"msg-uuid","role":"assistant","type":"tool_call","content":"{\"tool\":\"web-search\",\"args\":{\"query\":\"social media stats 2026\"}}","ts":"2026-02-11T10:00:02Z"}
{"id":"msg-uuid","role":"assistant","type":"tool_result","content":"{\"results\":[...]}","metadata":{"tokens":150},"ts":"2026-02-11T10:00:05Z"}
{"id":"msg-uuid","role":"assistant","type":"text","content":"Here is your social media report...","metadata":{"tokens":500,"model":"claude-sonnet-4"},"ts":"2026-02-11T10:00:10Z"}
```

### Jak se ctou zpravy:
1. UI pozada o zpravy session X
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
SELECT set_config('app.current_org_id', $orgId, true);

-- RLS politika:
CREATE POLICY "org_isolation" ON agents FOR ALL
  USING (org_id = current_setting('app.current_org_id', true)::uuid);
```

Funguje na jakomkoli PostgreSQL (plain, RDS, Cloud SQL, Supabase).

### RLS matice (Phase 2)

| Tabulka | SELECT | INSERT | UPDATE | DELETE |
|---|---|---|---|---|
| organizations | Member of org | Authenticated | OWNER/ADMIN | OWNER only |
| organization_members | Member of org | OWNER/ADMIN | OWNER/ADMIN | OWNER/ADMIN |
| teams | Member of org (team-scoped pro MGR/MBR) | OWNER/ADMIN/MANAGER | OWNER/ADMIN/MANAGER | OWNER/ADMIN |
| agents | Member of team | OWNER/ADMIN/MANAGER | OWNER/ADMIN/MANAGER | OWNER/ADMIN |
| credentials | OWNER/ADMIN/MANAGER (masked) | OWNER/ADMIN/MANAGER | OWNER/ADMIN/MANAGER | OWNER/ADMIN |
| conversation_sessions | Member of team | Any member | - | - |
| agent_runs | Member of team | System/Any member | System only | - |
| audit_logs | OWNER/ADMIN (all), MGR (team) | System only | Never | Never |

---

## 8. INDEXY (SOUHRN)

```sql
-- Nejcastejsi dotazy
CREATE INDEX idx_agent_team ON agents(team_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_org ON agents(org_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_session_agent ON conversation_sessions(agent_id);
CREATE INDEX idx_audit_org_time ON audit_logs(org_id, created_at DESC);
CREATE INDEX idx_run_status ON agent_runs(status) WHERE status IN ('PENDING', 'RUNNING');

-- Partial indexy (soft delete)
CREATE INDEX idx_org_active ON organizations(id) WHERE deleted_at IS NULL;
CREATE INDEX idx_team_active ON teams(org_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_agent_active ON agents(team_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_credential_active ON credentials(org_id) WHERE deleted_at IS NULL;
```

---

## 9. SEED DATA

```sql
-- Plan tiers
INSERT INTO plans (tier, display_name, max_agents, max_teams, max_skills, max_credentials, max_members, price_monthly) VALUES
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

## 10. AUTENTIZACE (NextAuth.js)

### MVP: NextAuth.js (Auth.js v5) s Prisma adapterem

- Email + heslo (CredentialsProvider, hashed_password v User tabulce)
- OAuth (Google, GitHub)
- Session management (JWT)
- Funguje s jakoukoli PostgreSQL

### Pouziti v kodu
```typescript
// lib/auth.ts
import { auth } from '@/auth'

export async function getCurrentUser() {
  const session = await auth()
  if (!session?.user?.id) return null
  return prisma.user.findUnique({ where: { id: session.user.id } })
}

export async function requireAuth() {
  const user = await getCurrentUser()
  if (!user) throw new Error('Unauthorized')
  return user
}
```

---

## 11. COMMUNITY vs ENTERPRISE MODE

Stejna schema, dva mody:

| Aspekt | Community | Enterprise |
|---|---|---|
| Seed data | Auto-create 1 org + admin | Prazdna DB, registrace |
| Plan | FREE (5 agents, 2 teams) | Plny tier system |
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

- **Prisma** = source of truth pro schema
- `prisma db push` pro development
- `prisma migrate` pro production
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
2. **Full-text search** — PostgreSQL `tsvector` pro session titles/metadata?
3. **Partitioning** — audit_logs muze rust rychle. Partitioning po mesicich az bude potreba?
4. **Archivace** — stare audit logy do cold storage (S3) po X mesicich?
