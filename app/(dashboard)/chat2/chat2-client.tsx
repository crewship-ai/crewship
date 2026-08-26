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
import { ConversationsSidebar } from "@/components/features/chat/v2/conversations-sidebar"
import { Skeleton } from "@/components/ui/skeleton"
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

  const [agentSlug, setAgentSlug] = useState<string | null>(null)
  const [sessionId, setSessionId] = useState<string | null>(null)

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

  const writeUrl = useCallback((slug: string, session: string) => {
    const url = `/chat2?agent=${encodeURIComponent(slug)}&session=${encodeURIComponent(session)}`
    window.history.replaceState(null, "", url)
  }, [])

  const selectThread = useCallback(
    (a: ChatTreeAgent, t: ChatTreeThread) => {
      setAgentSlug(a.slug)
      setSessionId(t.id)
      writeUrl(a.slug, t.id)
    },
    [writeUrl],
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
    if (agentSlug || !tree.threadsLoaded || !tree.agents) return
    let best: { agent: ChatTreeAgent; thread: ChatTreeThread; at: number } | null = null
    for (const a of tree.agents) {
      for (const t of tree.threadsByAgent[a.id] ?? []) {
        const at = parseSessionTimestamp(t.last_activity_at ?? t.started_at)
        if (!best || at > best.at) best = { agent: a, thread: t, at }
      }
    }
    if (best) selectThread(best.agent, best.thread)
  }, [agentSlug, tree.threadsLoaded, tree.agents, tree.threadsByAgent, selectThread])

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
     * Two depths, and only two.
     *
     * The first cut put `.surface-pane` on this wrapper and let the sidebar
     * sit on it transparently. That is what a pane wants — except it made the
     * column and the conversation the same surface, so the boundary between
     * "where I navigate" and "what I am reading" was carried by a 1px border
     * and nothing else. The two halves ran together.
     *
     * So: the shell is GROUND (`bg-background`, what the app sidebar and the
     * toolbar already sit on, and what classic /chat's column sits on), and
     * the pane is only on the conversation. The centre is then genuinely
     * raised — lit along its top edge, falling away at its foot — against a
     * column that reads as the surround. That is also the honest depth
     * order: you navigate on the ground and read on the thing in front of you.
     */
    <div className="flex h-full min-h-0 overflow-hidden bg-background">
      <ConversationsSidebar
        agents={tree.agents}
        threadsByAgent={tree.threadsByAgent}
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
