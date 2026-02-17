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

  const users = await prisma.user.findMany({
    include: {
      workspace_memberships: {
        include: {
          workspace: { select: { id: true, name: true, slug: true } },
        },
        take: 1,
      },
    },
    orderBy: { created_at: "desc" },
  })

  const result = users.map((user) => ({
    id: user.id,
    email: user.email,
    full_name: user.full_name,
    avatar_url: user.avatar_url,
    created_at: user.created_at,
    workspace: user.workspace_memberships[0]?.workspace ?? null,
    role: user.workspace_memberships[0]?.role ?? null,
  }))

  return NextResponse.json(result)
}
