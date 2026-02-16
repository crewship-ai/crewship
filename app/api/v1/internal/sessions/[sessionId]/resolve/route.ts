import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { decrypt } from "@/lib/encryption"
import { requireInternal } from "@/lib/internal-auth"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ sessionId: string }> },
) {
  const authErr = requireInternal(req)
  if (authErr) return authErr

  const { sessionId } = await params

  const session = await prisma.conversationSession.findUnique({
    where: { id: sessionId },
    include: {
      agent: {
        include: {
          team: { select: { id: true, slug: true } },
          credentials: {
            include: {
              credential: { select: { id: true, encrypted_value: true } },
            },
            orderBy: { priority: "asc" },
          },
        },
      },
    },
  })

  if (!session) {
    return NextResponse.json({ error: "Session not found" }, { status: 404 })
  }

  const agent = session.agent
  const team = agent.team

  const credentials = agent.credentials.map((ac) => {
    let value = ""
    try {
      value = decrypt(ac.credential.encrypted_value)
    } catch {
      // credential decryption failed -- skip silently, log server-side only
      console.error(`Failed to decrypt credential ${ac.credential_id} for agent ${agent.id}`)
    }
    return {
      id: ac.credential_id,
      env_var: ac.env_var_name,
      value,
      priority: ac.priority,
    }
  })

  return NextResponse.json({
    agent_id: agent.id,
    agent_slug: agent.slug,
    team_id: team?.id ?? "",
    team_slug: team?.slug ?? "",
    container_id: "", // resolved by crewshipd container provider
    cli_adapter: agent.cli_adapter,
    system_prompt: agent.system_prompt ?? "",
    tool_profile: agent.tool_profile,
    credentials,
    timeout_seconds: agent.timeout_seconds,
  })
}
