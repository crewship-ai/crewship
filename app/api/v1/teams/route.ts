import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createTeamSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const teams = await prisma.team.findMany({
    where: { org_id: authResult.orgId, deleted_at: null },
    include: {
      _count: { select: { agents: true, members: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(teams)
}

export async function POST(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("create", "Team")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const body = await req.json()
  const parsed = createTeamSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const existing = await prisma.team.findUnique({
    where: { uq_team_slug: { org_id: authResult.orgId, slug: parsed.data.slug } },
  })

  if (existing) {
    return NextResponse.json({ error: "Team slug already taken in this organization" }, { status: 409 })
  }

  const team = await prisma.team.create({
    data: {
      org_id: authResult.orgId,
      name: parsed.data.name,
      slug: parsed.data.slug,
      description: parsed.data.description,
      color: parsed.data.color,
      icon: parsed.data.icon,
    },
  })

  return NextResponse.json(team, { status: 201 })
}
