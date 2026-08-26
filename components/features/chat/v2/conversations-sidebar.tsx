"use client"

import { useMemo, useState } from "react"
import { MessageSquarePlus, Plus } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import {
  SIDEBAR_WIDTH,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { timeAgo } from "@/lib/time"
import { cn } from "@/lib/utils"

import { parseSessionTimestamp } from "../session-sort"
import type { ChatTreeAgent, ChatTreeThread } from "../chat-tree-sidebar"

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
 * Running and Done are gone and they are not coming back in this shape.
 * "Done" reads `chats.ended_at`, which the sidebar's own comment admits
 * "nothing writes yet" — a facet that can only ever be 0 is a control that
 * teaches the reader their filters do nothing. "Running" is the agent's
 * status, not the thread's, so it answered a question about a different
 * object than the list it filtered. `Live` replaces both with the one thing
 * either endpoint can actually say about a THREAD: it has moved recently.
 */
type Facet = "all" | "unread" | "live"

const FACETS: { id: Facet; label: string }[] = [
  { id: "all", label: "All" },
  { id: "unread", label: "Unread" },
  { id: "live", label: "Live" },
]

/** How recent counts as live. An hour, because a thread an agent replied in
 *  twenty minutes ago is still the one you are working in. */
const LIVE_WINDOW_MS = 60 * 60 * 1000

export interface ConversationRow {
  agent: ChatTreeAgent
  thread: ChatTreeThread
  /** Epoch ms of last activity, already resolved. Sort key and Live test. */
  at: number
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
  now: number,
): ConversationRow[] {
  const q = query.trim().toLowerCase()
  return rows.filter((row) => {
    if (facet === "unread" && !(row.thread.unread_count ?? 0)) return false
    if (facet === "live" && now - row.at > LIVE_WINDOW_MS) return false
    if (!q) return true
    // Search reaches past the facet the same way classic's does, and across
    // the agent name too: "morgan" is how people look for Morgan's threads.
    return (
      (row.thread.title ?? "").toLowerCase().includes(q) ||
      row.agent.name.toLowerCase().includes(q)
    )
  })
}

interface Props {
  agents: ChatTreeAgent[] | null
  threadsByAgent: Record<string, ChatTreeThread[]>
  threadsLoaded: boolean
  activeThreadId?: string | null
  onSelectThread: (agent: ChatTreeAgent, thread: ChatTreeThread) => void
  onStartConversation: (agent: ChatTreeAgent) => void
  className?: string
}

export function ConversationsSidebar({
  agents,
  threadsByAgent,
  threadsLoaded,
  activeThreadId,
  onSelectThread,
  onStartConversation,
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

  // Read once per render rather than per row: a clock that advances between
  // two rows of the same list can put a thread on both sides of the Live
  // boundary in one paint.
  const now = Date.now()
  const visible = useMemo(
    () => filterConversationRows(rows, facet, query, now),
    // `now` is deliberately not a dependency — it changes every render and
    // would defeat the memo. The list re-derives whenever the data or the
    // controls move, which is when the answer can actually differ.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [rows, facet, query],
  )

  const unreadTotal = rows.reduce((n, r) => n + (r.thread.unread_count ?? 0), 0)
  const idle = roster.filter((a) => (threadsByAgent[a.id] ?? []).length === 0)

  return (
    <div
      className={cn(
        SIDEBAR_WIDTH,
        "flex h-full min-h-0 shrink-0 flex-col border-r border-white/[0.06]",
        className,
      )}
    >
      <SidebarToolbar>
        <SidebarSearch
          value={query}
          onValueChange={setQuery}
          placeholder="Search conversations…"
          aria-label="Search conversations"
        />
      </SidebarToolbar>

      {/* Starting a conversation is the primary action of a chat surface and
          classic makes you find an agent with no threads to discover it. */}
      <div className="px-1.5 pb-1.5">
        <button
          type="button"
          onClick={() => roster[0] && onStartConversation(roster[0])}
          disabled={roster.length === 0}
          className={cn(
            "flex w-full items-center gap-2 rounded-md border border-dashed border-primary/35",
            "px-2.5 py-1.5 type-nav text-primary/90 transition-colors",
            "hover:border-primary/60 hover:bg-primary/5 disabled:opacity-40",
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
        className="mx-1.5 mb-1.5 flex overflow-hidden rounded-md border border-white/[0.08]"
      >
        {FACETS.map((f) => {
          const on = facet === f.id
          const n = f.id === "all" ? rows.length : f.id === "unread" ? unreadTotal : null
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
          {!threadsLoaded && rows.length === 0 && (
            <p className="px-3 py-2 text-micro text-muted-foreground">Loading…</p>
          )}
          {threadsLoaded && visible.length === 0 && (
            <p className="px-3 py-2 text-micro text-muted-foreground">
              {query.trim() || facet !== "all"
                ? "Nothing matches."
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
