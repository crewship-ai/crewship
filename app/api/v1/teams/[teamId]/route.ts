import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { updateTeamSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ teamId: string }> }
) {
  const { teamId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const team = await prisma.team.findFirst({
    where: { id: teamId, org_id: authResult.orgId, deleted_at: null },
    include: {
      _count: { select: { agents: true, members: true } },
    },
  })

  if (!team) {
    return NextResponse.json({ error: "Team not found" }, { status: 404 })
  }

  return NextResponse.json(team)
}

export async function PUT(
  req: NextRequest,
  { params }: { params: Promise<{ teamId: string }> }
) {
  const { teamId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Team")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const existing = await prisma.team.findFirst({
    where: { id: teamId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Team not found" }, { status: 404 })
  }

  const body = await req.json()
  const parsed = updateTeamSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  if (parsed.data.slug) {
    const slugTaken = await prisma.team.findFirst({
      where: {
        org_id: authResult.orgId,
        slug: parsed.data.slug,
        id: { not: teamId },
        deleted_at: null,
      },
      select: { id: true },
    })
    if (slugTaken) {
      return NextResponse.json({ error: "Team slug already taken in this organization" }, { status: 409 })
    }
  }

  const team = await prisma.team.update({
    where: { id: teamId },
    data: parsed.data,
    include: {
      _count: { select: { agents: true, members: true } },
    },
  })

  return NextResponse.json(team)
}

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ teamId: string }> }
) {
  const { teamId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("delete", "Team")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const existing = await prisma.team.findFirst({
    where: { id: teamId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Team not found" }, { status: 404 })
  }

  await prisma.team.update({
    where: { id: teamId },
    data: { deleted_at: new Date() },
  })

  return NextResponse.json({ success: true })
}
