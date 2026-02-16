import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createAgentSchema } from "@/lib/validations"
import { randomBytes } from "crypto"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")
  const teamId = req.nextUrl.searchParams.get("team_id")

  if (!orgId) {
    return NextResponse.json({ error: "org_id is required" }, { status: 400 })
  }

  const where: Record<string, unknown> = { org_id: orgId, deleted_at: null }
  if (teamId) where.team_id = teamId

  const agents = await prisma.agent.findMany({
    where,
    include: {
      team: { select: { name: true, slug: true, color: true } },
      _count: { select: { skills: true, credentials: true, sessions: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(agents)
}

export async function POST(req: NextRequest) {
  const body = await req.json()
  const orgId = req.nextUrl.searchParams.get("org_id")

  if (!orgId) {
    return NextResponse.json({ error: "org_id is required" }, { status: 400 })
  }

  const parsed = createAgentSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const webhookSecret = randomBytes(32).toString("hex")

  const agent = await prisma.agent.create({
    data: {
      org_id: orgId,
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
  })

  return NextResponse.json(agent, { status: 201 })
}
