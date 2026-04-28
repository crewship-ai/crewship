"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import Link from "next/link"
import { ChevronLeft, MessageSquarePlus } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { SessionsSidebar } from "@/components/features/chat/sessions-sidebar"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

/**
 * Read the agent slug from the live URL after client hydration.
 *
 * useParams() is unreliable in Next.js static export: the page is
 * prerendered with [{ agentSlug: "_" }] and useParams returns "_"
 * persistently for the prerendered file, even after the user navigates
 * to /chat/<real-slug>. Pulling from window.location.pathname instead
 * bypasses that bug and guarantees we see the actual URL.
 *
 * Returns null until client mount completes — page renders a loading
 * state during that brief window.
 */
function useAgentSlugFromUrl(): string | null {
  const [slug, setSlug] = useState<string | null>(null)
  useEffect(() => {
    if (typeof window === "undefined") return
    const m = window.location.pathname.match(/^\/chat\/([^/]+)\/?$/)
    if (m) setSlug(decodeURIComponent(m[1]))
  }, [])
  return slug
}

interface AgentRecord {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  avatar_seed: string | null
  avatar_style: string | null
  crew?: { name: string; slug: string; avatar_style: string | null } | null
}

interface SessionRecord {
  id: string
  title: string | null
  status: string
  message_count: number
  started_at: string
  ended_at: string | null
}

/**
 * Full-page chat at `/chat/[agentSlug]`. Replaces the older drawer-based
 * chat that lived inside /crews. Layout:
 *
 *   ┌─ TopBar (global) ────────────────────────────────────────┐
 *   ├─ Header strip (back · agent identity) ───────────────────┤
 *   ├─ Sessions sidebar │ ChatPanel │ RightPanel ──────────────┤
 *   └──────────────────────────────────────────────────────────┘
 *
 * Reuses the existing <ChatPanel> component (composer + turn list +
 * RightPanel files/team/context) without modification.
 */
export function ChatPageClient() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const slug = useAgentSlugFromUrl()

  const [agent, setAgent] = useState<AgentRecord | null>(null)
  const [sessions, setSessions] = useState<SessionRecord[]>([])
  const [loadingAgent, setLoadingAgent] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [creatingSession, setCreatingSession] = useState(false)

  const sessionId = searchParams.get("session")

  // Resolve agent by slug (workspace-scoped).
  useEffect(() => {
    // Wait for both workspace and the post-hydration slug. Don't flip
    // loadingAgent off while we're still waiting — that would render
    // a misleading "agent not found" early.
    if (!workspaceId || slug === null) return

    if (slug === "" || slug === "_") {
      // Static-export placeholder hit the client somehow (URL rewrite
      // failed). Surface a real error rather than rendering blank.
      setLoadingAgent(false)
      setError("Could not read agent slug from URL")
      return
    }

    let cancelled = false
    setLoadingAgent(true)
    fetch(`/api/v1/agents?workspace_id=${workspaceId}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((list: AgentRecord[]) => {
        if (cancelled) return
        const found = list.find((a) => a.slug === slug)
        if (!found) {
          setError(`Agent "${slug}" not found in workspace`)
        } else {
          setAgent(found)
          setError(null)
        }
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => { if (!cancelled) setLoadingAgent(false) })
    return () => { cancelled = true }
  }, [slug, workspaceId])

  // Pull recent sessions for the sidebar.
  useEffect(() => {
    if (!agent || !workspaceId) return
    let cancelled = false
    fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=20`)
      .then((r) => (r.ok ? r.json() : []))
      .then((list: SessionRecord[]) => {
        if (!cancelled && Array.isArray(list)) setSessions(list)
      })
      .catch(() => { /* silent */ })
    return () => { cancelled = true }
  }, [agent, workspaceId])

  // If no ?session= specified, allocate one from the freshest existing
  // session OR create a new one.
  const ensureSession = useCallback(async () => {
    if (!agent || !workspaceId || !slug || sessionId || creatingSession) return
    setCreatingSession(true)
    try {
      const res = await fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode: "CHAT" }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const created: SessionRecord = await res.json()
      router.replace(`/chat/${encodeURIComponent(slug)}?session=${created.id}`)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setCreatingSession(false)
    }
  }, [agent, workspaceId, sessionId, creatingSession, slug, router])

  useEffect(() => {
    if (agent && !sessionId && !creatingSession) void ensureSession()
  }, [agent, sessionId, creatingSession, ensureSession])

  const handleNewSession = useCallback(async () => {
    if (!agent || !workspaceId || !slug) return
    setCreatingSession(true)
    try {
      const res = await fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode: "CHAT" }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const created: SessionRecord = await res.json()
      setSessions((prev) => [created, ...prev])
      router.replace(`/chat/${encodeURIComponent(slug)}?session=${created.id}`)
    } catch {
      // toast handled at the chat panel level
    } finally {
      setCreatingSession(false)
    }
  }, [agent, workspaceId, slug, router])

  const avatarSrc = useMemo(() => {
    if (!agent) return ""
    return getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)
  }, [agent])

  // Wait for client mount + workspace + agent fetch before rendering chat.
  if (slug === null || wsLoading || loadingAgent) {
    return (
      <div className="h-full p-6">
        <Skeleton className="w-full h-full rounded-xl" />
      </div>
    )
  }
  if (error || !agent) {
    return (
      <div className="h-full flex flex-col items-center justify-center gap-3 p-6 text-center">
        <p className="text-sm text-red-300">Could not open chat</p>
        <p className="text-xs text-muted-foreground max-w-sm">{error}</p>
        <Link
          href="/crews"
          className="text-xs px-3 py-1.5 rounded border border-white/10 hover:bg-white/5 text-foreground/80"
        >
          Back to /crews
        </Link>
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full bg-background">
      {/* Identity strip */}
      <header className="h-12 shrink-0 border-b border-white/8 flex items-center gap-3 px-4 bg-card">
        <Link
          href={`/crews?agent=${encodeURIComponent(slug)}`}
          className="p-1 rounded hover:bg-white/5 text-muted-foreground"
          title="Back to agent canvas"
        >
          <ChevronLeft className="h-4 w-4" />
        </Link>
        <img src={avatarSrc} alt="" className="w-7 h-7 rounded-full" />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium truncate">{agent.name}</div>
          <div className="text-[11px] text-muted-foreground truncate">
            {agent.role_title || "Agent"}
            {agent.crew && (
              <>
                {" · "}
                <Link
                  href={`/crews?crew=${encodeURIComponent(agent.crew.slug)}`}
                  className="text-fuchsia-300 hover:underline"
                >
                  {agent.crew.name}
                </Link>
              </>
            )}
          </div>
        </div>
        <button
          type="button"
          onClick={handleNewSession}
          disabled={creatingSession}
          className="text-xs px-2.5 py-1 rounded border border-white/10 hover:bg-white/5 text-foreground/80 flex items-center gap-1.5"
        >
          <MessageSquarePlus className="h-3 w-3" />
          New session
        </button>
      </header>

      <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "240px 1fr" }}>
        <SessionsSidebar
          sessions={sessions}
          activeSessionId={sessionId}
          agentSlug={slug}
        />
        <div className="min-w-0 min-h-0 overflow-hidden">
          {sessionId && (
            <ChatPanel
              key={sessionId}
              agentId={agent.id}
              sessionId={sessionId}
              agentName={agent.name}
            />
          )}
          {!sessionId && (
            <div className="h-full grid place-items-center text-xs text-muted-foreground">
              Allocating session…
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
