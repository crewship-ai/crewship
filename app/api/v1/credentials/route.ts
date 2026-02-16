import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createCredentialSchema } from "@/lib/validations"
import { encrypt } from "@/lib/encryption"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  if (!orgId) {
    return NextResponse.json({ error: "org_id is required" }, { status: 400 })
  }

  const credentials = await prisma.credential.findMany({
    where: { org_id: orgId, deleted_at: null },
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
  const body = await req.json()
  const orgId = req.nextUrl.searchParams.get("org_id")

  if (!orgId) {
    return NextResponse.json({ error: "org_id is required" }, { status: 400 })
  }

  const parsed = createCredentialSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const encryptedValue = encrypt(parsed.data.value)

  // TODO: Get current user ID from session
  const credential = await prisma.credential.create({
    data: {
      org_id: orgId,
      name: parsed.data.name,
      description: parsed.data.description,
      encrypted_value: encryptedValue,
      scope: parsed.data.scope,
      team_id: parsed.data.team_id,
      created_by: "00000000-0000-0000-0000-000000000000", // TODO: real user ID
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
