"use client"

import { useParams, useSearchParams } from "next/navigation"
import { useState, useEffect, useCallback, useRef } from "react"
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

/** Agent chat page with session selector and split-view chat panel. */
export function ChatPageClient() {
  const params = useParams<{ agentId: string }>()
  const agentId = params.agentId
  const searchParams = useSearchParams()
  const sessionParam = searchParams.get("session") ?? undefined
  const wsParam = searchParams.get("workspace_id") ?? undefined
  const { workspaceId: storeWorkspaceId } = useWorkspace()
  const workspaceId = wsParam ?? storeWorkspaceId

  const [agent, setAgent] = useState<AgentInfo | null>(null)
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string>(sessionParam ?? "")
  const [showSessionList, setShowSessionList] = useState(false)
  const [sessionsLoaded, setSessionsLoaded] = useState(false)
  const dropdownRef = useRef<HTMLDivElement>(null)

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

  const refreshSessions = useCallback(async () => {
    if (!workspaceId) return
    try {
      const r = await fetch(`/api/v1/agents/${agentId}/chats?workspace_id=${workspaceId}`)
      if (!r.ok) return
      const data: SessionInfo[] | null = await r.json()
      if (data) setSessions(data)
    } catch { /* ignore */ }
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
        setSessionsLoaded(true)
      })
      .catch(() => {
        setSessionsLoaded(true)
      })
  // eslint-disable-next-line react-hooks/exhaustive-deps -- activeSessionId intentionally excluded to avoid refetch loop
  }, [agentId, workspaceId])

  useEffect(() => {
    if (sessionsLoaded && !activeSessionId) {
      setActiveSessionId(crypto.randomUUID())
    }
  }, [sessionsLoaded, activeSessionId])

  // Periodically refresh sessions to pick up newly created ones
  useEffect(() => {
    if (!sessionsLoaded || !workspaceId) return
    const interval = setInterval(refreshSessions, 5000)
    return () => clearInterval(interval)
  }, [sessionsLoaded, workspaceId, refreshSessions])

  // Close dropdown on click outside or Escape
  useEffect(() => {
    if (!showSessionList) return
    const handleClick = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setShowSessionList(false)
      }
    }
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setShowSessionList(false)
    }
    document.addEventListener("mousedown", handleClick)
    document.addEventListener("keydown", handleKey)
    return () => {
      document.removeEventListener("mousedown", handleClick)
      document.removeEventListener("keydown", handleKey)
    }
  }, [showSessionList])

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
      <div className="flex flex-wrap items-center gap-2 border-b px-4 md:px-6 py-2 bg-muted/30 shrink-0">
        <span className="text-xs text-muted-foreground">Session:</span>
        <div className="relative" ref={dropdownRef}>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5 text-xs max-w-[300px]"
            onClick={() => setShowSessionList(!showSessionList)}
          >
            <span className="truncate">
              {currentSession
                ? `#${sessions.length - sessions.indexOf(currentSession)} — ${currentSession.title ?? "Untitled"}`
                : "New Session"
              }
            </span>
            <ChevronDown className={`h-3 w-3 shrink-0 transition-transform ${showSessionList ? "rotate-180" : ""}`} />
          </Button>
          {showSessionList && (
            <div className="absolute top-full left-0 mt-1 w-80 bg-background border rounded-md shadow-lg z-50 py-1 max-h-80 overflow-y-auto">
              {sessions.map((s, i) => {
                const isActive = s.id === activeSessionId
                return (
                  <button
                    key={s.id}
                    className={`w-full text-left px-3 py-2 text-xs flex items-center gap-2 ${
                      isActive ? "bg-primary/10 text-primary" : "hover:bg-muted/50"
                    }`}
                    onClick={() => handleSelectSession(s.id)}
                  >
                    <span className="text-muted-foreground font-mono shrink-0">#{sessions.length - i}</span>
                    <span className="truncate flex-1">{s.title ?? "Untitled"}</span>
                    {s.status === "ACTIVE" && (
                      <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse shrink-0" />
                    )}
                    <span className="text-muted-foreground shrink-0">{s.message_count}</span>
                  </button>
                )
              })}
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
        <div className="hidden md:flex items-center gap-4 ml-auto text-xs text-muted-foreground">
          {agent && (
            <>
              <span>CLI: <strong className="text-foreground">{agent.cli_adapter.replaceAll("_", " ")}</strong></span>
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
        <div className="mx-4 md:mx-6 mt-2 flex items-center gap-2 rounded-md bg-muted/10 border border-border px-3 py-2 shrink-0">
          <Info className="h-4 w-4 text-muted-foreground shrink-0" />
          <p className="text-xs text-muted-foreground">
            Set <code>CREWSHIPD_URL</code> and start the <strong>engine</strong> for live chat.
          </p>
        </div>
      )}

      {/* Chat panel with split view */}
      <div className="flex-1 overflow-hidden">
        {activeSessionId ? (
          <ChatPanel
            agentId={agentId}
            sessionId={activeSessionId}
            agentName={agent?.name}
          />
        ) : (
          <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
            Loading session...
          </div>
        )}
      </div>
    </div>
  )
}
