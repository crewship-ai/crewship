import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ crewId: string; memberId: string }> }
) {
  const { crewId, memberId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Crew")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const team = await prisma.crew.findFirst({
    where: { id: crewId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!team) {
    return NextResponse.json({ error: "Crew not found" }, { status: 404 })
  }

  const member = await prisma.crewMember.findFirst({
    where: { id: memberId, crew_id: crewId },
    select: { id: true },
  })

  if (!member) {
    return NextResponse.json({ error: "Crew member not found" }, { status: 404 })
  }

  await prisma.crewMember.delete({
    where: { id: memberId },
  })

  return NextResponse.json({ success: true })
}
