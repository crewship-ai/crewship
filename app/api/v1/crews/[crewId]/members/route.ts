import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

const addCrewMemberSchema = z.object({
  user_id: z.string().uuid(),
})

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ crewId: string }> }
) {
  const { crewId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const team = await prisma.crew.findFirst({
    where: { id: crewId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!team) {
    return NextResponse.json({ error: "Crew not found" }, { status: 404 })
  }

  const members = await prisma.crewMember.findMany({
    where: { crew_id: crewId },
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
  { params }: { params: Promise<{ crewId: string }> }
) {
  const { crewId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Crew")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const team = await prisma.crew.findFirst({
    where: { id: crewId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!team) {
    return NextResponse.json({ error: "Crew not found" }, { status: 404 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = addCrewMemberSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  // Check user is an org member
  const orgMember = await prisma.workspaceMember.findUnique({
    where: {
      uq_workspace_member: { workspace_id: authResult.workspaceId, user_id: parsed.data.user_id },
    },
    select: { id: true },
  })

  if (!orgMember) {
    return NextResponse.json({ error: "User is not a member of this workspace" }, { status: 400 })
  }

  // Check not already in team
  const existingMember = await prisma.crewMember.findUnique({
    where: {
      uq_crew_member: { crew_id: crewId, user_id: parsed.data.user_id },
    },
    select: { id: true },
  })

  if (existingMember) {
    return NextResponse.json({ error: "User is already a member of this team" }, { status: 409 })
  }

  const member = await prisma.crewMember.create({
    data: {
      crew_id: crewId,
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
