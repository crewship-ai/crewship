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

  const runs = await prisma.agentRun.findMany({
    where: { agent_id: agentId, org_id: authResult.orgId },
    select: {
      id: true,
      status: true,
      trigger_type: true,
      started_at: true,
      finished_at: true,
      error_message: true,
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(runs)
}
