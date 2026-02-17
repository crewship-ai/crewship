import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { encrypt } from "@/lib/encryption"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

const CREDENTIAL_SAFE_SELECT = {
  id: true,
  name: true,
  description: true,
  type: true,
  provider: true,
  status: true,
  scope: true,
  crew_id: true,
  account_label: true,
  account_email: true,
  token_expires_at: true,
  last_checked_at: true,
  last_error: true,
  created_by: true,
  created_at: true,
  updated_at: true,
  _count: { select: { agent_credentials: true } },
} as const

const updateCredentialSchema = z.object({
  name: z.string().min(1).max(100).optional(),
  description: z.string().max(500).optional(),
  value: z.string().min(1).optional(),
  scope: z.enum(["WORKSPACE", "CREW"]).optional(),
  crew_id: z.string().uuid().optional().nullable(),
  account_label: z.string().max(100).optional().nullable(),
  account_email: z.string().email().optional().nullable(),
})

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ credentialId: string }> }
) {
  const { credentialId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Credential")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const credential = await prisma.credential.findFirst({
    where: { id: credentialId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: CREDENTIAL_SAFE_SELECT,
  })

  if (!credential) {
    return NextResponse.json({ error: "Credential not found" }, { status: 404 })
  }

  return NextResponse.json(credential)
}

export async function PUT(
  req: NextRequest,
  { params }: { params: Promise<{ credentialId: string }> }
) {
  const { credentialId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Credential")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const existing = await prisma.credential.findFirst({
    where: { id: credentialId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Credential not found" }, { status: 404 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = updateCredentialSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  if (parsed.data.crew_id) {
    const team = await prisma.crew.findFirst({
      where: { id: parsed.data.crew_id, workspace_id: authResult.workspaceId, deleted_at: null },
      select: { id: true },
    })
    if (!team) {
      return NextResponse.json({ error: "Invalid crew_id" }, { status: 400 })
    }
  }

  const data: Record<string, unknown> = {}
  if (parsed.data.name !== undefined) data.name = parsed.data.name
  if (parsed.data.description !== undefined) data.description = parsed.data.description
  if (parsed.data.scope !== undefined) data.scope = parsed.data.scope
  if (parsed.data.crew_id !== undefined) data.crew_id = parsed.data.crew_id
  if (parsed.data.value) data.encrypted_value = encrypt(parsed.data.value)

  const credential = await prisma.credential.update({
    where: { id: credentialId },
    data,
    select: CREDENTIAL_SAFE_SELECT,
  })

  return NextResponse.json(credential)
}

export async function DELETE(
  req: NextRequest,
  { params }: { params: Promise<{ credentialId: string }> }
) {
  const { credentialId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("delete", "Credential")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const existing = await prisma.credential.findFirst({
    where: { id: credentialId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!existing) {
    return NextResponse.json({ error: "Credential not found" }, { status: 404 })
  }

  await prisma.credential.update({
    where: { id: credentialId },
    data: { deleted_at: new Date() },
  })

  return NextResponse.json({ success: true })
}
