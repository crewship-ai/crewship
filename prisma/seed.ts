import { randomBytes } from "crypto"

import { hashSync } from "bcryptjs"
import pg from "pg"

import { PrismaClient } from "../lib/generated/prisma/client.js"
import { PrismaPg } from "@prisma/adapter-pg"
import { encrypt } from "../lib/encryption.js"

const connectionString = process.env.DATABASE_URL ?? ""

if (!connectionString) {
  console.error("DATABASE_URL is not set")
  process.exit(1)
}

if (!process.env.ENCRYPTION_KEY) {
  console.error("ENCRYPTION_KEY is not set (needed for credential encryption)")
  process.exit(1)
}

const pool = new pg.Pool({ connectionString })
const adapter = new PrismaPg(pool)
const prisma = new PrismaClient({ adapter })

async function main() {
  console.log("🌱 Starting seed...\n")

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

  // Step 2: Organization
  console.log("🏢 Seeding organization...")
  const org = await prisma.organization.upsert({
    where: { slug: "acme-corp" },
    update: { name: "Acme Corp" },
    create: {
      name: "Acme Corp",
      slug: "acme-corp",
    },
  })
  console.log(`  ✓ Organization: ${org.name} (${org.id})`)

  // Step 3: OrgMember (link user to org as OWNER)
  console.log("🔗 Linking user to organization...")
  await prisma.organizationMember.upsert({
    where: {
      uq_org_member: { org_id: org.id, user_id: user.id },
    },
    update: { role: "OWNER" },
    create: {
      org_id: org.id,
      user_id: user.id,
      role: "OWNER",
    },
  })
  console.log(`  ✓ ${user.email} → ${org.name} (OWNER)`)

  // Step 4: Teams
  console.log("👥 Seeding teams...")
  const engineering = await prisma.team.upsert({
    where: { uq_team_slug: { org_id: org.id, slug: "engineering" } },
    update: { name: "Engineering", color: "#3B82F6", icon: "💻" },
    create: {
      org_id: org.id,
      name: "Engineering",
      slug: "engineering",
      color: "#3B82F6",
      icon: "💻",
    },
  })
  const marketing = await prisma.team.upsert({
    where: { uq_team_slug: { org_id: org.id, slug: "marketing" } },
    update: { name: "Marketing", color: "#10B981", icon: "📈" },
    create: {
      org_id: org.id,
      name: "Marketing",
      slug: "marketing",
      color: "#10B981",
      icon: "📈",
    },
  })
  console.log(`  ✓ Team: ${engineering.name} (${engineering.id})`)
  console.log(`  ✓ Team: ${marketing.name} (${marketing.id})`)

  // Step 5: TeamMembers
  console.log("🔗 Linking user to teams...")
  await prisma.teamMember.upsert({
    where: { uq_team_member: { team_id: engineering.id, user_id: user.id } },
    update: {},
    create: { team_id: engineering.id, user_id: user.id },
  })
  await prisma.teamMember.upsert({
    where: { uq_team_member: { team_id: marketing.id, user_id: user.id } },
    update: {},
    create: { team_id: marketing.id, user_id: user.id },
  })
  console.log(`  ✓ ${user.email} → Engineering, Marketing`)

  // Step 6: Agents
  console.log("🤖 Seeding agents...")
  const claudeDev = await prisma.agent.upsert({
    where: { uq_agent_slug: { org_id: org.id, slug: "claude-dev" } },
    update: {},
    create: {
      org_id: org.id,
      team_id: engineering.id,
      name: "Claude Dev",
      slug: "claude-dev",
      role_title: "Senior Developer",
      agent_role: "WORKER",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-sonnet-4-20250514",
      tool_profile: "CODING",
      system_prompt:
        "You are a senior developer. Write clean, well-tested code. Follow project conventions and best practices.",
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })
  const codeReviewer = await prisma.agent.upsert({
    where: { uq_agent_slug: { org_id: org.id, slug: "code-reviewer" } },
    update: {},
    create: {
      org_id: org.id,
      team_id: engineering.id,
      name: "Code Reviewer",
      slug: "code-reviewer",
      role_title: "Code Reviewer",
      agent_role: "WORKER",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-sonnet-4-20250514",
      tool_profile: "CODING",
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })
  const contentWriter = await prisma.agent.upsert({
    where: { uq_agent_slug: { org_id: org.id, slug: "content-writer" } },
    update: {},
    create: {
      org_id: org.id,
      team_id: marketing.id,
      name: "Content Writer",
      slug: "content-writer",
      role_title: "Content Specialist",
      agent_role: "WORKER",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-sonnet-4-20250514",
      tool_profile: "MINIMAL",
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })
  const seoAnalyst = await prisma.agent.upsert({
    where: { uq_agent_slug: { org_id: org.id, slug: "seo-analyst" } },
    update: {},
    create: {
      org_id: org.id,
      team_id: marketing.id,
      name: "SEO Analyst",
      slug: "seo-analyst",
      role_title: "SEO Specialist",
      agent_role: "WORKER",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "OPENAI",
      llm_model: "gpt-4o",
      tool_profile: "FULL",
      webhook_secret: randomBytes(32).toString("hex"),
    },
  })
  console.log(`  ✓ Agent: ${claudeDev.name} (${claudeDev.id})`)
  console.log(`  ✓ Agent: ${codeReviewer.name} (${codeReviewer.id})`)
  console.log(`  ✓ Agent: ${contentWriter.name} (${contentWriter.id})`)
  console.log(`  ✓ Agent: ${seoAnalyst.name} (${seoAnalyst.id})`)

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
      description:
        "Code review, refactoring, debugging, test writing",
      icon: "💻",
    },
  })
  const webResearchSkill = await prisma.skill.upsert({
    where: { slug: "web-researcher" },
    update: {},
    create: {
      name: "Web Researcher",
      slug: "web-researcher",
      display_name: "Web Researcher",
      category: "DATA",
      source: "BUNDLED",
      description:
        "Web search, data extraction, competitive analysis",
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
      description:
        "Infrastructure monitoring, deployment, CI/CD",
      icon: "🔧",
    },
  })
  console.log(`  ✓ Skill: ${codingSkill.name}`)
  console.log(`  ✓ Skill: ${webResearchSkill.name}`)
  console.log(`  ✓ Skill: ${devopsSkill.name}`)

  // Step 8: AgentSkills
  console.log("🔗 Assigning skills to agents...")
  const agentSkillPairs = [
    { agent_id: claudeDev.id, skill_id: codingSkill.id },
    { agent_id: codeReviewer.id, skill_id: codingSkill.id },
    { agent_id: contentWriter.id, skill_id: webResearchSkill.id },
    { agent_id: seoAnalyst.id, skill_id: webResearchSkill.id },
    { agent_id: claudeDev.id, skill_id: devopsSkill.id },
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

  // Step 9: Credentials (encrypted demo keys -- NOT real)
  console.log("🔑 Seeding credentials...")
  const anthropicCred = await prisma.credential.upsert({
    where: {
      uq_credential_name: { org_id: org.id, name: "ANTHROPIC_API_KEY" },
    },
    update: {},
    create: {
      org_id: org.id,
      name: "ANTHROPIC_API_KEY",
      description: "Anthropic API key (demo placeholder)",
      encrypted_value: encrypt("sk-ant-demo-key-placeholder"),
      type: "API_KEY",
      provider: "ANTHROPIC",
      scope: "ORGANIZATION",
      created_by: user.id,
    },
  })
  const openaiCred = await prisma.credential.upsert({
    where: {
      uq_credential_name: { org_id: org.id, name: "OPENAI_API_KEY" },
    },
    update: {},
    create: {
      org_id: org.id,
      name: "OPENAI_API_KEY",
      description: "OpenAI API key (demo placeholder)",
      encrypted_value: encrypt("sk-demo-key-placeholder"),
      type: "API_KEY",
      provider: "OPENAI",
      scope: "ORGANIZATION",
      created_by: user.id,
    },
  })
  console.log(`  ✓ Credential: ${anthropicCred.name}`)
  console.log(`  ✓ Credential: ${openaiCred.name}`)

  // Step 10: AgentCredentials
  console.log("🔗 Assigning credentials to agents...")
  const agentCredPairs = [
    {
      agent_id: claudeDev.id,
      credential_id: anthropicCred.id,
      env_var_name: "ANTHROPIC_API_KEY",
    },
    {
      agent_id: codeReviewer.id,
      credential_id: anthropicCred.id,
      env_var_name: "ANTHROPIC_API_KEY",
    },
    {
      agent_id: contentWriter.id,
      credential_id: anthropicCred.id,
      env_var_name: "ANTHROPIC_API_KEY",
    },
    {
      agent_id: seoAnalyst.id,
      credential_id: anthropicCred.id,
      env_var_name: "ANTHROPIC_API_KEY",
    },
    {
      agent_id: seoAnalyst.id,
      credential_id: openaiCred.id,
      env_var_name: "OPENAI_API_KEY",
    },
  ]
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

  // Step 11: Plan
  console.log("📋 Seeding plans...")
  const freePlan = await prisma.plan.upsert({
    where: { tier: "FREE" },
    update: {},
    create: {
      tier: "FREE",
      display_name: "Community",
      max_agents: 5,
      max_teams: 2,
      max_skills: 10,
      max_credentials: 10,
      max_members: 3,
      price_monthly: 0,
    },
  })
  console.log(`  ✓ Plan: ${freePlan.display_name} (${freePlan.tier})`)

  // Step 12: Subscription
  console.log("💳 Seeding subscription...")
  await prisma.subscription.upsert({
    where: { org_id: org.id },
    update: { plan_id: freePlan.id },
    create: {
      org_id: org.id,
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
        org_id: org.id,
        user_id: user.id,
        action: "user.login",
        entity_type: "user",
        entity_id: user.id,
        metadata: { method: "password" },
      },
      {
        org_id: org.id,
        user_id: user.id,
        action: "agent.create",
        entity_type: "agent",
        entity_id: claudeDev.id,
        metadata: { agent_name: claudeDev.name },
      },
      {
        org_id: org.id,
        user_id: user.id,
        action: "team.create",
        entity_type: "team",
        entity_id: engineering.id,
        metadata: { team_name: engineering.name },
      },
    ],
  })
  console.log("  ✓ 3 audit log entries created")

  console.log("\n✅ Seed completed successfully!")
}

main()
  .catch((e) => {
    console.error("❌ Seed failed:", e)
    process.exit(1)
  })
  .finally(async () => {
    await prisma.$disconnect()
    await pool.end()
  })
