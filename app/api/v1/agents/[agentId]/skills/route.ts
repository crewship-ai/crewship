import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"
import { Prisma } from "@/lib/generated/prisma/client"

const assignSkillSchema = z.object({
  skill_id: z.string().uuid(),
  config: z.record(z.string(), z.unknown()).optional(),
  enabled: z.boolean().default(true),
})

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("read", "Skill")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, org_id: authResult.orgId, deleted_at: null },
    select: { id: true },
  })

  if (!agent) {
    return NextResponse.json({ error: "Agent not found" }, { status: 404 })
  }

  const agentSkills = await prisma.agentSkill.findMany({
    where: { agent_id: agentId },
    include: {
      skill: {
        select: {
          id: true,
          name: true,
          slug: true,
          display_name: true,
          description: true,
          category: true,
          source: true,
          icon: true,
          version: true,
        },
      },
    },
    orderBy: { created_at: "asc" },
  })

  return NextResponse.json(agentSkills)
}

export async function POST(
  req: NextRequest,
  { params }: { params: Promise<{ agentId: string }> }
) {
  const { agentId } = await params
  const orgId = req.nextUrl.searchParams.get("org_id")

  const authResult = await requireAuth(orgId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("update", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const agent = await prisma.agent.findFirst({
    where: { id: agentId, org_id: authResult.orgId, deleted_at: null },
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
  const parsed = assignSkillSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  // Verify skill exists
  const skill = await prisma.skill.findUnique({
    where: { id: parsed.data.skill_id },
    select: { id: true },
  })

  if (!skill) {
    return NextResponse.json({ error: "Skill not found" }, { status: 404 })
  }

  // Check not already assigned
  const existingAssignment = await prisma.agentSkill.findUnique({
    where: {
      uq_agent_skill: { agent_id: agentId, skill_id: parsed.data.skill_id },
    },
    select: { id: true },
  })

  if (existingAssignment) {
    return NextResponse.json({ error: "Skill already assigned to this agent" }, { status: 409 })
  }

  const agentSkill = await prisma.agentSkill.create({
    data: {
      agent_id: agentId,
      skill_id: parsed.data.skill_id,
      config: parsed.data.config as Prisma.InputJsonValue ?? undefined,
      enabled: parsed.data.enabled,
    },
    include: {
      skill: {
        select: {
          id: true,
          name: true,
          slug: true,
          display_name: true,
          description: true,
          category: true,
          source: true,
          icon: true,
          version: true,
        },
      },
    },
  })

  return NextResponse.json(agentSkill, { status: 201 })
}
