"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import {
  ChevronDown,
  Inbox,
  Activity,
  CheckCircle2,
  Mail,
  MessageSquarePlus,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { prefersReducedMotion, spring } from "@/lib/motion"
import { emitChatEvent } from "@/lib/telemetry"
import { formatDateTime, timeAgo } from "@/lib/time"
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

/** How long the search box has to be still before a keystroke counts as a
 *  search. Roughly the ⌘K debounce, so the two doors are comparable. */
const SEARCH_TELEMETRY_DEBOUNCE_MS = 250

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
 *                    Ship the export        2m ago      (thread, 24px)
 *                    Weekly summary        30m ago
 *                  Bob Robot                            (no threads, no chevron)
 *
 * **An agent row is a FILTER.** Click one and the other agents animate away,
 * leaving that agent expanded with its threads; click the same row again and
 * they animate back. Seven agents, six of them with no threads and a role line
 * each, is a lot of furniture around one conversation — and narrowing it is a
 * thing the reader does, not a thing the address bar does.
 *
 *   click an agent  → `filterSlug` toggles, and the branch unfolds. No router,
 *                     no URL write, no fetch, no remount.
 *   click the ▾     → open/close, and only that. So does ←/→ on the row.
 *
 * This replaces a route-driven "focus": clicking an agent used to navigate to
 * `/chat/<slug>`, and being on that route was what narrowed the tree. It cost
 * no state, and it cost a full page transition per pick — the dashboard chrome
 * tore down and rebuilt every time somebody looked at a different agent. The
 * user cannot feel a `useState`; they feel every transition. So the filter is
 * local state, `activeAgentSlug` narrows nothing any more, and `/chat/<slug>`
 * lists every agent until the reader says otherwise.
 *
 * Two things ride along with the filter, both inherited from the version it
 * replaces because both were right: a typed search reaches PAST it (a search
 * box that cannot see six of seven agents is a lie), and the STATUS facets
 * count what the narrowed list can show, saying whose they are in the section
 * header so the number never changes meaning in silence.
 *
 * **Starting a conversation has its own row.** The plain click belongs to the
 * filter now, so the defect that started all this — an agent nobody has talked
 * to had an inert row, on the one surface whose job is to make talking to it
 * easy — is answered by an explicit "Start a conversation" row under a filtered
 * agent with no threads. It is a `SidebarRow`, so it is tabbable and answers
 * Enter/Space. It reports `onOpenAgent(agent)` and POSTs nothing; a click is
 * not a message.
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
  /**
   * Slug of the agent whose surface is on screen, or null on the index.
   *
   * Selection and auto-expand ONLY. It used to also narrow the list to that
   * one agent, which made every pick a route change; the narrowing is the
   * filter's job now, and the filter is off until the reader turns it on.
   */
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
  /**
   * "Start a conversation with this agent" — the `Start a conversation` row
   * under a filtered agent that has none, NOT the agent row itself (that is
   * the filter).
   *
   * The tree reports the pick and nothing else. `/chat` has no panel to swap,
   * so it navigates; `/chat/<slug>` swaps the agent in place. Neither creates
   * a session: a click is not a message.
   *
   * Optional — without it a threadless agent simply says it has no
   * conversations, which is the truth, rather than offering a dead row.
   */
  onOpenAgent?: (agent: ChatTreeAgent) => void
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

/**
 * "2m ago" for the row, and the exact moment for its tooltip — both read off
 * the SAME timestamp the list is sorted by, and through the SAME parser.
 *
 * `timeAgo` alone would not do: it hands the string to `new Date`, which reads
 * the legacy SQLite format ("2026-07-01 10:00:00", implicitly UTC) in the
 * local zone, so a row could be labelled hours away from where the ordering
 * put it. A label that contradicts the order is worse than no label — and the
 * label is the whole reason the order is legible.
 */
function threadActivity(t: ChatTreeThread): { label: string; exact?: string } {
  const ms = parseSessionTimestamp(t.last_activity_at ?? t.started_at)
  // An em dash, not "just now": a timestamp we could not read is unknown, and
  // this row is at the bottom of the list precisely because it is.
  if (!ms) return { label: "—" }
  const iso = new Date(ms).toISOString()
  return { label: timeAgo(iso), exact: formatDateTime(iso) }
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
  onOpenAgent,
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
  /**
   * The one agent the reader has narrowed to, or null for "all of them".
   *
   * Local, deliberately. Its predecessor read the agent off `activeAgentSlug`
   * — no new state, at the price of a route change per pick, which remounts
   * the dashboard chrome on the static-export build. Nobody can feel a piece
   * of state; everybody feels a page transition.
   */
  const [filterSlug, setFilterSlug] = useState<string | null>(null)

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

  const expandAgent = useCallback((slug: string) => {
    setExpandedAgents((prev) => (prev.has(slug) ? prev : new Set(prev).add(slug)))
  }, [])

  const collapseAgent = useCallback((slug: string) => {
    setExpandedAgents((prev) => {
      if (!prev.has(slug)) return prev
      const next = new Set(prev)
      next.delete(slug)
      return next
    })
  }, [])

  const q = search.trim().toLowerCase()

  /**
   * The agent the filter is on, or null for "all of them". Two things suspend
   * it without clearing it:
   *
   *  · a typed search, which must be able to find the agents it hides;
   *  · a slug that is not in this list (the roster moved under us — a filter
   *    pinned to somebody who is gone must not empty the column).
   */
  const filterAgent = useMemo(() => {
    if (!filterSlug || q) return null
    return agents.find((a) => a.slug === filterSlug) ?? null
  }, [agents, filterSlug, q])

  /**
   * The agent row was picked. Toggle the filter, and unfold on the way in —
   * narrowing to one agent in order to show it closed would be a filter that
   * hides what it was asked to reveal.
   *
   * Nothing else happens here. No navigation, no fetch, no callback: the whole
   * point of the redo is that this costs a render.
   */
  const pickAgent = useCallback(
    (slug: string) => {
      setFilterSlug((prev) => (prev === slug ? null : slug))
      expandAgent(slug)
    },
    [expandAgent],
  )

  /**
   * The branches coming and going.
   *
   * `mode="popLayout"` on the list below (see the render) takes an exiting
   * branch out of the flow immediately, so the agent that STAYS springs up
   * into the gap while the others fade left — rather than everything waiting
   * on a height collapse to finish. `layout` is what animates that reflow.
   *
   * Reduced motion is not a slower version of this. It is no version of it:
   * no layout animation, no travel, and a zero-length transition, so the rows
   * are simply there or not.
   */
  const reduced = prefersReducedMotion()
  const branchMotion = reduced
    ? {
        layout: false,
        initial: false as const,
        animate: { opacity: 1 },
        exit: { opacity: 0 },
        transition: { duration: 0 },
      }
    : {
        layout: true as const,
        initial: { opacity: 0, x: -8 },
        animate: { opacity: 1, x: 0 },
        exit: { opacity: 0, x: -8 },
        transition: spring.smooth,
      }

  // Counts are computed over EVERYTHING loaded, not over the post-facet view —
  // otherwise picking "Unread" would make every other facet read 0, which is
  // the bug /routines already fixed once.
  //
  // What they count is the SCOPE of the list beneath them: the workspace when
  // the filter is off, the one agent it is on when it is on. A workspace-wide
  // "Unread 12" hanging over a list that can only ever show this agent's two is
  // a number the view cannot act on — and the facet is a filter on that list,
  // so it would also read as a promise the click cannot keep. The section
  // header carries the agent's name while the scope is narrowed, because a
  // number that changes meaning between views without saying so is worse than
  // either meaning. (The scope used to be the ROUTE's agent; it is the
  // filter's now, which is the only thing that narrows this column.)
  const statusCounts = useMemo(() => {
    const counts: Record<StatusFacet, number> = { all: 0, unread: 0, running: 0, done: 0 }
    for (const a of filterAgent ? [filterAgent] : agents) {
      for (const t of threadsByAgent[a.id] ?? []) {
        counts.all++
        if ((t.unread_count ?? 0) > 0) counts.unread++
        if (a.status === "RUNNING") counts.running++
        if (t.ended_at != null) counts.done++
      }
    }
    return counts
  }, [agents, filterAgent, threadsByAgent])

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
    // The filter first: while it is on, the only row the section can hold is
    // that agent's — the facets and the origin filter still apply to it, so
    // picking "Unread" on an agent with none empties the list rather than
    // quietly widening it back out.
    const scope = filterAgent ? agents.filter((a) => a.slug === filterAgent.slug) : agents
    const matching = scope.filter((a) => {
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
  }, [agents, filterAgent, threadsByAgent, visibleThreads, facet, origins, q])

  /* ------------------------------------------------------------------ *
   *  Measurement (lib/telemetry.ts)
   *
   *  This box and ⌘K are two doors onto one question — "where was that
   *  conversation" — and they answer it differently: this one filters titles
   *  already in hand, ⌘K asks the server about message bodies. Somebody who
   *  searches here, finds nothing, and then reaches for ⌘K is saying this
   *  scope is too narrow, and that is only legible if both doors emit the
   *  same event under a different `source`.
   *
   *  A session title is derived from its first message, so it is content
   *  wearing a label's clothes. What is recorded is how many rows survived
   *  and which rank was opened — never which row, and never the search text.
   * ------------------------------------------------------------------ */
  const searchResultIds = useMemo(() => {
    if (!q) return []
    return visibleAgents.flatMap((a) => visibleThreads(a).map((t) => t.id))
  }, [q, visibleAgents, visibleThreads])

  // Debounced, because the list re-filters on every keystroke and "rebuild"
  // typed out is one search, not seven.
  useEffect(() => {
    if (!q) return
    const timer = setTimeout(() => {
      emitChatEvent("conversation_search_run", {
        result_count: searchResultIds.length,
        has_results: searchResultIds.length > 0,
        source: "sidebar",
      })
    }, SEARCH_TELEMETRY_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [q, searchResultIds])

  const handleOpenThread = useCallback(
    (owner: ChatTreeAgent, threadId: string) => {
      const position = searchResultIds.indexOf(threadId)
      // Only while a search is running. Opening a thread from the unfiltered
      // tree is navigation, not a search result, and counting it as one would
      // put a ceiling of 100 % on every ranking question worth asking.
      if (position >= 0) {
        emitChatEvent("conversation_search_result_opened", {
          session_id: threadId,
          position,
          result_count: searchResultIds.length,
          source: "sidebar",
        })
      }
      onOpenThread(owner, threadId)
    },
    [searchResultIds, onOpenThread],
  )

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
        // Named scope, not a bare "Status": while the filter is on these
        // numbers describe one agent, and the reader has to be able to see
        // that from the counts themselves.
        label={filterAgent ? `Status · ${filterAgent.name}` : "Status"}
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
          // Clearing the filter, in the header rather than as a row in the
          // list. This is NOT the "All agents" row it replaces: that one was
          // a `router.push("/chat")` — the way out of a NAVIGATION — and it is
          // gone with the navigation. This writes one piece of local state,
          // and it exists because a filter whose only off-switch is "click the
          // same row again" is a filter people get stuck inside.
          actions={
            filterAgent ? (
              <button
                type="button"
                data-testid="chat-tree-clear-filter"
                onClick={() => setFilterSlug(null)}
                className="rounded px-1.5 py-0.5 text-[10px] text-muted-foreground/70 hover:bg-white/[0.05] hover:text-foreground"
              >
                Show all {agents.length}
              </button>
            ) : undefined
          }
        />
        {agentsOpen && (
          // `relative`: popLayout takes an exiting branch out of the flow by
          // positioning it absolutely, and it has to land against this list
          // rather than against whatever happens to be positioned above it.
          <div className="relative min-h-0 flex-1 overflow-y-auto pb-2">
            {visibleAgents.length === 0 ? (
              <p className="px-3 py-6 text-center text-xs text-muted-foreground">
                {loading ? "Loading agents…" : agents.length === 0 ? "No agents yet." : "Nothing matches."}
              </p>
            ) : (
              // `initial={false}`: the column must not perform a seven-row
              // entrance every time the page mounts. Rows added or removed
              // AFTER that — which is exactly the filter toggling — animate.
              <AnimatePresence initial={false} mode="popLayout">
                {visibleAgents.map((a) => (
                  <motion.div key={a.id} {...branchMotion}>
                    <AgentBranch
                      agent={a}
                      threads={visibleThreads(a)}
                      totalThreads={(threadsByAgent[a.id] ?? []).length}
                      threadsError={threadErrors[a.id] ?? null}
                      onRetryThreads={onRetryThreads}
                      expanded={expandedAgents.has(a.slug)}
                      onToggle={() => toggleAgent(a.slug)}
                      onExpand={() => expandAgent(a.slug)}
                      onCollapse={() => collapseAgent(a.slug)}
                      // The row is the filter. It never navigates and never
                      // calls back — see pickAgent.
                      onSelect={() => pickAgent(a.slug)}
                      filtered={filterAgent?.slug === a.slug}
                      // Only offered where the plain click no longer reaches:
                      // an agent the reader has narrowed to that has nothing
                      // to open. Anywhere else it would be a second, quieter
                      // copy of the header's New session button.
                      onStartConversation={onOpenAgent ? () => onOpenAgent(a) : undefined}
                      isActiveAgent={a.slug === activeAgentSlug}
                      activeThreadId={activeThreadId}
                      onOpenThread={handleOpenThread}
                    />
                  </motion.div>
                ))}
              </AnimatePresence>
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
  /** The chevron: open ↔ closed, and nothing else. */
  onToggle: () => void
  /** ArrowRight / the row's own click — reveal, never hide. */
  onExpand: () => void
  /** ArrowLeft. */
  onCollapse: () => void
  /** The row was picked: toggle the filter onto (or off) this agent. */
  onSelect: () => void
  /** True while this agent IS the filter — the reason the others are gone. */
  filtered: boolean
  /**
   * Offered under a filtered agent with no threads, and nowhere else: the
   * plain click is the filter now, so starting a conversation needs a row of
   * its own. Undefined when the caller cannot start one.
   */
  onStartConversation?: () => void
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
  onExpand,
  onCollapse,
  onSelect,
  filtered,
  onStartConversation,
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
      {/* The row is the filter, so open/close needs a key of its own — the
          same ←/→ the crews explorer's tree answers. It lives on a wrapper
          because the kit's row is a ListRow, whose prop surface is closed. */}
      <div
        onKeyDown={(e) => {
          if (!canExpand) return
          if (e.key === "ArrowRight" && !open) {
            e.preventDefault()
            onExpand()
          }
          if (e.key === "ArrowLeft" && open) {
            e.preventDefault()
            onCollapse()
          }
        }}
      >
        <SidebarRow
          as="div"
          data-testid={`chat-tree-agent-${agent.slug}`}
          // The kit's row is a ListRow, whose prop surface is closed, so the
          // disclosure state cannot ride an aria-expanded here. The accessible
          // name is pinned to the agent instead of drifting with the counts.
          aria-label={agent.name}
          // The row is a toggle, so it says so where a screen reader can hear
          // it — ListRow already emits aria-pressed from `selected`, and the
          // filter is the state this row actually toggles.
          selected={filtered || (isActiveAgent && activeThreadId === null)}
          onSelect={onSelect}
        >
          {canExpand ? (
            // Presentational, not a control — the same call the crews explorer
            // made: a role="button" inside a row that is itself role="button" is
            // `nested-interactive`, and a nameless one is `aria-command-name` on
            // top of it. Pointer users get the click; keyboard users get ←/→ on
            // the row above, which a screen reader announces once.
            <span
              aria-hidden="true"
              data-testid={`chat-tree-expander-${agent.slug}`}
              className="shrink-0"
              onClick={(e) => {
                e.stopPropagation()
                onToggle()
              }}
            >
              <ChevronDown
                className={cn(
                  "h-3 w-3 shrink-0 text-muted-foreground/60 transition-transform duration-150",
                  !open && "-rotate-90",
                )}
              />
            </span>
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
      </div>

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
          const activity = threadActivity(t)
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
              {/* The order IS most-recent-first (sortSessionsByActivity, on the
                  same last_activity_at the server sorts by) — this is what
                  lets a reader see that without being told, which is what was
                  actually being asked for by "more sorted". It replaces the
                  message count: 280px minus an indent holds one trailing
                  number, and the count of messages says nothing about where a
                  row sits in the list. The absolute time is on the title, for
                  the moment "2d ago" is not precise enough. */}
              <span
                title={activity.exact}
                className="shrink-0 text-[10px] tabular-nums text-muted-foreground/50"
              >
                {activity.label}
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

      {/* The original defect, answered. An agent nobody has talked to has no
          threads and no chevron, and now that the plain click belongs to the
          filter it would once again be a row that does nothing — on the one
          surface whose job is to make talking to it easy. So: an explicit row,
          under the agent the reader has narrowed to, and a SidebarRow rather
          than a bare link so it is tabbable and answers Enter/Space like every
          other row in this column. */}
      {filtered && !canExpand && !threadsError && onStartConversation && (
        <SidebarRow
          as="div"
          indent
          data-testid={`chat-tree-start-${agent.slug}`}
          aria-label={`Start a conversation with ${agent.name}`}
          onSelect={onStartConversation}
        >
          <MessageSquarePlus aria-hidden className="h-3 w-3 shrink-0 text-primary/70" />
          <span className="min-w-0 flex-1 truncate text-xs text-foreground/80">
            Start a conversation
          </span>
        </SidebarRow>
      )}
    </>
  )
}
