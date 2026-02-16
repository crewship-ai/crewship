import { NextResponse } from "next/server"
import { auth } from "@/auth"
import { prisma } from "@/lib/db"

interface AuthResult {
  userId: string
  orgId: string
  role: string
}

/**
 * Authenticate and authorize user for an org-scoped API request.
 * Returns user info + org role, or a NextResponse error.
 */
export async function requireAuth(
  orgId: string | null
): Promise<AuthResult | NextResponse> {
  const session = await auth()

  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  if (!orgId) {
    return NextResponse.json({ error: "org_id is required" }, { status: 400 })
  }

  const membership = await prisma.organizationMember.findUnique({
    where: {
      uq_org_member: { org_id: orgId, user_id: session.user.id },
    },
    select: { role: true },
  })

  if (!membership) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  return {
    userId: session.user.id,
    orgId,
    role: membership.role,
  }
}

export function isAuthError(result: AuthResult | NextResponse): result is NextResponse {
  return result instanceof NextResponse
}
