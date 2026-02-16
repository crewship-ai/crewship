import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { requireInternal } from "@/lib/internal-auth"

export async function POST(req: NextRequest) {
  const authErr = requireInternal(req)
  if (authErr) return authErr

  const body = await req.json() as {
    session_id: string
    agent_id: string
    org_id: string
    user_id?: string
    title?: string
  }

  if (!body.session_id || !body.agent_id || !body.org_id) {
    return NextResponse.json(
      { error: "session_id, agent_id, and org_id are required" },
      { status: 400 },
    )
  }

  const existing = await prisma.conversationSession.findUnique({
    where: { id: body.session_id },
  })

  if (existing) {
    return NextResponse.json({ id: existing.id, status: "already_exists" })
  }

  const session = await prisma.conversationSession.create({
    data: {
      id: body.session_id,
      agent_id: body.agent_id,
      org_id: body.org_id,
      created_by: body.user_id || null,
      title: body.title || null,
      mode: "CHAT",
      status: "ACTIVE",
    },
  })

  return NextResponse.json({ id: session.id, status: "created" }, { status: 201 })
}
