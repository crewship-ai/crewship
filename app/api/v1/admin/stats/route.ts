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

  const [orgsCount, usersCount, agentsCount, runsRunning] = await Promise.all([
    prisma.organization.count({ where: { deleted_at: null } }),
    prisma.user.count(),
    prisma.agent.count({ where: { deleted_at: null } }),
    prisma.agentRun.count({ where: { status: "RUNNING" } }),
  ])

  return NextResponse.json({
    organizations: orgsCount,
    users: usersCount,
    agents: agentsCount,
    running: runsRunning,
  })
}
