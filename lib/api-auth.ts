import { NextResponse } from "next/server"
import { auth } from "@/auth"
import { prisma } from "@/lib/db"

interface AuthResult {
  userId: string
  workspaceId: string
  role: string
}

/**
 * Authenticate and authorize user for an workspace-scoped API request.
 * Returns user info + workspace role, or a NextResponse error.
 */
export async function requireAuth(
  workspaceId: string | null
): Promise<AuthResult | NextResponse> {
  const session = await auth()

  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  if (!workspaceId) {
    return NextResponse.json({ error: "workspace_id is required" }, { status: 400 })
  }

  const membership = await prisma.workspaceMember.findUnique({
    where: {
      uq_workspace_member: { workspace_id: workspaceId, user_id: session.user.id },
    },
    select: { role: true },
  })

  if (!membership) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  return {
    userId: session.user.id,
    workspaceId,
    role: membership.role,
  }
}

export function isAuthError(result: AuthResult | NextResponse): result is NextResponse {
  return result instanceof NextResponse
}
