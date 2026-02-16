import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createAgentSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { randomBytes } from "crypto"

const AGENT_SAFE_SELECT = {
  id: true,
  org_id: true,
  team_id: true,
  name: true,
  slug: true,
  description: true,
  role_title: true,
  agent_role: true,
  status: true,
  cli_adapter: true,
  llm_provider: true,
  llm_model: true,
  system_prompt: true,
  temperature: true,
  max_tokens: true,
  timeout_seconds: true,
  tool_profile: true,
  memory_enabled: true,
  created_at: true,
  updated_at: true,
  team: { select: { name: true, slug: true, color: true } },
  _count: { select: { skills: true, credentials: true, sessions: true } },
} as const

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")
  const teamId = req.nextUrl.searchParams.get("team_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const where: Record<string, unknown> = { org_id: authResult.orgId, deleted_at: null }
  if (teamId) where.team_id = teamId

  const agents = await prisma.agent.findMany({
    where,
    select: AGENT_SAFE_SELECT,
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(agents)
}

export async function POST(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  if (!["OWNER", "ADMIN", "MANAGER"].includes(authResult.role)) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const body = await req.json()
  const parsed = createAgentSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const webhookSecret = randomBytes(32).toString("hex")

  const agent = await prisma.agent.create({
    data: {
      org_id: authResult.orgId,
      team_id: parsed.data.team_id,
      name: parsed.data.name,
      slug: parsed.data.slug,
      description: parsed.data.description,
      role_title: parsed.data.role_title,
      agent_role: parsed.data.agent_role,
      cli_adapter: parsed.data.cli_adapter,
      llm_provider: parsed.data.llm_provider,
      llm_model: parsed.data.llm_model,
      system_prompt: parsed.data.system_prompt,
      temperature: parsed.data.temperature,
      max_tokens: parsed.data.max_tokens,
      timeout_seconds: parsed.data.timeout_seconds,
      tool_profile: parsed.data.tool_profile,
      webhook_secret: webhookSecret,
    },
    select: AGENT_SAFE_SELECT,
  })

  return NextResponse.json(agent, { status: 201 })
}
