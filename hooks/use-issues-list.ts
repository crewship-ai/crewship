"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type { Mission } from "@/lib/types/mission"

// useIssuesList — the /api/v1/issues fetch behind the issues board.
//
// #2286: the previous inline fetchIssues (OrchestrationLayout) was
//
//   try {
//     const res = await apiFetch(...)
//     if (res.ok) setIssues(await res.json())
//   } catch { /* ignore */ }
//
// which has one branch for "worked" and none for "didn't" — a fetch that
// rejects, or a non-2xx response, left `issues` at whatever it was before
// (empty, on first load) with no signal that anything went wrong. A broken
// board and an empty workspace rendered byte-for-byte identically. This
// hook always resolves to exactly one of three states — loading, `error`
// set, or `issues` populated — so a caller can always tell "empty" from
// "broken" and render accordingly. It also carries the total/has-more the
// backend now reports (X-Total-Count / X-Has-More response headers,
// internal/api/issue_handler_crud.go List), since the board fetched at most
// 100 rows and had no way to say a 101st issue existed at all.
//
// #2285: dev1 observed a transient 403 on this request — made "from a live
// crew", i.e. under bearer/CLI-token auth (lib/server-base.ts's AuthMode;
// internal/api/middleware.go's AuthKindCLIToken is documented as "the PAT
// analogue an agent, CI job, or script holds") rather than an interactive
// session — take the whole board down indistinguishably from every other
// failure. There is exactly one /api/v1/issues request per fetch (apiFetch
// picks one auth mode for the whole app, not a per-request fallback), so
// there is no second "session-authenticated path" to degrade to here; the
// fix is the other half of #2285's acceptance criterion — a 403 is
// classified separately from a network/5xx failure and surfaces a message
// that names the likely cause (a scoped agent/CLI token) and that retrying
// may help, rather than collapsing into the same silent empty board every
// other error produced.
export type IssuesFetchErrorKind = "forbidden" | "unauthorized" | "server" | "network" | "unknown"

export interface IssuesFetchError {
  kind: IssuesFetchErrorKind
  status: number | null
  message: string
}

/** Matches the backend's default (issue_handler_crud.go: `limit > 100` is
 *  rejected back down to 50) at its ceiling, so one page is the largest the
 *  server will ever hand back in one round trip. */
export const ISSUES_PAGE_LIMIT = 100

function classify(status: number): IssuesFetchErrorKind {
  if (status === 403) return "forbidden"
  if (status === 401) return "unauthorized"
  if (status >= 500) return "server"
  return "unknown"
}

function messageFor(kind: IssuesFetchErrorKind, status: number | null): string {
  switch (kind) {
    case "forbidden":
      return "The issues board couldn't load — the request was refused (403). If you're working from an agent or CLI token, its scope may not cover issues; signing in with a full session has access. This can also be a transient token hiccup — retry in a moment."
    case "unauthorized":
      return "The issues board couldn't load — you're signed out, or your session expired. Sign in again to continue."
    case "server":
      return `The server had a problem loading issues (${status ?? "error"}). This is usually transient — retry in a moment.`
    case "network":
      return "Couldn't reach the server to load issues. Check your connection and retry."
    default:
      return `Couldn't load issues${status ? ` (${status})` : ""}.`
  }
}

export interface UseIssuesListResult {
  issues: Mission[]
  /** True only while the FIRST page of the current filter set is in flight
   *  — loadMore does not flip this back on, so the board doesn't blank out
   *  behind its own "load more" click. See `loadingMore` for that case. */
  loading: boolean
  /** True while a loadMore() page fetch is in flight. */
  loadingMore: boolean
  error: IssuesFetchError | null
  /** Total rows matching the current query, from X-Total-Count. Null until
   *  the first response lands (or if a proxy ever strips the header —
   *  callers must treat null as "unknown", not zero). */
  total: number | null
  hasMore: boolean
  /** How many rows are currently loaded — also where the next loadMore()
   *  page starts (see fetchPage's `nextOffset` argument below). */
  offset: number
  limit: number
  /** Re-fetch page 1. If more than one page had been loaded (via loadMore)
   *  before this call, those additional pages are re-fetched too — an
   *  edit/create-triggered refresh (OrchestrationLayout's
   *  handleIssueUpdated / onCreated) must not silently undo pagination
   *  progress by snapping the board back down to a single page. */
  refetch: () => Promise<void>
  /** Fetch the next page and append it to `issues`. No-op while a fetch is
   *  already in flight or when hasMore is false. Safe to call again after a
   *  failure — it always asks for the page starting at the current
   *  (unchanged, since a failed append never touched `issues`) `offset`,
   *  so retrying re-requests exactly the page that failed. */
  loadMore: () => Promise<void>
}

export interface UseIssuesListOptions {
  /** Server-side search (`?q=`, title and identifier). A change re-fetches
   *  page 1 — the board's search box used to filter only the rows it had. */
  search?: string
}

export function useIssuesList(
  workspaceId: string | null | undefined,
  { search = "" }: UseIssuesListOptions = {},
): UseIssuesListResult {
  const [issues, setIssues] = useState<Mission[]>([])
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<IssuesFetchError | null>(null)
  const [total, setTotal] = useState<number | null>(null)
  // Guards against a slow, superseded request clobbering a faster later one
  // (refetch fired twice in quick succession, or loadMore racing a refetch).
  const requestSeq = useRef(0)
  // Mirrors `issues`/`total` synchronously (state updates aren't visible
  // until the next render, and refetch's page-restoration loop below needs
  // up-to-date values BETWEEN awaits within a single call).
  const issuesRef = useRef<Mission[]>([])
  const totalRef = useRef<number | null>(null)
  // True only while the request that is CURRENTLY the latest one (seq ===
  // requestSeq.current) is in flight — see the finally block: only that
  // request's completion clears it, so a superseded request finishing late
  // can never mark "not fetching" while a newer one is still running.
  const fetchingRef = useRef(false)

  /** Fetches one page and returns whether it succeeded (false on error, or
   *  on being superseded by a newer request before it resolved). */
  const fetchPage = useCallback(
    async (nextOffset: number, append: boolean): Promise<boolean> => {
      if (!workspaceId) return false
      const seq = ++requestSeq.current
      fetchingRef.current = true
      if (append) setLoadingMore(true)
      else setLoading(true)
      try {
        const res = await apiFetch(
          `/api/v1/issues?workspace_id=${encodeURIComponent(workspaceId)}&limit=${ISSUES_PAGE_LIMIT}&offset=${nextOffset}${
            search ? `&q=${encodeURIComponent(search)}` : ""
          }`,
        )
        if (seq !== requestSeq.current) return false
        if (!res.ok) {
          const kind = classify(res.status)
          setError({ kind, status: res.status, message: messageFor(kind, res.status) })
          if (!append) {
            issuesRef.current = []
            setIssues([])
          }
          return false
        }
        const page = (await res.json()) as Mission[]
        const totalHeader = res.headers.get("X-Total-Count")
        const nextTotal = totalHeader !== null && totalHeader !== "" ? Number(totalHeader) : null
        const merged = append ? [...issuesRef.current, ...page] : page
        issuesRef.current = merged
        totalRef.current = nextTotal
        setError(null)
        setTotal(nextTotal)
        setIssues(merged)
        return true
      } catch {
        if (seq !== requestSeq.current) return false
        setError({ kind: "network", status: null, message: messageFor("network", null) })
        if (!append) {
          issuesRef.current = []
          setIssues([])
        }
        return false
      } finally {
        if (seq === requestSeq.current) {
          fetchingRef.current = false
          if (append) setLoadingMore(false)
          else setLoading(false)
        }
      }
    },
    [workspaceId, search],
  )

  const refetch = useCallback(async () => {
    const priorCount = issuesRef.current.length
    const ok = await fetchPage(0, false)
    if (!ok) return
    // Restore any pages beyond the first that were loaded via loadMore
    // before this refetch. Each page's correct offset is simply how many
    // rows are loaded so far — the backend always returns a full page
    // (ISSUES_PAGE_LIMIT rows) until the last one, so this naturally lands
    // on page boundaries without tracking them separately.
    while (
      issuesRef.current.length < priorCount &&
      (totalRef.current === null || issuesRef.current.length < totalRef.current)
    ) {
      const more = await fetchPage(issuesRef.current.length, true)
      if (!more) break
    }
  }, [fetchPage])

  const loadMore = useCallback(async () => {
    if (fetchingRef.current) return
    if (totalRef.current !== null && issuesRef.current.length >= totalRef.current) return
    await fetchPage(issuesRef.current.length, true)
  }, [fetchPage])

  useEffect(() => {
    fetchPage(0, false)
    // Re-running on every fetchPage identity change would refetch on every
    // render (fetchPage closes over workspaceId, so its identity already
    // changes exactly when workspaceId or search does) — those two are the
    // real dependencies.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId, search])

  const hasMore = total !== null && issues.length < total

  return {
    issues,
    loading,
    loadingMore,
    error,
    total,
    hasMore,
    offset: issues.length,
    limit: ISSUES_PAGE_LIMIT,
    refetch,
    loadMore,
  }
}
