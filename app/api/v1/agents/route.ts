import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createAgentSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { randomBytes } from "crypto"
import type { OrgRole } from "@/lib/generated/prisma/client"

const AGENT_SAFE_SELECT = {
  id: true,
  workspace_id: true,
  crew_id: true,
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
  crew: { select: { name: true, slug: true, color: true } },
  _count: { select: { skills: true, credentials: true, chats: true } },
} as const

export async function GET(req: NextRequest) {
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")
  const crewId = req.nextUrl.searchParams.get("crew_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const where: Record<string, unknown> = { workspace_id: authResult.workspaceId, deleted_at: null }
  if (crewId) where.crew_id = crewId

  const agents = await prisma.agent.findMany({
    where,
    select: AGENT_SAFE_SELECT,
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(agents)
}

export async function POST(req: NextRequest) {
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("create", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = createAgentSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  if (parsed.data.crew_id) {
    const team = await prisma.crew.findFirst({
      where: { id: parsed.data.crew_id, workspace_id: authResult.workspaceId, deleted_at: null },
      select: { id: true },
    })
    if (!team) {
      return NextResponse.json({ error: "Invalid crew_id" }, { status: 400 })
    }
  }

  const webhookSecret = randomBytes(32).toString("hex")

  const agent = await prisma.agent.create({
    data: {
      workspace_id: authResult.workspaceId,
      crew_id: parsed.data.crew_id,
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
