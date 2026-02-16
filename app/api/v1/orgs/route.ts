import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createOrgSchema } from "@/lib/validations"
import { auth } from "@/auth"

export async function GET() {
  const session = await auth()

  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  const orgs = await prisma.organization.findMany({
    where: {
      deleted_at: null,
      members: { some: { user_id: session.user.id } },
    },
    include: {
      _count: { select: { teams: true, agents: true, members: true } },
    },
    orderBy: { created_at: "desc" },
  })

  return NextResponse.json(orgs)
}

export async function POST(req: NextRequest) {
  const session = await auth()

  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  let body: unknown
  try {
    body = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON body" }, { status: 400 })
  }
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
      members: {
        create: {
          user_id: session.user.id,
          role: "OWNER",
        },
      },
    },
  })

  return NextResponse.json(org, { status: 201 })
}
