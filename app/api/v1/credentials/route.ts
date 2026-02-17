import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createCredentialSchema } from "@/lib/validations"
import { encrypt } from "@/lib/encryption"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

const CREDENTIAL_LIST_SELECT = {
  id: true,
  name: true,
  description: true,
  type: true,
  provider: true,
  status: true,
  scope: true,
  team_id: true,
  account_label: true,
  account_email: true,
  token_expires_at: true,
  last_checked_at: true,
  last_error: true,
  created_at: true,
  updated_at: true,
  _count: { select: { agent_credentials: true } },
} as const

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const credentials = await prisma.credential.findMany({
    where: { org_id: authResult.orgId, deleted_at: null },
    select: CREDENTIAL_LIST_SELECT,
    orderBy: [{ type: "asc" }, { created_at: "desc" }],
  })

  return NextResponse.json(credentials)
}

export async function POST(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("create", "Credential")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = createCredentialSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  if (parsed.data.team_id) {
    const team = await prisma.team.findFirst({
      where: { id: parsed.data.team_id, org_id: authResult.orgId, deleted_at: null },
      select: { id: true },
    })
    if (!team) {
      return NextResponse.json({ error: "Invalid team_id" }, { status: 400 })
    }
  }

  // Remove soft-deleted credential with same name to avoid unique constraint violation
  await prisma.credential.deleteMany({
    where: {
      org_id: authResult.orgId,
      name: parsed.data.name,
      deleted_at: { not: null },
    },
  })

  try {
    const credential = await prisma.credential.create({
      data: {
        org_id: authResult.orgId,
        name: parsed.data.name,
        description: parsed.data.description,
        encrypted_value: encrypt(parsed.data.value),
        type: parsed.data.type,
        provider: parsed.data.provider,
        scope: parsed.data.scope,
        team_id: parsed.data.team_id,
        account_label: parsed.data.account_label,
        account_email: parsed.data.account_email,
        encrypted_refresh_token: parsed.data.refresh_token
          ? encrypt(parsed.data.refresh_token)
          : undefined,
        token_expires_at: parsed.data.token_expires_at
          ? new Date(parsed.data.token_expires_at)
          : undefined,
        created_by: authResult.userId,
      },
      select: {
        id: true,
        name: true,
        description: true,
        type: true,
        provider: true,
        status: true,
        scope: true,
        team_id: true,
        created_at: true,
      },
    })

    return NextResponse.json(credential, { status: 201 })
  } catch (err) {
    if (
      err &&
      typeof err === "object" &&
      "code" in err &&
      err.code === "P2002"
    ) {
      return NextResponse.json(
        { error: `Credential "${parsed.data.name}" already exists` },
        { status: 409 }
      )
    }
    throw err
  }
}
