import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { updateCrewSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ crewId: string }> }
) {
  const { crewId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Crew")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const crew = await prisma.crew.findFirst({
    where: { id: crewId, workspace_id: authResult.workspaceId, deleted_at: null },
    include: {
      _count: { select: { agents: true, members: true } },
    },
  })

  if (!crew) {
    return NextResponse.json({ error: "Crew not found" }, { status: 404 })
  }

  return NextResponse.json(crew)
}

export async function PUT(
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

  const existing = await prisma.crew.findFirst({
    where: { id: crewId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Crew not found" }, { status: 404 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = updateCrewSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  if (parsed.data.slug) {
    const slugTaken = await prisma.crew.findFirst({
      where: {
        workspace_id: authResult.workspaceId,
        slug: parsed.data.slug,
        id: { not: crewId },
        deleted_at: null,
      },
      select: { id: true },
    })
    if (slugTaken) {
      return NextResponse.json({ error: "Crew slug already taken in this workspace" }, { status: 409 })
    }
  }

  const crew = await prisma.crew.update({
    where: { id: crewId },
    data: parsed.data,
    include: {
      _count: { select: { agents: true, members: true } },
    },
  })

  return NextResponse.json(crew)
}

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ crewId: string }> }
) {
  const { crewId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("delete", "Crew")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const existing = await prisma.crew.findFirst({
    where: { id: crewId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Crew not found" }, { status: 404 })
  }

  await prisma.crew.update({
    where: { id: crewId },
    data: { deleted_at: new Date() },
  })

  return NextResponse.json({ success: true })
}
