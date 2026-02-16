import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createCredentialSchema } from "@/lib/validations"
import { encrypt } from "@/lib/encryption"
import { requireAuth, isAuthError } from "@/lib/api-auth"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const credentials = await prisma.credential.findMany({
    where: { org_id: authResult.orgId, deleted_at: null },
    select: {
      id: true,
      name: true,
      description: true,
      scope: true,
      team_id: true,
      created_at: true,
      updated_at: true,
      _count: { select: { agent_credentials: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(credentials)
}

export async function POST(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  if (!["OWNER", "ADMIN"].includes(authResult.role)) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const body = await req.json()
  const parsed = createCredentialSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const encryptedValue = encrypt(parsed.data.value)

  const credential = await prisma.credential.create({
    data: {
      org_id: authResult.orgId,
      name: parsed.data.name,
      description: parsed.data.description,
      encrypted_value: encryptedValue,
      scope: parsed.data.scope,
      team_id: parsed.data.team_id,
      created_by: authResult.userId,
    },
    select: {
      id: true,
      name: true,
      description: true,
      scope: true,
      team_id: true,
      created_at: true,
    },
  })

  return NextResponse.json(credential, { status: 201 })
}
