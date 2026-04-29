"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useSearchParams } from "next/navigation"
import Link from "next/link"
import { ChevronLeft, MessageSquarePlus, MoreVertical, Trash2, RotateCcw, Settings as SettingsIcon, FolderOpen, Loader2 } from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { toast } from "sonner"
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
  /** Backend tag added in migration v59 — UI / CLI / WEBHOOK / CRON
   *  / AGENT. Older rows that pre-date the migration are NULL. */
  origin?: string | null
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
  const searchParams = useSearchParams()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const slug = useAgentSlugFromUrl()

  const [agent, setAgent] = useState<AgentRecord | null>(null)
  const [sessions, setSessions] = useState<SessionRecord[]>([])
  const [loadingAgent, setLoadingAgent] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [creatingSession, setCreatingSession] = useState(false)

  // Active session id is held in local state (not derived from useSearchParams)
  // so swapping sessions never goes through Next.js's router. router.replace +
  // useSearchParams forces the entire layout subtree to re-evaluate, which
  // visibly remounts the topbar / left rail / dashboard chrome on production
  // static-export builds. We update the URL via history.replaceState (no
  // router involvement) and listen for back/forward via popstate.
  const initialSessionFromUrl = searchParams.get("session")
  const [sessionId, setSessionIdState] = useState<string | null>(initialSessionFromUrl)

  const selectSession = useCallback((id: string | null) => {
    setSessionIdState(id)
    if (typeof window === "undefined" || slug === null) return
    const url = id
      ? `/chat/${encodeURIComponent(slug)}?session=${encodeURIComponent(id)}`
      : `/chat/${encodeURIComponent(slug)}`
    if (window.location.pathname + window.location.search !== url) {
      // pushState (not replaceState) so back/forward can traverse the
      // session history. The popstate listener below will sync state.
      window.history.pushState(null, "", url)
    }
  }, [slug])

  // Sync from URL on back/forward (the only path that should change sessionId
  // outside of selectSession, since we now own URL writes ourselves).
  useEffect(() => {
    const onPop = () => {
      const params = new URLSearchParams(window.location.search)
      setSessionIdState(params.get("session"))
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])

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

  // Pull recent sessions for the sidebar. `sessionsLoaded` gates the
  // ensure-session effect below so it can decide whether to reuse the
  // freshest existing session or create a new one — without it,
  // ensureSession used to fire before the GET resolved and unconditionally
  // POST'd a new chat, piling up empty "Untitled session" rows on every
  // visit (the sidebar would show 17+ stale entries within an hour).
  const [sessionsLoaded, setSessionsLoaded] = useState(false)
  useEffect(() => {
    if (!agent || !workspaceId) return
    let cancelled = false
    setSessionsLoaded(false)
    fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=20`)
      .then((r) => (r.ok ? r.json() : []))
      .then((list: SessionRecord[]) => {
        if (!cancelled && Array.isArray(list)) {
          setSessions(list)
          setSessionsLoaded(true)
        }
      })
      .catch(() => { if (!cancelled) setSessionsLoaded(true) })
    return () => { cancelled = true }
  }, [agent, workspaceId])

  // If no ?session= specified: route to the freshest existing session
  // (pre-existing chats with the agent shouldn't be replaced by a new
  // empty one). Only POST a new session when there are genuinely none.
  const ensureSession = useCallback(async () => {
    if (!agent || !workspaceId || !slug || sessionId || creatingSession || !sessionsLoaded) return
    if (sessions.length > 0) {
      // /chats?limit=20 returns sorted desc by created_at, so [0] is freshest.
      selectSession(sessions[0].id)
      return
    }
    setCreatingSession(true)
    try {
      const res = await fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ origin: "UI" }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const created: { id: string } = await res.json()
      const nowIso = new Date().toISOString()
      setSessions((prev) =>
        prev.some((s) => s.id === created.id)
          ? prev
          : [{ id: created.id, title: null, status: "ACTIVE", message_count: 0, started_at: nowIso, ended_at: null, origin: "UI" }, ...prev],
      )
      selectSession(created.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setCreatingSession(false)
    }
  }, [agent, workspaceId, sessionId, creatingSession, sessionsLoaded, sessions, slug, selectSession])

  useEffect(() => {
    if (agent && !sessionId && !creatingSession && sessionsLoaded) void ensureSession()
  }, [agent, sessionId, creatingSession, sessionsLoaded, ensureSession])

  // Owner-restricted: delete this agent. Confirmed via native confirm
  // (a richer Dialog variant lands later). On success the user is sent
  // back to the canvas, where the agent is no longer in the list.
  const [deleting, setDeleting] = useState(false)
  const handleDeleteAgent = useCallback(async () => {
    if (!agent || !workspaceId) return
    if (!confirm(`Delete agent "${agent.name}"?\n\nThis cannot be undone.`)) return
    setDeleting(true)
    try {
      const res = await fetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to delete agent" }))
        toast.error(typeof data.error === "string" ? data.error : "Failed to delete agent")
        return
      }
      toast.success("Agent deleted")
      window.location.href = "/crews"
    } catch (err) {
      toast.error(`Failed to delete: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setDeleting(false)
    }
  }, [agent, workspaceId])

  const handleNewSession = useCallback(async () => {
    if (!agent || !workspaceId || !slug) return
    setCreatingSession(true)
    try {
      const res = await fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ origin: "UI" }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const created: { id: string } = await res.json()
      // Refetch the sessions list (POST returns only {id}, not the full
      // record, so we'd otherwise show a partial entry in the sidebar).
      const listRes = await fetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=20`)
      if (listRes.ok) {
        const list: SessionRecord[] = await listRes.json()
        if (Array.isArray(list)) setSessions(list)
      }
      selectSession(created.id)
    } catch (err) {
      toast.error(`Could not create session: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setCreatingSession(false)
    }
  }, [agent, workspaceId, slug, selectSession])

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

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button
              type="button"
              className="p-1.5 rounded hover:bg-white/5 text-muted-foreground"
              title="Agent actions"
              aria-label="Agent actions"
            >
              <MoreVertical className="h-4 w-4" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-[220px]">
            <DropdownMenuLabel className="text-xs text-muted-foreground">
              {agent.name}
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem asChild>
              <Link href={`/crews/agents/${agent.id}/settings`} className="flex items-center gap-2">
                <SettingsIcon className="h-4 w-4" />
                <span>Agent settings</span>
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem asChild>
              <Link href={`/crews/agents/${agent.id}/workspace`} className="flex items-center gap-2">
                <FolderOpen className="h-4 w-4" />
                <span>Workspace files</span>
              </Link>
            </DropdownMenuItem>
            <DropdownMenuItem
              onClick={() => toast.info("Container restart will land in a follow-up")}
              className="flex items-center gap-2"
            >
              <RotateCcw className="h-4 w-4" />
              <span>Restart container</span>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={handleDeleteAgent}
              disabled={deleting}
              className="flex items-center gap-2 text-destructive focus:text-destructive focus:bg-destructive/10"
            >
              {deleting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Trash2 className="h-4 w-4" />}
              <span>Delete agent</span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </header>

      <div
        className="flex-1 min-h-0 grid"
        style={{ gridTemplateColumns: "240px 1fr" }}
      >
        <SessionsSidebar
          sessions={sessions}
          activeSessionId={sessionId}
          agentSlug={slug}
          onSelect={selectSession}
        />
        <div className="min-w-0 min-h-0 overflow-hidden">
          {sessionId ? (
            <ChatPanel
              agentId={agent.id}
              sessionId={sessionId}
              agentName={agent.name}
              agentSlug={agent.slug}
              sessionOrigin={sessions.find((s) => s.id === sessionId)?.origin ?? null}
            />
          ) : (
            <div className="h-full grid place-items-center text-xs text-muted-foreground">
              Allocating session…
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
