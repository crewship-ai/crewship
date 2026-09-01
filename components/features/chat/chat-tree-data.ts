"use client"

import { useEffect, useMemo, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { useWorkspace } from "@/hooks/use-workspace"

import { httpError, scopeErrorMessage, useRetry } from "./scope-fetch"

/** Mirrors `api.ChatKindCountsHeader`. */
export const CHAT_KIND_COUNTS_HEADER = "X-Chat-Kind-Counts"

/**
 * Read `direct=3,routine=182,issue=0,agent=1`.
 *
 * Tolerant on purpose: an unparseable pair is skipped rather than failing the
 * whole header, and a header that yields nothing answers `null` — the same
 * "the server did not say" the absent header means. A count strip is
 * decoration over a list that is otherwise fine, so nothing here may take the
 * list down with it.
 */
export function parseKindCounts(raw: string | null): Record<string, number> | null {
  if (!raw) return null
  const out: Record<string, number> = {}
  for (const pair of raw.split(",")) {
    const [k, v] = pair.split("=")
    const n = Number(v)
    if (!k?.trim() || !Number.isFinite(n)) continue
    out[k.trim()] = n
  }
  return Object.keys(out).length ? out : null
}

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
  /** CHAT · MISSION. An issue runs its work in a MISSION-mode chat. */
  mode?: string | null
  /** UI · CLI · WEBHOOK · CRON · ROUTINE · AGENT (migration v59); NULL on older rows. */
  origin?: string | null
  /**
   * `direct` · `routine` · `issue` · `agent` — mode+origin already decided,
   * server-side (internal/api/chat_kinds.go). Absent from a server older
   * than that change and from a locally minted draft, which is why
   * `classifyThread` still knows how to derive it.
   */
  kind?: string | null
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
 * newest live agents — the ones with threads if anyone has any.
 *
 * What the cap costs changed when the column became a list of CONVERSATIONS
 * rather than a tree of agents. It used to leave a visible agent row reporting
 * a thread count of 0; now the agent has no row of its own, so its
 * conversations are simply absent from the list with nothing to mark their
 * absence. `docs/guides/chat-surface-limits.mdx` says so in those terms.
 */
export const AGENT_FANOUT_CAP = 12

/** Per-agent page size. 12 × 10 is a comfortable superset of what is shown. */
export const PER_AGENT_CHAT_LIMIT = 10

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
  /**
   * Workspace-wide totals per kind, summed across the fan-out.
   *
   * They cannot come from `threadsByAgent`: the fetch is scoped, so the
   * response holds exactly one kind and the other three are absent by
   * construction. The server totals them per agent
   * (`X-Chat-Kind-Counts`) and this sums the scope's own agents.
   *
   * `null` means the server did not say — an older build, or a proxy that
   * dropped the header. The column then shows a count only on the scope it
   * actually fetched, which is what it knows.
   */
  kindCounts: Record<string, number> | null
  threadsLoaded: boolean
  /** Re-run the fan-out. Wired to the Retry beside a failed agent. */
  retryThreads: () => void
  /**
   * Re-read `/agents`. Separate from `retryThreads` because the two failures
   * are separate: the fan-out failing costs you one agent's list, the roster
   * failing costs you the whole column. Without this, `error` was a state the
   * surface could enter and never leave short of a page reload — which is why
   * reporting it needed this to land in the same change.
   */
  retryRoster: () => void
  error: string | null
  wsLoading: boolean
}

/**
 * The tree's data, fetched once and shared by whatever else the page draws
 * from the same lists.
 *
 * `ensureSlug` names the agent the URL asked for, and guarantees it a place in
 * the capped fan-out by sorting it to the front before the slice.
 *
 * It replaces `skipSlug`, which was the mirror-image option for a page that no
 * longer exists: the old `/chat/<agent>` fetched its own agent's sessions and
 * asked to be left out of the fan-out. The surface that replaced it has no
 * second fetch — the fan-out is the only source — so an agent past
 * `AGENT_FANOUT_CAP` arrived with an empty thread list, and the page cannot
 * tell that from an agent nobody has talked to. It would mint a fresh draft on
 * top of a real history.
 *
 * By slug rather than by id, for the same reason the old option was: the slug
 * is known from the URL before any request goes out, so there is no window in
 * which the fan-out fires without it.
 */
export function useChatTreeData<A extends ChatTreeAgent = ChatTreeAgent>({
  ensureSlug,
  kind,
}: { ensureSlug?: string | null; kind?: string | null } = {}): ChatTreeData<A> {
  const { workspaceId, loading: wsLoading } = useWorkspace()

  const [roster, setRoster] = useState<A[] | null>(null)
  const [threadsByAgent, setThreadsByAgent] = useState<Record<string, ChatTreeThread[]>>({})
  const [threadErrors, setThreadErrors] = useState<Record<string, string>>({})
  const [kindCounts, setKindCounts] = useState<Record<string, number> | null>(null)
  const [threadsLoaded, setThreadsLoaded] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const { nonce: threadsNonce, retry: retryThreads } = useRetry()
  const { nonce: rosterNonce, retry: retryRoster } = useRetry()

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
    // `setRoster([])` on failure rather than leaving it null is deliberate and
    // only safe because `error` is now rendered: null keeps the page on its
    // loading skeleton forever, so the failure has to resolve the list AND be
    // reported. One without the other is how "no conversations yet" came to
    // mean "the roster request 500ed".
  }, [workspaceId, rosterNonce])

  // Ghosts are retired agents; starting a conversation with one is not a thing
  // this surface should offer, so they are out of the tree — but still in the
  // roster, which is what resolves a URL.
  const agents = useMemo(() => roster?.filter((a) => !a.expired_at) ?? null, [roster])

  // ── The fan-out ──
  useEffect(() => {
    if (!workspaceId || agents === null) return
    // The named agent goes first, so the cap can never be the reason the one
    // conversation the reader actually asked for is missing.
    const ordered = ensureSlug
      ? [...agents].sort((a, b) => Number(b.slug === ensureSlug) - Number(a.slug === ensureSlug))
      : agents
    const scope = ordered.slice(0, AGENT_FANOUT_CAP)
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
            `?workspace_id=${encodeURIComponent(workspaceId)}&limit=${PER_AGENT_CHAT_LIMIT}` +
            (kind ? `&kind=${encodeURIComponent(kind)}` : "") +
            // The tab strip needs totals for the kinds this request is
            // filtering OUT, so they travel beside the page rather than in
            // it. `?counts=1` because the count is over every chat the agent
            // has, and nothing else asking this endpoint wants to pay for it.
            "&counts=1",
        )
          .then((r) => {
            if (!r.ok) throw httpError(r.status)
            // Optional-chained, and not to appease a stub: the counts
            // decorate a list that is otherwise fine, and the ONE rule this
            // whole path has is that nothing about a number on a tab may take
            // the list down with it. A response object without readable
            // headers — a proxy, a service worker, a test double — is the
            // same "the server did not say" as an absent header, and the
            // column already knows how to draw that.
            const counts = parseKindCounts(r.headers?.get(CHAT_KIND_COUNTS_HEADER) ?? null)
            return r.json().then((rows: unknown) => [rows, counts] as const)
          })
          .then(
            ([rows, counts]) =>
              [
                a.id,
                Array.isArray(rows) ? (rows as ChatTreeThread[]) : [],
                null,
                counts,
              ] as const,
          )
          // One agent's list failing must not blank the column — the other
          // eleven are still worth showing. It must not pass for an EMPTY
          // list either, which is what returning [] here used to do.
          .catch((e: unknown) => [a.id, null, scopeErrorMessage(e), null] as const),
      ),
    ).then((results) => {
      if (cancelled) return
      const lists: Record<string, ChatTreeThread[]> = {}
      const errors: Record<string, string> = {}
      // Summed only over agents that ANSWERED. An agent whose request failed
      // contributes nothing rather than a zero — a total that quietly drops a
      // failed agent's chats is a number that disagrees with the list for a
      // reason nothing on screen explains, which is the same defect
      // `threadErrors` exists to prevent one layer down.
      let totals: Record<string, number> | null = null
      for (const [id, rows, err, counts] of results) {
        if (rows) lists[id] = rows
        else if (err) errors[id] = err
        if (!counts) continue
        totals ??= {}
        for (const [k, n] of Object.entries(counts)) totals[k] = (totals[k] ?? 0) + n
      }
      setThreadsByAgent(lists)
      setThreadErrors(errors)
      setKindCounts(totals)
      setThreadsLoaded(true)
    })
    return () => {
      cancelled = true
    }
    // `kind` is a dependency and not a post-filter, and that is the whole
    // point of it. The page is cut by `LIMIT` on the server, so narrowing
    // after the response arrives narrows a page that routine rows have
    // already filled — the reader asks for their conversations and is told
    // there are none, which is the exact failure this parameter exists to
    // stop. Changing scope therefore re-runs the fan-out.
  }, [workspaceId, agents, ensureSlug, kind, threadsNonce])

  return {
    agents,
    roster,
    threadsByAgent,
    threadErrors,
    kindCounts,
    threadsLoaded,
    retryThreads,
    retryRoster,
    error,
    wsLoading,
  }
}

/* ------------------------------------------------------------------ props */

