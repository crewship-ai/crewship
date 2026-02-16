import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createTeamSchema } from "@/lib/validations"

export async function GET(req: NextRequest) {
  const orgId = req.nextUrl.searchParams.get("org_id")

  if (!orgId) {
    return NextResponse.json({ error: "org_id is required" }, { status: 400 })
  }

  const teams = await prisma.team.findMany({
    where: { org_id: orgId, deleted_at: null },
    include: {
      _count: { select: { agents: true, members: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(teams)
}

export async function POST(req: NextRequest) {
  const body = await req.json()
  const orgId = req.nextUrl.searchParams.get("org_id")

  if (!orgId) {
    return NextResponse.json({ error: "org_id is required" }, { status: 400 })
  }

  const parsed = createTeamSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const existing = await prisma.team.findUnique({
    where: { uq_team_slug: { org_id: orgId, slug: parsed.data.slug } },
  })

  if (existing) {
    return NextResponse.json({ error: "Team slug already taken in this organization" }, { status: 409 })
  }

  const team = await prisma.team.create({
    data: {
      org_id: orgId,
      name: parsed.data.name,
      slug: parsed.data.slug,
      description: parsed.data.description,
      color: parsed.data.color,
      icon: parsed.data.icon,
    },
  })

  return NextResponse.json(team, { status: 201 })
}
