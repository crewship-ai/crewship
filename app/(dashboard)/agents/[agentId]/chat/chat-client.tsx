"use client"

import { useParams, useSearchParams } from "next/navigation"
import { useState, useEffect, useCallback } from "react"
import { nanoid } from "nanoid"
import { Plus, Info, Search, User, Bot, LayoutGrid, X } from "lucide-react"
import { cn } from "@/lib/utils"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { agentTabsList } from "@/components/layout/agent-tabs"
import { useAgentDetail } from "@/hooks/use-agent-detail"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import Link from "next/link"
import { usePathname } from "next/navigation"

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
  created_by?: "human" | "agent"
}

type MobileView = "chat" | "sessions" | "files"

export function ChatPageClient() {
  const params = useParams<{ agentId: string }>()
  const agentId = params.agentId
  const pathname = usePathname()
  const searchParams = useSearchParams()
  const sessionParam = searchParams.get("session") ?? undefined
  const [prefillParam] = useState(() => searchParams.get("prefill") ?? undefined)

  useEffect(() => {
    if (prefillParam) {
      // Remove prefill from URL to avoid re-triggering on manual refresh
      const url = new URL(window.location.href)
      url.searchParams.delete("prefill")
      window.history.replaceState(null, "", url.toString())
    }
  }, [prefillParam])

  const wsParam = searchParams.get("workspace_id") ?? undefined
  const { workspaceId: storeWorkspaceId } = useWorkspace()
  const workspaceId = wsParam ?? storeWorkspaceId
  const isMobile = useIsMobile()
  const { agent: agentDetail } = useAgentDetail()

  const [agent, setAgent] = useState<AgentInfo | null>(null)
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [activeSessionId, setActiveSessionId] = useState<string>(sessionParam ?? "")
  const [sessionsLoaded, setSessionsLoaded] = useState(false)
  const [sessionFilter, setSessionFilter] = useState<"mine" | "agent">("mine")
  const [searchQuery, setSearchQuery] = useState("")
  const [mobileView, setMobileView] = useState<MobileView>("chat")
  const [agentMenuOpen, setAgentMenuOpen] = useState(false)

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
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [agentId, workspaceId])

  useEffect(() => {
    if (sessionsLoaded && !activeSessionId) {
      setActiveSessionId(nanoid())
    }
  }, [sessionsLoaded, activeSessionId])

  useEffect(() => {
    if (!sessionsLoaded || !workspaceId) return
    const interval = setInterval(refreshSessions, 5000)
    return () => clearInterval(interval)
  }, [sessionsLoaded, workspaceId, refreshSessions])

  const handleNewSession = useCallback(() => {
    setActiveSessionId(nanoid())
    setMobileView("chat")
  }, [])

  const handleSelectSession = useCallback((id: string) => {
    setActiveSessionId(id)
    setMobileView("chat")
  }, [])

  const filteredSessions = sessions.filter((s) => {
    const matchesFilter = sessionFilter === "mine"
      ? s.created_by !== "agent"
      : s.created_by === "agent"
    const matchesSearch = !searchQuery.trim() ||
      (s.title ?? "").toLowerCase().includes(searchQuery.toLowerCase())
    return matchesFilter && matchesSearch
  })

  return (
    <div className="flex flex-col md:flex-row h-full">
      {/* Mobile: [LayoutGrid] Chat | Sessions | Files bar */}
      <div className="flex items-center bg-card border-b shrink-0 md:hidden">
        <button
          className="h-10 w-10 flex items-center justify-center hover:bg-accent shrink-0 border-r"
          onClick={() => setAgentMenuOpen(true)}
          aria-label="Agent pages"
        >
          <LayoutGrid className="h-4 w-4 text-muted-foreground" />
        </button>
        {(["chat", "sessions", "files"] as const).map((tab) => (
          <button
            key={tab}
            onClick={() => setMobileView(tab)}
            className={cn(
              "flex-1 text-center pt-2.5 pb-2 text-label font-medium border-b-2 mb-[-1px] transition-colors",
              tab === mobileView
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground"
            )}
          >
            {tab.charAt(0).toUpperCase() + tab.slice(1)}
          </button>
        ))}
      </div>

      {/* Mobile: Agent sub-pages bottom sheet */}
      {isMobile && (
        <Sheet open={agentMenuOpen} onOpenChange={setAgentMenuOpen}>
          <SheetContent side="bottom" showCloseButton={false} className="rounded-t-2xl max-h-[85vh] p-0">
            <div className="w-12 h-1.5 rounded-full bg-border mx-auto mt-3 mb-1" />
            <SheetHeader className="px-4 py-2.5 border-b">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2.5">
                  {agentDetail && (
                    <img
                      src={getAgentAvatarUrl(agentDetail.avatar_seed || agentDetail.name, agentDetail.avatar_style || agentDetail.crew?.avatar_style)}
                      alt={agentDetail.name}
                      className="h-7 w-7 rounded-lg shrink-0"
                    />
                  )}
                  <SheetTitle className="text-label">{agentDetail?.name ?? agent?.name ?? "Agent"}</SheetTitle>
                </div>
                <button onClick={() => setAgentMenuOpen(false)} className="h-7 w-7 flex items-center justify-center rounded-md hover:bg-accent shrink-0">
                  <X className="h-3.5 w-3.5" />
                </button>
              </div>
            </SheetHeader>
            <div className="flex-1 overflow-y-auto py-1">
              {agentTabsList.map((tab) => {
                const basePath = `/agents/${agentId}`
                const tabPath = tab.href ? `${basePath}${tab.href}` : basePath
                const isActive = tab.href === ""
                  ? pathname === basePath
                  : pathname.startsWith(tabPath)
                return (
                  <Link
                    key={tab.href}
                    href={tabPath}
                    onClick={() => setAgentMenuOpen(false)}
                    className={cn(
                      "w-full flex items-center gap-3 px-4 py-2.5 text-body transition-colors",
                      isActive
                        ? "bg-accent text-foreground font-medium"
                        : "text-muted-foreground hover:text-foreground hover:bg-accent/50"
                    )}
                  >
                    <tab.icon className="h-4 w-4" />
                    {tab.label}
                  </Link>
                )
              })}
            </div>
          </SheetContent>
        </Sheet>
      )}

      {/* Mobile: Sessions panel */}
      {mobileView === "sessions" && (
        <div className="flex flex-col flex-1 md:hidden overflow-hidden">
          <div className="px-3 pt-3 pb-2 shrink-0">
            <div className="flex items-center">
              <button
                onClick={() => setSessionFilter("mine")}
                className={cn(
                  "flex-1 flex items-center justify-center gap-1.5 pb-2 text-micro font-medium border-b-2 transition-colors",
                  sessionFilter === "mine"
                    ? "border-foreground text-foreground"
                    : "border-transparent text-muted-foreground hover:text-foreground"
                )}
              >
                <User className="h-3 w-3" />
                Mine
              </button>
              <button
                onClick={() => setSessionFilter("agent")}
                className={cn(
                  "flex-1 flex items-center justify-center gap-1.5 pb-2 text-micro font-medium border-b-2 transition-colors",
                  sessionFilter === "agent"
                    ? "border-foreground text-foreground"
                    : "border-transparent text-muted-foreground hover:text-foreground"
                )}
              >
                <Bot className="h-3 w-3" />
                Agent
              </button>
            </div>
          </div>
          <div className="px-3 pb-2 shrink-0">
            <div className="relative">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
              <input
                className="w-full h-8 rounded-md border bg-card pl-8 pr-3 text-label outline-none placeholder:text-muted-foreground focus:ring-1 focus:ring-ring"
                placeholder="Search sessions..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
              />
            </div>
          </div>
          <div className="flex-1 overflow-y-auto px-3 py-1 space-y-1">
            {filteredSessions.map((s) => (
              <button
                key={s.id}
                onClick={() => handleSelectSession(s.id)}
                className={cn(
                  "w-full text-left px-3 py-2.5 rounded-lg text-body transition-colors flex items-center gap-2",
                  s.id === activeSessionId
                    ? "bg-card text-foreground shadow-sm border border-border"
                    : "text-muted-foreground hover:bg-card/60 hover:text-foreground"
                )}
              >
                <div className="flex-1 min-w-0">
                  <div className="truncate font-medium">{s.title ?? "Untitled"}</div>
                  <div className="text-micro text-muted-foreground mt-0.5">{s.message_count} msgs</div>
                </div>
                {s.status === "ACTIVE" && (
                  <span className="h-2 w-2 rounded-full bg-emerald-500 animate-pulse shrink-0" />
                )}
              </button>
            ))}
            {filteredSessions.length === 0 && (
              <p className="px-2 py-8 text-body text-muted-foreground text-center">
                {searchQuery ? "No matching sessions" : "No sessions yet"}
              </p>
            )}
          </div>
          <div className="p-3 border-t shrink-0">
            <button
              onClick={handleNewSession}
              className="w-full flex items-center justify-center gap-1.5 h-10 rounded-lg bg-card text-body font-medium text-muted-foreground hover:text-foreground border border-border shadow-sm transition-colors"
            >
              <Plus className="h-4 w-4" />
              New Session
            </button>
          </div>
        </div>
      )}

      {/* Mobile: Files panel -- only files, no Triggers/Team/Context tabs */}
      {mobileView === "files" && (
        <div className="flex-1 md:hidden overflow-hidden">
          <ChatPanel
            agentId={agentId}
            sessionId={activeSessionId}
            agentName={agent?.name}
            initialInput={prefillParam}
            mobilePanel="files-only"
          />
        </div>
      )}

      {/* Mobile: Chat (default) */}
      {mobileView === "chat" && (
        <div className="flex-1 flex flex-col md:hidden overflow-hidden min-w-0">
          {typeof window !== "undefined" && !process.env.NEXT_PUBLIC_WS_URL && (
            <div className="mx-3 mt-2 flex items-center gap-2 rounded-md bg-muted/10 border border-border px-3 py-2 shrink-0">
              <Info className="h-4 w-4 text-muted-foreground shrink-0" />
              <p className="text-label text-muted-foreground">
                Set <code>CREWSHIPD_URL</code> and start the <strong>engine</strong> for live chat.
              </p>
            </div>
          )}
          <div className="flex-1 overflow-hidden">
            {activeSessionId ? (
              <ChatPanel
                agentId={agentId}
                sessionId={activeSessionId}
                agentName={agent?.name}
                initialInput={prefillParam}
                mobilePanel="chat"
              />
            ) : (
              <div className="flex items-center justify-center h-full text-body text-muted-foreground">
                Loading session...
              </div>
            )}
          </div>
        </div>
      )}

      {/* Desktop: Session sidebar */}
      <div className="hidden md:flex w-52 border-r flex-col shrink-0 h-full">
        {/* Header row -- aligned h-[41px] border-b with rail + chat + right panel */}
        <div className="flex items-end h-[41px] px-3 border-b shrink-0">
          <button
            onClick={() => setSessionFilter("mine")}
            className={cn(
              "flex-1 flex items-center justify-center gap-1.5 pb-2.5 text-micro font-medium border-b-2 mb-[-1px] transition-colors",
              sessionFilter === "mine"
                ? "border-foreground text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            <User className="h-3 w-3" />
            Mine
          </button>
          <button
            onClick={() => setSessionFilter("agent")}
            className={cn(
              "flex-1 flex items-center justify-center gap-1.5 pb-2.5 text-micro font-medium border-b-2 mb-[-1px] transition-colors",
              sessionFilter === "agent"
                ? "border-foreground text-foreground"
                : "border-transparent text-muted-foreground hover:text-foreground"
            )}
          >
            <Bot className="h-3 w-3" />
            Agent
          </button>
        </div>
        {/* New session + search */}
        <div className="px-2 pt-2 pb-1 shrink-0 space-y-1.5">
          <button
            onClick={handleNewSession}
            className="w-full flex items-center justify-center gap-1.5 h-8 rounded-lg bg-card text-label font-medium text-muted-foreground hover:text-foreground border border-border shadow-sm transition-colors"
          >
            <Plus className="h-3.5 w-3.5" />
            New Session
          </button>
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
            <input
              className="w-full h-7 rounded-md border bg-card pl-8 pr-3 text-label outline-none placeholder:text-muted-foreground focus:ring-1 focus:ring-ring"
              placeholder="Search..."
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
            />
          </div>
        </div>
        {/* Session list */}
        <div className="flex-1 overflow-y-auto px-2 py-1 space-y-0.5">
          {filteredSessions.map((s) => (
            <button
              key={s.id}
              onClick={() => handleSelectSession(s.id)}
              className={cn(
                "w-full text-left px-2.5 py-2 rounded-lg text-label transition-colors flex items-center gap-2",
                s.id === activeSessionId
                  ? "bg-card text-foreground shadow-sm border border-border"
                  : "text-muted-foreground hover:bg-card/60 hover:text-foreground"
              )}
            >
              <div className="flex-1 min-w-0">
                <div className="truncate font-medium">{s.title ?? "Untitled"}</div>
                <div className="text-micro text-muted-foreground mt-0.5">{s.message_count} msgs</div>
              </div>
              {s.status === "ACTIVE" && (
                <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse shrink-0" />
              )}
            </button>
          ))}
          {filteredSessions.length === 0 && (
            <p className="px-2 py-4 text-label text-muted-foreground text-center">
              {searchQuery ? "No matching sessions" : "No sessions yet"}
            </p>
          )}
        </div>
      </div>

      {/* Desktop: Chat area */}
      <div className="hidden md:flex flex-1 flex-col overflow-hidden min-w-0">
        {typeof window !== "undefined" && !process.env.NEXT_PUBLIC_WS_URL && (
          <div className="mx-4 md:mx-6 mt-2 flex items-center gap-2 rounded-md bg-muted/10 border border-border px-3 py-2 shrink-0">
            <Info className="h-4 w-4 text-muted-foreground shrink-0" />
            <p className="text-label text-muted-foreground">
              Set <code>CREWSHIPD_URL</code> and start the <strong>engine</strong> for live chat.
            </p>
          </div>
        )}
        <div className="flex-1 overflow-hidden">
          {activeSessionId ? (
            <ChatPanel
              agentId={agentId}
              sessionId={activeSessionId}
              agentName={agent?.name}
              initialInput={prefillParam}
            />
          ) : (
            <div className="flex items-center justify-center h-full text-body text-muted-foreground">
              Loading session...
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
