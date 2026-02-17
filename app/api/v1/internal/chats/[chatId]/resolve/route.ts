import { NextRequest, NextResponse } from "next/server"
import { prisma } from "@/lib/db"
import { decrypt } from "@/lib/encryption"
import { requireInternal } from "@/lib/internal-auth"

export async function GET(
  req: NextRequest,
  { params }: { params: Promise<{ chatId: string }> },
) {
  const authErr = requireInternal(req)
  if (authErr) return authErr

  const { chatId } = await params

  const session = await prisma.chat.findUnique({
    where: { id: chatId },
    include: {
      agent: {
        include: {
          crew: { select: { id: true, slug: true } },
          credentials: {
            include: {
              credential: { select: { id: true, encrypted_value: true, type: true } },
            },
            orderBy: { priority: "asc" },
          },
        },
      },
    },
  })

  if (!session) {
    return NextResponse.json({ error: "Chat not found" }, { status: 404 })
  }

  const agent = session.agent
  const crew = agent.crew

  // SECURITY NOTE: Plaintext values are intentionally returned here.
  // This is an internal-only endpoint (requireInternal auth) consumed by crewshipd (Go)
  // via IPC. crewshipd needs plaintext to inject as ENV vars into Docker exec.
  // This endpoint is NEVER exposed to browsers or external clients.
  const credentials = agent.credentials.map((ac) => {
    let value = ""
    try {
      value = decrypt(ac.credential.encrypted_value)
    } catch {
      console.error(`Failed to decrypt credential ${ac.credential_id} for agent ${agent.id}`)
    }
    return {
      id: ac.credential_id,
      env_var: ac.env_var_name,
      value,
      priority: ac.priority,
      type: ac.credential.type,
    }
  })

  return NextResponse.json({
    agent_id: agent.id,
    agent_slug: agent.slug,
    crew_id: crew?.id ?? "",
    crew_slug: crew?.slug ?? "",
    container_id: "", // resolved by crewshipd container provider
    cli_adapter: agent.cli_adapter,
    system_prompt: agent.system_prompt ?? "",
    tool_profile: agent.tool_profile,
    credentials,
    timeout_seconds: agent.timeout_seconds,
  })
}
