"use client"

import { useEffect, useMemo, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"

import { httpError, scopeErrorMessage, useRetry } from "./scope-fetch"

/**
 * The chat surface's data layer: the roster, the per-agent thread fan-out, and
 * the one breakpoint below which a left column is not a column.
 *
 * Extracted from chat-tree-sidebar.tsx when the v2 surface replaced it. The
 * COMPONENT went; this did not, because none of it was ever about drawing a
 * tree — it is what any left column on this surface needs, and keeping it in a
 * file named after a deleted widget would have been the only reason to keep
 * that file.
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

/**
 * How many agents the thread fan-out will ask about. Bounded because a
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

