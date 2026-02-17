import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ workspaceId: string }> }
) {
  const { workspaceId } = await params

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Member")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const members = await prisma.workspaceMember.findMany({
    where: { workspace_id: authResult.workspaceId },
    include: {
      user: {
        select: { id: true, email: true, full_name: true, avatar_url: true },
      },
    },
    orderBy: { created_at: "asc" },
  })

  return NextResponse.json(members)
}
