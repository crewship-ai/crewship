import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { inviteMemberSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { createAuditLog } from "@/lib/audit"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ workspaceId: string }> }
) {
  const { workspaceId } = await params

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Member")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const invitations = await prisma.workspaceInvitation.findMany({
    where: { workspace_id: authResult.workspaceId, accepted_at: null },
    include: {
      inviter: {
        select: { id: true, email: true, full_name: true },
      },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(invitations)
}

export async function POST(
  req: NextRequest,
  { params }: { params: Promise<{ workspaceId: string }> }
) {
  const { workspaceId } = await params

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("create", "Member")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }

  const parsed = inviteMemberSchema.safeParse(body)
  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const existingMember = await prisma.workspaceMember.findFirst({
    where: {
      workspace_id: authResult.workspaceId,
      user: { email: parsed.data.email },
    },
    select: { id: true },
  })

  if (existingMember) {
    return NextResponse.json(
      { error: "User is already a member of this workspace" },
      { status: 409 }
    )
  }

  const existingInvite = await prisma.workspaceInvitation.findFirst({
    where: {
      workspace_id: authResult.workspaceId,
      email: parsed.data.email,
      accepted_at: null,
      expires_at: { gt: new Date() },
    },
    select: { id: true },
  })

  if (existingInvite) {
    return NextResponse.json(
      { error: "An active invitation already exists for this email" },
      { status: 409 }
    )
  }

  const expiresAt = new Date()
  expiresAt.setDate(expiresAt.getDate() + 7)

  const invitation = await prisma.workspaceInvitation.create({
    data: {
      workspace_id: authResult.workspaceId,
      email: parsed.data.email,
      role: parsed.data.role as OrgRole,
      invited_by: authResult.userId,
      expires_at: expiresAt,
    },
    include: {
      inviter: {
        select: { id: true, email: true, full_name: true },
      },
    },
  })

  await createAuditLog({
    workspaceId: authResult.workspaceId,
    userId: authResult.userId,
    action: "member.invited",
    entityType: "WorkspaceInvitation",
    entityId: invitation.id,
    metadata: { email: parsed.data.email, role: parsed.data.role },
  })

  return NextResponse.json(invitation, { status: 201 })
}
