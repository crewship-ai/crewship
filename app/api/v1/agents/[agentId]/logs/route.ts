import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { getAgentLogs } from "@/lib/crewshipd-client"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> },
) {
  const { agentId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")
  const offset = parseInt(req.nextUrl.searchParams.get("offset") ?? "0", 10)
  const limit = parseInt(req.nextUrl.searchParams.get("limit") ?? "100", 10)

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true, team_id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  if (!agent.team_id) {
    return NextResponse.json([])
  }

  try {
    const res = await getAgentLogs(agentId, agent.team_id, offset, limit)
    if (!res.ok) {
      return NextResponse.json([])
    }
    return NextResponse.json(res.data.logs ?? [])
  } catch {
    return NextResponse.json([])
  }
}
