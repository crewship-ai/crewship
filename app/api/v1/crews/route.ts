import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createCrewSchema } from "@/lib/validations"
import { requireAuth, isAuthError } from "@/lib/api-auth"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import type { OrgRole } from "@/lib/generated/prisma/client"

export async function GET(req: NextRequest) {
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const crews = await prisma.crew.findMany({
    where: { workspace_id: authResult.workspaceId, deleted_at: null },
    include: {
      _count: { select: { agents: true, members: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(crews)
}

export async function POST(req: NextRequest) {
  const workspaceId = req.nextUrl.searchParams.get("workspace_id")

  const authResult = await requireAuth(workspaceId)
  if (isAuthError(authResult)) return authResult

  const abilities = defineAbilitiesFor(authResult.role as OrgRole)
  if (!abilities.can("create", "Crew")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
  const parsed = createCrewSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const existing = await prisma.crew.findUnique({
    where: { uq_crew_slug: { workspace_id: authResult.workspaceId, slug: parsed.data.slug } },
  })

  if (existing) {
    return NextResponse.json({ error: "Crew slug already taken in this workspace" }, { status: 409 })
  }

  const team = await prisma.crew.create({
    data: {
      workspace_id: authResult.workspaceId,
      name: parsed.data.name,
      slug: parsed.data.slug,
      description: parsed.data.description,
      color: parsed.data.color,
      icon: parsed.data.icon,
    },
  })

  return NextResponse.json(team, { status: 201 })
}
