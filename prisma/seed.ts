import { randomBytes } from "crypto"

import { hashSync } from "bcryptjs"
import { PrismaBetterSqlite3 } from "@prisma/adapter-better-sqlite3"

import { PrismaClient } from "../lib/generated/prisma/client.js"
import { encrypt } from "../lib/encryption.js"

if (!process.env.DATABASE_URL) {
  console.error("DATABASE_URL is not set")
  process.exit(1)
}

if (!process.env.ENCRYPTION_KEY) {
  console.error("ENCRYPTION_KEY is not set (needed for credential encryption)")
  process.exit(1)
}

const adapter = new PrismaBetterSqlite3({ url: process.env.DATABASE_URL })
const prisma = new PrismaClient({ adapter })

async function main() {
  console.log("🌱 Starting seed...\n")

  // Step 0: Verify DB is clean
  console.log("🔍 Checking DB cleanliness...")
  const existingUsers = await prisma.user.count()
  const existingAgents = await prisma.agent.count()
  if (existingUsers > 0 || existingAgents > 0) {
    console.warn(`  ⚠ DB is not clean: ${existingUsers} users, ${existingAgents} agents found`)
    console.warn("  → Deleting all data for a fresh seed...")
    // Delete in dependency order (tables with FK references first)
    // Use raw SQL for tables managed by Go migrations (not in Prisma schema)
    await prisma.$executeRawUnsafe("DELETE FROM cli_tokens").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM oauth_states").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM captain_chats").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM mcp_tool_calls").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM skill_reviews").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM credential_crews").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM agent_mcp_bindings").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM agent_config_history").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM keeper_requests").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM mission_tasks").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM missions").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM escalations").catch(() => {})
    await prisma.$executeRawUnsafe("DELETE FROM peer_conversations").catch(() => {})
    await prisma.auditLog.deleteMany()
    await prisma.assignment.deleteMany()
    await prisma.agentCredential.deleteMany()
    await prisma.agentSkill.deleteMany()
    await prisma.agentRun.deleteMany()
    await prisma.chat.deleteMany()
    await prisma.agent.deleteMany()
    await prisma.credential.deleteMany()
    await prisma.skill.deleteMany()
    await prisma.subscription.deleteMany()
    await prisma.plan.deleteMany()
    await prisma.crewMember.deleteMany()
    await prisma.crew.deleteMany()
    await prisma.workspaceMember.deleteMany()
    await prisma.workspace.deleteMany()
    await prisma.user.deleteMany()
    console.log("  ✓ DB cleaned")
  } else {
    console.log("  ✓ DB is clean")
  }

  // Step 1: Demo User
  console.log("👤 Seeding demo user...")
  const user = await prisma.user.upsert({
    where: { email: "demo@crewship.ai" },
    update: {
      full_name: "Demo User",
      hashed_password: hashSync("password123", 12),
    },
    create: {
      email: "demo@crewship.ai",
      full_name: "Demo User",
      hashed_password: hashSync("password123", 12),
    },
  })
  console.log(`  ✓ User: ${user.email} (${user.id})`)

  // Mark onboarding as completed (column added by Go migration, not in Prisma schema)
  // Only works if Go server has run first to apply migrations
  try {
    await prisma.$executeRawUnsafe(
      "UPDATE users SET onboarding_completed = 1 WHERE id = ?",
      user.id,
    )
    console.log("  ✓ Onboarding marked as completed")
  } catch {
    console.warn("  ⚠ onboarding_completed column not found (start Go server first to apply migrations)")
  }

  // Step 2: Workspace
  console.log("🏢 Seeding workspace...")
  const org = await prisma.workspace.upsert({
    where: { slug: "crewship-hq" },
    update: { name: "Crewship HQ" },
    create: {
      name: "Crewship HQ",
      slug: "crewship-hq",
    },
  })
  console.log(`  ✓ Workspace: ${org.name} (${org.id})`)

  // Step 3: WorkspaceMember (link user to workspace as OWNER)
  console.log("🔗 Linking user to workspace...")
  await prisma.workspaceMember.upsert({
    where: {
      uq_workspace_member: { workspace_id: org.id, user_id: user.id },
    },
    update: { role: "OWNER" },
    create: {
      workspace_id: org.id,
      user_id: user.id,
      role: "OWNER",
    },
  })
  console.log(`  ✓ ${user.email} → ${org.name} (OWNER)`)

  // Step 4: Crews
  console.log("👥 Seeding crews...")
  const engineering = await prisma.crew.upsert({
    where: { uq_crew_slug: { workspace_id: org.id, slug: "engineering" } },
    update: { name: "Engineering", color: "#3B82F6", icon: "💻" },
    create: {
      workspace_id: org.id,
      name: "Engineering",
      slug: "engineering",
      color: "#3B82F6",
      icon: "💻",
    },
  })
  const quality = await prisma.crew.upsert({
    where: { uq_crew_slug: { workspace_id: org.id, slug: "quality" } },
    update: { name: "Quality", color: "#10B981", icon: "🔍" },
    create: {
      workspace_id: org.id,
      name: "Quality",
      slug: "quality",
      color: "#10B981",
      icon: "🔍",
    },
  })
  console.log(`  ✓ Crew: ${engineering.name} (${engineering.id})`)
  console.log(`  ✓ Crew: ${quality.name} (${quality.id})`)

  // Step 5: CrewMembers
  console.log("🔗 Linking user to crews...")
  await prisma.crewMember.upsert({
    where: { uq_crew_member: { crew_id: engineering.id, user_id: user.id } },
    update: {},
    create: { crew_id: engineering.id, user_id: user.id },
  })
  await prisma.crewMember.upsert({
    where: { uq_crew_member: { crew_id: quality.id, user_id: user.id } },
    update: {},
    create: { crew_id: quality.id, user_id: user.id },
  })
  console.log(`  ✓ ${user.email} → Engineering, Quality`)

  // Step 6: Agents — Engineering Crew
  console.log("🤖 Seeding agents (Engineering)...")

  const tomas = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "tomas" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: engineering.id,
      name: "Tomáš",
      slug: "tomas",
      role_title: "Technical Architect",
      agent_role: "LEAD",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "FULL",
      timeout_seconds: 3600,
      memory_enabled: true,
      system_prompt: `You are Tomáš, the Technical Architect and Lead of the Engineering crew at Crewship HQ.

RESPONSIBILITIES:
- Architect and oversee all technical decisions for the Crewship platform
- Coordinate work across Engineering crew members (Viktor, Nela, Martin)
- Review and approve architectural changes
- Maintain code quality standards and project conventions

CREWSHIP CONVENTIONS:
- Go backend: database/sql with raw SQL, no ORM. Provider pattern for infrastructure abstractions.
- Frontend: Next.js 16 App Router, static export, Tailwind CSS 4, shadcn/ui
- Database: SQLite (modernc.org/sqlite, driver name "sqlite", NOT "sqlite3")
- Migrations: Go constants in internal/database/migrate.go (NOT Prisma migrate)
- API: Go 1.22 http.ServeMux with "METHOD /path" patterns, RFC 7807 errors
- Auth: JWE tokens (NextAuth v5 compatible), HKDF-SHA256 key derivation
- IDs: CUID format. Tables: snake_case, plural names.

WORKFLOW:
- Always follow TDD: write tests first, then implement
- Verification loop: go test ./... && go vet ./... must pass before done
- For frontend: pnpm lint && pnpm build must pass
- Single binary architecture: Next.js static export embedded in Go binary via //go:embed

KEY PATHS:
- cmd/crewship/ (production entry), internal/api/ (HTTP API), internal/orchestrator/ (agent execution)
- app/ (Next.js pages), components/ (React), .claude/context/ (authoritative docs)`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  const viktor = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "viktor" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: engineering.id,
      name: "Viktor",
      slug: "viktor",
      role_title: "Backend Engineer",
      agent_role: "AGENT",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "CODING",

      timeout_seconds: 1800,
      memory_enabled: true,
      system_prompt: `You are Viktor, a Backend Engineer in the Engineering crew at Crewship HQ.

RESPONSIBILITIES:
- Implement Go backend features: API endpoints, database queries, orchestrator logic
- Write comprehensive table-driven tests for all new code
- Maintain and extend the SQLite migration system
- Implement provider interfaces (ContainerProvider, StorageProvider, StateProvider)

CONVENTIONS:
- database/sql with raw SQL, no ORM. Use QueryRowContext/ExecContext.
- Error format: RFC 7807 Problem Details
- Router: Go 1.22 http.ServeMux with "METHOD /path" patterns
- Imports: stdlib → external → internal
- Driver: "sqlite" (modernc.org/sqlite, pure Go, no CGO)
- IDs: CUID. Tables: snake_case, plural.
- When adding columns: update ALL SELECT queries AND their Scan calls

WORKFLOW:
- TDD: write tests first, implement to make them pass
- go test ./... -count=1 && go vet ./... must pass
- Never skip verification loop`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  const nela = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "nela" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: engineering.id,
      name: "Nela",
      slug: "nela",
      role_title: "Frontend Engineer",
      agent_role: "AGENT",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "CODING",

      timeout_seconds: 1800,
      memory_enabled: true,
      system_prompt: `You are Nela, a Frontend Engineer in the Engineering crew at Crewship HQ.

RESPONSIBILITIES:
- Build and maintain the Next.js 16 App Router frontend (static export)
- Implement React components using Tailwind CSS 4 and shadcn/ui (Radix primitives)
- Create responsive, accessible UI following existing patterns
- Write frontend tests (Vitest, Playwright for E2E)

CONVENTIONS:
- Next.js 16 App Router with static export (no API routes in app/ — they break in prod)
- Tailwind CSS 4, shadcn/ui components in components/ui/
- Auth: custom useAuth hook (hooks/use-auth.tsx), NOT next-auth package
- State: React hooks for local, custom hooks for shared state
- Validation: Zod schemas. RBAC: CASL (lib/permissions/abilities.ts)
- ES modules only — never use require() or CommonJS
- Path alias: @/* maps to project root
- pnpm only (not npm/yarn)

WORKFLOW:
- pnpm lint && pnpm build must pass before done
- Check .claude/context/wireframes/ for screen designs
- Feature components in components/features/, layout in components/layout/`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  const martin = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "martin" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: engineering.id,
      name: "Martin",
      slug: "martin",
      role_title: "Infrastructure Engineer",
      agent_role: "AGENT",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "CODING",

      timeout_seconds: 2400,
      memory_enabled: true,
      system_prompt: `You are Martin, an Infrastructure Engineer in the Engineering crew at Crewship HQ.

RESPONSIBILITIES:
- Maintain Docker container infrastructure and sidecar proxy system
- Manage build system (Makefile, dev.sh, CI/CD)
- Configure container networking, resource limits, and security boundaries
- Maintain the single-binary build pipeline (Next.js static export → Go embed)

CONVENTIONS:
- One container per crew (not per agent). Container name: crewship-team-{slug}
- Sidecar proxy at 127.0.0.1:9119, UID 1002. Agent UID 1001. Never change these UIDs.
- Sidecar credentials via base64 stdin, never env vars or disk
- Default allowlist: api.anthropic.com, api.openai.com, generativelanguage.googleapis.com
- Build: make build = pnpm build + go build → ./crewship
- Dev: ./dev.sh start (Go :8080 + Next.js :3001)
- Container provider: docker (MVP), k8s (enterprise)

WORKFLOW:
- Infrastructure changes must be precise — wrong port/UID = broken system
- Test with: go test ./... && go vet ./...
- For full build verification: make build`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  console.log(`  ✓ Agent: ${tomas.name} (LEAD)`)
  console.log(`  ✓ Agent: ${viktor.name}`)
  console.log(`  ✓ Agent: ${nela.name}`)
  console.log(`  ✓ Agent: ${martin.name}`)

  // Step 6b: Agents — Quality Crew
  console.log("🤖 Seeding agents (Quality)...")

  const eva = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "eva" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: quality.id,
      name: "Eva",
      slug: "eva",
      role_title: "Quality Director",
      agent_role: "LEAD",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "FULL",

      timeout_seconds: 3600,
      memory_enabled: true,
      system_prompt: `You are Eva, the Quality Director and Lead of the Quality crew at Crewship HQ.

RESPONSIBILITIES:
- Oversee code quality, test coverage, and review standards across the entire codebase
- Coordinate Quality crew members (Daniel, Petra, Jakub)
- Define and enforce quality gates for PRs and releases
- Analyze full codebase patterns and inconsistencies

QUALITY STANDARDS:
- All Go code: go test ./... -count=1 && go vet ./... must pass
- All frontend: pnpm lint && pnpm build must pass
- TDD mandatory: tests written before implementation
- Table-driven tests in Go, t.Skip() for optional deps (Docker)
- No security vulnerabilities (OWASP top 10)
- RFC 7807 error format consistency

CREWSHIP ARCHITECTURE AWARENESS:
- Single Go binary with embedded Next.js static export
- SQLite via modernc.org/sqlite (pure Go, no CGO)
- No ORM — raw database/sql queries
- JWE auth, credential encryption (v1:{base64} format)
- Container-per-crew model with sidecar proxy

WORKFLOW:
- Review PRs for correctness, security, and convention adherence
- Ensure .claude/context/ docs are updated after significant changes
- Flag any NEVER DO violations from CLAUDE.md`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  const daniel = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "daniel" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: quality.id,
      name: "Daniel",
      slug: "daniel",
      role_title: "Code Reviewer",
      agent_role: "AGENT",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "MINIMAL",

      timeout_seconds: 1800,
      memory_enabled: true,
      system_prompt: `You are Daniel, a Code Reviewer in the Quality crew at Crewship HQ.

RESPONSIBILITIES:
- Perform thorough code reviews on all PRs
- Check for convention violations, anti-patterns, and potential bugs
- Verify test coverage and quality
- Check cross-file consistency

REVIEW CHECKLIST:
- Go: proper error handling (RFC 7807), correct SQL query patterns, all SELECT columns match Scan
- Frontend: ES modules only, no require(). useAuth hook not next-auth. pnpm only.
- Security: no command injection, XSS, SQL injection. Check OWASP top 10.
- No secrets committed (.env.local, real API keys)
- Database: "sqlite" driver name, not "sqlite3". No Prisma migrate.
- GCM byte layout: IV||AuthTag||Ciphertext — never change
- Sidecar UID 1002, agent UID 1001 — never change
- API routes must NOT be in app/ (static export excludes them)

CONVENTIONS:
- snake_case for DB, plural table names, CUID IDs
- Imports: stdlib → external → internal
- Provider pattern for infrastructure abstractions`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  const petra = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "petra" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: quality.id,
      name: "Petra",
      slug: "petra",
      role_title: "Test Engineer",
      agent_role: "AGENT",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "CODING",

      timeout_seconds: 2400,
      memory_enabled: true,
      system_prompt: `You are Petra, a Test Engineer in the Quality crew at Crewship HQ.

RESPONSIBILITIES:
- Write and maintain comprehensive test suites for Go backend and React frontend
- Ensure test coverage for critical paths (auth, orchestrator, API routes)
- Create E2E tests with Playwright
- Design table-driven test patterns for Go packages

TESTING PATTERNS:
- Go: table-driven tests, t.Skip() for Docker deps, -count=1 for no cache
- Frontend: Vitest for unit tests, Playwright for E2E
- TDD: always write tests BEFORE implementation
- Verify: go test ./... -count=1 && go vet ./... (Go), pnpm test && pnpm lint (frontend)

KEY TEST AREAS:
- internal/api/ — HTTP handlers, auth middleware, workspace resolution
- internal/orchestrator/ — agent execution, credential selection, sidecar lifecycle
- internal/scrubber/ — credential pattern scrubbing (13+ patterns)
- internal/memory/ — FTS5 search, chunking, reindexing
- internal/encryption/ — AES-256-GCM encrypt/decrypt (v1:{base64} format)
- components/ — React component rendering, state management`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  const jakub = await prisma.agent.upsert({
    where: { uq_agent_slug: { workspace_id: org.id, slug: "jakub" } },
    update: {},
    create: {
      workspace_id: org.id,
      crew_id: quality.id,
      name: "Jakub",
      slug: "jakub",
      role_title: "Security Analyst",
      agent_role: "AGENT",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-haiku-4-5",
      tool_profile: "MINIMAL",

      timeout_seconds: 2400,
      memory_enabled: true,
      system_prompt: `You are Jakub, a Security Analyst in the Quality crew at Crewship HQ.

RESPONSIBILITIES:
- Audit codebase for security vulnerabilities (OWASP top 10)
- Review credential handling, encryption, and auth flows
- Verify sidecar proxy security boundaries
- Analyze container isolation and UID separation

SECURITY DOMAINS:
- Auth: JWE tokens (NextAuth v5), HKDF-SHA256 key derivation, 30-day HttpOnly cookies
- Credentials: AES-256-GCM encryption, v1:{base64} format, IV||AuthTag||Ciphertext byte layout
- Sidecar: forward proxy at 127.0.0.1:9119, credentials via base64 stdin (never env/disk)
- Container: agent UID 1001, sidecar UID 1002 — UID separation prevents agent reading sidecar memory
- WebSocket: short-lived token via GET /api/v1/ws-token, passed as ?token= query param
- Scrubber: 13+ credential patterns scrubbed from agent output

NEVER ACCEPTABLE:
- Secrets in env vars inside containers (use sidecar proxy)
- Changing GCM byte layout (breaks all stored credentials)
- Changing sidecar UID 1002 or agent UID 1001
- API routes in app/ directory (static export leaks them)
- Committing .env.local or real API keys`,
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })

  console.log(`  ✓ Agent: ${eva.name} (LEAD)`)
  console.log(`  ✓ Agent: ${daniel.name}`)
  console.log(`  ✓ Agent: ${petra.name}`)
  console.log(`  ✓ Agent: ${jakub.name}`)

  // Step 7: Skills
  console.log("🧩 Seeding skills...")
  const codingSkill = await prisma.skill.upsert({
    where: { slug: "coding-assistant" },
    update: {},
    create: {
      name: "Coding Assistant",
      slug: "coding-assistant",
      display_name: "Coding Assistant",
      category: "CODING",
      source: "BUNDLED",
      description: "Code review, refactoring, debugging, test writing",
      icon: "💻",
    },
  })
  const codeReviewSkill = await prisma.skill.upsert({
    where: { slug: "code-reviewer" },
    update: {},
    create: {
      name: "Code Reviewer",
      slug: "code-reviewer",
      display_name: "Code Reviewer",
      category: "CODING",
      source: "BUNDLED",
      description: "Thorough code review, convention checking, bug detection",
      icon: "🔍",
    },
  })
  const devopsSkill = await prisma.skill.upsert({
    where: { slug: "devops-helper" },
    update: {},
    create: {
      name: "DevOps Helper",
      slug: "devops-helper",
      display_name: "DevOps Helper",
      category: "DEVOPS",
      source: "BUNDLED",
      description: "Infrastructure monitoring, deployment, CI/CD",
      icon: "🔧",
    },
  })
  const testingSkill = await prisma.skill.upsert({
    where: { slug: "testing-specialist" },
    update: {},
    create: {
      name: "Testing Specialist",
      slug: "testing-specialist",
      display_name: "Testing Specialist",
      category: "CODING",
      source: "BUNDLED",
      description: "Test design, coverage analysis, TDD workflows",
      icon: "🧪",
    },
  })
  const securitySkill = await prisma.skill.upsert({
    where: { slug: "security-auditor" },
    update: {},
    create: {
      name: "Security Auditor",
      slug: "security-auditor",
      display_name: "Security Auditor",
      category: "CODING",
      source: "BUNDLED",
      description: "Security audit, vulnerability scanning, compliance checks",
      icon: "🛡️",
    },
  })
  console.log(`  ✓ Skill: ${codingSkill.name}`)
  console.log(`  ✓ Skill: ${codeReviewSkill.name}`)
  console.log(`  ✓ Skill: ${devopsSkill.name}`)
  console.log(`  ✓ Skill: ${testingSkill.name}`)
  console.log(`  ✓ Skill: ${securitySkill.name}`)

  // Step 8: AgentSkills
  console.log("🔗 Assigning skills to agents...")
  const agentSkillPairs = [
    // Engineering crew
    { agent_id: tomas.id, skill_id: codingSkill.id },
    { agent_id: tomas.id, skill_id: codeReviewSkill.id },
    { agent_id: tomas.id, skill_id: devopsSkill.id },
    { agent_id: viktor.id, skill_id: codingSkill.id },
    { agent_id: nela.id, skill_id: codingSkill.id },
    { agent_id: martin.id, skill_id: devopsSkill.id },
    { agent_id: martin.id, skill_id: codingSkill.id },
    // Quality crew
    { agent_id: eva.id, skill_id: codeReviewSkill.id },
    { agent_id: eva.id, skill_id: testingSkill.id },
    { agent_id: eva.id, skill_id: securitySkill.id },
    { agent_id: daniel.id, skill_id: codeReviewSkill.id },
    { agent_id: petra.id, skill_id: testingSkill.id },
    { agent_id: petra.id, skill_id: codingSkill.id },
    { agent_id: jakub.id, skill_id: securitySkill.id },
  ]
  for (const pair of agentSkillPairs) {
    await prisma.agentSkill.upsert({
      where: {
        uq_agent_skill: {
          agent_id: pair.agent_id,
          skill_id: pair.skill_id,
        },
      },
      update: {},
      create: pair,
    })
  }
  console.log(`  ✓ Assigned ${agentSkillPairs.length} agent-skill links`)

  // Step 9: Credentials
  // Use real key from SEED_ANTHROPIC_API_KEY env var if available, otherwise demo placeholder
  const isRealKey = !!process.env.SEED_ANTHROPIC_API_KEY
  const anthropicApiKey = process.env.SEED_ANTHROPIC_API_KEY || `demo-placeholder-${crypto.randomUUID()}`
  // Detect OAuth token (sk-ant-oat01-*) vs API key (sk-ant-api03-* or other)
  const isOAuthToken = anthropicApiKey.startsWith("sk-ant-oat")
  const credType = isOAuthToken ? "AI_CLI_TOKEN" : "API_KEY"
  const credName = isOAuthToken ? "CLAUDE_CODE_OAUTH_TOKEN" : "ANTHROPIC_API_KEY"
  const credEnvVar = isOAuthToken ? "CLAUDE_CODE_OAUTH_TOKEN" : "ANTHROPIC_API_KEY"
  console.log(`🔑 Seeding credentials... (${isRealKey ? `real ${credType} from SEED_ANTHROPIC_API_KEY` : "demo placeholder — set SEED_ANTHROPIC_API_KEY in .env.local for real agents"})`)
  const anthropicCred = await prisma.credential.upsert({
    where: {
      uq_credential_name: { workspace_id: org.id, name: credName },
    },
    update: {
      encrypted_value: encrypt(anthropicApiKey),
      type: credType,
    },
    create: {
      workspace_id: org.id,
      name: credName,
      description: isOAuthToken
        ? "Claude Code OAuth token for all agents"
        : "Anthropic API key for all agents",
      encrypted_value: encrypt(anthropicApiKey),
      type: credType,
      provider: "ANTHROPIC",
      scope: "WORKSPACE",
      created_by: user.id,
    },
  })
  console.log(`  ✓ Credential: ${anthropicCred.name} (type: ${credType})`)

  // Google API credential (workspace-scoped, available to all crews/agents)
  const googleEmail = process.env.SEED_GOOGLE_EMAIL
  const googlePassword = process.env.SEED_GOOGLE_PASSWORD
  let googleCred = null
  if (googleEmail && googlePassword) {
    console.log("🔑 Seeding Google credential...")
    const googleSecret = JSON.stringify({ email: googleEmail, password: googlePassword })
    googleCred = await prisma.credential.upsert({
      where: {
        uq_credential_name: { workspace_id: org.id, name: "GOOGLE_API_CREDENTIALS" },
      },
      update: {
        encrypted_value: encrypt(googleSecret),
      },
      create: {
        workspace_id: org.id,
        name: "GOOGLE_API_CREDENTIALS",
        description: "Google API credentials (workspace-scoped, all crews)",
        encrypted_value: encrypt(googleSecret),
        type: "SECRET",
        provider: "GOOGLE",
        scope: "WORKSPACE",
        created_by: user.id,
      },
    })
    // Set security_level to L3 (sensitive) so Keeper always evaluates access
    try {
      await prisma.$executeRawUnsafe(
        "UPDATE credentials SET security_level = 3 WHERE id = ?",
        googleCred.id,
      )
    } catch {
      // security_level column may not exist if Go migrations haven't run
    }
    console.log(`  ✓ Credential: GOOGLE_API_CREDENTIALS (L3, Keeper-guarded)`)
  } else {
    console.log("  ⚠ Skipping Google credential (set SEED_GOOGLE_EMAIL + SEED_GOOGLE_PASSWORD in .env.local)")
  }

  // Step 10: AgentCredentials — all agents use Anthropic
  console.log("🔗 Assigning credentials to agents...")
  const allAgents = [tomas, viktor, nela, martin, eva, daniel, petra, jakub]
  const agentCredPairs = allAgents.map((agent) => ({
    agent_id: agent.id,
    credential_id: anthropicCred.id,
    env_var_name: credEnvVar,
  }))
  if (googleCred) {
    for (const agent of allAgents) {
      agentCredPairs.push({
        agent_id: agent.id,
        credential_id: googleCred.id,
        env_var_name: "GOOGLE_API_CREDENTIALS",
      })
    }
  }
  for (const pair of agentCredPairs) {
    await prisma.agentCredential.upsert({
      where: {
        uq_agent_credential: {
          agent_id: pair.agent_id,
          credential_id: pair.credential_id,
        },
      },
      update: {},
      create: pair,
    })
  }
  console.log(`  ✓ Assigned ${agentCredPairs.length} agent-credential links`)

  // Step 11: MCP Integrations (Linear + GitHub for Engineering crew)
  console.log("🔌 Seeding MCP integrations...")

  // Linear MCP — remote HTTP with OAuth (token from SEED_LINEAR_OAUTH_ACCESS_TOKEN)
  const linearServerId = `seed-linear-${engineering.id.slice(-8)}`
  await prisma.$executeRawUnsafe(
    `INSERT OR IGNORE INTO crew_mcp_servers (id, crew_id, name, display_name, transport, endpoint, enabled, created_at, updated_at)
     VALUES (?, ?, 'linear', 'Linear', 'streamable-http', 'https://mcp.linear.app/mcp', 1, datetime('now'), datetime('now'))`,
    linearServerId, engineering.id,
  )

  // GitHub MCP — remote HTTP with PAT (token from SEED_GITHUB_TOKEN)
  const githubServerId = `seed-github-${engineering.id.slice(-8)}`
  await prisma.$executeRawUnsafe(
    `INSERT OR IGNORE INTO crew_mcp_servers (id, crew_id, name, display_name, transport, endpoint, enabled, created_at, updated_at)
     VALUES (?, ?, 'github', 'GitHub', 'streamable-http', 'https://api.githubcopilot.com/mcp/', 1, datetime('now'), datetime('now'))`,
    githubServerId, engineering.id,
  )

  console.log(`  ✓ MCP: Linear (Engineering crew)`)
  console.log(`  ✓ MCP: GitHub (Engineering crew)`)

  // Seed OAuth/token credentials + agent bindings for MCP servers
  const mcpConfigs = [
    {
      envVar: "SEED_LINEAR_OAUTH_ACCESS_TOKEN",
      serverId: linearServerId,
      credName: "linear-oauth",
      credType: "OAUTH2",
      oauthClientId: process.env.SEED_LINEAR_OAUTH_CLIENT_ID || "",
      oauthAuthUrl: "https://linear.app/oauth/authorize",
      oauthTokenUrl: "https://api.linear.app/oauth/token",
      oauthScopes: "read write",
    },
    {
      envVar: "SEED_GITHUB_TOKEN",
      serverId: githubServerId,
      credName: "github-pat",
      credType: "API_KEY",
      oauthClientId: "",
      oauthAuthUrl: "",
      oauthTokenUrl: "",
      oauthScopes: "",
    },
  ]

  const engineeringAgents = [tomas, viktor, nela, martin]

  for (const cfg of mcpConfigs) {
    const tokenValue = process.env[cfg.envVar]
    if (!tokenValue) {
      console.log(`  ⚠ Skipping ${cfg.credName} credential (set ${cfg.envVar} in .env.local)`)
      // Still create agent bindings without credential (so UI shows "No credential")
      for (const agent of engineeringAgents) {
        const bindingId = `seed-bind-${agent.id.slice(-6)}-${cfg.serverId.slice(-6)}`
        await prisma.$executeRawUnsafe(
          `INSERT OR IGNORE INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, cred_type, enabled, created_at)
           VALUES (?, ?, ?, 'crew', 'bearer', 1, datetime('now'))`,
          bindingId, agent.id, cfg.serverId,
        )
      }
      continue
    }

    const credId = `seed-cred-${cfg.credName}-${org.id.slice(-6)}`
    const encToken = encrypt(tokenValue)

    if (cfg.credType === "OAUTH2") {
      const encSecret = cfg.oauthClientId
        ? (process.env.SEED_LINEAR_OAUTH_CLIENT_SECRET ? encrypt(process.env.SEED_LINEAR_OAUTH_CLIENT_SECRET) : "")
        : ""
      await prisma.$executeRawUnsafe(
        `INSERT OR IGNORE INTO credentials (id, workspace_id, name, type, encrypted_value, status,
          oauth_client_id, oauth_client_secret_enc, oauth_auth_url, oauth_token_url, oauth_scopes,
          created_by, created_at, updated_at)
         VALUES (?, ?, ?, 'OAUTH2', ?, 'ACTIVE', ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
        credId, org.id, cfg.credName, encToken,
        cfg.oauthClientId, encSecret, cfg.oauthAuthUrl, cfg.oauthTokenUrl, cfg.oauthScopes,
        user.id,
      )
    } else {
      await prisma.$executeRawUnsafe(
        `INSERT OR IGNORE INTO credentials (id, workspace_id, name, type, encrypted_value, status,
          provider, scope, created_by, created_at, updated_at)
         VALUES (?, ?, ?, ?, ?, 'ACTIVE', 'GITHUB', 'WORKSPACE', ?, datetime('now'), datetime('now'))`,
        credId, org.id, cfg.credName, cfg.credType, encToken, user.id,
      )
    }

    // Create agent bindings with credential for all engineering agents
    for (const agent of engineeringAgents) {
      const bindingId = `seed-bind-${agent.id.slice(-6)}-${cfg.serverId.slice(-6)}`
      await prisma.$executeRawUnsafe(
        `INSERT OR IGNORE INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope, credential_id, cred_type, enabled, created_at)
         VALUES (?, ?, ?, 'crew', ?, 'bearer', 1, datetime('now'))`,
        bindingId, agent.id, cfg.serverId, credId,
      )
    }
    console.log(`  ✓ Credential: ${cfg.credName} + bindings for ${engineeringAgents.length} agents`)
  }

  // Step 12: Plan (max_agents bumped to 10 for 8-agent team)
  console.log("📋 Seeding plans...")
  const freePlan = await prisma.plan.upsert({
    where: { tier: "FREE" },
    update: { max_agents: 10 },
    create: {
      tier: "FREE",
      display_name: "Community",
      max_agents: 10,
      max_crews: 3,
      max_skills: 20,
      max_credentials: 10,
      max_members: 3,
      price_monthly: 0,
    },
  })
  console.log(`  ✓ Plan: ${freePlan.display_name} (${freePlan.tier})`)

  // Step 12: Subscription
  console.log("💳 Seeding subscription...")
  await prisma.subscription.upsert({
    where: { workspace_id: org.id },
    update: { plan_id: freePlan.id },
    create: {
      workspace_id: org.id,
      plan_id: freePlan.id,
      status: "ACTIVE",
    },
  })
  console.log(`  ✓ ${org.name} → ${freePlan.display_name} plan`)

  // Step 13: Sample AuditLog entries
  console.log("📝 Seeding audit log entries...")
  await prisma.auditLog.createMany({
    data: [
      {
        workspace_id: org.id,
        user_id: user.id,
        action: "user.login",
        entity_type: "user",
        entity_id: user.id,
        metadata: { method: "password" },
      },
      {
        workspace_id: org.id,
        user_id: user.id,
        action: "crew.create",
        entity_type: "crew",
        entity_id: engineering.id,
        metadata: { crew_name: engineering.name },
      },
      {
        workspace_id: org.id,
        user_id: user.id,
        action: "crew.create",
        entity_type: "crew",
        entity_id: quality.id,
        metadata: { crew_name: quality.name },
      },
    ],
  })
  console.log("  ✓ 3 audit log entries created")

  console.log("\n✅ Seed completed successfully!")
  console.log(`   Workspace: ${org.name} (${org.slug})`)
  console.log(`   Crews: ${engineering.name} (4 agents), ${quality.name} (4 agents)`)
  console.log(`   Total: 8 agents, 1 credential (Anthropic), 5 skills`)
}

main()
  .catch((e) => {
    console.error("❌ Seed failed:", e)
    process.exit(1)
  })
  .finally(async () => {
    await prisma.$disconnect()
  })
