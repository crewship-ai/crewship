import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(req: NextRequest) {
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  if ((authResult.role as OrgRole) !== "OWNER") {
    return NextResponse.json({ error: "Forbidden: OWNER only" }, { status: 403 })
  }

  const orgs = await prisma.workspace.findMany({
    where: { deleted_at: null },
    include: {
      _count: { select: { members: true, agents: true, crews: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(orgs)
}
