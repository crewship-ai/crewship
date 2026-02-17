import { NextRequest, NextResponse } from "next/server"
import { auth } from "@/auth"
import { prisma } from "@/lib/db"
import { getSessionMessages } from "@/lib/crewshipd-client"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ sessionId: string }> },
) {
  const session = await auth()
  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  const { sessionId } = await params

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

  const offset = parseInt(req.nextUrl.searchParams.get("offset") ?? "0", 10)
  const limit = parseInt(req.nextUrl.searchParams.get("limit") ?? "50", 10)

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
