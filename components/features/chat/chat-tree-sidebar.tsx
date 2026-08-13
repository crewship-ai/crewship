"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  ChevronDown,
  Inbox,
  Activity,
  CheckCircle2,
  Mail,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import {
  SIDEBAR_WIDTH,
  SIDEBAR_WIDTH_COLLAPSED,
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { cn } from "@/lib/utils"

import { parseSessionTimestamp, sortSessionsByActivity } from "./session-sort"
import { ScopeFailure, httpError, scopeErrorMessage, useRetry } from "./scope-fetch"

/**
 * chat-tree-sidebar — the ONE left column of the chat surface.
 *
 * `/chat` and `/chat/<agent>` used to grow a left column each: a centred index
 * on one, a flat 240px session list on the other, and neither was the shape
 * `/routines` and `/issues` use. This is the shared one, and it is assembled
 * out of `components/layout/sidebar-kit` rather than hand-rolled, so the
 * toolbar, the section headers and the accent-bar selection are literally the
 * same components every other in-page sidebar renders.
 *
 * Content order mirrors /routines — search → facets → entities:
 *
 *   [🔍 search] [⧩ filter] [⇤ collapse]
 *   STATUS       All · Unread · Running · Done          (counts, collapsible)
 *   AGENTS       ▾ Ada Lovelace                         (expandable)
 *                    Ship the export                    (thread, 24px)
 *                    Weekly summary
 *                  Bob Robot                            (no threads, no chevron)
 *
 * **An agent expands to its threads and to nothing else.** It briefly carried
 * four folders — Sessions / Files / Asks / Memory — and three of them were a
 * second copy of a surface that already existed: Files is in the chat's own
 * right rail, Asks is the agent's configuration tab, Memory is the agent
 * canvas. The rule that settled it, and that this file is now bound by:
 *
 *   left column = navigation between objects  ("where am I going")
 *   right panel = context of the open object  ("what is here")
 *   config page = the object's own settings
 *
 * Sessions is navigation, so it stayed — as the agent's own children rather
 * than as a folder row you had to open first.
 *
 * On the counts: every number here is computed from data that was actually
 * fetched — `GET /agents` (agent `status`) and `GET /agents/{id}/chats`
 * (`unread_count`, `ended_at`). There is deliberately no "Needs you" facet:
 * nothing either endpoint returns can answer it, and a facet whose number is
 * a guess is worse than a facet that is not there.
 *
 * Two constraints this file is bound by, both of them load-bearing:
 *
 *  · **No deeper route.** The static export rewrites exactly one path level
 *    (internal/api/static.go) and the agent slug is parsed out of
 *    window.location.pathname, so nothing this column selects may become a
 *    second path segment. It only ever reports a selection; the page decides
 *    how to record it.
 *  · **One WebSocket.** ChatPanel opens one per mounted panel, so this column
 *    never mounts a panel of its own.
 */

/* ------------------------------------------------------------------ types */

export interface ChatTreeAgent {
  id: string
  name: string
  slug: string
  status: string
  role_title?: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew_id?: string | null
  /** Non-null marks a ghost (a retired agent). Not a chat destination. */
  expired_at?: string | null
}

export interface ChatTreeThread {
  id: string
  title: string | null
  status: string
  message_count: number
  started_at: string
  ended_at?: string | null
  last_activity_at?: string | null
  unread_count?: number
  /** UI · CLI · WEBHOOK · CRON · AGENT (migration v59); NULL on older rows. */
  origin?: string | null
}

type StatusFacet = "all" | "unread" | "running" | "done"

const STATUS_FACETS: { id: StatusFacet; label: string; icon: LucideIcon; tone: string }[] = [
  { id: "all", label: "All", icon: Inbox, tone: "text-foreground/70" },
  // Unread comes from chat_read_cursors via GET /agents/{id}/chats.
  { id: "unread", label: "Unread", icon: Mail, tone: "text-info" },
  // Running is the agent's own status column, the one live signal either
  // endpoint carries. A thread has no "running" of its own.
  { id: "running", label: "Running", icon: Activity, tone: "text-primary" },
  // Done is chats.ended_at — a real column that nothing writes yet, so this
  // reads a real zero rather than inventing a number.
  { id: "done", label: "Done", icon: CheckCircle2, tone: "text-success" },
]

/**
 * The width below which the tree is not a column.
 *
 * Higher than the app-wide mobile breakpoint (768) on purpose: 240px of list
 * beside a conversation was survivable on an 800px window, and 280px of tree
 * plus a status section is not. Below this the surface falls back to the shape
 * that was built for exactly this problem — the session drawer and the
 * chat/files/more tab strip — rather than to a squeezed version of the tree.
 */
export const CHAT_TREE_BREAKPOINT = 900

/**
 * True while the viewport is too narrow for a left column. Same shape as
 * hooks/use-mobile.ts, a different number: `useIsMobile` answers "is this a
 * phone", which is a question the rest of the app asks and this surface does
 * not — it needs to know whether two panes fit.
 */
export function useChatCompactLayout(): boolean {
  const [compact, setCompact] = useState(false)
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return
    const mql = window.matchMedia(`(max-width: ${CHAT_TREE_BREAKPOINT - 1}px)`)
    const onChange = () => setCompact(window.innerWidth < CHAT_TREE_BREAKPOINT)
    mql.addEventListener("change", onChange)
    onChange()
    return () => mql.removeEventListener("change", onChange)
  }, [])
  return compact
}

/** Origins worth filtering by; only those the backend actually emits. */
const ORIGINS: { id: string; label: string }[] = [
  { id: "UI", label: "From the console" },
  { id: "CLI", label: "From the CLI" },
  { id: "WEBHOOK", label: "Webhook" },
  { id: "CRON", label: "Scheduled" },
  { id: "AGENT", label: "Another agent" },
]

/* ------------------------------------------------------------------- data */

/**
 * How many agents get a chats request. The fan-out is one round trip per
 * agent, in parallel, on a surface whose whole job is to open fast; a
 * workspace with 60 agents would otherwise spend 60 requests to draw a column.
 *
 * 12 is chosen against the ordering the roster already has: `/agents` returns
 * live agents by creation recency with ghosts last, so the first 12 are the
 * newest live agents — the ones with threads if anyone has any. Every agent
 * still gets a row; the cap only bounds the thread lookup.
 */
export const AGENT_FANOUT_CAP = 12

/** Per-agent page size. 12 × 10 is a comfortable superset of what is shown. */
export const PER_AGENT_CHAT_LIMIT = 10

/** How many merged threads reach the /chat index. */
export const RECENT_THREAD_LIMIT = 25

export interface ChatTreeData<A extends ChatTreeAgent = ChatTreeAgent> {
  /** Live agents, ghosts removed — what the tree lists. null until resolved. */
  agents: A[] | null
  /**
   * Everything `GET /agents` returned, ghosts included. Resolving the agent a
   * URL names goes through this: a retired agent still has a chat history, and
   * "not found in workspace" is the wrong answer for one.
   */
  roster: A[] | null
  threadsByAgent: Record<string, ChatTreeThread[]>
  /**
   * Agent id → why its thread list is missing.
   *
   * The fan-out used to write `.then((r) => (r.ok ? r.json() : []))`, so a
   * 500 arrived as an empty array and the tree said "this agent has no
   * conversations" — in the product's primary navigation, to someone whose
   * server had been unhappy for ten seconds. An agent in here has an unknown
   * list, not an empty one, and the tree says so.
   */
  threadErrors: Record<string, string>
  threadsLoaded: boolean
  /** Re-run the fan-out. Wired to the Retry beside a failed agent. */
  retryThreads: () => void
  error: string | null
  wsLoading: boolean
}

/**
 * The tree's data, fetched once and shared by whatever else the page draws
 * from the same lists.
 *
 * `skipSlug` names an agent whose threads the CALLER already owns —
 * `/chat/<agent>` keeps its own `sessions` state (optimistic inserts, auto
 * titles, mark-read) and passes it back in. Skipping by slug rather than by id
 * is deliberate: the slug is known from the URL before any request goes out,
 * so there is no window in which the fan-out fires for an agent the page was
 * about to claim.
 */
export function useChatTreeData<A extends ChatTreeAgent = ChatTreeAgent>({
  skipSlug,
}: { skipSlug?: string | null } = {}): ChatTreeData<A> {
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [roster, setRoster] = useState<A[] | null>(null)
  const [threadsByAgent, setThreadsByAgent] = useState<Record<string, ChatTreeThread[]>>({})
  const [threadErrors, setThreadErrors] = useState<Record<string, string>>({})
  const [threadsLoaded, setThreadsLoaded] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const { nonce: threadsNonce, retry: retryThreads } = useRetry()

  // ── The roster ──
  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((list: A[]) => {
        if (cancelled) return
        setRoster(Array.isArray(list) ? list : [])
        setError(null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setRoster([])
        setError(err instanceof Error ? err.message : String(err))
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId])

  // Ghosts are retired agents; starting a conversation with one is not a thing
  // this surface should offer, so they are out of the tree — but still in the
  // roster, which is what resolves a URL.
  const agents = useMemo(() => roster?.filter((a) => !a.expired_at) ?? null, [roster])

  // ── The fan-out ──
  useEffect(() => {
    if (!workspaceId || agents === null) return
    const scope = agents.filter((a) => a.slug !== skipSlug).slice(0, AGENT_FANOUT_CAP)
    if (scope.length === 0) {
      setThreadsByAgent({})
      setThreadErrors({})
      setThreadsLoaded(true)
      return
    }
    let cancelled = false
    setThreadsLoaded(false)
    Promise.all(
      scope.map((a) =>
        apiFetch(
          `/api/v1/agents/${encodeURIComponent(a.id)}/chats` +
            `?workspace_id=${encodeURIComponent(workspaceId)}&limit=${PER_AGENT_CHAT_LIMIT}`,
        )
          .then((r) => {
            if (!r.ok) throw httpError(r.status)
            return r.json()
          })
          .then(
            (rows: unknown) =>
              [a.id, Array.isArray(rows) ? (rows as ChatTreeThread[]) : [], null] as const,
          )
          // One agent's list failing must not blank the column — the other
          // eleven are still worth showing. It must not pass for an EMPTY
          // list either, which is what returning [] here used to do.
          .catch((e: unknown) => [a.id, null, scopeErrorMessage(e)] as const),
      ),
    ).then((results) => {
      if (cancelled) return
      const lists: Record<string, ChatTreeThread[]> = {}
      const errors: Record<string, string> = {}
      for (const [id, rows, err] of results) {
        if (rows) lists[id] = rows
        else if (err) errors[id] = err
      }
      setThreadsByAgent(lists)
      setThreadErrors(errors)
      setThreadsLoaded(true)
    })
    return () => {
      cancelled = true
    }
  }, [workspaceId, agents, skipSlug, threadsNonce])

  return { agents, roster, threadsByAgent, threadErrors, threadsLoaded, retryThreads, error, wsLoading }
}

/* ------------------------------------------------------------------ props */

export interface ChatTreeSidebarProps {
  agents: ChatTreeAgent[]
  threadsByAgent: Record<string, ChatTreeThread[]>
  /** Agent id → why its thread list is missing. See ChatTreeData. */
  threadErrors?: Record<string, string>
  /** Re-ask for the thread lists. Rendered as the Retry beside a failure. */
  onRetryThreads?: () => void
  /** Slug of the agent whose surface is on screen, or null on the index. */
  activeAgentSlug?: string | null
  activeThreadId?: string | null
  /**
   * A thread was picked. `owner` is the agent the thread is filed under
   * (`chats.agent_id`) — the caller needs it to build the URL, since the slug
   * is the path segment. Deliberately not named "the thread's agent": PRD §7
   * binds this code to vocabulary that does not assume a conversation has
   * exactly one agent in it, because the group-thread groundwork
   * (`chat_participants`, v118) is being kept usable.
   */
  onOpenThread: (owner: ChatTreeAgent, threadId: string) => void
  /** Rows are still arriving; drawn as a hint, never as a blank column. */
  loading?: boolean
  collapsed?: boolean
  onToggleCollapsed?: () => void
  className?: string
}

/* ----------------------------------------------------------------- helpers */

function threadLabel(t: ChatTreeThread): string {
  return t.title?.trim() || "Untitled session"
}

function agentMatches(a: ChatTreeAgent, q: string): boolean {
  return (
    a.name.toLowerCase().includes(q) ||
    a.slug.toLowerCase().includes(q) ||
    (a.role_title ?? "").toLowerCase().includes(q)
  )
}

/** Epoch millis of an agent's most recent thread; 0 when it has none. */
function lastActivityOf(threads: ChatTreeThread[]): number {
  let newest = 0
  for (const t of threads) {
    const ms = parseSessionTimestamp(t.last_activity_at ?? t.started_at)
    if (ms > newest) newest = ms
  }
  return newest
}

/**
 * Agents newest-conversation-first.
 *
 * Alphabetical put "Aaron" at the top of a workspace where nobody has talked
 * to Aaron since March. The roster arrives ordered by creation recency, which
 * is no better — it is the order the agents were *made*, not the order they
 * are used. An agent nobody has a thread with sorts last, by name, so the tail
 * of the list is at least stable and readable.
 */
function sortAgentsByActivity<A extends ChatTreeAgent>(
  agents: A[],
  threadsByAgent: Record<string, ChatTreeThread[]>,
): A[] {
  return [...agents].sort((a, b) => {
    const delta = lastActivityOf(threadsByAgent[b.id] ?? []) - lastActivityOf(threadsByAgent[a.id] ?? [])
    return delta !== 0 ? delta : a.name.localeCompare(b.name)
  })
}

function threadMatchesFacet(t: ChatTreeThread, a: ChatTreeAgent, facet: StatusFacet): boolean {
  switch (facet) {
    case "unread":
      return (t.unread_count ?? 0) > 0
    case "running":
      return a.status === "RUNNING"
    case "done":
      return t.ended_at != null
    default:
      return true
  }
}

/* -------------------------------------------------------------- component */

export function ChatTreeSidebar({
  agents,
  threadsByAgent,
  threadErrors = {},
  onRetryThreads,
  activeAgentSlug = null,
  activeThreadId = null,
  onOpenThread,
  loading = false,
  collapsed = false,
  onToggleCollapsed,
  className,
}: ChatTreeSidebarProps) {
  const [search, setSearch] = useState("")
  const [statusOpen, setStatusOpen] = useState(true)
  const [agentsOpen, setAgentsOpen] = useState(true)
  const [facet, setFacet] = useState<StatusFacet>("all")
  const [origins, setOrigins] = useState<string[]>([])
  const [expandedAgents, setExpandedAgents] = useState<Set<string>>(new Set())

  // Arriving at /chat/<agent>?session=<id> must SHOW where you are. Without
  // this the tree would open collapsed on the very row the URL just named.
  useEffect(() => {
    if (!activeAgentSlug) return
    setExpandedAgents((prev) => (prev.has(activeAgentSlug) ? prev : new Set(prev).add(activeAgentSlug)))
  }, [activeAgentSlug])

  const toggleAgent = useCallback((slug: string) => {
    setExpandedAgents((prev) => {
      const next = new Set(prev)
      if (next.has(slug)) next.delete(slug)
      else next.add(slug)
      return next
    })
  }, [])

  const q = search.trim().toLowerCase()

  // Counts are computed over EVERYTHING loaded, not over the post-facet view —
  // otherwise picking "Unread" would make every other facet read 0, which is
  // the bug /routines already fixed once.
  const statusCounts = useMemo(() => {
    const counts: Record<StatusFacet, number> = { all: 0, unread: 0, running: 0, done: 0 }
    for (const a of agents) {
      for (const t of threadsByAgent[a.id] ?? []) {
        counts.all++
        if ((t.unread_count ?? 0) > 0) counts.unread++
        if (a.status === "RUNNING") counts.running++
        if (t.ended_at != null) counts.done++
      }
    }
    return counts
  }, [agents, threadsByAgent])

  /** Threads shown under one agent, after facet + origin + search. */
  const visibleThreads = useCallback(
    (a: ChatTreeAgent): ChatTreeThread[] => {
      const rows = (threadsByAgent[a.id] ?? []).filter(
        (t) =>
          threadMatchesFacet(t, a, facet) &&
          (origins.length === 0 || (t.origin != null && origins.includes(t.origin))) &&
          (!q || agentMatches(a, q) || threadLabel(t).toLowerCase().includes(q)),
      )
      return sortSessionsByActivity(rows) as ChatTreeThread[]
    },
    [threadsByAgent, facet, origins, q],
  )

  const visibleAgents = useMemo(() => {
    const matching = agents.filter((a) => {
      const threads = visibleThreads(a)
      // "Running" is a property of the agent, so a running agent belongs in
      // the list whether or not it has a thread to show for it.
      if (facet === "running") return a.status === "RUNNING"
      if (facet !== "all") return threads.length > 0
      if (origins.length > 0) return threads.length > 0
      if (!q) return true
      return agentMatches(a, q) || threads.length > 0
    })
    // Ordered by the work, not by the alphabet — see sortAgentsByActivity.
    return sortAgentsByActivity(matching, threadsByAgent)
  }, [agents, threadsByAgent, visibleThreads, facet, origins, q])

  if (collapsed) {
    return (
      <aside
        data-testid="chat-tree-sidebar"
        aria-label="Agents and conversations"
        className={cn(
          "flex min-h-0 shrink-0 flex-col items-center border-r border-white/8 bg-card pt-1.5",
          SIDEBAR_WIDTH_COLLAPSED,
          className,
        )}
      >
        {onToggleCollapsed && <SidebarCollapseButton collapsed onToggle={onToggleCollapsed} />}
      </aside>
    )
  }

  const activeFilterCount = origins.length

  return (
    <aside
      data-testid="chat-tree-sidebar"
      aria-label="Agents and conversations"
      className={cn(
        "flex min-h-0 shrink-0 flex-col border-r border-white/8 bg-card",
        SIDEBAR_WIDTH,
        className,
      )}
    >
      <SidebarToolbar className="border-b border-white/8">
        <SidebarSearch
          value={search}
          onValueChange={setSearch}
          placeholder="Search agents, threads…"
          aria-label="Search agents and conversations"
        />
        <SidebarFilterPopover
          label="Filter conversations"
          activeCount={activeFilterCount}
          onClear={() => setOrigins([])}
        >
          <SidebarFacet
            first
            label="Started from"
            resetLabel="Any origin"
            resetActive={origins.length === 0}
            onReset={() => setOrigins([])}
          >
            {ORIGINS.map((o) => (
              <SidebarFacetOption
                key={o.id}
                active={origins.includes(o.id)}
                onToggle={() =>
                  setOrigins((prev) =>
                    prev.includes(o.id) ? prev.filter((x) => x !== o.id) : [...prev, o.id],
                  )
                }
              >
                {o.label}
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
        </SidebarFilterPopover>
        {onToggleCollapsed && <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapsed} />}
      </SidebarToolbar>

      {/* ── Status ── (single-select bucket, same shape as /routines) */}
      <SidebarSection
        label="Status"
        count={statusCounts.all}
        collapsible
        collapsed={!statusOpen}
        onToggle={() => setStatusOpen((v) => !v)}
        className="border-b border-white/[0.06] pb-1"
      >
        {STATUS_FACETS.map((f) => {
          const Icon = f.icon
          const selected = facet === f.id
          const count = statusCounts[f.id]
          return (
            <SidebarRow
              key={f.id}
              as="div"
              data-testid={`chat-tree-status-${f.id}`}
              aria-label={f.label}
              selected={selected}
              onSelect={() => setFacet(f.id)}
            >
              <Icon className={cn("h-3.5 w-3.5 shrink-0", f.tone, count === 0 && !selected && "opacity-40")} />
              <span
                className={cn(
                  "flex-1 truncate",
                  count === 0 && !selected ? "text-foreground/40" : "text-foreground/80",
                )}
              >
                {f.label}
              </span>
              <span
                className={cn(
                  "rounded-full px-1.5 py-px text-[10px] tabular-nums",
                  count === 0
                    ? "text-muted-foreground-soft/50"
                    : selected
                      ? "bg-primary/15 text-primary"
                      : "bg-white/[0.05] text-muted-foreground",
                )}
              >
                {count}
              </span>
            </SidebarRow>
          )
        })}
      </SidebarSection>

      {/* ── Agents ── (the tree proper) */}
      <div className="flex min-h-0 flex-1 flex-col">
        <SidebarSection
          label="Agents"
          count={visibleAgents.length}
          collapsible
          collapsed={!agentsOpen}
          onToggle={() => setAgentsOpen((v) => !v)}
        />
        {agentsOpen && (
          <div className="min-h-0 flex-1 overflow-y-auto pb-2">
            {visibleAgents.length === 0 ? (
              <p className="px-3 py-6 text-center text-xs text-muted-foreground">
                {loading ? "Loading agents…" : agents.length === 0 ? "No agents yet." : "Nothing matches."}
              </p>
            ) : (
              visibleAgents.map((a) => (
                <AgentBranch
                  key={a.id}
                  agent={a}
                  threads={visibleThreads(a)}
                  totalThreads={(threadsByAgent[a.id] ?? []).length}
                  threadsError={threadErrors[a.id] ?? null}
                  onRetryThreads={onRetryThreads}
                  expanded={expandedAgents.has(a.slug)}
                  onToggle={() => toggleAgent(a.slug)}
                  isActiveAgent={a.slug === activeAgentSlug}
                  activeThreadId={activeThreadId}
                  onOpenThread={onOpenThread}
                />
              ))
            )}
          </div>
        )}
      </div>
    </aside>
  )
}

/* ---------------------------------------------------------- one agent */

interface AgentBranchProps {
  agent: ChatTreeAgent
  threads: ChatTreeThread[]
  totalThreads: number
  /** Non-null when this agent's list could not be fetched at all. */
  threadsError: string | null
  onRetryThreads?: () => void
  expanded: boolean
  onToggle: () => void
  isActiveAgent: boolean
  activeThreadId: string | null
  /**
   * `owner` is the agent the thread is filed under today (`chats.agent_id`,
   * single-valued and NOT NULL) — not a claim that a conversation has exactly
   * one agent in it. PRD §7 keeps the vocabulary open for the group threads
   * the schema is being kept ready for.
   */
  onOpenThread: (owner: ChatTreeAgent, threadId: string) => void
}

function AgentBranch({
  agent,
  threads,
  totalThreads,
  threadsError,
  onRetryThreads,
  expanded,
  onToggle,
  isActiveAgent,
  activeThreadId,
  onOpenThread,
}: AgentBranchProps) {
  const unread = threads.reduce((n, t) => n + (t.unread_count ?? 0), 0)
  // No sessions, no disclosure. A chevron is a promise that there is
  // something under the row; on an agent nobody has talked to, opening it
  // used to reveal "No threads yet." — a row whose only content is the
  // admission that it has none.
  const canExpand = totalThreads > 0
  const open = expanded && canExpand

  return (
    <>
      <SidebarRow
        as="div"
        data-testid={`chat-tree-agent-${agent.slug}`}
        // The kit's row is a ListRow, whose prop surface is closed, so the
        // disclosure state cannot ride an aria-expanded here. The accessible
        // name is pinned to the agent instead of drifting with the counts.
        aria-label={agent.name}
        selected={isActiveAgent && activeThreadId === null}
        onSelect={onToggle}
      >
        {canExpand ? (
          <ChevronDown
            aria-hidden
            data-testid={`chat-tree-expander-${agent.slug}`}
            className={cn(
              "h-3 w-3 shrink-0 text-muted-foreground/60 transition-transform duration-150",
              !open && "-rotate-90",
            )}
          />
        ) : (
          <span aria-hidden className="w-3 shrink-0" />
        )}
        <AgentAvatar
          seed={agent.avatar_seed || agent.slug}
          style={agent.avatar_style}
          agentId={agent.id}
          avatarUrl={agent.avatar_url}
          alt=""
          className="h-5 w-5 shrink-0"
        />
        <span className="min-w-0 flex-1">
          <span className="block truncate text-xs text-foreground/90">{agent.name}</span>
          <span className="block truncate text-[10px] text-muted-foreground-soft">
            {agent.role_title || "Agent"}
          </span>
        </span>
        <span
          title={agent.status}
          aria-label={`Status: ${agent.status.toLowerCase()}`}
          className={cn(
            "h-1.5 w-1.5 shrink-0 rounded-full",
            agent.status === "RUNNING"
              ? "animate-pulse bg-primary"
              : agent.status === "STOPPED"
                ? "bg-muted-foreground/30"
                : "bg-success",
          )}
        />
        {/* The unread badge used to live on the Sessions folder row. With the
            folder gone it belongs on the agent — which is also the row that
            is still visible when the branch is closed. */}
        {unread > 0 && (
          <span
            aria-label={`${unread} unread message${unread === 1 ? "" : "s"}`}
            className="rounded-full bg-info/20 px-1.5 py-px text-[10px] leading-none text-info"
          >
            {unread > 99 ? "99+" : unread}
          </span>
        )}
        {/* An em dash, not a 0: the count of a list that failed to load is
            not zero, it is unknown. */}
        <span className="shrink-0 text-[10px] tabular-nums text-muted-foreground/50">
          {threadsError ? "—" : totalThreads}
        </span>
      </SidebarRow>

      {/* Not gated on `expanded`: a failure the reader has to open a row to
          discover is a failure they will read as "no conversations". */}
      {threadsError && (
        <ScopeFailure
          data-testid={`chat-tree-threads-error-${agent.slug}`}
          className="ml-6"
          label={`Could not load ${agent.name}'s conversations.`}
          detail={`${threadsError} — this is not an empty history; the list could not be fetched.`}
          onRetry={onRetryThreads}
        />
      )}

      {open &&
        threads.map((t) => {
          const threadUnread = t.unread_count ?? 0
          const isSelected = isActiveAgent && activeThreadId === t.id
          return (
            <SidebarRow
              key={t.id}
              as="div"
              // 24px — one step in from the agent. It was 48px when a thread
              // hung off a Sessions folder; the folder is gone and so is the
              // level it added.
              indent
              data-testid={`chat-tree-thread-${t.id}`}
              aria-label={threadLabel(t)}
              selected={isSelected}
              onSelect={() => onOpenThread(agent, t.id)}
            >
              <span
                aria-hidden
                className={cn(
                  "h-1.5 w-1.5 shrink-0 rounded-full",
                  threadUnread > 0 ? "bg-info" : "bg-muted-foreground/25",
                )}
              />
              <span
                className={cn(
                  "flex-1 truncate text-xs",
                  t.title ? "text-foreground/80" : "italic text-muted-foreground",
                  threadUnread > 0 && "font-medium text-foreground",
                )}
              >
                {threadLabel(t)}
              </span>
              <span className="shrink-0 text-[10px] tabular-nums text-muted-foreground/50">
                {t.message_count}
              </span>
            </SidebarRow>
          )
        })}

      {/* Expanded, but every thread was filtered out by the search or a
          facet — distinct from "this agent has none", which has no chevron
          to open in the first place. */}
      {open && threads.length === 0 && (
        <p className="ml-6 px-2 py-1.5 text-[11px] text-muted-foreground-soft">
          No sessions match the filter.
        </p>
      )}
    </>
  )
}
