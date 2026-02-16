import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createOrgSchema } from "@/lib/validations"

export async function GET() {
  // TODO: Get current user from session and filter by membership
  const orgs = await prisma.organization.findMany({
    where: { deleted_at: null },
    include: {
      _count: { select: { teams: true, agents: true, members: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(orgs)
}

export async function POST(req: NextRequest) {
  const body = await req.json()
  const parsed = createOrgSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const existing = await prisma.organization.findUnique({
    where: { slug: parsed.data.slug },
  })

  if (existing) {
    return NextResponse.json({ error: "Organization slug already taken" }, { status: 409 })
  }

  const org = await prisma.organization.create({
    data: {
      name: parsed.data.name,
      slug: parsed.data.slug,
    },
  })

  // TODO: Create membership for current user as OWNER
  // TODO: Create default FREE subscription

  return NextResponse.json(org, { status: 201 })
}
