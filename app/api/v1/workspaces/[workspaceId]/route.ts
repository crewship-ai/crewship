import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { updateWorkspaceSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ workspaceId: string }> }
) {
  const { workspaceId } = await params

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Workspace")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const org = await prisma.workspace.findFirst({
    where: { id: authResult.workspaceId, deleted_at: null },
    include: {
      _count: { select: { crews: true, agents: true, members: true } },
    },
  })

  if (!org) {
    return NextResponse.json({ error: "Workspace not found" }, { status: 404 })
  }

  return NextResponse.json(org)
}

export async function PUT(
  req: NextRequest,
  { params }: { params: Promise<{ workspaceId: string }> }
) {
  const { workspaceId } = await params

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Workspace")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = updateWorkspaceSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  if (parsed.data.slug) {
    const slugTaken = await prisma.workspace.findFirst({
      where: {
        slug: parsed.data.slug,
        id: { not: authResult.workspaceId },
      },
      select: { id: true },
    })
    if (slugTaken) {
      return NextResponse.json({ error: "Workspace slug already taken" }, { status: 409 })
    }
  }

  const org = await prisma.workspace.update({
    where: { id: authResult.workspaceId },
    data: parsed.data,
    include: {
      _count: { select: { crews: true, agents: true, members: true } },
    },
  })

  return NextResponse.json(org)
}

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ workspaceId: string }> }
) {
  const { workspaceId } = await params

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("delete", "Workspace")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  // Only OWNER can delete an org (ADMIN has "manage all" but we add extra check)
  if (authResult.role !== "OWNER") {
    return NextResponse.json({ error: "Only the workspace owner can delete it" }, { status: 403 })
  }

  await prisma.workspace.update({
    where: { id: authResult.workspaceId },
    data: { deleted_at: new Date() },
  })

  return NextResponse.json({ success: true })
}
