import { Plus, ChevronDown, Info } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { prisma } from "@/lib/db"
import { auth } from "@/auth"
import { redirect } from "next/navigation"

export default async function ChatPage({ params, searchParams }: {
  params: Promise<{ agentId: string }>
  searchParams: Promise<{ session?: string; workspace_id?: string }>
}) {
  const { agentId } = await params
  const { session: sessionId, workspace_id: workspaceId } = await searchParams

  const authSession = await auth()
  if (!authSession?.user?.id) redirect("/login")

  let agent: { id: string; name: string; cli_adapter: string } | null = null
  let sessions: { id: string; title: string | null; status: string }[] = []
  let activeSessionId = sessionId

  try {
    const found = await prisma.agent.findFirst({
      where: { id: agentId, deleted_at: null },
      select: { id: true, name: true, cli_adapter: true },
    })
    if (found) {
      agent = { id: found.id, name: found.name, cli_adapter: String(found.cli_adapter) }
    }

    if (agent && workspaceId) {
      sessions = await prisma.chat.findMany({
        where: { agent_id: agentId, workspace_id: workspaceId },
        select: { id: true, title: true, status: true },
        orderBy: { started_at: "desc" },
        take: 20,
      })
    }
  } catch {
    // DB not available -- render without data
  }

  if (!activeSessionId && sessions.length > 0) {
    activeSessionId = sessions[0].id
  }

  if (!activeSessionId) {
    activeSessionId = crypto.randomUUID()
  }

  const currentSession = sessions.find((s) => s.id === activeSessionId)

  return (
    <div className="flex flex-col h-full">
      {/* Session selector bar */}
      <div className="flex flex-wrap items-center gap-2 border-b px-4 sm:px-6 py-2 bg-muted/30">
        {currentSession ? (
          <Button variant="outline" size="sm" className="gap-1.5 text-xs">
            {currentSession.title ?? `Session ${currentSession.id.slice(0, 8)}`}
            <ChevronDown className="h-3 w-3" />
          </Button>
        ) : (
          <Button variant="outline" size="sm" className="gap-1.5 text-xs">
            New Session <ChevronDown className="h-3 w-3" />
          </Button>
        )}
        <Button variant="outline" size="sm" className="gap-1.5 text-xs" asChild>
          <a href={`/agents/${agentId}/chat?workspace_id=${workspaceId ?? ""}`}>
            <Plus className="h-3 w-3" /> New Session
          </a>
        </Button>
        <div className="hidden sm:flex items-center gap-3 ml-auto text-xs text-muted-foreground">
          {agent && (
            <>
              <span>Agent: <strong className="text-foreground">{agent.name}</strong></span>
              <span>CLI: <code className="text-[11px]">{agent.cli_adapter}</code></span>
            </>
          )}
        </div>
      </div>

      {/* Backend info banner */}
      {!process.env.CREWSHIPD_URL && (
        <div className="mx-4 sm:mx-6 mt-2 flex items-center gap-2 rounded-md bg-muted/10 border border-border px-3 py-2">
          <Info className="h-4 w-4 text-muted-foreground shrink-0" />
          <p className="text-xs text-muted-foreground">
            Set <code>CREWSHIPD_URL</code> and run <strong>crewshipd</strong> for live chat.
          </p>
        </div>
      )}

      {/* Chat panel (client component) */}
      <div className="flex-1 overflow-hidden">
        <ChatPanel agentId={agentId} sessionId={activeSessionId} />
      </div>
    </div>
  )
}
