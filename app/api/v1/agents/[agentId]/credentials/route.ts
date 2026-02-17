import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

const assignCredentialSchema = z.object({
  credential_id: z.string().uuid(),
  env_var_name: z.string().min(1).max(100),
  priority: z.number().int().min(0).default(0),
})

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Credential")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  const agentCredentials = await prisma.agentCredential.findMany({
    where: { agent_id: agentId },
    include: {
      credential: {
        select: {
          id: true,
          name: true,
          description: true,
          scope: true,
        },
      },
    },
    orderBy: [{ env_var_name: "asc" }, { priority: "asc" }],
  })

  return NextResponse.json(agentCredentials)
}

export async function POST(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = assignCredentialSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  // Verify credential exists in this org
  const credential = await prisma.credential.findFirst({
    where: { id: parsed.data.credential_id, workspace_id: authResult.workspaceId, deleted_at: null },
    select: { id: true },
  })

  if (!credential) {
    return NextResponse.json({ error: "Credential not found" }, { status: 404 })
  }

  // Check not already assigned
  const existingAssignment = await prisma.agentCredential.findUnique({
    where: {
      uq_agent_credential: { agent_id: agentId, credential_id: parsed.data.credential_id },
    },
    select: { id: true },
  })

  if (existingAssignment) {
    return NextResponse.json({ error: "Credential already assigned to this agent" }, { status: 409 })
  }

  const agentCredential = await prisma.agentCredential.create({
    data: {
      agent_id: agentId,
      credential_id: parsed.data.credential_id,
      env_var_name: parsed.data.env_var_name,
      priority: parsed.data.priority,
    },
    include: {
      credential: {
        select: {
          id: true,
          name: true,
          description: true,
          scope: true,
        },
      },
    },
  })

  return NextResponse.json(agentCredential, { status: 201 })
}
