import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ teamId: string; memberId: string }> }
) {
  const { teamId, memberId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Team")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const team = await prisma.team.findFirst({
    where: { id: teamId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true },
  })

  if (!team) {
    return NextResponse.json({ error: "Team not found" }, { status: 404 })
  }

  const member = await prisma.teamMember.findFirst({
    where: { id: memberId, team_id: teamId },
    select: { id: true },
  })

  if (!member) {
    return NextResponse.json({ error: "Team member not found" }, { status: 404 })
  }

  await prisma.teamMember.delete({
    where: { id: memberId },
  })

  return NextResponse.json({ success: true })
}
