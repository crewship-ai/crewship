import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

const createSessionSchema = z.object({
  chat_id: z.string().uuid().optional(),
  title: z.string().max(255).optional(),
})

export async function POST(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("create", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  let body: z.infer<typeof createSessionSchema> = {}
  try {
    const raw = await req.json()
    const parsed = createSessionSchema.safeParse(raw)
    if (parsed.success) body = parsed.data
  } catch {
    // empty body is fine
  }

  const chatId = body.chat_id ?? crypto.randomUUID()

  const existing = await prisma.chat.findUnique({
    where: { id: chatId },
    select: { id: true, title: true, status: true, workspace_id: true, agent_id: true },
  })

  if (existing) {
    if (existing.workspace_id !== authResult.workspaceId || existing.agent_id !== agentId) {
      return NextResponse.json({ error: "Forbidden" }, { status: 403 })
    }
    return NextResponse.json({
      id: existing.id,
      title: existing.title,
      status: existing.status,
    })
  }

  const session = await prisma.chat.create({
    data: {
      id: chatId,
      agent_id: agentId,
      workspace_id: authResult.workspaceId,
      created_by: authResult.userId,
      title: body.title ?? null,
      mode: "CHAT",
      status: "ACTIVE",
    },
  })

  return NextResponse.json({
    id: session.id,
    title: session.title,
    status: session.status,
  }, { status: 201 })
}

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
    select: { id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  const sessions = await prisma.chat.findMany({
    where: { agent_id: agentId, workspace_id: authResult.workspaceId },
    select: {
      id: true,
      title: true,
      mode: true,
      status: true,
      message_count: true,
      started_at: true,
      ended_at: true,
    },
    orderBy: { started_at: "desc" },
  })

  return NextResponse.json(sessions)
}
