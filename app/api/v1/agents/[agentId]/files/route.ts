import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { getTeamFiles } from "@/lib/crewshipd-client"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> },
) {
  const { agentId } = await params
  if (!z.string().uuid().safeParse(agentId).success) {
    return NextResponse.json({ error: "Invalid agent ID" }, { status: 400 })
  }
  const orgId = req.nextUrl.searchParams.get("org_id")
  if (orgId && !z.string().uuid().safeParse(orgId).success) {
    return NextResponse.json({ error: "Invalid org_id" }, { status: 400 })
  }

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true, slug: true, team_id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  if (!agent.team_id) {
    return NextResponse.json([])
  }

  try {
    const res = await getTeamFiles(agent.team_id, agent.slug)
    if (!res.ok) {
      return NextResponse.json({ error: "Failed to fetch files" }, { status: 502 })
    }
    return NextResponse.json(res.data.files ?? [])
  } catch {
    return NextResponse.json({ error: "Failed to fetch files" }, { status: 502 })
  }
}
