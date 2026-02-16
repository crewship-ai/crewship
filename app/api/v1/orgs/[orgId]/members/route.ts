import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ orgId: string }> }
) {
  const { orgId } = await params

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const members = await prisma.organizationMember.findMany({
    where: { org_id: authResult.orgId },
    include: {
      user: {
        select: { id: true, email: true, full_name: true, avatar_url: true },
      },
    },
    orderBy: { created_at: "asc" },
  })

  return NextResponse.json(members)
}
