import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const status = req.nextUrl.searchParams.get("status")
  const agentId = req.nextUrl.searchParams.get("agent_id")
  const trigger = req.nextUrl.searchParams.get("trigger")
  const from = req.nextUrl.searchParams.get("from")
  const to = req.nextUrl.searchParams.get("to")
  const page = Math.max(1, parseInt(req.nextUrl.searchParams.get("page") ?? "1"))
  const limit = Math.min(100, Math.max(1, parseInt(req.nextUrl.searchParams.get("limit") ?? "50")))

  const where: Record<string, unknown> = { org_id: authResult.orgId }

  if (status) where.status = status
  if (agentId) where.agent_id = agentId
  if (trigger) where.trigger_type = trigger
  if (from || to) {
    where.created_at = {
      ...(from ? { gte: new Date(from) } : {}),
      ...(to ? { lte: new Date(to) } : {}),
    }
  }

  const [runs, total] = await Promise.all([
    prisma.agentRun.findMany({
      where,
      include: {
        agent: {
          select: {
            id: true,
            name: true,
            slug: true,
            cli_adapter: true,
            llm_provider: true,
            team: { select: { id: true, name: true, color: true } },
          },
        },
        triggerer: {
          select: { id: true, email: true, full_name: true },
        },
      },
      orderBy: { created_at: "desc" },
      take: limit,
      skip: (page - 1) * limit,
    }),
    prisma.agentRun.count({ where }),
  ])

  const now = new Date()
  const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate())

  const [runningCount, todayCount, failedCount] = await Promise.all([
    prisma.agentRun.count({ where: { org_id: authResult.orgId, status: "RUNNING" } }),
    prisma.agentRun.count({ where: { org_id: authResult.orgId, created_at: { gte: todayStart } } }),
    prisma.agentRun.count({ where: { org_id: authResult.orgId, status: "FAILED", created_at: { gte: todayStart } } }),
  ])

  return NextResponse.json({
    data: runs,
    stats: { running: runningCount, today: todayCount, failed: failedCount },
    pagination: { page, limit, total, total_pages: Math.ceil(total / limit) },
  })
}
