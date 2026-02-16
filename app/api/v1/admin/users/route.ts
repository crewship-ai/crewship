import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  if ((authResult.role as OrgRole) !== "OWNER") {
    return NextResponse.json({ error: "Forbidden: OWNER only" }, { status: 403 })
  }

  const users = await prisma.user.findMany({
    include: {
      org_memberships: {
        include: {
          organization: { select: { id: true, name: true, slug: true } },
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
    organization: user.org_memberships[0]?.organization ?? null,
    role: user.org_memberships[0]?.role ?? null,
  }))

  return NextResponse.json(result)
}
