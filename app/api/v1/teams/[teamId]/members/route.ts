import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

const addTeamMemberSchema = z.object({
  user_id: z.string().uuid(),
})

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
    select: { id: true },
  })

  if (!team) {
    return NextResponse.json({ error: "Team not found" }, { status: 404 })
  }

  const members = await prisma.teamMember.findMany({
    where: { team_id: teamId },
    include: {
      user: {
        select: { id: true, email: true, full_name: true, avatar_url: true },
      },
    },
    orderBy: { created_at: "asc" },
  })

  return NextResponse.json(members)
}

export async function POST(
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

  const team = await prisma.team.findFirst({
    where: { id: teamId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true },
  })

  if (!team) {
    return NextResponse.json({ error: "Team not found" }, { status: 404 })
  }

  const body = await req.json()
  const parsed = addTeamMemberSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  // Check user is an org member
  const orgMember = await prisma.organizationMember.findUnique({
    where: {
      uq_org_member: { org_id: authResult.orgId, user_id: parsed.data.user_id },
    },
    select: { id: true },
  })

  if (!orgMember) {
    return NextResponse.json({ error: "User is not a member of this organization" }, { status: 400 })
  }

  // Check not already in team
  const existingMember = await prisma.teamMember.findUnique({
    where: {
      uq_team_member: { team_id: teamId, user_id: parsed.data.user_id },
    },
    select: { id: true },
  })

  if (existingMember) {
    return NextResponse.json({ error: "User is already a member of this team" }, { status: 409 })
  }

  const member = await prisma.teamMember.create({
    data: {
      team_id: teamId,
      user_id: parsed.data.user_id,
    },
    include: {
      user: {
        select: { id: true, email: true, full_name: true, avatar_url: true },
      },
    },
  })

  return NextResponse.json(member, { status: 201 })
}
