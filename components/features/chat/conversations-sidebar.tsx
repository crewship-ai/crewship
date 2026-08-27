"use client"

import { useMemo, useState } from "react"
import { MessageSquarePlus, Plus, Search, X } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import {
  SIDEBAR_WIDTH,
  SidebarRow,
  SidebarSection,
} from "@/components/layout/sidebar-kit"
import { timeAgo } from "@/lib/time"
import { cn } from "@/lib/utils"

import { parseSessionTimestamp } from "./session-sort"
import { ScopeFailure } from "./scope-fetch"
import type { ChatTreeAgent, ChatTreeThread } from "./chat-tree-data"

/**
 * The v2 left column: conversations first, roster second.
 *
 * `chat-tree-sidebar` states the rule this follows — *left column is
 * navigation between objects* — and then navigates between AGENTS, which is
 * the wrong object. What the reader picks is a conversation; the agent is an
 * attribute of it, the way a sender is an attribute of an email. Seven agents
 * at two lines each, most of them with nothing to open, is 400px of furniture
 * in front of the one row anybody came for.
 *
 * So: threads at the top level, newest first, with the agent's face carrying
 * the attribution that used to need its own row. Agents nobody has talked to
 * fold into a single "not started yet" row — still one click from a
 * conversation, no longer six rows of scrolling.
 *
 * This adds NO fetch. `useChatTreeData` already fans out
 * `GET /agents/{id}/chats` across the roster and `/chat`'s index already
 * merges and re-sorts exactly this list to render "Recent conversations" in
 * its right pane. The list existed; it was in the wrong half of the screen.
 */

/**
 * Three facets, where classic has four.
 *
 * "Done" is gone and is not coming back in this shape: it reads
 * `chats.ended_at`, which the sidebar's own comment admits "nothing writes
 * yet", and a facet that can only ever be 0 is a control that teaches the
 * reader their filters do nothing.
 *
 * `Live` means what a reader assumes it means: the agent is working RIGHT
 * NOW. It reads `agent.status === "RUNNING"`, which is the one live signal
 * either endpoint carries — the column flips when a chat message starts a
 * run. Classic exposed the same signal as "Running" but let it filter a list
 * of THREADS, so it answered a question about a different object than the one
 * it was narrowing.
 *
 * The honest limitation, and the reason `liveThreadIds` is computed rather
 * than being a per-row test: a RUNNING agent is running *something*, and
 * nothing either endpoint returns says which of its threads. Marking all of
 * them live would be wrong, so the agent's most recently active thread is
 * marked and the rest are not. That is the correct answer unless you have two
 * threads with one agent and wrote to the older one — at which point the
 * indicator is one row off, which is a better failure than lighting up four
 * rows for one run.
 *
 * It replaced "has moved in the last hour", which was measurable but was not
 * what anybody reads the word to mean: a thread the agent answered forty
 * minutes ago is finished, not live.
 */
type Facet = "all" | "unread" | "live"

const FACETS: { id: Facet; label: string }[] = [
  { id: "all", label: "All" },
  { id: "unread", label: "Unread" },
  { id: "live", label: "Live" },
]

export interface ConversationRow {
  agent: ChatTreeAgent
  thread: ChatTreeThread
  /** Epoch ms of last activity, already resolved. The sort key. */
  at: number
}

/**
 * Which threads to call live: for every RUNNING agent, its freshest thread.
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
      const moved = parseSessionTimestamp(t.last_activity_at ?? t.started_at)
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
      // different order in this column than in the classic tree.
      //
      // Not `parse(a) ?? parse(b)`: parseSessionTimestamp answers 0 for a
      // missing or unparseable stamp, and 0 is not nullish, so that form
      // pins every thread with no last_activity_at to the epoch and never
      // reaches started_at at all.
      const at = parseSessionTimestamp(thread.last_activity_at ?? thread.started_at)
      rows.push({ agent, thread, at })
    }
  }
  return rows.sort((a, b) => b.at - a.at)
}

export function filterConversationRows(
  rows: ConversationRow[],
  facet: Facet,
  query: string,
  live: Set<string>,
  /**
   * The thread currently open in the panel.
   *
   * It survives every facet, and that is the fix for the most disorienting
   * thing this column did: open an unread conversation and it is marked read,
   * which under the Unread facet meant the row you had just clicked vanished
   * from under the cursor. The same happened under Live the moment the agent
   * stopped working. Reading something must not delete it from the list you
   * are reading it from — mail clients settled this decades ago.
   *
   * Pinning rather than switching the facet to All: the facet is a choice the
   * reader made, and throwing it away because they opened one row is a bigger
   * surprise than keeping one extra row visible.
   */
  activeThreadId?: string | null,
): ConversationRow[] {
  const q = query.trim().toLowerCase()
  return rows.filter((row) => {
    const isActive = !!activeThreadId && row.thread.id === activeThreadId
    if (!isActive) {
      if (facet === "unread" && !(row.thread.unread_count ?? 0)) return false
      if (facet === "live" && !live.has(row.thread.id)) return false
    }
    if (!q) return true
    // Search reaches past the facet the same way classic's does, and across
    // the agent name too: "morgan" is how people look for Morgan's threads.
    // The active row is pinned against the FACET, not against the query: a
    // search is the reader asking to see a specific thing, and answering it
    // with something they did not ask for is not helpful.
    return (
      (row.thread.title ?? "").toLowerCase().includes(q) ||
      row.agent.name.toLowerCase().includes(q)
    )
  })
}

interface Props {
  agents: ChatTreeAgent[] | null
  threadsByAgent: Record<string, ChatTreeThread[]>
  /**
   * Why the roster is missing, or null when it is not.
   *
   * `useChatTreeData` resolves a failed `/agents` to an EMPTY roster so the
   * page leaves its loading skeleton, which means an empty column is
   * ambiguous by construction: it is either a workspace with no agents or a
   * request that failed. This prop is the disambiguator, and without it this
   * column told someone whose server was unhappy that they had no
   * conversations — the same lie the file download used to tell when it
   * answered "not found" for a file it merely could not open.
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
  className?: string
}

export function ConversationsSidebar({
  agents,
  threadsByAgent,
  loadError = null,
  threadErrors,
  threadsLoaded,
  activeThreadId,
  onSelectThread,
  onStartConversation,
  onRetryRoster,
  onRetryThreads,
  className,
}: Props) {
  const [query, setQuery] = useState("")
  const [facet, setFacet] = useState<Facet>("all")
  const [idleOpen, setIdleOpen] = useState(false)

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
    () => filterConversationRows(rows, facet, query, live, activeThreadId),
    [rows, facet, query, live, activeThreadId],
  )

  // Counts describe what the facet WOULD show, so they are computed without
  // the active-row pin — otherwise "Unread 0" could sit above a list with a
  // row in it, and the number would stop meaning anything.
  const unreadTotal = rows.reduce((n, r) => n + (r.thread.unread_count ?? 0), 0)
  const liveTotal = live.size
  // An agent whose fan-out failed is NOT idle. Listing it under "not started
  // yet" is the per-agent form of the same lie the column-wide one tells, and
  // it is the more dangerous of the two: clicking that row starts a second
  // conversation on top of a history the page simply could not read.
  const failedAgentIds = threadErrors ?? {}
  const idle = roster.filter(
    (a) => !failedAgentIds[a.id] && (threadsByAgent[a.id] ?? []).length === 0,
  )
  const failedAgents = roster.filter((a) => failedAgentIds[a.id])

  return (
    <div
      className={cn(
        SIDEBAR_WIDTH,
        "flex h-full min-h-0 shrink-0 flex-col border-r border-white/[0.06]",
        className,
      )}
    >
      {/**
       * `pt-4`, and it is load-bearing rather than taste.
       *
       * The dashboard shell rounds the top of its content area
       * (`rounded-t-2xl`, app/(dashboard)/layout.tsx), so the first 16px of
       * this column is cut by a curve. The search field used to start inside
       * that curve, which is why its top-left corner looked bitten off. A
       * child cannot un-round its parent, so the fix is to let the curve pass
       * through empty space and start the content below it.
       */}
      <div className="flex flex-col gap-2 px-2 pb-2 pt-4">
        <div
          className={cn(
            "flex h-9 items-center gap-2 rounded-lg px-2.5",
            "border border-white/[0.09] bg-white/[0.03]",
            "transition-colors focus-within:border-primary/50 focus-within:bg-white/[0.05]",
          )}
        >
          <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
          <input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            // Escape clears rather than blurring: the box is a filter over the
            // list below it, so "get me back to everything" is the thing you
            // want one key away, and it was previously three backspaces.
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault()
                setQuery("")
              }
            }}
            placeholder="Search conversations…"
            aria-label="Search conversations"
            className={cn(
              "min-w-0 flex-1 bg-transparent type-nav text-foreground",
              "placeholder:text-muted-foreground focus:outline-none",
            )}
          />
          {query && (
            <button
              type="button"
              onClick={() => setQuery("")}
              aria-label="Clear search"
              className="shrink-0 rounded p-0.5 text-muted-foreground hover:text-foreground"
            >
              <X className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          )}
        </div>

        {/* Starting a conversation is the primary action of a chat surface,
            and classic makes you find an agent with no threads to discover
            it. Solid rather than the dashed outline it started as: a dashed
            border is the vocabulary of a drop zone or a placeholder, and this
            is neither — it is the one button on the column. */}
        {/* Disabled while the roster is unknown: `roster[0]` is what this
            starts a conversation WITH, and after a failed `/agents` there is
            no roster to index. Offering the primary action against data we
            do not have is how a failure becomes a wrong write. */}
        <button
          type="button"
          onClick={() => roster[0] && onStartConversation(roster[0])}
          disabled={roster.length === 0 || !!loadError}
          className={cn(
            "flex h-9 w-full items-center justify-center gap-2 rounded-lg",
            "bg-primary/15 type-nav font-medium text-primary",
            "border border-primary/25 transition-colors",
            "hover:bg-primary/25 hover:border-primary/40",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50",
            "disabled:opacity-40 disabled:hover:bg-primary/15",
          )}
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          <span>New conversation</span>
        </button>
      </div>

      {/* One segmented strip in place of four full-width rows. Same
          information, a tenth of the vertical budget. */}
      <div
        role="tablist"
        aria-label="Filter conversations"
        className="mx-2 mb-2 flex overflow-hidden rounded-lg border border-white/[0.08]"
      >
        {FACETS.map((f) => {
          const on = facet === f.id
          const n = f.id === "all" ? rows.length : f.id === "unread" ? unreadTotal : liveTotal
          return (
            <button
              key={f.id}
              type="button"
              role="tab"
              aria-selected={on}
              onClick={() => setFacet(f.id)}
              className={cn(
                "flex-1 border-r border-white/[0.05] px-2 py-1 text-micro transition-colors last:border-r-0",
                on ? "bg-white/[0.07] font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {f.label}
              {n !== null && n > 0 && <span className="ml-1 tabular-nums opacity-70">{n}</span>}
            </button>
          )
        })}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain pb-2">
        <SidebarSection label="Conversations" count={visible.length}>
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
              // The first message, not a join of all of them: they are almost
              // always the same status, and a 280px column has room for one.
              detail={failedAgentIds[failedAgents[0].id]}
              onRetry={onRetryThreads}
              className="mx-2 my-1"
              data-testid="conversations-fanout-failure"
            />
          )}
          {!loadError && !threadsLoaded && rows.length === 0 && (
            <p className="px-3 py-2 text-micro text-muted-foreground">Loading…</p>
          )}
          {/* "No conversations yet" is a claim about the server's state, so it
              is only made when the server actually answered. */}
          {!loadError && threadsLoaded && visible.length === 0 && (
            <p className="px-3 py-2 text-micro text-muted-foreground">
              {query.trim() || facet !== "all"
                ? "Nothing matches."
                : failedAgents.length > 0
                  ? "No conversations loaded."
                  : "No conversations yet."}
            </p>
          )}
          {visible.map(({ agent, thread, at }) => (
            <SidebarRow
              key={thread.id}
              selected={thread.id === activeThreadId}
              onSelect={() => onSelectThread(agent, thread)}
            >
              <AgentAvatar
                seed={agent.avatar_seed || agent.slug}
                style={agent.avatar_style}
                agentId={agent.id}
                avatarUrl={agent.avatar_url}
                alt=""
                className="h-5 w-5 shrink-0 rounded-[6px]"
              />
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate">{thread.title || "Untitled conversation"}</span>
                <span className="truncate text-micro text-muted-foreground">
                  {agent.name}
                  {at > 0 && ` · ${timeAgo(new Date(at).toISOString())}`}
                </span>
              </span>
              {(thread.unread_count ?? 0) > 0 && (
                <span className="ml-auto shrink-0 rounded-full bg-primary/20 px-1.5 text-micro tabular-nums text-primary">
                  {thread.unread_count}
                </span>
              )}
            </SidebarRow>
          ))}
        </SidebarSection>

        {/* The roster, demoted to what it is on this screen: a way to start
            something, not a thing to browse. */}
        {idle.length > 0 && (
          <SidebarSection label="Not started yet" count={idle.length}>
            <button
              type="button"
              onClick={() => setIdleOpen((v) => !v)}
              aria-expanded={idleOpen}
              className="flex w-full items-center gap-2 px-3 py-1.5 text-micro text-muted-foreground hover:text-foreground"
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
              <span className="ml-2 truncate">
                {idle.map((a) => a.name).join(", ")}
              </span>
            </button>
            {idleOpen &&
              idle.map((a) => (
                <SidebarRow key={a.id} onSelect={() => onStartConversation(a)}>
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
                      <span className="truncate text-micro text-muted-foreground">
                        {a.role_title}
                      </span>
                    )}
                  </span>
                  <MessageSquarePlus
                    className="ml-auto h-3.5 w-3.5 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                </SidebarRow>
              ))}
          </SidebarSection>
        )}
      </div>
    </div>
  )
}
