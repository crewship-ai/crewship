import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string; assignmentId: string }> }
) {
  const { agentId, assignmentId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  const assignment = await prisma.agentCredential.findFirst({
    where: { id: assignmentId, agent_id: agentId },
    select: { id: true },
  })

  if (!assignment) {
    return NextResponse.json({ error: "Assignment not found" }, { status: 404 })
  }

  await prisma.agentCredential.delete({ where: { id: assignmentId } })

  return NextResponse.json({ deleted: true })
}
