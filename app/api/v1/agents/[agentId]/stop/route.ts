import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { stopAgent } from "@/lib/crewshipd-client"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function POST(
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

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true, status: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  try {
    await stopAgent(agentId)
  } catch {
    // crewshipd may not be running -- still update DB status
  }

  const updated = await prisma.agent.update({
    where: { id: agentId },
    data: { status: "STOPPED" },
    select: { id: true, status: true },
  })

  return NextResponse.json(updated)
}
