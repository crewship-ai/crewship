import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { prisma } from "@/lib/db"
import { requireInternal } from "@/lib/internal-auth"

const createSessionSchema = z.object({
  session_id: z.string().uuid(),
  agent_id: z.string().uuid(),
  org_id: z.string().uuid(),
  user_id: z.string().uuid().optional(),
  title: z.string().max(255).optional(),
})

/** Create a conversation session in Prisma (IPC-only, called by crewshipd). */
export async function POST(req: NextRequest) {
  const authErr = requireInternal(req)
  if (authErr) return authErr

  let rawBody: unknown
  try {
    rawBody = await req.json()
  } catch {
    return NextResponse.json({ error: "Invalid JSON" }, { status: 400 })
  }

  const parsed = createSessionSchema.safeParse(rawBody)
  if (!parsed.success) {
    return NextResponse.json(
      { error: "Validation failed", details: parsed.error.flatten().fieldErrors },
      { status: 400 },
    )
  }

  const body = parsed.data

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
