import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { createWorkspaceSchema } from "@/lib/validations"
import { auth } from "@/auth"

export async function GET() {
  const session = await auth()

  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  const orgs = await prisma.workspace.findMany({
    where: {
      deleted_at: null,
      members: { some: { user_id: session.user.id } },
    },
    include: {
      _count: { select: { crews: true, agents: true, members: true } },
      members: {
        where: { user_id: session.user.id },
        select: { role: true },
        take: 1,
      },
    },
    orderBy: { created_at: "desc" },
  })

  const result = orgs.map(({ members, ...org }) => ({
    ...org,
    currentUserRole: members[0]?.role ?? null,
  }))

  return NextResponse.json(result)
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
  const parsed = createWorkspaceSchema.safeParse(body)

  if (!parsed.success) {
    return NextResponse.json({ error: parsed.error.flatten() }, { status: 400 })
  }

  const existing = await prisma.workspace.findUnique({
    where: { slug: parsed.data.slug },
  })

  if (existing) {
    return NextResponse.json({ error: "Workspace slug already taken" }, { status: 409 })
  }

  const org = await prisma.workspace.create({
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
