"use client"

import { useEffect, useState } from "react"
import { Plus, ChevronDown } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { useParams, useSearchParams } from "next/navigation"
import { useWorkspace } from "@/hooks/use-workspace"

interface ChatSession {
  id: string
  title: string | null
  status: string
}

interface AgentInfo {
  id: string
  name: string
  cli_adapter: string
}

export function ChatPageClient() {
  const params = useParams<{ agentId: string }>()
  const searchParams = useSearchParams()
  const { workspaceId: wsId } = useWorkspace()
  const agentId = params.agentId
  const sessionId = searchParams.get("session") ?? undefined
  const workspaceId = searchParams.get("workspace_id") ?? wsId

  const [agent, setAgent] = useState<AgentInfo | null>(null)
  const [sessions, setSessions] = useState<ChatSession[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string>(sessionId ?? "")

  useEffect(() => {
    if (!agentId || !workspaceId) return

    fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : null)
      .then((data) => {
        if (data) setAgent({ id: data.id, name: data.name, cli_adapter: data.cli_adapter })
      })
      .catch(() => {})

    fetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((data: ChatSession[]) => {
        setSessions(data)
        if (!sessionId && data.length > 0) {
          setActiveSessionId(data[0].id)
        }
      })
      .catch(() => {})
  }, [agentId, workspaceId, sessionId])

  useEffect(() => {
    if (sessionId) {
      setActiveSessionId(sessionId)
    } else if (!activeSessionId && sessions.length > 0) {
      setActiveSessionId(sessions[0].id)
    } else if (!activeSessionId) {
      setActiveSessionId(crypto.randomUUID())
    }
  }, [sessionId, sessions, activeSessionId])

  const currentSession = sessions.find((s) => s.id === activeSessionId)

  return (
    <div className="flex flex-col h-full">
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

      <div className="flex-1 overflow-hidden">
        {activeSessionId && (
          <ChatPanel agentId={agentId} sessionId={activeSessionId} />
        )}
      </div>
    </div>
  )
}
