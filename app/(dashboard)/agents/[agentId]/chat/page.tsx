"use client"

import { use, useState, useEffect, useCallback } from "react"
import { Plus, ChevronDown, Info } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { useWorkspace } from "@/hooks/use-workspace"

interface AgentInfo {
  id: string
  name: string
  cli_adapter: string
  llm_model: string | null
  llm_provider: string | null
  tool_profile: string
}

interface SessionInfo {
  id: string
  title: string | null
  status: string
  message_count: number
}

export default function ChatPage({ params, searchParams }: {
  params: Promise<{ agentId: string }>
  searchParams: Promise<{ session?: string; workspace_id?: string }>
}) {
  const { agentId } = use(params)
  const { session: sessionParam, workspace_id: wsParam } = use(searchParams)
  const { workspaceId: storeWorkspaceId } = useWorkspace()
  const workspaceId = wsParam ?? storeWorkspaceId

  const [agent, setAgent] = useState<AgentInfo | null>(null)
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string>(sessionParam ?? "")
  const [showSessionList, setShowSessionList] = useState(false)

  useEffect(() => {
    if (!workspaceId) return
    fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : null)
      .then((data) => {
        if (data) setAgent({
          id: data.id,
          name: data.name,
          cli_adapter: String(data.cli_adapter),
          llm_model: data.llm_model,
          llm_provider: data.llm_provider,
          tool_profile: data.tool_profile,
        })
      })
      .catch(() => {})
  }, [agentId, workspaceId])

  useEffect(() => {
    if (!workspaceId) return
    fetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : null)
      .then((data: SessionInfo[] | null) => {
        if (data) {
          setSessions(data)
          if (!activeSessionId && data.length > 0) {
            setActiveSessionId(data[0].id)
          }
        }
      })
      .catch(() => {})
  }, [agentId, workspaceId, activeSessionId])

  useEffect(() => {
    if (!activeSessionId) {
      setActiveSessionId(crypto.randomUUID())
    }
  }, [activeSessionId])

  const currentSession = sessions.find((s) => s.id === activeSessionId)
  const handleNewSession = useCallback(() => {
    setActiveSessionId(crypto.randomUUID())
    setShowSessionList(false)
  }, [])

  const handleSelectSession = useCallback((id: string) => {
    setActiveSessionId(id)
    setShowSessionList(false)
  }, [])

  return (
    <div className="flex flex-col h-full">
      {/* Session selector bar with metadata */}
      <div className="flex flex-wrap items-center gap-2 border-b px-4 sm:px-6 py-2 bg-muted/30 shrink-0">
        <span className="text-xs text-muted-foreground">Session:</span>
        <div className="relative">
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5 text-xs"
            onClick={() => setShowSessionList(!showSessionList)}
          >
            {currentSession
              ? `#${sessions.indexOf(currentSession) + 1} — "${currentSession.title ?? "Untitled"}" ${currentSession.status === "ACTIVE" ? "(active)" : ""}`
              : "New Session"
            }
            <ChevronDown className="h-3 w-3" />
          </Button>
          {showSessionList && (
            <div className="absolute top-full left-0 mt-1 w-72 bg-background border rounded-md shadow-lg z-50 py-1">
              {sessions.map((s, i) => (
                <button
                  key={s.id}
                  className="w-full text-left px-3 py-2 text-xs hover:bg-muted/50 flex items-center gap-2"
                  onClick={() => handleSelectSession(s.id)}
                >
                  <span className="text-muted-foreground font-mono">#{sessions.length - i}</span>
                  <span className="truncate flex-1">{s.title ?? "Untitled"}</span>
                  {s.status === "ACTIVE" && (
                    <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse shrink-0" />
                  )}
                  <span className="text-muted-foreground">{s.message_count} msgs</span>
                </button>
              ))}
              {sessions.length === 0 && (
                <p className="px-3 py-2 text-xs text-muted-foreground">No sessions yet</p>
              )}
            </div>
          )}
        </div>
        <Button variant="outline" size="sm" className="gap-1.5 text-xs" onClick={handleNewSession}>
          <Plus className="h-3 w-3" /> New Session
        </Button>

        {/* Agent metadata */}
        <div className="hidden sm:flex items-center gap-4 ml-auto text-xs text-muted-foreground">
          {agent && (
            <>
              <span>CLI: <strong className="text-foreground">{agent.cli_adapter.replace("_", " ")}</strong></span>
              {agent.llm_model && (
                <span>Model: <strong className="text-foreground">{agent.llm_model}</strong></span>
              )}
              <span>Profile: <strong className="text-foreground">{agent.tool_profile}</strong></span>
              {currentSession && (
                <span>{currentSession.message_count} messages</span>
              )}
            </>
          )}
        </div>
      </div>

      {/* Backend info banner */}
      {typeof window !== "undefined" && !process.env.NEXT_PUBLIC_WS_URL && (
        <div className="mx-4 sm:mx-6 mt-2 flex items-center gap-2 rounded-md bg-muted/10 border border-border px-3 py-2 shrink-0">
          <Info className="h-4 w-4 text-muted-foreground shrink-0" />
          <p className="text-xs text-muted-foreground">
            Set <code>CREWSHIPD_URL</code> and run <strong>crewshipd</strong> for live chat.
          </p>
        </div>
      )}

      {/* Chat panel with split view */}
      <div className="flex-1 overflow-hidden">
        <ChatPanel
          agentId={agentId}
          sessionId={activeSessionId}
          agentName={agent?.name}
        />
      </div>
    </div>
  )
}
