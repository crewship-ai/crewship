import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { updateOrgSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ orgId: string }> }
) {
  const { orgId } = await params

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Organization")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const org = await prisma.organization.findFirst({
    where: { id: authResult.orgId, deleted_at: null },
    include: {
      _count: { select: { teams: true, agents: true, members: true } },
    },
  })

  if (!org) {
    return NextResponse.json({ error: "Organization not found" }, { status: 404 })
  }

  return NextResponse.json(org)
}

export async function PUT(
  req: NextRequest,
  { params }: { params: Promise<{ orgId: string }> }
) {
  const { orgId } = await params

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Organization")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const body = await req.json()
  const parsed = updateOrgSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  if (parsed.data.slug) {
    const slugTaken = await prisma.organization.findFirst({
      where: {
        slug: parsed.data.slug,
        id: { not: authResult.orgId },
      },
      select: { id: true },
    })
    if (slugTaken) {
      return NextResponse.json({ error: "Organization slug already taken" }, { status: 409 })
    }
  }

  const org = await prisma.organization.update({
    where: { id: authResult.orgId },
    data: parsed.data,
    include: {
      _count: { select: { teams: true, agents: true, members: true } },
    },
  })

  return NextResponse.json(org)
}

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ orgId: string }> }
) {
  const { orgId } = await params

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("delete", "Organization")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  // Only OWNER can delete an org (ADMIN has "manage all" but we add extra check)
  if (authResult.role !== "OWNER") {
    return NextResponse.json({ error: "Only the organization owner can delete it" }, { status: 403 })
  }

  await prisma.organization.update({
    where: { id: authResult.orgId },
    data: { deleted_at: new Date() },
  })

  return NextResponse.json({ success: true })
}
