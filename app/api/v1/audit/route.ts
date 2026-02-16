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
  if (!abilities.can("read", "AuditLog")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const action = req.nextUrl.searchParams.get("action")
  const entityType = req.nextUrl.searchParams.get("entity_type")
  const userId = req.nextUrl.searchParams.get("user_id")
  const dateFrom = req.nextUrl.searchParams.get("date_from")
  const dateTo = req.nextUrl.searchParams.get("date_to")
  const page = parseInt(req.nextUrl.searchParams.get("page") ?? "1", 10)
  const limit = Math.min(parseInt(req.nextUrl.searchParams.get("limit") ?? "50", 10), 100)
  const skip = (page - 1) * limit

  const where: Record<string, unknown> = { org_id: authResult.orgId }

  if (action) where.action = action
  if (entityType) where.entity_type = entityType
  if (userId) where.user_id = userId

  if (dateFrom || dateTo) {
    const createdAt: Record<string, Date> = {}
    if (dateFrom) createdAt.gte = new Date(dateFrom)
    if (dateTo) createdAt.lte = new Date(dateTo)
    where.created_at = createdAt
  }

  const [logs, total] = await Promise.all([
    prisma.auditLog.findMany({
      where,
      include: {
        user: {
          select: { id: true, email: true, full_name: true },
        },
      },
      orderBy: { created_at: "desc" },
      skip,
      take: limit,
    }),
    prisma.auditLog.count({ where }),
  ])

  return NextResponse.json({
    data: logs,
    pagination: {
      page,
      limit,
      total,
      total_pages: Math.ceil(total / limit),
    },
  })
}
