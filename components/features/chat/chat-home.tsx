"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Plus, Users } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { SubBar } from "@/components/layout/sub-bar"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Skeleton } from "@/components/ui/skeleton"
import { timeAgo } from "@/lib/time"
import { cn } from "@/lib/utils"

import { parseSessionTimestamp } from "./session-sort"

/**
 * `/chat` — the front door to the agents.
 *
 * Threads first, agents beneath (PRD O3). Everything here is built from
 * endpoints that already exist: `GET /agents` once, then `GET /agents/{id}/chats`
 * per agent, merged and re-sorted on the client. There is no cross-agent
 * "recent conversations" endpoint and this page is not a reason to add one.
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

/**
 * How many agents get a chats request. The fan-out is one round trip per
 * agent, in parallel, on a page whose entire job is to open fast; a workspace
 * with 60 agents would otherwise spend 60 requests to render ~20 rows that
 * come from the handful of agents anyone actually talks to.
 *
 * 12 is chosen against the ordering the roster already has: `/agents` returns
 * live agents by creation recency with ghosts last (internal/api/agents_query.go),
 * so the first 12 are the newest live agents — the ones with recent threads if
 * anyone has any. Every agent still gets a row in the Agents section; the cap
 * only bounds the thread lookup.
 */
export const AGENT_FANOUT_CAP = 12

/** Per-agent page size. 12 × 10 is a comfortable superset of what is shown. */
export const PER_AGENT_CHAT_LIMIT = 10

/** How many merged threads reach the screen. */
export const RECENT_THREAD_LIMIT = 25

export interface ChatHomeAgent {
  id: string
  name: string
  slug: string
  status: string
  role_title?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  /** Non-null marks a ghost (a retired agent). Not a chat destination. */
  expired_at?: string | null
}

export interface ChatHomeThread {
  id: string
  title: string | null
  status: string
  message_count: number
  started_at: string
  ended_at?: string | null
  last_activity_at?: string | null
  unread_count?: number
  /**
   * First line of the last message. `GET /agents/{id}/chats` does not emit
   * one today (internal/api/agent_chats.go selects id/title/mode/status/
   * counts/timestamps), so this is read defensively and the row simply omits
   * the line when it is absent, rather than the page growing a second fetch
   * per thread to manufacture it.
   */
  last_message_preview?: string | null
  /** Attached client-side during the merge — the agent the thread belongs to. */
  agent: ChatHomeAgent
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
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [agents, setAgents] = useState<ChatHomeAgent[] | null>(null)
  const [threads, setThreads] = useState<ChatHomeThread[]>([])
  const [threadsLoaded, setThreadsLoaded] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // ── The roster ──
  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((list: ChatHomeAgent[]) => {
        if (cancelled) return
        // Ghosts are retired agents; opening a chat with one is not a thing
        // this page should offer.
        setAgents(Array.isArray(list) ? list.filter((a) => !a.expired_at) : [])
        setError(null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setAgents([])
        setError(err instanceof Error ? err.message : String(err))
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId])

  // ── The fan-out ──
  useEffect(() => {
    if (!workspaceId || agents === null) return
    if (agents.length === 0) {
      setThreads([])
      setThreadsLoaded(true)
      return
    }
    let cancelled = false
    setThreadsLoaded(false)
    const scope = agents.slice(0, AGENT_FANOUT_CAP)
    Promise.all(
      scope.map((a) =>
        apiFetch(
          `/api/v1/agents/${encodeURIComponent(a.id)}/chats` +
            `?workspace_id=${encodeURIComponent(workspaceId)}&limit=${PER_AGENT_CHAT_LIMIT}`,
        )
          .then((r) => (r.ok ? r.json() : []))
          .then((rows: unknown) =>
            Array.isArray(rows)
              ? (rows as Omit<ChatHomeThread, "agent">[]).map((row) => ({ ...row, agent: a }))
              : [],
          )
          // One agent's list failing must not blank the page — the other
          // eleven are still worth showing.
          .catch(() => [] as ChatHomeThread[]),
      ),
    ).then((chunks) => {
      if (cancelled) return
      setThreads(mergeRecentThreads(chunks.flat()))
      setThreadsLoaded(true)
    })
    return () => {
      cancelled = true
    }
  }, [workspaceId, agents])

  const loading = wsLoading || agents === null

  const description = loading
    ? undefined
    : agents.length === 0
      ? "No agents yet"
      : `${threads.length} recent · ${agents.length} agent${agents.length === 1 ? "" : "s"}`

  return (
    <div className="flex h-[calc(100vh-48px)] min-h-0 flex-col">
      <SubBar icon={CONCEPT_ICON.sessions} title="Chat" description={description} ariaLabel="Chat" />

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl px-4 py-6">
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
      href={threadHref(t.agent.slug, t.id)}
      className={cn(
        "flex items-start gap-3 rounded-md border border-transparent px-3 py-2 transition-colors",
        "hover:border-white/[0.08] hover:bg-white/[0.03]",
      )}
    >
      <AgentAvatar
        seed={t.agent.avatar_seed || t.agent.slug}
        style={t.agent.avatar_style}
        agentId={t.agent.id}
        avatarUrl={t.agent.avatar_url}
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
          <span className="truncate">{t.agent.name}</span>
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
