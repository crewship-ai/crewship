"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"
import { FolderOpen, Menu, MessageSquare, SlidersHorizontal, X } from "lucide-react"

import { ChatPanel } from "@/components/features/chat/chat-panel"
import {
  useChatCompactLayout,
  useChatTreeData,
  type ChatTreeAgent,
  type ChatTreeThread,
} from "@/components/features/chat/chat-tree-data"
import { ChatAgentProvider, type ChatAgent } from "@/components/features/chat/chat-agent-context"
import {
  applyReadOverrides,
  ConversationsSidebar,
} from "@/components/features/chat/conversations-sidebar"
import { Skeleton } from "@/components/ui/skeleton"
import { deriveSessionTitle } from "@/lib/chat-title"
import { useComposerStore } from "@/stores/composer-store"
import { emitChatEvent } from "@/lib/telemetry"
import { cn } from "@/lib/utils"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { parseSessionTimestamp } from "@/components/features/chat/session-sort"
import { randomUUIDv4 } from "@/lib/random-id"

/**
 * The agent slug, read from the address bar rather than from `params`.
 *
 * `generateStaticParams` emits one placeholder and internal/api/static.go
 * rewrites every real slug onto that single file, so the route's own params
 * are always `"_"` on a served build. The pathname is the only place the real
 * slug exists on the client.
 *
 * Listens for `popstate` so Back between two conversations re-reads it — this
 * page writes the URL with `replaceState` on every selection, and a reader who
 * arrived from a notification and pressed Back expects to go somewhere.
 */
function useAgentSlugFromUrl(): string | null {
  const [slug, setSlug] = useState<string | null>(null)
  useEffect(() => {
    if (typeof window === "undefined") return
    const read = () => {
      const m = window.location.pathname.match(/^\/chat\/([^/]+)\/?$/)
      setSlug(m ? decodeURIComponent(m[1]) : null)
    }
    read()
    window.addEventListener("popstate", read)
    return () => window.removeEventListener("popstate", read)
  }, [])
  return slug
}

/** Which full-screen panel the phone is showing. ChatPanel has had these
 *  branches since it was written. */
type MobilePanel = "chat" | "files" | "more"

const MOBILE_PANELS: { id: MobilePanel; label: string; icon: typeof MessageSquare }[] = [
  { id: "chat", label: "Chat", icon: MessageSquare },
  { id: "files", label: "Files", icon: FolderOpen },
  { id: "more", label: "More", icon: SlidersHorizontal },
]

/**
 * The chat surface.
 *
 * Reached two ways, both landing here:
 *
 *   /chat                         — pick up where you left off
 *   /chat/<agentSlug>?session=<id> — a deep link
 *
 * The second one is not optional and is why the `[agentSlug]` route still
 * exists after the surface was rewritten: `internal/chatnotify/notify.go`
 * emits that exact shape for "your agent replied" notifications, `crewship
 * open` builds it, and a dozen Links across crews/dashboard/routines point at
 * it. Deleting the route would have broken all of them silently.
 *
 * Selection past that point is query-state, never a deeper path segment. The
 * static export rewrites exactly one path level (internal/api/static.go), so
 * `/chat/<agent>/<session>` would 404 on a served build even though it works
 * under `next dev`.
 *
 * The page does not call the router for a selection either. Picking a thread
 * is `useState` plus `history.replaceState` — a route change tears down and
 * rebuilds the dashboard chrome to look at a different name, and the reader
 * feels every one of those.
 */
export function ChatClient() {
  const searchParams = useSearchParams()
  const pathAgentSlug = useAgentSlugFromUrl()
  const tree = useChatTreeData<ChatTreeAgent>()

  const { workspaceId } = useWorkspace()
  const isMobile = useChatCompactLayout()
  const [mobilePanel, setMobilePanel] = useState<MobilePanel>("chat")
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [agentSlug, setAgentSlug] = useState<string | null>(null)
  const [sessionId, setSessionId] = useState<string | null>(null)

  /**
   * When this page last marked each thread read — epoch ms, not a boolean.
   *
   * `useChatTreeData` owns `threadsByAgent` and exposes no mutator. Classic
   * gets around that by claiming one agent's threads via `skipSlug`, which a
   * page showing every agent at once cannot do, so this page overlays its own
   * knowledge on the fetched lists instead.
   *
   * A TIMESTAMP rather than a Set, and that is the fix for the second round
   * of this bug. A Set said "this thread is read" forever: open thread A,
   * move to thread B, let the agent reply in A — A is genuinely unread again,
   * the server says so, and the override kept forcing it to zero. Switching
   * between agents therefore produced exactly the reported symptom, a column
   * that no longer remembered what was unread.
   *
   * Comparing against the thread's own last activity makes the override
   * self-expiring: it suppresses the count only while the thread has not
   * moved since we read it, and the moment it does the server's number is the
   * truth again. No cleanup, no staleness.
   */
  const [readAt, setReadAt] = useState<Record<string, number>>({})

  /**
   * Live agent status, straight off the workspace event stream.
   *
   * The roster is fetched once, on workspaceId change — `retryThreads` re-runs
   * the per-agent thread fan-out and does NOT re-read `/agents`. So
   * `agent.status` was frozen at whatever it was when the page mounted, which
   * is why the Live facet never changed: it is derived from that column.
   *
   * `agent.status` is broadcast on every run start and finish
   * (internal/api/internal_runs.go), so subscribing is both cheaper and more
   * accurate than polling — the facet flips at the same moment the run does.
   */
  const [statusOverrides, setStatusOverrides] = useState<Record<string, string>>({})

  useRealtimeEventSafe("agent.status", (event) => {
    const agentId = event.payload?.agent_id
    const status = event.payload?.status
    if (typeof agentId !== "string" || typeof status !== "string") return
    setStatusOverrides((prev) => (prev[agentId] === status ? prev : { ...prev, [agentId]: status }))
  })

  // Read the URL once per navigation. After that the page owns the selection:
  // picking a thread is a `useState` plus a `history.replaceState`, never a
  // router push, for the reason the classic page documents at length — a
  // route change tears down and rebuilds the dashboard chrome to look at a
  // different name, and the user feels every one of those.
  useEffect(() => {
    // The path segment is authoritative when there is one: `/chat/riley` is a
    // stronger statement about which agent you want than a leftover `?agent=`
    // in the same URL could be.
    const a = pathAgentSlug ?? searchParams.get("agent")
    const s = searchParams.get("session")
    if (a) setAgentSlug(a)
    if (s) setSessionId(s)
  }, [searchParams, pathAgentSlug])

  /**
   * The one-shot `?prompt=` handoff.
   *
   * `routine-create-dialog` navigates to `/chat/<lead>?prompt=<goal>` and
   * expects the message to send itself — describe-first routine authoring is
   * built on it. Read ONCE into state rather than off `searchParams` every
   * render, because the panel auto-sends whatever it is handed and a value
   * that survives a re-render would send twice.
   */
  const [handoffPrompt, setHandoffPrompt] = useState<string | null>(null)
  const handoffConsumedRef = useRef(false)
  useEffect(() => {
    if (handoffConsumedRef.current) return
    const p = searchParams.get("prompt")
    if (!p) return
    handoffConsumedRef.current = true
    setHandoffPrompt(p)
  }, [searchParams])

  const agent = useMemo(
    () => tree.roster?.find((a) => a.slug === agentSlug) ?? null,
    [tree.roster, agentSlug],
  )

  /**
   * The fetched lists with this page's read overrides applied.
   *
   * Applied here rather than inside the sidebar so the column stays a pure
   * function of what it is given — it renders the counts it is handed and
   * owns no opinion about which of them are stale.
   */
  const threadsByAgent = useMemo(
    () => applyReadOverrides(tree.threadsByAgent, readAt, sessionId),
    [tree.threadsByAgent, readAt, sessionId],
  )

  // Read by autoTitleSession, which must not re-subscribe on every thread
  // fetch just to answer "does this one already have a name".
  const threadsRef = useRef(threadsByAgent)
  useEffect(() => {
    threadsRef.current = threadsByAgent
  }, [threadsByAgent])

  /** The roster with live status applied. Same overlay idea, other column. */
  const agents = useMemo(() => {
    if (!tree.agents) return tree.agents
    if (Object.keys(statusOverrides).length === 0) return tree.agents
    return tree.agents.map((a) =>
      statusOverrides[a.id] && statusOverrides[a.id] !== a.status
        ? { ...a, status: statusOverrides[a.id] }
        : a,
    )
  }, [tree.agents, statusOverrides])

  /**
   * Record the selection in the address bar without navigating.
   *
   * Writes the `/chat/<slug>?session=` shape — the one every deep link in the
   * product already uses — so a URL copied out of the bar is a URL that can be
   * pasted back in. `replaceState`, not push: a conversation switch is not a
   * page the reader should have to press Back through.
   */
  const writeUrl = useCallback((slug: string, session: string) => {
    const url = `/chat/${encodeURIComponent(slug)}?session=${encodeURIComponent(session)}`
    window.history.replaceState(null, "", url)
  }, [])

  /**
   * Advance the server-side read cursor (migration v130 — the unread badge's
   * source) and clear the paired inbox notification.
   *
   * Fire-and-forget: a failed PUT just leaves the badge until the next visit,
   * which is the same contract classic works under. The local override goes
   * in FIRST so the sidebar never lags behind the click.
   */
  const markThreadRead = useCallback(
    (agentId: string, threadId: string) => {
      if (!workspaceId || !threadId) return
      setReadAt((prev) => ({ ...prev, [threadId]: Date.now() }))
      apiFetch(
        `/api/v1/agents/${agentId}/chats/${encodeURIComponent(threadId)}/read?workspace_id=${workspaceId}`,
        { method: "PUT" },
      ).catch(() => {
        /* non-fatal: the cursor advances on the next successful visit */
      })
    },
    [workspaceId],
  )

  const selectThread = useCallback(
    (a: ChatTreeAgent, t: ChatTreeThread) => {
      setAgentSlug(a.slug)
      setSessionId(t.id)
      writeUrl(a.slug, t.id)
      markThreadRead(a.id, t.id)
    },
    [writeUrl, markThreadRead],
  )

  /**
   * Re-fire when a streamed reply settles.
   *
   * The server counts the just-persisted reply as unread against the cursor
   * we advanced at selection time, so without this the thread you are sitting
   * in grows a badge the moment you look away. Classic does the same thing
   * for the same reason.
   */
  const handleReplySettled = useCallback(
    (sid: string) => {
      if (agent) markThreadRead(agent.id, sid)
      // And refresh the fan-out, so a reply that landed in ANOTHER thread —
      // or an agent whose status has gone back to idle — is reflected in the
      // column rather than sitting on numbers fetched when the page mounted.
      tree.retryThreads()
    },
    [agent, markThreadRead, tree],
  )

  /**
   * Name a session after its first message.
   *
   * Fired from `onSend`, which every path reaches only AFTER `ensureSession()`
   * has resolved — that POST is what creates the row for a draft, and a PATCH
   * that overtook it would 404 against a chat that does not exist yet.
   *
   * Three things it deliberately does not do: block the send (the message is
   * already gone by the time we are called), report failure (nobody needs a
   * toast because a name they did not ask for could not be written), or
   * overwrite a title that already exists — including one the user typed.
   *
   * The ref is not redundant with the "already has a title" check: the write
   * is asynchronous, so two quick sends would both read a null title and fire
   * two PATCHes. It makes "once" a property of the page rather than a race.
   */
  const autoTitledRef = useRef<Set<string>>(new Set())
  const autoTitleSession = useCallback(
    (sid: string, text: string) => {
      if (!agent || !workspaceId || !sid) return
      if (autoTitledRef.current.has(sid)) return
      const existing = (threadsRef.current[agent.id] ?? []).find((t) => t.id === sid)
      if (existing?.title) return
      // Read (do not subscribe to) the composer's attachments: they are still
      // in the store at onSend time — the composer clears them one step later,
      // on onSent. Only consulted when the text says nothing usable, which is
      // the "here, look at this" message whose whole content is a file name.
      const attachmentNames = (useComposerStore.getState().attachments[sid] ?? []).map((a) => a.name)
      const title = deriveSessionTitle({ text, attachmentNames })
      // An untitled session reads worse than a good name and better than "…".
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
          // Emitted only once the server ACCEPTED the title: a metric that
          // disagrees with the sidebar the reader is looking at is worse than
          // no metric. The name is derived from the message, so it is content
          // and it does not travel — the event says `auto` and stops.
          emitChatEvent("chat_session_titled", { session_id: sid, source: "auto" })
          tree.retryThreads()
        } catch {
          /* the session stays untitled, which is where it already was */
        }
      })()
    },
    [agent, workspaceId, tree],
  )

  const handleSend = useCallback(
    (sid: string, text: string) => {
      autoTitleSession(sid, text)
    },
    [autoTitleSession],
  )

  const startConversation = useCallback(
    (a: ChatTreeAgent) => {
      // A locally minted id, not a POST. The server accepts a client-supplied
      // session_id and inserts OR IGNOREs on it, so the row appears when the
      // first message is sent — opening a conversation you never write in
      // must not leave one behind. Same contract chat-page-client relies on.
      const draft = randomUUIDv4()
      setAgentSlug(a.slug)
      setSessionId(draft)
      writeUrl(a.slug, draft)
    },
    [writeUrl],
  )

  /**
   * Decide what to open when the URL did not say.
   *
   * Two arrivals, two answers, and neither of them creates a row merely
   * because somebody opened a link:
   *
   *  · `/chat` — the freshest conversation anywhere. The reader wants to pick
   *    up where they left off, and the newest thread is where that was.
   *  · `/chat/<slug>` with no `?session=` — that agent's freshest, and a
   *    DRAFT when they have none. This is the shape `crewship open <agent>`
   *    and every crews/dashboard link produce, and landing them on "pick a
   *    conversation" when they named the agent is answering a question they
   *    already answered.
   *
   * A draft id is minted locally rather than POSTed; the row appears when the
   * first message is sent.
   */
  useEffect(() => {
    if (sessionId || !tree.threadsLoaded || !agents) return

    const freshestOf = (list: ChatTreeThread[]) =>
      list.reduce<ChatTreeThread | null>((best, t) => {
        if (!best) return t
        const a = parseSessionTimestamp(t.last_activity_at ?? t.started_at)
        const b = parseSessionTimestamp(best.last_activity_at ?? best.started_at)
        return a > b ? t : best
      }, null)

    if (agentSlug) {
      const named = agents.find((a) => a.slug === agentSlug)
      if (!named) return
      const thread = freshestOf(threadsByAgent[named.id] ?? [])
      if (thread) selectThread(named, thread)
      else startConversation(named)
      return
    }

    let best: { agent: ChatTreeAgent; thread: ChatTreeThread; at: number } | null = null
    for (const a of agents) {
      for (const t of threadsByAgent[a.id] ?? []) {
        const at = parseSessionTimestamp(t.last_activity_at ?? t.started_at)
        if (!best || at > best.at) best = { agent: a, thread: t, at }
      }
    }
    if (best) selectThread(best.agent, best.thread)
  }, [
    sessionId,
    agentSlug,
    tree.threadsLoaded,
    agents,
    threadsByAgent,
    selectThread,
    startConversation,
  ])

  const chatAgent: ChatAgent | null = useMemo(
    () =>
      agent
        ? {
            id: agent.id,
            name: agent.name,
            slug: agent.slug,
            crewId: agent.crew_id ?? null,
            avatarSeed: agent.avatar_seed ?? agent.slug,
            avatarStyle: agent.avatar_style ?? null,
            avatarUrl: agent.avatar_url ?? null,
          }
        : null,
    [agent],
  )

  const sidebar = (
    <ConversationsSidebar
      agents={agents}
      threadsByAgent={threadsByAgent}
      threadsLoaded={tree.threadsLoaded}
      activeThreadId={sessionId}
      onSelectThread={(a, t) => {
        selectThread(a, t)
        setDrawerOpen(false)
      }}
      onStartConversation={(a) => {
        startConversation(a)
        setDrawerOpen(false)
      }}
    />
  )

  const conversation =
    agent && sessionId ? (
      <ChatAgentProvider agent={chatAgent}>
        <ChatPanel
          key={`${agent.id}:${sessionId}`}
          agentId={agent.id}
          agentName={agent.name}
          agentSlug={agent.slug}
          agentRole={agent.role_title ?? null}
          sessionId={sessionId}
          initialInput={handoffPrompt ?? undefined}
          autoSendInitial={!!handoffPrompt}
          mobilePanel={isMobile ? mobilePanel : undefined}
          onSend={handleSend}
          onReplySettled={handleReplySettled}
        />
      </ChatAgentProvider>
    ) : tree.agents === null ? (
      <div className="h-full p-6">
        <Skeleton className="h-full w-full rounded-xl" />
      </div>
    ) : (
      <div className="grid h-full place-items-center px-6 text-center">
        <div className="max-w-sm">
          <p className="text-body text-foreground">Pick a conversation</p>
          <p className="mt-1 text-label text-muted-foreground">
            Or start one with an agent from the list on the left.
          </p>
        </div>
      </div>
    )

  /**
   * Below 900px a left column is not a column.
   *
   * The threshold is the surface's own (`CHAT_TREE_BREAKPOINT`), not the app's
   * phone breakpoint: 280px of conversations beside a transcript survives an
   * 800px window and does not survive a 390px one. The column becomes an
   * overlay drawer reached from a header button, and ChatPanel is handed the
   * `mobilePanel` prop its chat/files/more branches have always had — on
   * desktop those three live side by side in the panel's own right rail, so
   * the strip would be a duplicate there.
   */
  if (isMobile) {
    return (
      <div className="flex h-full min-h-0 flex-col overflow-hidden bg-card">
        <header className="flex h-12 shrink-0 items-center gap-2 border-b border-white/[0.08] px-3">
          <button
            type="button"
            onClick={() => setDrawerOpen(true)}
            aria-label="Show conversations"
            className="rounded p-1.5 text-muted-foreground hover:text-foreground"
          >
            <Menu className="h-4 w-4" />
          </button>
          <span className="truncate type-nav font-medium">{agent?.name ?? "Chat"}</span>
        </header>

        <div
          role="tablist"
          aria-label="Chat panel"
          className="flex h-9 shrink-0 items-stretch border-b border-white/[0.08]"
        >
          {MOBILE_PANELS.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={mobilePanel === id}
              onClick={() => setMobilePanel(id)}
              className={cn(
                "-mb-px flex flex-1 items-center justify-center gap-1.5 border-b-2 text-micro",
                mobilePanel === id
                  ? "border-primary text-foreground"
                  : "border-transparent text-muted-foreground",
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {label}
            </button>
          ))}
        </div>

        <div className="surface-pane min-h-0 flex-1 overflow-hidden">{conversation}</div>

        {drawerOpen && (
          <>
            <button
              type="button"
              aria-label="Close conversations"
              className="fixed inset-0 z-30 bg-black/50"
              onClick={() => setDrawerOpen(false)}
            />
            <div className="fixed inset-y-0 left-0 z-40 flex bg-card">
              <div className="relative">
                <button
                  type="button"
                  onClick={() => setDrawerOpen(false)}
                  aria-label="Close conversations"
                  className="absolute right-2 top-3 z-10 rounded p-1 text-muted-foreground hover:text-foreground"
                >
                  <X className="h-4 w-4" />
                </button>
                {sidebar}
              </div>
            </div>
          </>
        )}
      </div>
    )
  }

  return (
    /**
     * Chrome is one colour; the conversation is the other.
     *
     * `bg-card`, and specifically because that is what the rest of the app
     * chrome already is: measured in the browser, the app rail, the top bar
     * and this column all compute to the SAME value. A left column painted
     * `bg-background` is the one strip of near-black in a row of grey, and it
     * reads as a hole rather than as part of the frame.
     *
     * Which leaves the conversation needing to be the thing that is not
     * chrome, and it is: `.surface-pane` is lifted above `--card`, so the
     * centre is a raised reading surface inside a card-coloured frame.
     */
    <div className="flex h-full min-h-0 overflow-hidden bg-card">
      {sidebar}
      {/* The pane is on this element and this element does not scroll — the
          scroll containers are inside it. On a scroll container the gradient
          stretches to the full document height and the top highlight, which
          is the entire point of the treatment, leaves the screen. */}
      <div className="surface-pane min-h-0 min-w-0 flex-1 overflow-hidden">{conversation}</div>
    </div>
  )
}
