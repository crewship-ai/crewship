"use client"

import { useMemo } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Plus, Users } from "lucide-react"

import { CONCEPT_ICON } from "@/lib/concept-icons"
import { SubBar } from "@/components/layout/sub-bar"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Skeleton } from "@/components/ui/skeleton"
import { timeAgo } from "@/lib/time"
import { cn } from "@/lib/utils"

import { parseSessionTimestamp } from "./session-sort"
import {
  AGENT_FANOUT_CAP,
  ChatTreeSidebar,
  PER_AGENT_CHAT_LIMIT,
  RECENT_THREAD_LIMIT,
  useChatCompactLayout,
  useChatTreeData,
  type ChatTreeAgent,
  type ChatTreeThread,
} from "./chat-tree-sidebar"

/**
 * `/chat` — the front door to the agents.
 *
 * Threads first, agents beneath (PRD O3). Everything here is built from
 * endpoints that already exist: `GET /agents` once, then `GET /agents/{id}/chats`
 * per agent, merged and re-sorted on the client. There is no cross-agent
 * "recent conversations" endpoint and this page is not a reason to add one.
 *
 * The shape changed once the agent tree came back into scope: this content is
 * no longer the whole page, it is the RIGHT pane. The left is the same
 * `ChatTreeSidebar` that `/chat/<agent>` renders, so the surface has ONE left
 * column instead of one per route, and a wide screen shows a page rather than a
 * strip of text in a lot of empty. The index keeps a max width of its own —
 * beside a 280px column there is more than enough room to stretch a line of
 * text to 2000px, which is not an improvement on the strip.
 *
 * Both halves read the SAME fetch (`useChatTreeData`). A tree with its own copy
 * of the fan-out would double every request on the busiest page of the surface.
 *
 * What this page deliberately does NOT do (PRD O7): mount a ChatPanel. The
 * panel opens its own WebSocket on mount, separate from RealtimeProvider, so
 * an index that previewed a conversation would hold a live socket for a thread
 * the user has not chosen yet. Picking a thread navigates to
 * `/chat/<slug>?session=<id>` — the same shape internal/chatnotify/notify.go
 * already emits for deep links, which is why the session stays a query
 * parameter and never becomes a path segment (the Go static rewrite in
 * internal/api/static.go resolves exactly one level).
 */

// The fan-out constants live with the fetching, in chat-tree-sidebar. Kept
// exported from here because this module is where they were first named and
// callers (and tests) still reach for them at this path.
export { AGENT_FANOUT_CAP, PER_AGENT_CHAT_LIMIT, RECENT_THREAD_LIMIT }

export type ChatHomeAgent = ChatTreeAgent
export type ChatHomeThread = ChatTreeThread & {
  /**
   * First line of the last message. `GET /agents/{id}/chats` does not emit
   * one today (internal/api/agent_chats.go selects id/title/mode/status/
   * counts/timestamps), so this is read defensively and the row simply omits
   * the line when it is absent, rather than the page growing a second fetch
   * per thread to manufacture it.
   */
  last_message_preview?: string | null
  /**
   * The agent this thread is currently filed under — `chats.agent_id`,
   * attached client-side during the merge because the per-agent responses do
   * not carry it back.
   *
   * Named `owner`, not `agent`: PRD §7 binds shipped code not to name things
   * "the agent of this thread". The column is single-valued and NOT NULL
   * today, so one agent owns the row; that is a fact about the schema, not a
   * claim that a conversation has exactly one agent in it. Group threads are
   * deferred, not foreclosed, and reads are deliberately NOT routed through a
   * participant list while that list is always length one (§7 calls that
   * YAGNI) — only the vocabulary is kept open.
   */
  owner: ChatHomeAgent
}

/** `/chat/<slug>?session=<id>` — session as a query param, never a path segment. */
export function threadHref(slug: string, chatId: string): string {
  return `/chat/${encodeURIComponent(slug)}?session=${encodeURIComponent(chatId)}`
}

/**
 * Merge per-agent thread lists into one recency-ordered list.
 *
 * Each `/chats` response is already ordered by last activity, but a
 * concatenation of ordered lists is not ordered, so the sort is the point of
 * doing this on the client at all. Zero-message sessions are dropped: they are
 * the residue of sessions that used to be created merely by arriving at a
 * chat, and an index full of "Untitled session" is worse than an empty one.
 */
export function mergeRecentThreads(
  rows: ChatHomeThread[],
  limit: number = RECENT_THREAD_LIMIT,
): ChatHomeThread[] {
  return rows
    .filter((t) => t.message_count > 0)
    .sort(
      (a, b) =>
        parseSessionTimestamp(b.last_activity_at ?? b.started_at) -
        parseSessionTimestamp(a.last_activity_at ?? a.started_at),
    )
    .slice(0, limit)
}

export function ChatHome() {
  const router = useRouter()
  const compact = useChatCompactLayout()
  const { agents, threadsByAgent, threadErrors, threadsLoaded, retryThreads, error, wsLoading } =
    useChatTreeData()

  const threads = useMemo(() => {
    if (!agents) return []
    return mergeRecentThreads(
      agents.flatMap((a) => (threadsByAgent[a.id] ?? []).map((t) => ({ ...t, owner: a }))),
    )
  }, [agents, threadsByAgent])

  const loading = wsLoading || agents === null

  const description = loading
    ? undefined
    : agents.length === 0
      ? "No agents yet"
      : `${threads.length} recent · ${agents.length} agent${agents.length === 1 ? "" : "s"}`

  return (
    <div className="flex h-[calc(100vh-48px)] min-h-0 flex-col bg-background">
      <SubBar icon={CONCEPT_ICON.sessions} title="Chat" description={description} ariaLabel="Chat" />

      <div className="flex min-h-0 flex-1 overflow-hidden">
        {/* The tree is a two-pane information architecture. Where two panes do
            not fit, the index IS the navigation — a 280px column on a 390px
            phone is the mistake this surface already made once. */}
        {!compact && (
          <ChatTreeSidebar
            agents={agents ?? []}
            threadsByAgent={threadsByAgent}
            threadErrors={threadErrors}
            onRetryThreads={retryThreads}
            loading={loading || !threadsLoaded}
            activeAgentSlug={null}
            activeThreadId={null}
            onOpenThread={(owner, threadId) => router.push(threadHref(owner.slug, threadId))}
          />
        )}

        <div className="min-h-0 flex-1 overflow-y-auto">
          <div data-testid="chat-home-pane" className="mx-auto w-full max-w-3xl px-4 py-6">
            {loading ? (
              <div className="space-y-2" data-testid="chat-home-loading">
                <Skeleton className="h-4 w-32" />
                <Skeleton className="h-12 w-full" />
                <Skeleton className="h-12 w-full" />
                <Skeleton className="h-12 w-full" />
              </div>
            ) : agents.length === 0 ? (
              <NoAgents error={error} />
            ) : (
              <>
                <RecentThreads threads={threads} loaded={threadsLoaded} />
                <AgentList agents={agents} />
              </>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ------------------------------------------------------------------ empty */

function NoAgents({ error }: { error: string | null }) {
  return (
    <div className="rounded-lg border border-white/[0.08] bg-card px-6 py-10 text-center">
      <Users className="mx-auto h-6 w-6 text-muted-foreground-soft" aria-hidden />
      <h2 className="mt-3 text-sm font-medium">No agents yet</h2>
      <p className="mx-auto mt-1 max-w-sm text-xs text-muted-foreground">
        Chat is a conversation with an agent, so there is nobody to talk to until one exists.
        Agents are created from Crews &amp; Agents.
      </p>
      <Link
        href="/crews?new=agent"
        className="mt-4 inline-flex items-center gap-1.5 rounded-md bg-primary/10 px-3 py-1.5 text-xs text-primary-hover hover:bg-primary/15"
      >
        <Plus className="h-3 w-3" aria-hidden />
        Create an agent
      </Link>
      {error && <p className="mt-3 text-[10px] text-muted-foreground-soft">Could not load agents: {error}</p>}
    </div>
  )
}

/* ---------------------------------------------------------------- threads */

function RecentThreads({ threads, loaded }: { threads: ChatHomeThread[]; loaded: boolean }) {
  return (
    <section className="mb-8">
      <SectionHeader label="Recent conversations" count={loaded ? threads.length : undefined} />

      {!loaded ? (
        <div className="space-y-1.5" data-testid="chat-home-threads-loading">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : threads.length === 0 ? (
        <p className="rounded-lg border border-dashed border-white/[0.08] px-4 py-6 text-center text-xs text-muted-foreground">
          No conversations yet — pick an agent below and say something.
        </p>
      ) : (
        <ul role="list" aria-label="Recent conversations" className="space-y-1">
          {threads.map((t) => (
            <li key={t.id}>
              <ThreadRow thread={t} />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function ThreadRow({ thread: t }: { thread: ChatHomeThread }) {
  const unread = t.unread_count ?? 0
  const preview = t.last_message_preview?.trim()
  return (
    <Link
      href={threadHref(t.owner.slug, t.id)}
      className={cn(
        "flex items-start gap-3 rounded-md border border-transparent px-3 py-2 transition-colors",
        "hover:border-white/[0.08] hover:bg-white/[0.03]",
      )}
    >
      <AgentAvatar
        seed={t.owner.avatar_seed || t.owner.slug}
        style={t.owner.avatar_style}
        agentId={t.owner.id}
        avatarUrl={t.owner.avatar_url}
        alt=""
        className="mt-0.5 h-7 w-7 shrink-0"
      />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span
            className={cn(
              "truncate text-xs",
              t.title ? "text-foreground" : "italic text-muted-foreground",
              unread > 0 && "font-medium",
            )}
          >
            {t.title || "Untitled session"}
          </span>
          <span className="ml-auto shrink-0 text-[10px] text-muted-foreground-soft tabular-nums">
            {timeAgo(t.last_activity_at ?? t.started_at)}
          </span>
        </span>
        {preview && <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">{preview}</span>}
        <span className="mt-0.5 flex items-center gap-2 text-[10px] text-muted-foreground-soft">
          <span className="truncate">{t.owner.name}</span>
          <span aria-hidden>·</span>
          <span className="tabular-nums">
            {t.message_count} msg{t.message_count === 1 ? "" : "s"}
          </span>
          {unread > 0 && (
            <span
              aria-label={`${unread} unread message${unread === 1 ? "" : "s"}`}
              className="inline-flex items-center gap-1 rounded-full bg-info/20 px-1.5 py-0.5 leading-none text-info"
            >
              <span className="h-1.5 w-1.5 rounded-full bg-info" aria-hidden />
              {unread > 99 ? "99+" : unread}
            </span>
          )}
        </span>
      </span>
    </Link>
  )
}

/* ----------------------------------------------------------------- agents */

function AgentList({ agents }: { agents: ChatHomeAgent[] }) {
  return (
    <section>
      <SectionHeader label="Agents" count={agents.length} />
      <ul role="list" aria-label="Agents" className="space-y-1">
        {agents.map((a) => (
          <li key={a.id}>
            <Link
              href={`/chat/${encodeURIComponent(a.slug)}`}
              className="flex items-center gap-3 rounded-md border border-transparent px-3 py-2 transition-colors hover:border-white/[0.08] hover:bg-white/[0.03]"
            >
              <AgentAvatar
                seed={a.avatar_seed || a.slug}
                style={a.avatar_style}
                agentId={a.id}
                avatarUrl={a.avatar_url}
                alt=""
                className="h-6 w-6 shrink-0"
              />
              <span className="min-w-0 flex-1 truncate text-xs text-foreground">{a.name}</span>
              {a.role_title && (
                <span className="shrink-0 truncate text-[10px] text-muted-foreground-soft">{a.role_title}</span>
              )}
            </Link>
          </li>
        ))}
      </ul>
    </section>
  )
}

function SectionHeader({ label, count }: { label: string; count?: number }) {
  return (
    <div className="mb-2 flex items-center gap-1.5 px-1">
      <span className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">{label}</span>
      {count != null && <span className="text-[10px] tabular-nums text-muted-foreground-soft">{count}</span>}
    </div>
  )
}
