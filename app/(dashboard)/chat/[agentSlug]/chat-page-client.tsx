"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
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
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useComposerStore } from "@/stores/composer-store"
import { deriveSessionTitle } from "@/lib/chat-title"
import { cn } from "@/lib/utils"
import { ChatPanel } from "@/components/features/chat/chat-panel"
import { SessionsSidebar } from "@/components/features/chat/sessions-sidebar"
import { withActiveSessionRead } from "@/components/features/chat/session-sort"
import {
  ChatTreeSidebar,
  useChatCompactLayout,
  useChatTreeData,
  type ChatTreeAgent,
  type ChatTreeThread,
} from "@/components/features/chat/chat-tree-sidebar"
import { httpError, scopeErrorMessage, useRetry } from "@/components/features/chat/scope-fetch"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { apiFetch } from "@/lib/api-fetch"
import { emitChatEvent } from "@/lib/telemetry"

/**
 * Read the agent slug from the live URL after client hydration, and let the
 * page write it back.
 *
 * useParams() is unreliable in Next.js static export: the page is
 * prerendered with [{ agentSlug: "_" }] and useParams returns "_"
 * persistently for the prerendered file, even after the user navigates
 * to /chat/<real-slug>. Pulling from window.location.pathname instead
 * bypasses that bug and guarantees we see the actual URL.
 *
 * Returns null until client mount completes — page renders a loading
 * state during that brief window.
 *
 * The setter is what makes an in-place agent swap possible. It is the same
 * trade this page already makes for the session (see `selectSession`): the
 * router is the thing that remounts the dashboard chrome on a static-export
 * build, so the page owns the URL and re-reads it on popstate rather than
 * asking Next.js to re-resolve a route param. Back/forward across a swap is
 * why the listener lives here and not only beside the session's.
 */
function useAgentSlugFromUrl(): [string | null, (slug: string) => void] {
  const [slug, setSlug] = useState<string | null>(null)
  useEffect(() => {
    if (typeof window === "undefined") return
    const read = () => {
      const m = window.location.pathname.match(/^\/chat\/([^/]+)\/?$/)
      if (m) setSlug(decodeURIComponent(m[1]))
    }
    read()
    window.addEventListener("popstate", read)
    return () => window.removeEventListener("popstate", read)
  }, [])
  return [slug, setSlug]
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

interface AgentRecord extends ChatTreeAgent {
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
  /** The agent's questionnaire forms — `agents.ask_forms`, a TEXT column
   *  holding a JSON array. On the list response for the same reason
   *  `suggested_prompts` is: this page resolves its agent by slug out of that
   *  list and never fetches the detail, and the chat below it would otherwise
   *  spend a request per mount on this one column. null for every agent nobody
   *  configured, which is what makes the rail render plain suggestion chips. */
  ask_forms?: string | null
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

/** The list row shape this page keeps in state — same rows the tree shows. */
type SessionRecord = ChatTreeThread & { ended_at: string | null }

/**
 * Full-page chat at `/chat/[agentSlug]`. Layout:
 *
 *   ┌─ TopBar (global) ────────────────────────────────────────┐
 *   ├─ Header strip (back · agent identity) ───────────────────┤
 *   ├─ Agent tree │ ChatPanel ─────────────────────────────────┤
 *   └──────────────────────────────────────────────────────────┘
 *
 * The left column is the SHARED ChatTreeSidebar, the same one `/chat` renders,
 * so the surface has one left column rather than one per route. What is
 * selected in it is a conversation — the centre is always the conversation.
 *
 * Here it renders UNNARROWED: every agent, with this one selected and open.
 * It briefly narrowed to the agent in the URL, and clicking an agent was what
 * put it there — so every pick was a route change, and the dashboard chrome
 * tore down and rebuilt to look at a different name. Narrowing is now a local
 * filter inside the tree (chat-tree-sidebar), which costs a render, and this
 * page passes `activeAgentSlug` for selection only.
 *
 * **This page no longer calls the router at all.** Two things the column
 * reports that it answers, and neither is a navigation:
 *
 *  · **a thread was picked** — this agent's is `selectSession` (local state +
 *    history.pushState). Another agent's is the same swap as below, carrying
 *    the thread.
 *  · **start a conversation** — offered under a filtered agent with nothing to
 *    open. Another agent is `swapAgent`: push the URL, move the local slug,
 *    re-resolve the agent out of the roster already in memory, let the panel
 *    follow. No `?session=`, because which conversation opens is decided once,
 *    in openInitialSession. Nothing POSTs: a click is not a message.
 *
 * It briefly wasn't. The tree gave each agent four folders and three of them
 * (Files, Asks, Memory) replaced ChatPanel with a pane that duplicated a
 * surface elsewhere in the product — `Files` was on screen twice at once, in
 * this page's tree and in its own right rail. Those panes are deleted, and
 * with them `?folder=`: the page scrubs the parameter out of a bookmarked URL
 * rather than leaving dead state in the address bar.
 *
 * Two things the layout is not allowed to do:
 *
 *  · **Grow a path segment.** The static export rewrites exactly one level
 *    (internal/api/static.go) and the slug is read from
 *    window.location.pathname, so the session is `?session=`, never
 *    `/chat/<agent>/<session>`.
 *  · **Mount two panels.** ChatPanel opens a WebSocket per mount, so exactly
 *    one is on screen at a time.
 *
 * Below the mobile breakpoint the tree steps aside for the behaviour that
 * already shipped: the session drawer plus the chat/files/more tab strip.
 * A 280px column on a 390px phone is the mistake this page made once already.
 */
export function ChatPageClient() {
  const searchParams = useSearchParams()
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [slug, setSlug] = useAgentSlugFromUrl()

  // The tree's roster and its per-agent thread lists. `skipSlug` hands THIS
  // agent's threads back to the page: the page owns them (optimistic inserts,
  // auto titles, mark-read), and a second fetch of the same list would both
  // waste a request and race the state the page has already moved on from.
  const tree = useChatTreeData<AgentRecord>({ skipSlug: slug })

  // Below 900px a left column is not a column. Same branch every other layout
  // has (components/features/crews/crews-layout.tsx is the one this follows):
  // the grid collapses to a single track, the session list moves into an
  // overlay drawer reached from the header, and ChatPanel is handed the
  // `mobilePanel` prop its mobile branches have been waiting for.
  //
  // The threshold is the tree's, not the app's phone breakpoint: 240px of flat
  // list beside a conversation survived an 800px window; 280px of tree plus
  // a status section does not. ChatPanel's mobile mode is entirely
  // prop-driven (it never asks the viewport itself), so raising the number
  // here moves the whole shape together instead of producing a hybrid.
  //
  // The page opens on "chat". It is a chat page reached by tapping an agent;
  // the conversation is the thing the user came for, and files/triggers are
  // where you go after a reply, not before one.
  const isMobile = useChatCompactLayout()
  const [mobilePanel, setMobilePanel] = useState<MobilePanel>("chat")
  const [sessionsOpen, setSessionsOpen] = useState(false)

  // The tree is a desktop column; on a phone it is collapsed to the drawer
  // that already shipped, which is why this flag only ever matters on desktop.
  const [treeCollapsed, setTreeCollapsed] = useState(false)

  const [sessions, setSessions] = useState<SessionRecord[]>([])
  const [error, setError] = useState<string | null>(null)
  const [creatingSession, setCreatingSession] = useState(false)

  // The agent comes out of the SHARED roster rather than a fetch of this
  // page's own — one `GET /agents` for the page and the column it renders.
  // Ghosts are resolved here on purpose: the tree does not offer a retired
  // agent as a destination, but a link to one still has a history to show.
  const agent = useMemo(
    () => (slug ? (tree.roster?.find((a) => a.slug === slug) ?? null) : null),
    [tree.roster, slug],
  )
  const badSlug = slug === "" || slug === "_"
  const resolveError = badSlug
    ? "Could not read agent slug from URL"
    : tree.error
      ? tree.error
      : tree.roster !== null && slug !== null && !agent
        ? `Agent "${slug}" not found in workspace`
        : null
  const loadingAgent = !badSlug && !tree.error && tree.roster === null

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

  const pageUrl = useCallback((id: string | null) => {
    if (slug === null) return null
    const params = new URLSearchParams()
    if (id) params.set("session", id)
    // One path level. /chat/<agent>/<session> would be a second, and the Go
    // static handler resolves exactly one.
    const qs = params.toString()
    return `/chat/${encodeURIComponent(slug)}${qs ? `?${qs}` : ""}`
  }, [slug])

  /** Write the URL without going through the router — see the note above. */
  const pushUrl = useCallback((url: string | null) => {
    if (!url || typeof window === "undefined") return
    if (window.location.pathname + window.location.search === url) return
    // pushState (not replaceState) so back/forward can traverse the
    // selection history. The popstate listener below will sync state.
    window.history.pushState(null, "", url)
  }, [])

  const selectSession = useCallback((id: string | null) => {
    setSessionIdState(id)
    setDraftSessionId(null)
    if (slug === null) return
    pushUrl(pageUrl(id))
  }, [slug, pageUrl, pushUrl])

  // Sync from URL on back/forward (the only path that should change the
  // selection outside of selectSession, since we own URL writes).
  useEffect(() => {
    const onPop = () => {
      const params = new URLSearchParams(window.location.search)
      setSessionIdState(params.get("session"))
      setDraftSessionId(null)
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])

  // `?folder=` is dead. It selected one of the Files / Asks / Memory panes,
  // and those were deleted for duplicating the right rail, the agent config
  // tab and the agent canvas. A URL bookmarked while it worked still opens
  // the conversation — but it must not keep carrying a parameter nothing
  // reads, so it is scrubbed in place (replaceState: this is the same visit,
  // and a back button that returns to the dead URL would be a trap).
  useEffect(() => {
    if (slug === null || typeof window === "undefined") return
    const params = new URLSearchParams(window.location.search)
    if (!params.has("folder")) return
    params.delete("folder")
    const qs = params.toString()
    window.history.replaceState(null, "", `/chat/${encodeURIComponent(slug)}${qs ? `?${qs}` : ""}`)
  }, [slug])

  // Pull recent sessions for the sidebar. `sessionsLoaded` gates the
  // ensure-session effect below so it can decide whether to reuse the
  // freshest existing session or create a new one — without it,
  // ensureSession used to fire before the GET resolved and unconditionally
  // POST'd a new chat, piling up empty "Untitled session" rows on every
  // visit (the sidebar would show 17+ stale entries within an hour).
  const [sessionsLoaded, setSessionsLoaded] = useState(false)
  // Why the list is missing, when it is. Same rule the tree's fan-out now
  // follows: a failed fetch is not an empty history, and this page owns the
  // active agent's list, so it owns the failure too.
  const [sessionsError, setSessionsError] = useState<string | null>(null)
  const { nonce: sessionsNonce, retry: retrySessions } = useRetry()
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
      .then((r) => {
        if (!r.ok) throw httpError(r.status)
        return r.json()
      })
      .then((list: SessionRecord[]) => {
        if (cancelled) return
        // The active session is read by definition — this GET can race
        // the mark-read PUT (GET served first → stale unread lands here).
        if (Array.isArray(list)) setSessions(withActiveSessionRead(list, sessionIdRef.current))
        setSessionsError(null)
        setSessionsLoaded(true)
      })
      .catch((e: unknown) => {
        if (cancelled) return
        // `sessionsLoaded` still flips: the composer must work even when the
        // history could not be read. What must NOT happen is the sidebar
        // reporting the failure as "no conversations".
        setSessionsError(scopeErrorMessage(e))
        setSessionsLoaded(true)
      })
    return () => { cancelled = true }
  }, [agent, workspaceId, sessionsNonce])

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
      // The third door: arriving with ?prompt= mints a session of its own
      // (routine-create-dialog sends people here). Counting only the button
      // and the composer would make every handoff conversation look like it
      // started nowhere.
      emitChatEvent("chat_session_created", {
        session_id: created.id,
        agent_id: agent.id,
        source: "deeplink",
      })
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
        // A name was written, and nobody typed it. The name itself is derived
        // from the first message, so it is content and it is not here — the
        // event says `auto` and stops. Emitted only once the server has
        // ACCEPTED the title: a refused PATCH leaves the session untitled, and
        // a metric that disagrees with the sidebar the user is looking at is
        // worse than no metric.
        emitChatEvent("chat_session_titled", { session_id: sid, source: "auto" })
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
    const url = pageUrl(sid)
    if (url && typeof window !== "undefined" && window.location.pathname + window.location.search !== url) {
      window.history.replaceState(null, "", url)
    }
  }, [draftSessionId, pageUrl, autoTitleSession])

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
      // Somebody asked for a fresh conversation outright, rather than a draft
      // becoming one because they typed into it (chat-panel.tsx, `composer`).
      // Which door gets used is the question the pair answers, so both fire —
      // and neither fires for a create the server refused, which is why this
      // sits after the throw above.
      emitChatEvent("chat_session_created", {
        session_id: created.id,
        agent_id: agent.id,
        source: "sidebar",
      })
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

  /* ---------------------------------------------------------- the tree */

  // What the column draws: the shared roster (ghosts already dropped) with
  // THIS agent's threads coming from the page's own state, so an optimistic
  // insert, a rename, or a mark-read shows up in the tree the moment it
  // happens rather than on the next fetch.
  const treeAgents = tree.agents ?? (agent ? [agent] : [])
  const treeThreads = useMemo(
    () => (agent ? { ...tree.threadsByAgent, [agent.id]: sessions } : tree.threadsByAgent),
    [tree.threadsByAgent, agent, sessions],
  )
  // …and the same for the failures: this agent's comes from the page's own
  // fetch, every other agent's from the fan-out.
  const treeErrors = useMemo(
    () =>
      agent && sessionsError
        ? { ...tree.threadErrors, [agent.id]: sessionsError }
        : tree.threadErrors,
    [tree.threadErrors, agent, sessionsError],
  )
  const treeRetryThreads = tree.retryThreads
  const retryTreeThreads = useCallback(() => {
    retrySessions()
    treeRetryThreads()
  }, [retrySessions, treeRetryThreads])

  /**
   * Swap the agent this page is about, WITHOUT a navigation.
   *
   * The slug is a path segment, so this used to be `router.push` — and a
   * router-driven param change re-evaluates the whole layout subtree, which
   * visibly remounts the topbar, the left rail and the dashboard chrome on
   * production static-export builds. That is the same defect `selectSession`
   * already routes around for the session, and the fix is the same one: write
   * the URL with `history.pushState`, move the local state, and let React
   * re-render in place. `popstate` (in `useAgentSlugFromUrl` and beside the
   * session, above) is what puts back/forward back in the loop.
   *
   * The agent itself is re-resolved from `tree.roster`, which is already in
   * memory — the swap costs no request to find out who we just moved to.
   *
   * Everything cleared here is about the agent being left: its sessions, the
   * failure of its list, the draft it was sitting on. `sessionId` is set to
   * the thread being opened, or null so `openInitialSession` decides — the one
   * place that decision is ever made.
   */
  const swapAgent = useCallback((nextSlug: string, threadId: string | null = null) => {
    if (nextSlug === slug) return
    const qs = threadId ? `?session=${encodeURIComponent(threadId)}` : ""
    const url = `/chat/${encodeURIComponent(nextSlug)}${qs}`
    setSlug(nextSlug)
    setSessionIdState(threadId)
    setDraftSessionId(null)
    setSessions([])
    setSessionsLoaded(false)
    setSessionsError(null)
    setError(null)
    setAuthoringSession(null)
    // A `?prompt=` handoff belongs to the agent it was addressed to. Leaving
    // it armed would auto-send somebody else's goal into this conversation.
    promptConsumedRef.current = true
    if (
      typeof window !== "undefined" &&
      window.location.pathname + window.location.search !== url
    ) {
      // pushState, not replaceState: this is a new place, and Back should
      // return to the agent the reader came from.
      window.history.pushState(null, "", url)
    }
  }, [slug, setSlug])

  // Selecting inside this agent is local state plus a history write; another
  // agent's thread is the swap above, carrying the thread so the page lands on
  // the conversation that was actually clicked.
  // `owner` is the agent the thread is filed under, not "the thread's agent"
  // (PRD §7).
  const handleOpenThread = useCallback((owner: ChatTreeAgent, threadId: string) => {
    if (owner.slug !== slug) {
      swapAgent(owner.slug, threadId)
      return
    }
    selectSession(threadId)
  }, [slug, swapAgent, selectSession])

  /**
   * "Start a conversation with this agent" — the tree's own row, offered under
   * a filtered agent that has no threads. The agent row itself is a filter now
   * and reports nothing.
   *
   * Another agent swaps in place, with no `?session=`: which conversation
   * opens is `openInitialSession`'s decision, made once.
   *
   * The agent already open needs nothing done to it. The row only appears when
   * it has no conversations, and a page with no conversations is already
   * sitting on the draft that the first message will create — re-minting one
   * would throw away whatever is in the composer.
   */
  const handleStartConversation = useCallback((picked: ChatTreeAgent) => {
    if (picked.slug !== slug) swapAgent(picked.slug)
  }, [slug, swapAgent])

  // Wait for client mount + workspace + roster before rendering chat.
  if (slug === null || wsLoading || loadingAgent) {
    return (
      <div className="h-full p-6">
        <Skeleton className="w-full h-full rounded-xl" />
      </div>
    )
  }
  const openError = error ?? resolveError
  if (openError || !agent) {
    return (
      <div className="h-full flex flex-col items-center justify-center gap-3 p-6 text-center">
        <p className="text-sm text-destructive">Could not open chat</p>
        <p className="text-xs text-muted-foreground max-w-sm">{openError}</p>
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
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : treeCollapsed
              // The kit's collapsed rail. It stays in flow so the expand
              // button never moves.
              ? "44px 1fr"
              // 280px is the shared sidebar width (SIDEBAR_WIDTH). The 240px
              // this used to be was this page's own number and nobody else's.
              : "280px 1fr",
        }}
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
          <ChatTreeSidebar
            // The grid track already sizes this column; w-full keeps the
            // aside from fighting it at the collapsed width.
            className="w-full"
            agents={treeAgents}
            threadsByAgent={treeThreads}
            threadErrors={treeErrors}
            onRetryThreads={retryTreeThreads}
            loading={!tree.threadsLoaded || !sessionsLoaded}
            // Selection and auto-expand only. Naming the agent in the URL used
            // to narrow the column to it; narrowing is the tree's own filter
            // now, and it is off until the reader turns it on.
            activeAgentSlug={slug}
            activeThreadId={sessionId}
            onOpenThread={handleOpenThread}
            onOpenAgent={handleStartConversation}
            collapsed={treeCollapsed}
            onToggleCollapsed={() => setTreeCollapsed((v) => !v)}
          />
        )}
        <div className="min-w-0 min-h-0 overflow-hidden">
          {sessionId ? (
            <ChatPanel
              // Keyed on the AGENT, never on the session. A session swap has
              // always been a prop change (the panel is keyed on sessionId
              // internally); an agent swap is a different conversation
              // surface, and now that it happens in place rather than through
              // a route change, the key is what guarantees it starts clean
              // instead of inheriting the previous agent's panel state.
              key={agent.id}
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
              // Same record, same trip. `null` says "this agent has no forms",
              // which is an answer — leaving the prop off would say "I don't
              // know", and the panel would go and ask the server.
              askForms={agent.ask_forms ?? null}
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
