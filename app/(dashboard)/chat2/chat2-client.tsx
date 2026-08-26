"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { useSearchParams } from "next/navigation"

import { ChatPanel } from "@/components/features/chat/chat-panel"
import {
  useChatTreeData,
  type ChatTreeAgent,
  type ChatTreeThread,
} from "@/components/features/chat/chat-tree-sidebar"
import { ChatSkinProvider, type ChatSkinAgent } from "@/components/features/chat/v2/chat-skin"
import {
  applyReadOverrides,
  ConversationsSidebar,
} from "@/components/features/chat/v2/conversations-sidebar"
import { Skeleton } from "@/components/ui/skeleton"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { parseSessionTimestamp } from "@/components/features/chat/session-sort"
import { randomUUIDv4 } from "@/lib/random-id"

/**
 * The v2 shell.
 *
 * Three responsibilities and no more: pick a conversation, hand ChatPanel the
 * (agent, session) pair, and put the whole thing on the raised pane. It owns
 * no transcript rendering of its own — that lives in the skin, so `/chat` and
 * `/chat2` can never drift into two different renderers for the same turn.
 *
 * Selection is query-state (`?agent=&session=`) rather than a path segment.
 * Same constraint the classic page is under: the static export rewrites
 * exactly one path level (internal/api/static.go), so `/chat2/<agent>` would
 * 404 on a served build even though it works under `next dev`.
 */
export function Chat2Client() {
  const searchParams = useSearchParams()
  const tree = useChatTreeData<ChatTreeAgent>()

  const { workspaceId } = useWorkspace()
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
    const a = searchParams.get("agent")
    const s = searchParams.get("session")
    if (a) setAgentSlug(a)
    if (s) setSessionId(s)
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

  const writeUrl = useCallback((slug: string, session: string) => {
    const url = `/chat2?agent=${encodeURIComponent(slug)}&session=${encodeURIComponent(session)}`
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

  // Land on the most recent conversation rather than on an empty pane. The
  // reader who opens /chat2 with no query wants to see the surface working,
  // and the newest thread is the one they were last in.
  useEffect(() => {
    if (agentSlug || !tree.threadsLoaded || !agents) return
    let best: { agent: ChatTreeAgent; thread: ChatTreeThread; at: number } | null = null
    for (const a of agents) {
      for (const t of threadsByAgent[a.id] ?? []) {
        const at = parseSessionTimestamp(t.last_activity_at ?? t.started_at)
        if (!best || at > best.at) best = { agent: a, thread: t, at }
      }
    }
    if (best) selectThread(best.agent, best.thread)
  }, [agentSlug, tree.threadsLoaded, agents, threadsByAgent, selectThread])

  const skinAgent: ChatSkinAgent | null = useMemo(
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

  return (
    /**
     * Chrome is one colour; the conversation is the other.
     *
     * `bg-card`, and specifically because that is what the rest of the app
     * chrome already is: measured on /chat, the app rail (`bg-sidebar`), the
     * top bar (`bg-card`) and the classic chat column (`bg-card`) all compute
     * to the SAME value. A left column painted `bg-background` — which is
     * where the second cut of this page left it — is therefore the one strip
     * of near-black in a row of grey, and it reads as a hole rather than as
     * part of the frame.
     *
     * Which leaves the conversation needing to be the thing that is not
     * chrome, and it is: `.surface-pane` is anchored on `--background`, so the
     * centre is the recessed reading surface inside a card-coloured frame.
     * That is exactly the relationship classic already has (its content area
     * is `bg-background` inside `bg-card` chrome) with the pane's lighting
     * added on top, rather than a third value invented for this page.
     */
    <div className="flex h-full min-h-0 overflow-hidden bg-card">
      <ConversationsSidebar
        agents={agents}
        threadsByAgent={threadsByAgent}
        threadsLoaded={tree.threadsLoaded}
        activeThreadId={sessionId}
        onSelectThread={selectThread}
        onStartConversation={startConversation}
      />

      {/* The pane is on this element and this element does not scroll — the
          scroll containers are inside it. On a scroll container the gradient
          stretches to the full document height and the top highlight, which
          is the entire point of the treatment, leaves the screen. */}
      <div className="surface-pane min-h-0 min-w-0 flex-1 overflow-hidden">
        {agent && sessionId ? (
          <ChatSkinProvider variant="v2" agent={skinAgent}>
            <ChatPanel
              key={`${agent.id}:${sessionId}`}
              agentId={agent.id}
              agentName={agent.name}
              agentSlug={agent.slug}
              agentRole={agent.role_title ?? null}
              sessionId={sessionId}
              onReplySettled={handleReplySettled}
            />
          </ChatSkinProvider>
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
        )}
      </div>
    </div>
  )
}
