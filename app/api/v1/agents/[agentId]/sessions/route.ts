import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  const sessions = await prisma.conversationSession.findMany({
    where: { agent_id: agentId, org_id: authResult.orgId },
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
