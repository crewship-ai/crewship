import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { updateAgentSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
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

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: AGENT_SAFE_SELECT,
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  return NextResponse.json(agent)
}

export async function PUT(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const existing = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = updateAgentSchema.safeParse(body)

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

  const agent = await prisma.agent.update({
    where: { id: agentId },
    data: parsed.data,
    select: AGENT_SAFE_SELECT,
  })

  return NextResponse.json(agent)
}

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("delete", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const existing = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  await prisma.agent.update({
    where: { id: agentId },
    data: { deleted_at: new Date() },
  })

  return NextResponse.json({ success: true })
}
