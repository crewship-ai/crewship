"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { motion } from "motion/react"
import { Activity, ArrowUpRight, MailOpen, Plus, Users } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import {
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { timeAgo } from "@/lib/time"
import { cn } from "@/lib/utils"

import { ScopeFailure } from "./scope-fetch"
import {
  CHAT_SCOPES,
  KIND_META,
  classifyThread,
  groupByRecency,
  routineGroupOf,
  scopeCount,
  threadActivityAt,
  type ChatKind,
  type ChatScope,
} from "./chat-kind"
import type { ChatTreeAgent, ChatTreeThread } from "./chat-tree-data"

/**
 * The left column, built on the shared sidebar-kit primitives so it reads as
 * the same app as /issues and /routines.
 *
 * Those two settled the vocabulary of an in-page sidebar here: Search + Filter
 * in a toolbar, one collapsible bucket section carrying the primary facet with
 * a count on every row, then the list — every row through `SidebarRow` so the
 * selected state is the tokenized brand accent-bar. This column had drifted
 * into its own dialect: a tall solid button, a segmented facet strip, its own
 * search chrome. Same information, three different-looking answers to "how do
 * I narrow a list", one per page.
 *
 * What it does NOT copy is which question the primary facet asks. /routines
 * buckets by STATUS because a routine's states are what you sort it by; this
 * column buckets by KIND, because four different writers put rows in `chats`
 * and only one of them is a conversation:
 *
 *   · a person opening a thread
 *   · a routine, minting one chat PER STEP
 *   · an issue starting work
 *   · an agent delegating to another agent
 *
 * On a workspace that runs routines that is not clutter, it is eviction: the
 * per-agent page is ten rows, a five-step run writes five of them, and after
 * two runs the thread somebody wrote yesterday is off the end of the query.
 * Which is why the bucket strip here is wired to the FETCH (`?kind=`) rather
 * than filtering what came back — see `chat-kind.ts`, and
 * `internal/api/chat_kinds.go` for the half that runs before `LIMIT`.
 */

/* ------------------------------------------------------------ view state */

/**
 * The narrowing that is not the scope: read state, live state, and one agent.
 *
 * All three live in the Filter popover, which is where /issues and /routines
 * put everything that is not their primary bucket. Two of them used to be
 * tabs — a three-way segmented strip of All · Unread · Live — and that shape
 * had three problems, the last one fatal:
 *
 *  · They answer a different question from their neighbour. "All" is a scope,
 *    "Unread" and "Live" are predicates.
 *  · Exclusive is the wrong arity. "Unread routines" is a real question and
 *    the strip could not express it, because choosing Unread threw the scope
 *    away.
 *  · They were permanently visible and usually zero. A control that reads
 *    "Unread 0 · Live 0" on every visit is a control that teaches its reader
 *    that filters here do nothing — which is exactly what got reported.
 *
 * In the popover they compose, they carry their counts, and the Filter button
 * badges how many are on, so nothing is hidden by being one click away.
 */
export interface ConversationFilters {
  unreadOnly: boolean
  liveOnly: boolean
  /** Narrow to one agent's threads. `null` is every agent. */
  agentId: string | null
}

export const NO_FILTERS: ConversationFilters = {
  unreadOnly: false,
  liveOnly: false,
  agentId: null,
}

export function activeFilterCount(f: ConversationFilters): number {
  return (f.unreadOnly ? 1 : 0) + (f.liveOnly ? 1 : 0) + (f.agentId ? 1 : 0)
}

export interface ConversationRow {
  agent: ChatTreeAgent
  thread: ChatTreeThread
  /** Epoch ms of last activity, already resolved. The sort key. */
  at: number
  kind: ChatKind
}

/**
 * Which threads to call live: for every RUNNING agent, its freshest thread.
 *
 * `Live` means what a reader assumes it means — the agent is working RIGHT
 * NOW — and `agent.status === "RUNNING"` is the one live signal either
 * endpoint carries. The honest limitation is unchanged: a RUNNING agent is
 * running *something* and nothing either endpoint returns says which of its
 * threads, so the freshest one is marked and the rest are not. That is right
 * unless you have two threads with one agent and wrote to the older one, at
 * which point the indicator is one row off — a better failure than lighting
 * up four rows for one run.
 *
 * Rows arrive newest-first from `buildConversationRows`, so the first row an
 * agent appears in IS its freshest — no second sort, and no chance of the two
 * orderings disagreeing.
 */
export function liveThreadIds(rows: ConversationRow[]): Set<string> {
  const live = new Set<string>()
  const claimed = new Set<string>()
  for (const row of rows) {
    if (row.agent.status !== "RUNNING") continue
    if (claimed.has(row.agent.id)) continue
    claimed.add(row.agent.id)
    live.add(row.thread.id)
  }
  return live
}

/**
 * Overlay what the PAGE knows about read state onto what the fetch returned.
 *
 * Two rules, and the second one is why `readAt` is a timestamp rather than a
 * set of ids:
 *
 *  · The thread you are looking at is read by definition. The list GET can be
 *    served before the mark-read PUT commits, so the fetched count for the
 *    open thread is routinely stale by one reply.
 *  · A thread stays read only while it has not MOVED since you read it. A
 *    boolean "this is read" survives the agent replying in it — open thread
 *    A, switch to B, let A get an answer, and A is genuinely unread again
 *    while the override keeps forcing zero. Comparing against the thread's
 *    own last activity makes the override expire by itself.
 *
 * Only ever forces a count DOWN, so it cannot invent unread that the server
 * does not have.
 */
export function applyReadOverrides(
  threadsByAgent: Record<string, ChatTreeThread[]>,
  readAt: Record<string, number>,
  activeThreadId: string | null,
): Record<string, ChatTreeThread[]> {
  const out: Record<string, ChatTreeThread[]> = {}
  for (const [agentId, threads] of Object.entries(threadsByAgent)) {
    out[agentId] = threads.map((t) => {
      if (!t.unread_count) return t
      if (t.id === activeThreadId) return { ...t, unread_count: 0 }
      const seen = readAt[t.id]
      if (seen === undefined) return t
      const moved = threadActivityAt(t)
      return moved <= seen ? { ...t, unread_count: 0 } : t
    })
  }
  return out
}

export function buildConversationRows(
  agents: ChatTreeAgent[],
  threadsByAgent: Record<string, ChatTreeThread[]>,
): ConversationRow[] {
  const rows: ConversationRow[] = []
  for (const agent of agents) {
    for (const thread of threadsByAgent[agent.id] ?? []) {
      // Pick the field FIRST, then parse once — the exact shape
      // sortSessionsByActivity uses, so a thread cannot come out in a
      // different order in this column than anywhere else.
      //
      // Not `parse(a) ?? parse(b)`: parseSessionTimestamp answers 0 for a
      // missing or unparseable stamp, and 0 is not nullish, so that form
      // pins every thread with no last_activity_at to the epoch and never
      // reaches started_at at all. `threadActivityAt` is that resolution.
      rows.push({ agent, thread, at: threadActivityAt(thread), kind: classifyThread(thread) })
    }
  }
  return rows.sort((a, b) => b.at - a.at)
}

/**
 * Narrow by the popover facets and the search box.
 *
 * Scope is deliberately absent: it is applied by the FETCH, so a row that is
 * here is already of the right kind. Re-applying it would be a second opinion
 * about a page the server already cut, and the two could disagree — which is
 * the whole class of bug the server-side `kind` parameter exists to remove.
 *
 * `activeThreadId` survives every facet, and that is the fix for the most
 * disorienting thing this column did: open an unread conversation, it is
 * marked read, and under Unread the row you had just clicked vanished from
 * under the cursor. The same happened under Live the moment the agent
 * stopped. Reading something must not delete it from the list you are reading
 * it from — mail clients settled this decades ago.
 */
export function filterConversationRows(
  rows: ConversationRow[],
  filters: ConversationFilters,
  query: string,
  live: Set<string>,
  activeThreadId?: string | null,
): ConversationRow[] {
  const q = query.trim().toLowerCase()
  return rows.filter((row) => {
    const isActive = !!activeThreadId && row.thread.id === activeThreadId
    if (!isActive) {
      if (filters.unreadOnly && !(row.thread.unread_count ?? 0)) return false
      if (filters.liveOnly && !live.has(row.thread.id)) return false
      if (filters.agentId && row.agent.id !== filters.agentId) return false
    }
    if (!q) return true
    // Search reaches past the facets and across the agent name too:
    // "morgan" is how people look for Morgan's threads. The active row is
    // pinned against the FACETS, not against the query — a search is the
    // reader asking to see a specific thing, and answering it with something
    // they did not ask for is not helpful.
    return (
      (row.thread.title ?? "").toLowerCase().includes(q) ||
      row.agent.name.toLowerCase().includes(q)
    )
  })
}

/**
 * Stack the Routines scope by routine instead of by clock.
 *
 * Recency headers are the right structure for conversations and the wrong one
 * for routine steps: a five-step run writes five rows in the same second, so
 * "Today" over twenty identical-looking rows tells the reader nothing they did
 * not already know. What they want to know is which ROUTINE moved, and then
 * which step.
 *
 * Groups are ordered by their freshest member so the strip still reads
 * newest-first at the top level, and a group of one renders as a group of one
 * — the degenerate case is the flat list this replaced, so nothing can go
 * missing when the title does not match the runner's shape.
 */
export function groupRowsByRoutine(
  rows: ConversationRow[],
): { label: string | null; rows: ConversationRow[] }[] {
  const byGroup = new Map<string, ConversationRow[]>()
  for (const row of rows) {
    const { group } = routineGroupOf(row.thread.title)
    const list = byGroup.get(group)
    if (list) list.push(row)
    else byGroup.set(group, [row])
  }
  const groups = [...byGroup.entries()]
  // Grouping that never groups anything is pure chrome. Six routines that
  // each ran one step became six headers over six rows — twelve lines to say
  // what six were already saying, and the headers were the longer half. One
  // unlabelled group is the flat list, which is the honest rendering when no
  // stacking happened.
  //
  // The row reads the same signal (`grouped`) to decide whether to lead with
  // the step or the whole title: without a header carrying the routine's
  // name, a row saying only "echo" has lost which routine it belongs to.
  if (!groups.some(([, g]) => g.length > 1)) {
    return rows.length ? [{ label: null, rows }] : []
  }
  // Insertion order already IS freshest-first: `rows` arrives sorted, so a
  // group is created the first time its freshest member is seen. Sorting
  // again would be a second ordering that could disagree with the first.
  return groups.map(([label, groupRows]) => ({ label, rows: groupRows }))
}

/* ------------------------------------------------------------------ props */

interface Props {
  agents: ChatTreeAgent[] | null
  threadsByAgent: Record<string, ChatTreeThread[]>
  /** Which kind of thing the list is showing. Owned by the page — it drives
   *  the fetch, not a filter, so the column cannot own it. */
  scope: ChatScope
  onScopeChange: (scope: ChatScope) => void
  /** Per-kind totals from the fan-out, or null when the server did not say. */
  kindCounts?: Record<string, number> | null
  /**
   * Why the roster is missing, or null when it is not.
   *
   * `useChatTreeData` resolves a failed `/agents` to an EMPTY roster so the
   * page leaves its loading skeleton, which means an empty column is
   * ambiguous by construction: it is either a workspace with no agents or a
   * request that failed. This prop is the disambiguator, and without it this
   * column told someone whose server was unhappy that they had no
   * conversations.
   */
  loadError?: string | null
  /** Agent id → why its thread list is unknown. An agent in here has an
   *  UNKNOWN list, not an empty one, and must not be filed under "not
   *  started yet". */
  threadErrors?: Record<string, string>
  threadsLoaded: boolean
  activeThreadId?: string | null
  onSelectThread: (agent: ChatTreeAgent, thread: ChatTreeThread) => void
  onStartConversation: (agent: ChatTreeAgent) => void
  /** Re-read the roster. Omitted when the caller has no way to re-ask. */
  onRetryRoster?: () => void
  /** Re-run the per-agent fan-out. */
  onRetryThreads?: () => void
  /** Collapse toggle — rendered in the toolbar next to search, same as the
   *  /issues and /routines explorers. Omitted where the layout cannot fold. */
  onToggleCollapse?: () => void
  /** Overrides the collapse button's accessible name. The control does the
   *  same thing at both sizes — put this column away — but "Collapse sidebar"
   *  is the wrong description of dismissing a drawer. */
  collapseLabel?: string
  className?: string
  /** Injected by tests so bucketing is not at the mercy of the wall clock. */
  now?: number
}

export function ConversationsSidebar({
  agents,
  threadsByAgent,
  scope,
  onScopeChange,
  kindCounts = null,
  loadError = null,
  threadErrors,
  threadsLoaded,
  activeThreadId,
  onSelectThread,
  onStartConversation,
  onRetryRoster,
  onRetryThreads,
  onToggleCollapse,
  collapseLabel,
  className,
  now,
}: Props) {
  const [query, setQuery] = useState("")
  const [filters, setFilters] = useState<ConversationFilters>(NO_FILTERS)
  const [filterOpen, setFilterOpen] = useState(false)
  const [showOpen, setShowOpen] = useState(true)
  const [picking, setPicking] = useState(false)
  const [collapsedGroups, setCollapsedGroups] = useState<Record<string, boolean>>({})

  // A scope change re-runs the fetch, so the rows the facets were narrowing
  // are gone. Carrying "unread only" across into a scope with no unread
  // leaves the list reading as empty for a reason the badge alone does not
  // explain — and the reader did not ask for that narrowing here.
  useEffect(() => setFilters(NO_FILTERS), [scope])

  // `agents ?? []` inline would mint a new array on every render and defeat
  // every memo below it — the list would rebuild and re-sort on each keystroke
  // in the search box.
  const roster = useMemo(() => agents ?? [], [agents])
  const rows = useMemo(
    () => buildConversationRows(roster, threadsByAgent),
    [roster, threadsByAgent],
  )

  const live = useMemo(() => liveThreadIds(rows), [rows])
  const visible = useMemo(
    () => filterConversationRows(rows, filters, query, live, activeThreadId),
    [rows, filters, query, live, activeThreadId],
  )

  // Counts describe what the facet WOULD show, so they are computed without
  // the active-row pin — otherwise "Unread 0" could sit above a list with a
  // row in it and the number would stop meaning anything. ROWS, not messages:
  // the facet keeps rows with any unread, so the count is how many rows that
  // is.
  const unreadTotal = rows.filter((r) => (r.thread.unread_count ?? 0) > 0).length
  const liveTotal = live.size

  // Agents that actually have a thread in this scope. The facet lists what is
  // narrowable, the way /routines lists only agents that authored something —
  // an option that can only ever produce an empty list is not a filter.
  const facetAgents = useMemo(() => {
    const seen = new Map<string, { agent: ChatTreeAgent; count: number }>()
    for (const row of rows) {
      const cur = seen.get(row.agent.id)
      if (cur) cur.count++
      else seen.set(row.agent.id, { agent: row.agent, count: 1 })
    }
    return [...seen.values()].sort((a, b) => a.agent.name.localeCompare(b.agent.name))
  }, [rows])

  // Resolved rather than taken raw, and every read below goes through
  // `activeScope`: an unrecognised value (a stale persisted preference, a
  // caller that forgot the prop) then degrades to Direct everywhere at once,
  // instead of showing the Direct list under a strip with nothing selected
  // and a roster section that silently disappeared.
  const spec = CHAT_SCOPES.find((s) => s.id === scope) ?? CHAT_SCOPES[0]
  const activeScope = spec.id

  /**
   * Agents we KNOW have nothing, which is not the same as agents with no rows.
   *
   * `threadsByAgent` only gains a key when that agent's request came back, so
   * a present-and-empty list is the only evidence that an agent has never been
   * talked to. Three states are absent instead, and none of them is idle: the
   * fan-out has not settled; that agent's request failed (`threadErrors` says
   * so and it gets a failure row); or the agent is past `AGENT_FANOUT_CAP` and
   * was never asked about. All three end the same way if we guess — a row
   * offered as a fresh start, and clicking it mints a draft on top of a
   * history nobody read.
   *
   * Scoped to `direct`, because the question it answers is "who have I not
   * talked to", and an agent no ROUTINE has touched is not somebody you have
   * been neglecting.
   */
  const failedAgentIds = threadErrors ?? {}
  const idle = useMemo(
    () =>
      activeScope === "direct" ? roster.filter((a) => threadsByAgent[a.id]?.length === 0) : [],
    [activeScope, roster, threadsByAgent],
  )
  const failedAgents = roster.filter((a) => failedAgentIds[a.id])

  // Rows the picker offers: every live agent, not just the idle ones. Starting
  // a SECOND conversation with somebody you already talk to is the common
  // case, and the button this replaced could not express it — it called
  // `onStartConversation(roster[0])`, i.e. whichever agent `/agents` happened
  // to return first, with no way to say who you meant.
  const pickable = useMemo(() => {
    const q = query.trim().toLowerCase()
    return q ? roster.filter((a) => a.name.toLowerCase().includes(q)) : roster
  }, [roster, query])

  const grouped = useMemo(() => {
    if (activeScope === "routine") return groupRowsByRoutine(visible)
    return groupByRecency(visible, (r) => r.at, now ?? Date.now()).map((g) => ({
      label: g.label,
      rows: g.rows,
    }))
  }, [activeScope, visible, now])

  const nFilters = activeFilterCount(filters)

  return (
    // Width, border and background belong to the WRAPPER, the way
    // /routines and /issues do it: the collapsed rail is the same element at
    // `w-9`, so a width baked in here would fight it. The column itself is
    // just a full-height flex stack.
    <div className={cn("flex h-full min-h-0 flex-col", className)}>
      {/* ── Search + Filter ── */}
      <SidebarToolbar>
        <div data-chat-search className="min-w-0 flex-1">
          <SidebarSearch
            value={query}
            onValueChange={setQuery}
            placeholder={picking ? "Search agents…" : "Search conversations, agents…"}
            aria-label={picking ? "Search agents" : "Search conversations"}
            // Escape clears rather than blurring: the box is a filter over the
            // list below it, so "get me back to everything" is one key away.
            // With the picker open it closes that first — Escape means "undo
            // the narrowest thing I just did".
            onKeyDown={(e) => {
              if (e.key !== "Escape") return
              e.preventDefault()
              if (picking && !query) setPicking(false)
              else setQuery("")
            }}
          />
        </div>
        <SidebarFilterPopover
          label="Filter conversations"
          activeCount={nFilters}
          onClear={() => setFilters(NO_FILTERS)}
          open={filterOpen}
          onOpenChange={setFilterOpen}
        >
          <SidebarFacet
            label="Read state"
            resetLabel="Any"
            resetActive={!filters.unreadOnly}
            onReset={() => setFilters((f) => ({ ...f, unreadOnly: false }))}
            first
          >
            <SidebarFacetOption
              active={filters.unreadOnly}
              onToggle={() => setFilters((f) => ({ ...f, unreadOnly: !f.unreadOnly }))}
            >
              <MailOpen className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              <span className="flex-1">Unread only</span>
              <FacetCount n={unreadTotal} />
            </SidebarFacetOption>
          </SidebarFacet>

          <SidebarFacet
            label="Activity"
            resetLabel="Any"
            resetActive={!filters.liveOnly}
            onReset={() => setFilters((f) => ({ ...f, liveOnly: false }))}
          >
            <SidebarFacetOption
              active={filters.liveOnly}
              onToggle={() => setFilters((f) => ({ ...f, liveOnly: !f.liveOnly }))}
            >
              <Activity className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
              <span className="flex-1">Live now</span>
              <FacetCount n={liveTotal} />
            </SidebarFacetOption>
          </SidebarFacet>

          {facetAgents.length > 1 && (
            <SidebarFacet
              label="Agents"
              resetLabel="All agents"
              resetActive={!filters.agentId}
              onReset={() => setFilters((f) => ({ ...f, agentId: null }))}
            >
              {facetAgents.map(({ agent, count }) => (
                <SidebarFacetOption
                  key={agent.id}
                  active={filters.agentId === agent.id}
                  onToggle={() =>
                    setFilters((f) => ({
                      ...f,
                      agentId: f.agentId === agent.id ? null : agent.id,
                    }))
                  }
                >
                  <AgentAvatar
                    seed={agent.avatar_seed || agent.slug}
                    style={agent.avatar_style}
                    agentId={agent.id}
                    avatarUrl={agent.avatar_url}
                    alt=""
                    className="h-4 w-4 shrink-0 rounded-[5px]"
                  />
                  <span className="flex-1 truncate">{agent.name}</span>
                  <FacetCount n={count} />
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}

          {facetAgents.length <= 1 && (
            <SidebarFacet
              label="Agents"
              resetLabel="All agents"
              resetActive
              onReset={() => setFilters((f) => ({ ...f, agentId: null }))}
            >
              {/* An empty facet says why rather than showing a lone reset row
                  that does nothing. */}
              <p className="px-3 pb-1 text-[10px] text-muted-foreground/70">
                <Users className="mr-1 inline h-3 w-3" aria-hidden="true" />
                Only one agent has threads here.
              </p>
            </SidebarFacet>
          )}
        </SidebarFilterPopover>
        {onToggleCollapse && (
          <SidebarCollapseButton
            collapsed={false}
            onToggle={onToggleCollapse}
            // Spread only when there IS an override. `SidebarCollapseButton`
            // sets its own aria-label and then spreads `...props` after it, so
            // passing `aria-label={undefined}` does not fall back to the
            // default — React removes the attribute, and the control loses its
            // accessible name entirely on every desktop render.
            {...(collapseLabel ? { "aria-label": collapseLabel, title: collapseLabel } : {})}
          />
        )}
      </SidebarToolbar>

      {/* ── Show ── (single-select bucket, wired to the fetch) */}
      <SidebarSection
        label="Show"
        count={CHAT_SCOPES.length}
        collapsible
        collapsed={!showOpen}
        onToggle={() => setShowOpen((v) => !v)}
        className="border-b border-white/[0.06]"
      >
        {CHAT_SCOPES.map((s) => {
          const Icon = s.icon
          const isSelected = s.id === activeScope
          // From the server's per-kind totals, so a bucket can carry its count
          // even though the fetch only asked for one of them. Null when the
          // server did not say, and then the selected bucket falls back to
          // what it can actually see — never an invented number.
          const total = scopeCount(s.id, kindCounts) ?? (isSelected ? visible.length : null)
          const empty = total === 0
          return (
            <SidebarRow key={s.id} selected={isSelected} onSelect={() => onScopeChange(s.id)}>
              {/* A bucket holding nothing dims to match — the same treatment
                  /routines gives its empty status buckets, so the two columns
                  do not disagree about what "nothing here" looks like. */}
              {/* One neutral for all three, the way the nav rail paints them.
                  A per-concept colour would make the same symbol read
                  differently here than two inches to the left, and it encodes
                  nothing — unlike /routines' status tones, which are claims
                  about what happened. Emphasis comes from selection and the
                  count; a bucket holding nothing dims, which IS a claim. */}
              <Icon
                className={cn(
                  "h-3.5 w-3.5 shrink-0",
                  empty && !isSelected ? "text-foreground/40" : "text-foreground/70",
                )}
                aria-hidden="true"
              />
              <span
                className={cn(
                  "flex-1 truncate",
                  empty && !isSelected ? "text-foreground/40" : "text-foreground/80",
                )}
                title={s.title}
              >
                {s.label}
              </span>
              {total !== null && (
                <span
                  className={cn(
                    "rounded-full px-1.5 py-px text-[10px] tabular-nums",
                    empty
                      ? "text-muted-foreground-soft/50"
                      : isSelected
                        ? "bg-primary/15 text-primary"
                        : "bg-white/[0.05] text-muted-foreground",
                  )}
                >
                  {total}
                </span>
              )}
            </SidebarRow>
          )
        })}
      </SidebarSection>

      {/* ── The list ── */}
      <div className="flex min-h-0 flex-1 flex-col">
        <SidebarSection
          label={picking ? "Start a conversation with" : "Conversations"}
          count={picking ? pickable.length : visible.length}
          actions={
            <button
              type="button"
              onClick={() => {
                setPicking((v) => !v)
                setQuery("")
              }}
              disabled={roster.length === 0 || !!loadError}
              aria-expanded={picking}
              aria-label={picking ? "Cancel" : "New conversation"}
              title={picking ? "Cancel" : "New conversation"}
              className={cn(
                "inline-flex h-5 w-5 items-center justify-center rounded transition-colors",
                "text-muted-foreground/70 hover:bg-white/[0.06] hover:text-foreground",
                "focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/50",
                "disabled:pointer-events-none disabled:opacity-40",
                picking && "bg-primary/15 text-primary",
              )}
            >
              <Plus
                className={cn("h-3.5 w-3.5 transition-transform", picking && "rotate-45")}
                aria-hidden="true"
              />
            </button>
          }
        />

        <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-1">
          {picking ? (
            <>
              {pickable.length === 0 && (
                <p className="px-3 py-2 text-[11px] text-muted-foreground">No agent matches.</p>
              )}
              {pickable.map((a) => (
                <SidebarRow
                  key={a.id}
                  onSelect={() => {
                    setPicking(false)
                    setQuery("")
                    onStartConversation(a)
                  }}
                >
                  <AgentAvatar
                    seed={a.avatar_seed || a.slug}
                    style={a.avatar_style}
                    agentId={a.id}
                    avatarUrl={a.avatar_url}
                    alt=""
                    className="h-5 w-5 shrink-0 rounded-[6px]"
                  />
                  <span className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate">{a.name}</span>
                    {a.role_title && (
                      <span className="truncate text-[10px] text-muted-foreground">
                        {a.role_title}
                      </span>
                    )}
                  </span>
                </SidebarRow>
              ))}
            </>
          ) : (
            <>
              {loadError && (
                <ScopeFailure
                  label="Conversations could not be loaded"
                  detail={loadError}
                  onRetry={onRetryRoster}
                  className="mx-2 my-1"
                  data-testid="conversations-roster-failure"
                />
              )}
              {failedAgents.length > 0 && (
                <ScopeFailure
                  label={
                    failedAgents.length === 1
                      ? `${failedAgents[0].name}'s conversations could not be loaded`
                      : `${failedAgents.length} agents' conversations could not be loaded`
                  }
                  // The first message, not a join of all of them: they are
                  // almost always the same status, and a 280px column has room
                  // for one.
                  detail={failedAgentIds[failedAgents[0].id]}
                  onRetry={onRetryThreads}
                  className="mx-2 my-1"
                  data-testid="conversations-fanout-failure"
                />
              )}
              {!loadError && !threadsLoaded && rows.length === 0 && (
                <p className="px-3 py-2 text-[11px] text-muted-foreground">Loading…</p>
              )}
              {/* An empty claim about the server's state is only made once the
                  server actually answered, and it names the SCOPE — "No
                  conversations yet" under the Routines bucket was a sentence
                  about a list nobody was looking at. */}
              {!loadError && threadsLoaded && visible.length === 0 && (
                <div className="flex flex-col items-start gap-1 px-3 py-2">
                  <p className="text-[11px] text-muted-foreground">
                    {query.trim() || nFilters > 0
                      ? "Nothing matches."
                      : failedAgents.length > 0
                        ? "No conversations loaded."
                        : spec.empty}
                  </p>
                  {activeScope === "direct" && !query.trim() && nFilters === 0 && (
                    <button
                      type="button"
                      onClick={() => setPicking(true)}
                      className="text-[11px] text-primary hover:underline"
                    >
                      Start one with an agent
                    </button>
                  )}
                  {spec.home && !query.trim() && (
                    <Link
                      href={spec.home.href}
                      className="inline-flex items-center gap-1 text-[11px] text-primary hover:underline"
                    >
                      {spec.home.label}
                      <ArrowUpRight className="h-3 w-3" aria-hidden="true" />
                    </Link>
                  )}
                </div>
              )}

              {grouped.map((group) => {
                const key = `${activeScope}:${group.label}`
                const collapsed = !!group.label && !!collapsedGroups[key]
                const body = (
                  <>
                    {group.rows.map((row, i) => (
                      // Rows arrive in sequence rather than all at once, which
                      // is what makes a scope change read as the list
                      // narrowing instead of the list being replaced. Capped:
                      // past a dozen rows a per-row stagger stops being a
                      // cascade and starts being a wait. Same numbers the
                      // /routines explorer uses.
                      <motion.div
                        key={row.thread.id}
                        initial={{ opacity: 0, y: 4 }}
                        animate={{ opacity: 1, y: 0 }}
                        transition={{
                          duration: 0.22,
                          ease: [0.22, 1, 0.36, 1],
                          delay: Math.min(i, 12) * 0.018,
                        }}
                      >
                        <ConversationListRow
                          row={row}
                          scope={activeScope}
                          grouped={!!group.label}
                          selected={row.thread.id === activeThreadId}
                          live={live.has(row.thread.id)}
                          onSelect={() => onSelectThread(row.agent, row.thread)}
                        />
                      </motion.div>
                    ))}
                  </>
                )
                // A one-group list gets NO header of its own. `groupByRecency`
                // answers a single unlabelled group below its minimum, and
                // naming it after the scope put "Direct" on screen twice — the
                // bucket above and a heading over the only list there is. The
                // "Conversations" header already sits above this scroller.
                if (!group.label) return <div key="_">{body}</div>
                return (
                  <SidebarSection
                    key={group.label}
                    label={
                      activeScope === "routine" ? (
                        // A routine's NAME, not a section label. The kit sets
                        // the header slot in uppercase with wide tracking,
                        // which is right for "SHOW" and "CONVERSATIONS" and
                        // wrong for a proper noun — "Pipeline
                        // pln_cmtem1pwl000ae8220cbd" became an unreadable wall
                        // of caps wider than the column. The metrics stay so
                        // the header still reads as a header; only the case
                        // and the tracking come back.
                        <span className="block max-w-[168px] truncate normal-case tracking-normal">
                          {group.label}
                        </span>
                      ) : (
                        group.label
                      )
                    }
                    count={group.rows.length}
                    // Recency headers are labels; routine headers are objects
                    // you can fold away, because a routine with thirty steps is
                    // one thing in the list and thirty rows on screen.
                    collapsible={activeScope === "routine"}
                    collapsed={collapsed}
                    onToggle={() => setCollapsedGroups((c) => ({ ...c, [key]: !collapsed }))}
                  >
                    {body}
                  </SidebarSection>
                )
              })}

              {/* The roster, demoted to what it is on this screen: a way to
                  start something, not a thing to browse. */}
              {idle.length > 0 && (
                <SidebarSection label="Not started yet" count={idle.length}>
                  <button
                    type="button"
                    onClick={() => {
                      setPicking(true)
                      setQuery("")
                    }}
                    className="flex w-full items-center gap-2 px-3 py-1.5 text-[10px] text-muted-foreground hover:text-foreground"
                  >
                    <span className="flex">
                      {idle.slice(0, 4).map((a) => (
                        <AgentAvatar
                          key={a.id}
                          seed={a.avatar_seed || a.slug}
                          style={a.avatar_style}
                          agentId={a.id}
                          avatarUrl={a.avatar_url}
                          alt=""
                          className="-mr-1.5 h-4 w-4 rounded-[5px] ring-1 ring-background"
                        />
                      ))}
                    </span>
                    <span className="ml-2 truncate">{idle.map((a) => a.name).join(", ")}</span>
                  </button>
                </SidebarSection>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

/* ------------------------------------------------------------------- bits */

function FacetCount({ n }: { n: number }) {
  return <span className="text-[10px] tabular-nums text-muted-foreground-soft">{n}</span>
}

/**
 * One row.
 *
 * The second line is where the scope pays off. Under Direct it is what it
 * always was — who and when — because the kind is the same for every row and
 * repeating it would be noise. Under any other scope the KIND goes on the row,
 * because that scope holds more than one (routine steps and delegations share
 * the Routines bucket), and a row that does not say what it is is exactly the
 * ambiguity this whole change is about.
 */
function ConversationListRow({
  row,
  scope,
  grouped,
  selected,
  live,
  onSelect,
}: {
  row: ConversationRow
  scope: ChatScope
  /** True when a header above this row already names its routine. */
  grouped: boolean
  selected: boolean
  live: boolean
  onSelect: () => void
}) {
  const { agent, thread, at, kind } = row
  const meta = KIND_META[kind]
  // Lead with the STEP only when a header above is carrying the routine's
  // name — otherwise every row in the group repeats that name and the thing
  // that distinguishes them is what gets truncated off the end.
  //
  // Conditioned on `grouped`, not on the scope: when nothing grouped there is
  // no header, and a row reading "echo" with the routine stripped off it has
  // lost the only thing that said which routine it belongs to.
  const step = scope === "routine" && grouped ? routineGroupOf(thread.title).step : null
  const primary = step ?? thread.title ?? "Untitled conversation"

  return (
    // `items-center`, and it is a fix rather than taste. `.row-interactive`
    // (app/globals.css) is `display:flex` with no `align-items`, so the
    // default `stretch` applies and every child grows to the row's height.
    // Nothing else in the app noticed: every other sidebar row is ONE line
    // tall, where stretch and centre look identical. This is the first
    // two-line row, and it stretched both children that have a shape of their
    // own — the unread count into a tall capsule, and the avatar's wrapper so
    // far down that the live dot anchored to the bottom of the ROW instead of
    // the bottom of the portrait.
    <SidebarRow selected={selected} onSelect={onSelect} className="items-center">
      <span className="relative shrink-0">
        <AgentAvatar
          seed={agent.avatar_seed || agent.slug}
          style={agent.avatar_style}
          agentId={agent.id}
          avatarUrl={agent.avatar_url}
          alt=""
          className="h-5 w-5 rounded-[6px]"
        />
        {/* The live dot rides the portrait instead of taking a column of its
            own: it is a property of the agent in that row, and a 280px column
            has no width to spend on a fourth element. */}
        {live && (
          <span
            className="absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full bg-success ring-2 ring-background"
            aria-label="Running"
          />
        )}
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate">{primary}</span>
        <span className="flex min-w-0 items-center gap-1 truncate text-[10px] text-muted-foreground">
          {scope !== "direct" && (
            <>
              <meta.icon className="h-3 w-3 shrink-0" aria-hidden="true" />
              <span className="sr-only">{meta.label}</span>
            </>
          )}
          <span className="truncate">
            {agent.name}
            {at > 0 && ` · ${timeAgo(new Date(at).toISOString())}`}
          </span>
        </span>
      </span>
      {(thread.unread_count ?? 0) > 0 && (
        // The app's count-bubble geometry, the one bar-menu's bell badge
        // already uses: a 16px box with a 16px floor on its width, so a single
        // digit is a circle and only a two- or three-digit count grows into a
        // capsule. `px-1.5` with no height was a capsule at every count.
        <span
          className={cn(
            "ml-auto flex h-4 min-w-[16px] shrink-0 items-center justify-center",
            "rounded-full bg-primary/20 px-1 text-[9px] font-semibold tabular-nums text-primary",
          )}
        >
          {thread.unread_count}
        </span>
      )}
    </SidebarRow>
  )
}
