-- Crewship initial migration
-- Generated from prisma/schema.prisma (20 tables)

-- Enums
CREATE TYPE "OrgRole" AS ENUM ('OWNER', 'ADMIN', 'MANAGER', 'MEMBER', 'VIEWER');
CREATE TYPE "AgentStatus" AS ENUM ('IDLE', 'RUNNING', 'ERROR', 'STOPPED');
CREATE TYPE "LLMProvider" AS ENUM ('OPENAI', 'ANTHROPIC', 'GOOGLE', 'OLLAMA');
CREATE TYPE "CLIAdapter" AS ENUM ('CLAUDE_CODE', 'OPENCODE', 'CODEX_CLI', 'GEMINI_CLI');
CREATE TYPE "ToolProfile" AS ENUM ('MINIMAL', 'CODING', 'MESSAGING', 'FULL');
CREATE TYPE "AgentRole" AS ENUM ('WORKER', 'LEADER', 'DIRECTOR');
CREATE TYPE "DelegationStatus" AS ENUM ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'TIMEOUT', 'CANCELLED');
CREATE TYPE "SkillSource" AS ENUM ('BUNDLED', 'MANAGED', 'MARKETPLACE', 'CUSTOM');
CREATE TYPE "SkillCategory" AS ENUM ('CODING', 'MESSAGING', 'AUTOMATION', 'DATA', 'DEVOPS', 'SUPPORT', 'SALES', 'CUSTOM');
CREATE TYPE "VerificationStatus" AS ENUM ('UNVERIFIED', 'PENDING_REVIEW', 'VERIFIED', 'REJECTED', 'DEPRECATED');
CREATE TYPE "SkillPricing" AS ENUM ('FREE', 'PREMIUM');
CREATE TYPE "SessionMode" AS ENUM ('CHAT', 'TASK');
CREATE TYPE "SessionStatus" AS ENUM ('ACTIVE', 'COMPLETED', 'ERROR');
CREATE TYPE "RunStatus" AS ENUM ('PENDING', 'RUNNING', 'COMPLETED', 'FAILED', 'CANCELLED', 'TIMEOUT');
CREATE TYPE "RunTrigger" AS ENUM ('USER', 'WEBHOOK', 'CRON', 'AGENT', 'SYSTEM');
CREATE TYPE "SubscriptionStatus" AS ENUM ('ACTIVE', 'PAST_DUE', 'CANCELLED', 'TRIALING', 'INCOMPLETE');
CREATE TYPE "PlanTier" AS ENUM ('FREE', 'PRO', 'TEAM', 'ENTERPRISE');
CREATE TYPE "CredentialScope" AS ENUM ('ORGANIZATION', 'TEAM');

-- 1. Users
CREATE TABLE "users" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "email" TEXT NOT NULL,
    "full_name" TEXT,
    "avatar_url" TEXT,
    "hashed_password" TEXT,
    "email_verified" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "users_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "users_email_key" ON "users"("email");

-- NextAuth: Accounts
CREATE TABLE "accounts" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "userId" UUID NOT NULL,
    "type" TEXT NOT NULL,
    "provider" TEXT NOT NULL,
    "providerAccountId" TEXT NOT NULL,
    "refresh_token" TEXT,
    "access_token" TEXT,
    "expires_at" INTEGER,
    "token_type" TEXT,
    "scope" TEXT,
    "id_token" TEXT,
    "session_state" TEXT,
    CONSTRAINT "accounts_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "accounts_provider_providerAccountId_key" ON "accounts"("provider", "providerAccountId");

-- NextAuth: Sessions
CREATE TABLE "sessions" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "sessionToken" TEXT NOT NULL,
    "userId" UUID NOT NULL,
    "expires" TIMESTAMP(3) NOT NULL,
    CONSTRAINT "sessions_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "sessions_sessionToken_key" ON "sessions"("sessionToken");

-- NextAuth: Verification tokens
CREATE TABLE "verification_tokens" (
    "identifier" TEXT NOT NULL,
    "token" TEXT NOT NULL,
    "expires" TIMESTAMP(3) NOT NULL
);
CREATE UNIQUE INDEX "verification_tokens_token_key" ON "verification_tokens"("token");
CREATE UNIQUE INDEX "verification_tokens_identifier_token_key" ON "verification_tokens"("identifier", "token");

-- 2. Organizations
CREATE TABLE "organizations" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "name" TEXT NOT NULL,
    "slug" TEXT NOT NULL,
    "logo_url" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "deleted_at" TIMESTAMPTZ,
    "default_container_ttl_hours" INTEGER,
    CONSTRAINT "organizations_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "organizations_slug_key" ON "organizations"("slug");

-- 3. Organization members
CREATE TABLE "organization_members" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "role" "OrgRole" NOT NULL DEFAULT 'MEMBER',
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "organization_members_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_org_member" ON "organization_members"("org_id", "user_id");
CREATE INDEX "idx_org_member_org" ON "organization_members"("org_id");
CREATE INDEX "idx_org_member_user" ON "organization_members"("user_id");

-- 4. Organization invitations
CREATE TABLE "organization_invitations" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" UUID NOT NULL,
    "email" TEXT NOT NULL,
    "role" "OrgRole" NOT NULL DEFAULT 'MEMBER',
    "invited_by" UUID NOT NULL,
    "token" TEXT NOT NULL DEFAULT gen_random_uuid(),
    "expires_at" TIMESTAMPTZ NOT NULL,
    "accepted_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "organization_invitations_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "organization_invitations_token_key" ON "organization_invitations"("token");
CREATE INDEX "idx_invitation_org" ON "organization_invitations"("org_id");
CREATE INDEX "idx_invitation_token" ON "organization_invitations"("token");
CREATE INDEX "idx_invitation_email_org" ON "organization_invitations"("email", "org_id");

-- 5. Teams
CREATE TABLE "teams" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "slug" TEXT NOT NULL,
    "description" TEXT,
    "color" VARCHAR(7),
    "icon" VARCHAR(10),
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "deleted_at" TIMESTAMPTZ,
    "container_ttl_hours" INTEGER,
    "container_memory_mb" INTEGER NOT NULL DEFAULT 4096,
    "container_cpus" DOUBLE PRECISION NOT NULL DEFAULT 2.0,
    CONSTRAINT "teams_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_team_slug" ON "teams"("org_id", "slug");
CREATE INDEX "idx_team_org" ON "teams"("org_id");

-- 6. Team members
CREATE TABLE "team_members" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "team_id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "team_members_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_team_member" ON "team_members"("team_id", "user_id");
CREATE INDEX "idx_team_member_team" ON "team_members"("team_id");
CREATE INDEX "idx_team_member_user" ON "team_members"("user_id");

-- 7. Agents
CREATE TABLE "agents" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "team_id" UUID,
    "org_id" UUID NOT NULL,
    "name" TEXT NOT NULL,
    "slug" TEXT NOT NULL,
    "description" TEXT,
    "role_title" TEXT,
    "agent_role" "AgentRole" NOT NULL DEFAULT 'WORKER',
    "status" "AgentStatus" NOT NULL DEFAULT 'IDLE',
    "cli_adapter" "CLIAdapter" NOT NULL DEFAULT 'CLAUDE_CODE',
    "llm_provider" "LLMProvider",
    "llm_model" TEXT,
    "system_prompt" TEXT,
    "temperature" DOUBLE PRECISION NOT NULL DEFAULT 0.7,
    "max_tokens" INTEGER,
    "timeout_seconds" INTEGER NOT NULL DEFAULT 1800,
    "tool_profile" "ToolProfile" NOT NULL DEFAULT 'CODING',
    "memory_enabled" BOOLEAN NOT NULL DEFAULT false,
    "webhook_secret" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "deleted_at" TIMESTAMPTZ,
    "delegation_timeout_s" INTEGER,
    "max_delegation_depth" INTEGER DEFAULT 3,
    "max_parallel_delegates" INTEGER DEFAULT 5,
    CONSTRAINT "agents_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_agent_slug" ON "agents"("org_id", "slug");
CREATE INDEX "idx_agent_org" ON "agents"("org_id");
CREATE INDEX "idx_agent_team" ON "agents"("team_id");
CREATE INDEX "idx_agent_status" ON "agents"("status");
CREATE INDEX "idx_agent_role" ON "agents"("agent_role");

-- 7b. Delegation logs
CREATE TABLE "delegation_logs" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" UUID NOT NULL,
    "session_id" UUID NOT NULL,
    "source_agent_id" UUID NOT NULL,
    "target_agent_id" UUID NOT NULL,
    "task" TEXT NOT NULL,
    "status" "DelegationStatus" NOT NULL DEFAULT 'PENDING',
    "started_at" TIMESTAMPTZ,
    "finished_at" TIMESTAMPTZ,
    "result_summary" TEXT,
    "error_message" TEXT,
    "group_id" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "delegation_logs_pkey" PRIMARY KEY ("id")
);
CREATE INDEX "idx_delegation_session" ON "delegation_logs"("session_id");
CREATE INDEX "idx_delegation_source" ON "delegation_logs"("source_agent_id");
CREATE INDEX "idx_delegation_target" ON "delegation_logs"("target_agent_id");
CREATE INDEX "idx_delegation_group" ON "delegation_logs"("group_id");

-- 8. Skills
CREATE TABLE "skills" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "name" TEXT NOT NULL,
    "slug" TEXT NOT NULL,
    "display_name" TEXT NOT NULL,
    "description" TEXT,
    "version" TEXT NOT NULL DEFAULT '1.0.0',
    "author" TEXT,
    "license" TEXT DEFAULT 'MIT',
    "category" "SkillCategory" NOT NULL DEFAULT 'CUSTOM',
    "source" "SkillSource" NOT NULL DEFAULT 'CUSTOM',
    "config_schema" JSONB,
    "tool_definitions" JSONB,
    "content" TEXT,
    "icon" VARCHAR(10),
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "mcp_server_command" TEXT,
    "mcp_server_image" TEXT,
    "mcp_transport" TEXT DEFAULT 'stdio',
    "credential_requirements" JSONB,
    "dependencies" JSONB,
    "tool_count" INTEGER,
    "defer_loading" BOOLEAN NOT NULL DEFAULT false,
    "verification" "VerificationStatus" NOT NULL DEFAULT 'UNVERIFIED',
    "security_score" INTEGER,
    "security_report" JSONB,
    "downloads" INTEGER NOT NULL DEFAULT 0,
    "rating_avg" DOUBLE PRECISION,
    "rating_count" INTEGER NOT NULL DEFAULT 0,
    "tags" TEXT[],
    "featured" BOOLEAN NOT NULL DEFAULT false,
    "oci_image" TEXT,
    "oci_digest" TEXT,
    "sbom_url" TEXT,
    "allowed_domains" TEXT[],
    "pricing_tier" "SkillPricing" NOT NULL DEFAULT 'FREE',
    "price_monthly" INTEGER,
    "author_id" UUID,
    "revenue_share_pct" INTEGER DEFAULT 70,
    "changelog" JSONB,
    CONSTRAINT "skills_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "skills_name_key" ON "skills"("name");
CREATE UNIQUE INDEX "skills_slug_key" ON "skills"("slug");
CREATE INDEX "idx_skill_category" ON "skills"("category");
CREATE INDEX "idx_skill_source" ON "skills"("source");
CREATE INDEX "idx_skill_verification" ON "skills"("verification");
CREATE INDEX "idx_skill_featured" ON "skills"("featured");

-- 8b. Skill reviews
CREATE TABLE "skill_reviews" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "skill_id" UUID NOT NULL,
    "user_id" UUID NOT NULL,
    "rating" INTEGER NOT NULL,
    "title" TEXT,
    "body" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "skill_reviews_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_skill_review_user" ON "skill_reviews"("skill_id", "user_id");
CREATE INDEX "idx_skill_review_skill" ON "skill_reviews"("skill_id");

-- 9. Agent skills
CREATE TABLE "agent_skills" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "agent_id" UUID NOT NULL,
    "skill_id" UUID NOT NULL,
    "config" JSONB,
    "enabled" BOOLEAN NOT NULL DEFAULT true,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "agent_skills_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_agent_skill" ON "agent_skills"("agent_id", "skill_id");

-- 10. Credentials
CREATE TABLE "credentials" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" UUID NOT NULL,
    "team_id" UUID,
    "name" TEXT NOT NULL,
    "description" TEXT,
    "encrypted_value" TEXT NOT NULL,
    "scope" "CredentialScope" NOT NULL DEFAULT 'ORGANIZATION',
    "created_by" UUID NOT NULL,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "deleted_at" TIMESTAMPTZ,
    CONSTRAINT "credentials_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_credential_name" ON "credentials"("org_id", "name");
CREATE INDEX "idx_credential_org" ON "credentials"("org_id");
CREATE INDEX "idx_credential_team" ON "credentials"("team_id");

-- 11. Agent credentials
CREATE TABLE "agent_credentials" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "agent_id" UUID NOT NULL,
    "credential_id" UUID NOT NULL,
    "env_var_name" TEXT NOT NULL,
    "priority" INTEGER NOT NULL DEFAULT 0,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "agent_credentials_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_agent_credential" ON "agent_credentials"("agent_id", "credential_id");
CREATE INDEX "idx_agent_credential_env" ON "agent_credentials"("agent_id", "env_var_name");

-- 12. Conversation sessions
CREATE TABLE "conversation_sessions" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "agent_id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "created_by" UUID,
    "title" TEXT,
    "mode" "SessionMode" NOT NULL DEFAULT 'CHAT',
    "status" "SessionStatus" NOT NULL DEFAULT 'ACTIVE',
    "message_count" INTEGER NOT NULL DEFAULT 0,
    "jsonl_path" TEXT,
    "started_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "ended_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "conversation_sessions_pkey" PRIMARY KEY ("id")
);
CREATE INDEX "idx_session_agent" ON "conversation_sessions"("agent_id");
CREATE INDEX "idx_session_org" ON "conversation_sessions"("org_id");
CREATE INDEX "idx_session_created" ON "conversation_sessions"("created_at");

-- 13. Agent runs
CREATE TABLE "agent_runs" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "agent_id" UUID NOT NULL,
    "session_id" UUID,
    "org_id" UUID NOT NULL,
    "triggered_by" UUID,
    "trigger_type" "RunTrigger" NOT NULL DEFAULT 'USER',
    "status" "RunStatus" NOT NULL DEFAULT 'PENDING',
    "started_at" TIMESTAMPTZ,
    "finished_at" TIMESTAMPTZ,
    "error_message" TEXT,
    "exit_code" INTEGER,
    "metadata" JSONB,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "agent_runs_pkey" PRIMARY KEY ("id")
);
CREATE INDEX "idx_run_agent_time" ON "agent_runs"("agent_id", "created_at");
CREATE INDEX "idx_run_org" ON "agent_runs"("org_id");
CREATE INDEX "idx_run_status" ON "agent_runs"("status");

-- 14. Audit logs
CREATE TABLE "audit_logs" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" UUID NOT NULL,
    "user_id" UUID,
    "action" TEXT NOT NULL,
    "entity_type" TEXT NOT NULL,
    "entity_id" UUID,
    "metadata" JSONB,
    "ip_address" VARCHAR(45),
    "user_agent" TEXT,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "audit_logs_pkey" PRIMARY KEY ("id")
);
CREATE INDEX "idx_audit_org_time" ON "audit_logs"("org_id", "created_at");
CREATE INDEX "idx_audit_entity" ON "audit_logs"("entity_type", "entity_id");
CREATE INDEX "idx_audit_user" ON "audit_logs"("user_id");
CREATE INDEX "idx_audit_action" ON "audit_logs"("action");

-- 15. Subscriptions
CREATE TABLE "subscriptions" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "org_id" UUID NOT NULL,
    "plan_id" UUID NOT NULL,
    "stripe_customer_id" TEXT,
    "stripe_subscription_id" TEXT,
    "status" "SubscriptionStatus" NOT NULL DEFAULT 'ACTIVE',
    "current_period_start" TIMESTAMPTZ,
    "current_period_end" TIMESTAMPTZ,
    "cancel_at" TIMESTAMPTZ,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "subscriptions_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "subscriptions_org_id_key" ON "subscriptions"("org_id");
CREATE UNIQUE INDEX "subscriptions_stripe_customer_id_key" ON "subscriptions"("stripe_customer_id");
CREATE UNIQUE INDEX "subscriptions_stripe_subscription_id_key" ON "subscriptions"("stripe_subscription_id");

-- 16. Plans
CREATE TABLE "plans" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "tier" "PlanTier" NOT NULL,
    "display_name" TEXT NOT NULL,
    "stripe_price_id" TEXT,
    "max_agents" INTEGER NOT NULL,
    "max_teams" INTEGER NOT NULL,
    "max_skills" INTEGER NOT NULL,
    "max_credentials" INTEGER NOT NULL,
    "max_members" INTEGER NOT NULL,
    "features" JSONB,
    "price_monthly" INTEGER NOT NULL DEFAULT 0,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "plans_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "plans_tier_key" ON "plans"("tier");
CREATE UNIQUE INDEX "plans_stripe_price_id_key" ON "plans"("stripe_price_id");

-- 17. Feature flags
CREATE TABLE "feature_flags" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "key" TEXT NOT NULL,
    "description" TEXT,
    "enabled" BOOLEAN NOT NULL DEFAULT false,
    "percentage" INTEGER NOT NULL DEFAULT 0,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "updated_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "feature_flags_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "feature_flags_key_key" ON "feature_flags"("key");

-- 18. Feature flag overrides
CREATE TABLE "feature_flag_overrides" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "flag_id" UUID NOT NULL,
    "org_id" UUID NOT NULL,
    "enabled" BOOLEAN NOT NULL,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "feature_flag_overrides_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_flag_override" ON "feature_flag_overrides"("flag_id", "org_id");

-- 19. Agent config history
CREATE TABLE "agent_config_history" (
    "id" UUID NOT NULL DEFAULT gen_random_uuid(),
    "agent_id" UUID NOT NULL,
    "changed_by" UUID NOT NULL,
    "version" INTEGER NOT NULL,
    "changes" JSONB NOT NULL,
    "snapshot" JSONB NOT NULL,
    "created_at" TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "agent_config_history_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "uq_config_version" ON "agent_config_history"("agent_id", "version");
CREATE INDEX "idx_config_history_agent_time" ON "agent_config_history"("agent_id", "created_at");

-- Foreign keys
ALTER TABLE "accounts" ADD CONSTRAINT "accounts_userId_fkey" FOREIGN KEY ("userId") REFERENCES "users"("id") ON DELETE CASCADE;
ALTER TABLE "sessions" ADD CONSTRAINT "sessions_userId_fkey" FOREIGN KEY ("userId") REFERENCES "users"("id") ON DELETE CASCADE;

ALTER TABLE "organization_members" ADD CONSTRAINT "organization_members_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "organization_members" ADD CONSTRAINT "organization_members_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE;

ALTER TABLE "organization_invitations" ADD CONSTRAINT "organization_invitations_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "organization_invitations" ADD CONSTRAINT "organization_invitations_invited_by_fkey" FOREIGN KEY ("invited_by") REFERENCES "users"("id");

ALTER TABLE "teams" ADD CONSTRAINT "teams_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;

ALTER TABLE "team_members" ADD CONSTRAINT "team_members_team_id_fkey" FOREIGN KEY ("team_id") REFERENCES "teams"("id") ON DELETE CASCADE;
ALTER TABLE "team_members" ADD CONSTRAINT "team_members_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id") ON DELETE CASCADE;

ALTER TABLE "agents" ADD CONSTRAINT "agents_team_id_fkey" FOREIGN KEY ("team_id") REFERENCES "teams"("id") ON DELETE CASCADE;
ALTER TABLE "agents" ADD CONSTRAINT "agents_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;

ALTER TABLE "delegation_logs" ADD CONSTRAINT "delegation_logs_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "delegation_logs" ADD CONSTRAINT "delegation_logs_session_id_fkey" FOREIGN KEY ("session_id") REFERENCES "conversation_sessions"("id");
ALTER TABLE "delegation_logs" ADD CONSTRAINT "delegation_logs_source_agent_id_fkey" FOREIGN KEY ("source_agent_id") REFERENCES "agents"("id");
ALTER TABLE "delegation_logs" ADD CONSTRAINT "delegation_logs_target_agent_id_fkey" FOREIGN KEY ("target_agent_id") REFERENCES "agents"("id");

ALTER TABLE "skills" ADD CONSTRAINT "skills_author_id_fkey" FOREIGN KEY ("author_id") REFERENCES "users"("id");

ALTER TABLE "skill_reviews" ADD CONSTRAINT "skill_reviews_skill_id_fkey" FOREIGN KEY ("skill_id") REFERENCES "skills"("id") ON DELETE CASCADE;
ALTER TABLE "skill_reviews" ADD CONSTRAINT "skill_reviews_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id");

ALTER TABLE "agent_skills" ADD CONSTRAINT "agent_skills_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agents"("id") ON DELETE CASCADE;
ALTER TABLE "agent_skills" ADD CONSTRAINT "agent_skills_skill_id_fkey" FOREIGN KEY ("skill_id") REFERENCES "skills"("id") ON DELETE CASCADE;

ALTER TABLE "credentials" ADD CONSTRAINT "credentials_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "credentials" ADD CONSTRAINT "credentials_team_id_fkey" FOREIGN KEY ("team_id") REFERENCES "teams"("id") ON DELETE SET NULL;
ALTER TABLE "credentials" ADD CONSTRAINT "credentials_created_by_fkey" FOREIGN KEY ("created_by") REFERENCES "users"("id");

ALTER TABLE "agent_credentials" ADD CONSTRAINT "agent_credentials_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agents"("id") ON DELETE CASCADE;
ALTER TABLE "agent_credentials" ADD CONSTRAINT "agent_credentials_credential_id_fkey" FOREIGN KEY ("credential_id") REFERENCES "credentials"("id") ON DELETE CASCADE;

ALTER TABLE "conversation_sessions" ADD CONSTRAINT "conversation_sessions_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agents"("id") ON DELETE CASCADE;
ALTER TABLE "conversation_sessions" ADD CONSTRAINT "conversation_sessions_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "conversation_sessions" ADD CONSTRAINT "conversation_sessions_created_by_fkey" FOREIGN KEY ("created_by") REFERENCES "users"("id");

ALTER TABLE "agent_runs" ADD CONSTRAINT "agent_runs_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agents"("id") ON DELETE CASCADE;
ALTER TABLE "agent_runs" ADD CONSTRAINT "agent_runs_session_id_fkey" FOREIGN KEY ("session_id") REFERENCES "conversation_sessions"("id");
ALTER TABLE "agent_runs" ADD CONSTRAINT "agent_runs_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "agent_runs" ADD CONSTRAINT "agent_runs_triggered_by_fkey" FOREIGN KEY ("triggered_by") REFERENCES "users"("id");

ALTER TABLE "audit_logs" ADD CONSTRAINT "audit_logs_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "audit_logs" ADD CONSTRAINT "audit_logs_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "users"("id");

ALTER TABLE "subscriptions" ADD CONSTRAINT "subscriptions_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;
ALTER TABLE "subscriptions" ADD CONSTRAINT "subscriptions_plan_id_fkey" FOREIGN KEY ("plan_id") REFERENCES "plans"("id");

ALTER TABLE "feature_flag_overrides" ADD CONSTRAINT "feature_flag_overrides_flag_id_fkey" FOREIGN KEY ("flag_id") REFERENCES "feature_flags"("id") ON DELETE CASCADE;
ALTER TABLE "feature_flag_overrides" ADD CONSTRAINT "feature_flag_overrides_org_id_fkey" FOREIGN KEY ("org_id") REFERENCES "organizations"("id") ON DELETE CASCADE;

ALTER TABLE "agent_config_history" ADD CONSTRAINT "agent_config_history_agent_id_fkey" FOREIGN KEY ("agent_id") REFERENCES "agents"("id") ON DELETE CASCADE;
ALTER TABLE "agent_config_history" ADD CONSTRAINT "agent_config_history_changed_by_fkey" FOREIGN KEY ("changed_by") REFERENCES "users"("id");
