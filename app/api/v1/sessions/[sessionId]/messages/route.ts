import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { auth } from "@/auth"
import { prisma } from "@/lib/db"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { getSessionMessages } from "@/lib/crewshipd-client"
import type { OrgRole } from "@/lib/generated/prisma/client"

const querySchema = z.object({
  offset: z.coerce.number().int().min(0).default(0),
  limit: z.coerce.number().int().min(1).max(500).default(50),
})

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ sessionId: string }> },
) {
  const session = await auth()
  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  const { sessionId } = await params
  if (!z.string().uuid().safeParse(sessionId).success) {
    return NextResponse.json({ error: "Invalid session ID" }, { status: 400 })
  }

  const convSession = await prisma.conversationSession.findUnique({
    where: { id: sessionId },
    select: { id: true, org_id: true },
  })

  if (!convSession) {
    return NextResponse.json({ error: "Session not found" }, { status: 404 })
  }

  const membership = await prisma.organizationMember.findUnique({
    where: {
      uq_org_member: { org_id: convSession.org_id, user_id: session.user.id },
    },
  })

  if (!membership) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const abilities = defineAbilitiesFor(membership.role as OrgRole)
  if (!abilities.can("read", "Agent")) {
    return NextResponse.json({ error: "Forbidden" }, { status: 403 })
  }

  const parsed = querySchema.safeParse({
    offset: req.nextUrl.searchParams.get("offset") ?? "0",
    limit: req.nextUrl.searchParams.get("limit") ?? "50",
  })
  if (!parsed.success) {
    return NextResponse.json({ error: "Invalid query parameters" }, { status: 400 })
  }
  const { offset, limit } = parsed.data

  try {
    const result = await getSessionMessages(sessionId, offset, limit)
    if (!result.ok) {
      return NextResponse.json({ error: "Failed to fetch messages" }, { status: 502 })
    }
    return NextResponse.json(result.data)
  } catch {
    return NextResponse.json({ error: "Failed to fetch messages" }, { status: 502 })
  }
}
