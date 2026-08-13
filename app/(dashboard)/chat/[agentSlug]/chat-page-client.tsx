"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"
import Link from "next/link"
import { AnimatePresence, motion } from "motion/react"
import {
  ChevronLeft, FolderOpen, MessageSquare, MessageSquarePlus, Menu, MoreVertical,
  SlidersHorizontal, Trash2, RotateCcw, Settings as SettingsIcon,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
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
import { useIsMobile } from "@/hooks/use-mobile"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useComposerStore } from "@/stores/composer-store"
import { deriveSessionTitle } from "@/lib/chat-title"
import { cn } from "@/lib/utils"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { SessionsSidebar } from "@/components/features/chat/sessions-sidebar"
import { withActiveSessionRead } from "@/components/features/chat/session-sort"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"

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

/**
 * Id for a chat that the user has opened but not yet written into.
 *
 * The server accepts a client-supplied `session_id` on
 * POST /agents/{id}/chats and inserts OR IGNOREs on it
 * (internal/api/agent_chats.go), so a locally minted id is a promise the
 * first send can redeem — nothing exists in the database until then. That is
 * what lets this page hand the composer something to write into without
 * creating a row merely because someone opened a URL.
 *
 * crypto.randomUUID is unavailable in non-secure (HTTP) contexts, which is
 * how the dev clones are reached; same fallback as hooks/use-chat.ts.
 */
function newDraftSessionId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID()
  }
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (c) => {
    const r = (Math.random() * 16) | 0
    return (c === "x" ? r : (r & 0x3) | 0x8).toString(16)
  })
}

interface AgentRecord {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  avatar_seed: string | null
  avatar_style: string | null
  /** Stored avatar render (#1297); null means generate from the seed. */
  avatar_url?: string | null
  /** The agent's own chat suggestions — `agents.suggested_prompts`, one per
   *  line (PRD Step 7). GET /agents returns it on the list response
   *  precisely because this page resolves its agent by slug out of that list
   *  and never fetches the detail. null for every agent nobody configured,
   *  which is what makes lib/agent-suggestions.ts fall back to the role pack. */
  suggested_prompts?: string | null
  crew?: { name: string; slug: string; avatar_style: string | null } | null
}

/** Which full-screen panel the phone is showing. ChatPanel has had these
 *  branches since it was written; nothing passed the prop until now. */
type MobilePanel = "chat" | "files" | "more"

const MOBILE_PANELS: { id: MobilePanel; label: string; icon: typeof MessageSquare }[] = [
  { id: "chat", label: "Chat", icon: MessageSquare },
  { id: "files", label: "Files", icon: FolderOpen },
  { id: "more", label: "More", icon: SlidersHorizontal },
]

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
  /** Bumped on every message append (migration v130); drives sidebar order. */
  last_activity_at?: string | null
  /** Per-user unread messages in this session (own messages excluded). */
  unread_count?: number
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

  // Below 768px the 240px session column is not a column. Same branch every
  // other layout has (components/features/crews/crews-layout.tsx is the one
  // this follows): the grid collapses to a single track, the list moves into
  // an overlay drawer reached from the header, and ChatPanel is handed the
  // `mobilePanel` prop its mobile branches have been waiting for.
  //
  // The page opens on "chat". It is a chat page reached by tapping an agent;
  // the conversation is the thing the user came for, and files/triggers are
  // where you go after a reply, not before one.
  const isMobile = useIsMobile()
  const [mobilePanel, setMobilePanel] = useState<MobilePanel>("chat")
  const [sessionsOpen, setSessionsOpen] = useState(false)

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

  // Describe-first authoring handoff: `/chat/<lead>?prompt=<goal>` opens a
  // FRESH session and auto-sends the prompt (e.g. "Author a routine for
  // me: …"). `promptConsumedRef` makes it one-shot; `authoringSession`
  // scopes the auto-send to exactly the session we create for it so it
  // never re-fires on an unrelated session.
  const initialPrompt = searchParams.get("prompt")
  const promptConsumedRef = useRef(false)
  const [authoringSession, setAuthoringSession] = useState<{ id: string; prompt: string } | null>(null)

  // Id of the session this page minted locally and that does not exist on the
  // server yet. Null once it has been sent into (and therefore created), or
  // when the active session came from the URL or the sidebar.
  const [draftSessionId, setDraftSessionId] = useState<string | null>(null)
  const isDraftSession = sessionId !== null && sessionId === draftSessionId

  const sessionUrl = useCallback((id: string | null) => {
    if (slug === null) return null
    return id
      ? `/chat/${encodeURIComponent(slug)}?session=${encodeURIComponent(id)}`
      : `/chat/${encodeURIComponent(slug)}`
  }, [slug])

  const selectSession = useCallback((id: string | null) => {
    setSessionIdState(id)
    setDraftSessionId(null)
    if (typeof window === "undefined" || slug === null) return
    const url = sessionUrl(id)
    if (url && window.location.pathname + window.location.search !== url) {
      // pushState (not replaceState) so back/forward can traverse the
      // session history. The popstate listener below will sync state.
      window.history.pushState(null, "", url)
    }
  }, [slug, sessionUrl])

  // Sync from URL on back/forward (the only path that should change sessionId
  // outside of selectSession, since we now own URL writes ourselves).
  useEffect(() => {
    const onPop = () => {
      const params = new URLSearchParams(window.location.search)
      setSessionIdState(params.get("session"))
      setDraftSessionId(null)
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
    apiFetch(`/api/v1/agents?workspace_id=${workspaceId}`)
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
  // Live mirror of sessionId for fetch callbacks whose effects don't (and
  // shouldn't) re-run on session swaps — they need "the active session at
  // response time" to zero its unread count, not a stale closure value.
  const sessionIdRef = useRef(sessionId)
  useEffect(() => { sessionIdRef.current = sessionId }, [sessionId])
  useEffect(() => {
    if (!agent || !workspaceId) return
    let cancelled = false
    setSessionsLoaded(false)
    apiFetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=20`)
      .then((r) => (r.ok ? r.json() : []))
      .then((list: SessionRecord[]) => {
        if (!cancelled && Array.isArray(list)) {
          // The active session is read by definition — this GET can race
          // the mark-read PUT (GET served first → stale unread lands here).
          setSessions(withActiveSessionRead(list, sessionIdRef.current))
          setSessionsLoaded(true)
        }
      })
      .catch(() => { if (!cancelled) setSessionsLoaded(true) })
    return () => { cancelled = true }
  }, [agent, workspaceId])

  // Mark a session read: advances the server-side read cursor (unread
  // badge source, migration v130) and clears the paired "agent replied"
  // inbox notification. Fire-and-forget — a failed call just leaves the
  // badge until the next visit. Local state zeroes immediately so the
  // sidebar badge never lags.
  const markSessionRead = useCallback((sid: string) => {
    if (!agent || !workspaceId || !sid) return
    setSessions((prev) =>
      prev.map((s) => (s.id === sid && s.unread_count ? { ...s, unread_count: 0 } : s)),
    )
    apiFetch(
      `/api/v1/agents/${agent.id}/chats/${encodeURIComponent(sid)}/read?workspace_id=${workspaceId}`,
      { method: "PUT" },
    ).catch(() => {
      /* non-fatal: cursor advances on the next successful visit */
    })
  }, [agent, workspaceId])

  // Fires on selection/view change… but never for a draft: the server has no
  // row to advance a read cursor on, so the PUT would 404 on every visit to a
  // fresh agent. It re-fires with a real id the moment the draft is sent into.
  useEffect(() => {
    if (sessionId && !isDraftSession) markSessionRead(sessionId)
  }, [sessionId, isDraftSession, markSessionRead])

  // …and again when a watched reply settles (ChatPanel's isStreaming
  // true→false). The server counted the just-persisted reply as unread
  // after our selection-time cursor; without this re-fire the session
  // grows a phantom badge the moment the user switches away.
  const handleReplySettled = useCallback((sid: string) => {
    markSessionRead(sid)
  }, [markSessionRead])

  // Decide what the page opens on when the URL named no session. It does NOT
  // create one: arriving is not sending, and this effect used to POST a chat
  // for every mount that found no existing session — which, once chat is one
  // click from everywhere, means a new "Untitled session" per stray click.
  //
  //   · existing sessions      → open the freshest (unchanged)
  //   · nothing yet            → mint a draft id locally and let ChatPanel's
  //                              own ensureSession() create it on first send
  //   · ?prompt= handoff       → still POSTs up front. routine-create-dialog
  //                              sends the user here with a goal that
  //                              auto-sends, and it must land in a clean
  //                              session of its own even when others exist.
  const openInitialSession = useCallback(async () => {
    if (!agent || !workspaceId || !slug || sessionId || creatingSession || !sessionsLoaded) return
    // A pending authoring prompt always wants a clean, fresh session —
    // never reuse an existing conversation for it.
    const promptText = !promptConsumedRef.current ? initialPrompt : null
    if (!promptText) {
      if (sessions.length > 0) {
        // /chats?limit=20 returns sorted desc by created_at, so [0] is freshest.
        selectSession(sessions[0].id)
        return
      }
      // Straight to the composer on an id nothing has been written to yet.
      // Deliberately not selectSession(): the URL must stay clean, because a
      // draft id in the address bar would survive a reload and point at a
      // conversation that was never created.
      const draft = newDraftSessionId()
      setDraftSessionId(draft)
      setSessionIdState(draft)
      return
    }
    setCreatingSession(true)
    try {
      const res = await apiFetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}`, {
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
      promptConsumedRef.current = true
      setAuthoringSession({ id: created.id, prompt: promptText })
      selectSession(created.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setCreatingSession(false)
    }
  }, [agent, workspaceId, sessionId, creatingSession, sessionsLoaded, sessions, slug, selectSession, initialPrompt])

  useEffect(() => {
    if (agent && !sessionId && !creatingSession && sessionsLoaded) void openInitialSession()
  }, [agent, sessionId, creatingSession, sessionsLoaded, openInitialSession])

  // Live mirror of the session list for callbacks that must read "the title as
  // it is right now" without being re-created on every list write (and without
  // capturing a stale snapshot of one).
  const sessionsRef = useRef(sessions)
  useEffect(() => { sessionsRef.current = sessions }, [sessions])

  // Sessions this page has already auto-titled. The list itself is the primary
  // guard (a session with a title is never touched), but the write is
  // asynchronous: two sends in quick succession would both read a null title
  // and fire two PATCHes. This closes that window, and makes "fire once" a
  // property of the page rather than a race.
  const autoTitledRef = useRef<Set<string>>(new Set())

  /**
   * Name a session after its first message (PRD Step 2).
   *
   * Called from onSend, which every path (composer, suggestion chip, ?prompt=
   * auto-send) reaches only AFTER `ensureSession()` has resolved — that POST is
   * what creates the row for a draft session, and a PATCH that overtook it
   * would 404 against a chat that does not exist yet.
   *
   * Three things this deliberately does not do:
   *   · block the send — the message is already gone by the time we are called,
   *     and the title is a follow-up request, never a precondition;
   *   · report failure — nobody needs a toast because a name they did not ask
   *     for could not be written. The session stays untitled, which is exactly
   *     where it was;
   *   · overwrite. A session that already has a title — including one the user
   *     typed themselves — is left alone, forever.
   */
  const autoTitleSession = useCallback((sid: string, text: string) => {
    if (!agent || !workspaceId || !sid) return
    if (autoTitledRef.current.has(sid)) return
    const existing = sessionsRef.current.find((s) => s.id === sid)
    if (existing?.title) return
    // Read (don't subscribe to) the composer's attachments for this session:
    // they are still in the store at onSend time — the composer clears them on
    // onSent, one step later. They are only ever consulted when the text says
    // nothing usable, which is the "here, look at this file" message whose
    // whole content is the file's name.
    const attachmentNames = (useComposerStore.getState().attachments[sid] ?? []).map((a) => a.name)
    const title = deriveSessionTitle({ text, attachmentNames })
    // Nothing usable in the message (an empty send, punctuation, a control
    // character run). An untitled session reads worse than a good name and
    // better than "…".
    if (!title) return
    autoTitledRef.current.add(sid)
    void (async () => {
      try {
        const res = await apiFetch(
          `/api/v1/agents/${agent.id}/chats/${encodeURIComponent(sid)}?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ title }),
          },
        )
        if (!res.ok) return
        const row: { title?: string | null } = await res.json()
        if (typeof row?.title !== "string" || !row.title) return
        // Render the SERVER's normalised title, not the one we derived, and
        // take only the title: the response's last_activity_at is deliberately
        // not bumped by a rename (internal/api/agent_chats_rename.go), so
        // splicing the whole row in would undo the send's activity bump and
        // drag the thread back down a sidebar ordered by it.
        setSessions((prev) => prev.map((s) => (s.id === sid ? { ...s, title: row.title! } : s)))
      } catch {
        /* silent by design — see the doc comment */
      }
    })()
  }, [agent, workspaceId])

  // Another client (or another tab) renamed a chat. The backend broadcasts the
  // new title on the workspace channel precisely so open sidebars repaint
  // instead of polling; the row is matched by id, so an event for a chat this
  // page is not showing is a no-op. Nothing is refetched, and nothing moves:
  // the rename does not carry an activity bump.
  useRealtimeEventSafe("chat_renamed", useCallback((event) => {
    const chatId = event.payload?.chat_id
    const title = event.payload?.title
    if (typeof chatId !== "string" || typeof title !== "string" || !title) return
    setSessions((prev) => prev.map((s) => (s.id === chatId ? { ...s, title } : s)))
  }, []))

  // When a message is sent, bring the (possibly brand-new) session into the
  // sidebar and float it to the top. The row's NAME is not set here: the title
  // is whatever the server stores in response to the PATCH below, so the
  // sidebar shows a title only once one really exists.
  const handleSessionSend = useCallback((sid: string, text: string) => {
    const nowIso = new Date().toISOString()
    setSessions((prev) =>
      // A draft has no row in the list yet — this send is what brought it into
      // existence, so it is inserted rather than patched.
      prev.some((s) => s.id === sid)
        ? prev.map((s) =>
            s.id === sid
              ? {
                  ...s,
                  message_count: Math.max(s.message_count, 1),
                  // Mirror the server-side activity bump so the sidebar
                  // (ordered by last activity) floats this session to the
                  // top immediately, without a refetch.
                  last_activity_at: nowIso,
                }
              : s,
          )
        : [
            {
              id: sid, title: null, status: "ACTIVE", message_count: 1,
              started_at: nowIso, ended_at: null, origin: "UI", last_activity_at: nowIso,
            },
            ...prev,
          ],
    )
    // Name it after what was just said, if it has no name yet. Fires after the
    // row exists — see autoTitleSession.
    autoTitleSession(sid, text)
    if (sid !== draftSessionId) return
    // The draft is a real chat now (ChatPanel's ensureSession POSTed it before
    // the message went out). Put it in the URL so a reload comes back to this
    // conversation — replaceState, not push: the clean /chat/<slug> entry and
    // this one are the same visit, and a back button that returns to a URL
    // which mints a *different* draft would be a trap.
    setDraftSessionId(null)
    const url = sessionUrl(sid)
    if (url && typeof window !== "undefined" && window.location.pathname + window.location.search !== url) {
      window.history.replaceState(null, "", url)
    }
  }, [draftSessionId, sessionUrl, autoTitleSession])

  // Owner-restricted: delete this agent. Confirmed via native confirm
  // (a richer Dialog variant lands later). On success the user is sent
  // back to the canvas, where the agent is no longer in the list.
  const [deleting, setDeleting] = useState(false)
  const handleDeleteAgent = useCallback(async () => {
    if (!agent || !workspaceId) return
    if (!confirm(`Delete agent "${agent.name}"?\n\nThis cannot be undone.`)) return
    setDeleting(true)
    try {
      const res = await apiFetch(`/api/v1/agents/${agent.id}?workspace_id=${workspaceId}`, {
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
      const res = await apiFetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ origin: "UI" }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const created: { id: string } = await res.json()
      // Refetch the sessions list (POST returns only {id}, not the full
      // record, so we'd otherwise show a partial entry in the sidebar).
      // Force-read the session the user is leaving AND the one being
      // created — the refetched counts predate the mark-read PUT, so the
      // just-watched session would otherwise pop a phantom unread badge.
      const listRes = await apiFetch(`/api/v1/agents/${agent.id}/chats?workspace_id=${workspaceId}&limit=20`)
      if (listRes.ok) {
        const list: SessionRecord[] = await listRes.json()
        if (Array.isArray(list)) {
          setSessions(withActiveSessionRead(withActiveSessionRead(list, sessionIdRef.current), created.id))
        }
      }
      selectSession(created.id)
    } catch (err) {
      toast.error(`Could not create session: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setCreatingSession(false)
    }
  }, [agent, workspaceId, slug, selectSession])

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
        <p className="text-sm text-destructive">Could not open chat</p>
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
        {isMobile && (
          <button
            type="button"
            onClick={() => setSessionsOpen(true)}
            className="p-1 rounded hover:bg-white/5 text-muted-foreground"
            aria-label="Sessions"
            title="Sessions"
          >
            <Menu className="h-4 w-4" />
          </button>
        )}
        <AgentAvatar
          seed={agent.avatar_seed || agent.name}
          style={agent.avatar_style || agent.crew?.avatar_style}
          agentId={agent.id}
          avatarUrl={agent.avatar_url}
          className="w-7 h-7 rounded-full"
        />
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium truncate">{agent.name}</div>
          <div className="text-[11px] text-muted-foreground truncate">
            {agent.role_title || "Agent"}
            {agent.crew && (
              <>
                {" · "}
                <Link
                  href={`/crews?crew=${encodeURIComponent(agent.crew.slug)}`}
                  className="text-purple hover:underline"
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
          aria-label="New session"
          title="New session"
        >
          <MessageSquarePlus className="h-3 w-3" />
          {/* Label drops on a phone — the header has a back arrow, a sessions
              button, the agent identity and the overflow menu to fit first. */}
          {!isMobile && "New session"}
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
            {/* One row, not two. This used to offer "Agent settings" and
                "Workspace files" as separate items pointing at
                /crews/agents/<id>/settings and /crews/agents/<id>/workspace —
                routes the selection-driven /crews redesign deleted, so both
                404'd. Configuration and the file browser now live on the same
                agent canvas, reached by slug, and this panel's own right rail
                already has a Files tab. */}
            <DropdownMenuItem asChild>
              <Link href={`/crews?agent=${encodeURIComponent(slug)}`} className="flex items-center gap-2">
                <SettingsIcon className="h-4 w-4" />
                <span>Agent settings &amp; files</span>
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
              {deleting ? <Spinner className="h-4 w-4" /> : <Trash2 className="h-4 w-4" />}
              <span>Delete agent</span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </header>

      {/* Panel switcher. The phone shows one full-screen panel at a time, so
          the chat / files / more branches ChatPanel already implements need a
          way to be reached; on desktop all three live side by side in the
          panel's own right rail and this strip would be a duplicate. */}
      {isMobile && (
        <div
          role="tablist"
          aria-label="Chat panel"
          className="h-9 shrink-0 border-b border-white/8 bg-card flex items-stretch"
        >
          {MOBILE_PANELS.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={mobilePanel === id}
              onClick={() => setMobilePanel(id)}
              className={cn(
                "flex-1 flex items-center justify-center gap-1.5 text-xs border-b-2 -mb-px",
                mobilePanel === id
                  ? "border-purple text-foreground"
                  : "border-transparent text-muted-foreground",
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {label}
            </button>
          ))}
        </div>
      )}

      <div
        data-testid="chat-layout-grid"
        className="flex-1 min-h-0 grid relative"
        style={{ gridTemplateColumns: isMobile ? "1fr" : "240px 1fr" }}
      >
        {isMobile ? (
          // Overlay drawer, not a track. Same shape as the crews explorer:
          // backdrop + spring-slid panel, and picking a session closes it,
          // because on a phone the list and the conversation are the same
          // screen and leaving the sheet open would hide what was just opened.
          <AnimatePresence>
            {sessionsOpen && (
              <>
                <motion.div
                  className="fixed inset-0 bg-black/50 z-30"
                  initial={{ opacity: 0 }}
                  animate={{ opacity: 1 }}
                  exit={{ opacity: 0 }}
                  onClick={() => setSessionsOpen(false)}
                />
                <motion.div
                  className="fixed left-0 top-0 bottom-0 w-[280px] z-40 bg-card"
                  initial={{ x: -280 }}
                  animate={{ x: 0 }}
                  exit={{ x: -280 }}
                  transition={{ type: "spring", damping: 25, stiffness: 300 }}
                >
                  <SessionsSidebar
                    sessions={sessions}
                    activeSessionId={sessionId}
                    agentSlug={slug}
                    onSelect={(id) => {
                      selectSession(id)
                      setSessionsOpen(false)
                      setMobilePanel("chat")
                    }}
                  />
                </motion.div>
              </>
            )}
          </AnimatePresence>
        ) : (
          <SessionsSidebar
            sessions={sessions}
            activeSessionId={sessionId}
            agentSlug={slug}
            onSelect={selectSession}
          />
        )}
        <div className="min-w-0 min-h-0 overflow-hidden">
          {sessionId ? (
            <ChatPanel
              agentId={agent.id}
              sessionId={sessionId}
              agentName={agent.name}
              agentSlug={agent.slug}
              // role_title, not agent_role. getSuggestions lowercases and
              // underscores whatever it is given to look up a pack
              // (lib/agent-suggestions.ts), so "Data Analyst" → data_analyst,
              // which is a pack; agent_role only ever holds AGENT or LEAD
              // (internal/api/agents.go:191) and would match nothing. This
              // prop had never been passed at all, which is why every agent in
              // the product showed the `default` chips.
              agentRole={agent.role_title}
              suggestedPrompts={agent.suggested_prompts ?? null}
              // Undefined on desktop — that is what selects ChatPanel's split
              // view; a value selects one of its full-screen mobile branches.
              mobilePanel={isMobile ? mobilePanel : undefined}
              sessionOrigin={sessions.find((s) => s.id === sessionId)?.origin ?? null}
              initialInput={authoringSession?.id === sessionId ? authoringSession.prompt : undefined}
              autoSendInitial={authoringSession?.id === sessionId}
              onSend={handleSessionSend}
              onReplySettled={handleReplySettled}
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
