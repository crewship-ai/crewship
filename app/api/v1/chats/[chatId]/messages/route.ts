import { NextRequest, NextResponse } from "next/server"
import { z } from "zod"
import { auth } from "@/auth"
import { prisma } from "@/lib/db"
import { defineAbilitiesFor } from "@/lib/permissions/abilities"
import { getChatMessages } from "@/lib/crewshipd-client"
import type { OrgRole } from "@/lib/generated/prisma/client"

const querySchema = z.object({
  offset: z.coerce.number().int().min(0).default(0),
  limit: z.coerce.number().int().min(1).max(500).default(50),
})

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ chatId: string }> },
) {
  const session = await auth()
  if (!session?.user?.id) {
    return NextResponse.json({ error: "Unauthorized" }, { status: 401 })
  }

  const { chatId } = await params
  if (!z.string().uuid().safeParse(chatId).success) {
    return NextResponse.json({ error: "Invalid session ID" }, { status: 400 })
  }

  const chatRecord = await prisma.chat.findUnique({
    where: { id: chatId },
    select: { id: true, workspace_id: true },
  })

  if (!chatRecord) {
    return NextResponse.json({ error: "Session not found" }, { status: 404 })
  }

  const membership = await prisma.workspaceMember.findUnique({
    where: {
      uq_workspace_member: { workspace_id: chatRecord.workspace_id, user_id: session.user.id },
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
    const result = await getChatMessages(chatId, offset, limit)
    if (!result.ok) {
      return NextResponse.json({ error: "Failed to fetch messages" }, { status: 502 })
    }
    return NextResponse.json(result.data)
  } catch {
    return NextResponse.json({ error: "Failed to fetch messages" }, { status: 502 })
  }
}
