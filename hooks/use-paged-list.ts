"use client"

import { useCallback, useEffect, useRef, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"

/**
 * The client half of the S1 paging convention (docs/ux/PLAN.md §0):
 * `?limit=&offset=` on the request, the body unchanged, and the total in the
 * `X-Total-Count` header (plus `X-Limit`, `X-Offset`). Lists used to fetch
 * once, get the server's 100-row ceiling and present it as everything —
 * "100 crews" on a workspace with 103, and a deep link past the cap said
 * "not found".
 *
 * `total` is null until the server sends the header, so a caller can tell
 * "the endpoint does not page yet" from "there are zero more".
 */
export interface PagedListState<T> {
  items: T[]
  total: number | null
  hasMore: boolean
  loading: boolean
  loadingMore: boolean
  error: string | null
  loadMore: () => Promise<void>
  refresh: () => Promise<void>
}

export interface UsePagedListOptions<T> {
  /** Base URL with its own query string; limit/offset are appended. Null disables. */
  url: string | null
  limit?: number
  /** How to read the rows out of the body — default: the body itself when it
   *  is an array, else `body.data`. */
  select?: (body: unknown) => T[]
  /** Re-run the first page when this changes (realtime ticks, filters). */
  reloadKey?: unknown
}

function defaultSelect<T>(body: unknown): T[] {
  if (Array.isArray(body)) return body as T[]
  if (body && typeof body === "object" && Array.isArray((body as { data?: unknown }).data)) {
    return (body as { data: T[] }).data
  }
  return []
}

export function readTotalCount(headers: Headers): number | null {
  const raw = headers.get("X-Total-Count")
  if (raw == null) return null
  const n = Number(raw)
  return Number.isFinite(n) && n >= 0 ? n : null
}

export function pagedUrl(base: string, limit: number, offset: number): string {
  const sep = base.includes("?") ? "&" : "?"
  return `${base}${sep}limit=${limit}&offset=${offset}`
}

export function usePagedList<T>({ url, limit = 100, select = defaultSelect, reloadKey }: UsePagedListOptions<T>): PagedListState<T> {
  const [items, setItems] = useState<T[]>([])
  const [total, setTotal] = useState<number | null>(null)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const reqRef = useRef(0)
  const selectRef = useRef(select)
  selectRef.current = select

  const fetchPage = useCallback(
    async (offset: number, append: boolean) => {
      if (!url) return
      const id = ++reqRef.current
      append ? setLoadingMore(true) : setLoading(true)
      setError(null)
      try {
        const res = await apiFetch(pagedUrl(url, limit, offset))
        if (id !== reqRef.current) return
        if (!res.ok) {
          setError(`Could not load (HTTP ${res.status})`)
          return
        }
        const body = await res.json().catch(() => null)
        if (id !== reqRef.current) return
        const rows = selectRef.current(body)
        setTotal(readTotalCount(res.headers))
        setItems((prev) => (append ? [...prev, ...rows] : rows))
      } catch (e) {
        if (id !== reqRef.current) return
        setError(e instanceof Error ? e.message : "Could not load")
      } finally {
        if (id === reqRef.current) {
          setLoading(false)
          setLoadingMore(false)
        }
      }
    },
    [url, limit],
  )

  useEffect(() => {
    if (!url) {
      setItems([])
      setTotal(null)
      return
    }
    void fetchPage(0, false)
    // reloadKey is an intentional trigger.
  }, [url, limit, reloadKey, fetchPage])

  const hasMore = total == null ? false : items.length < total
  const loadMore = useCallback(async () => {
    if (!hasMore || loadingMore) return
    await fetchPage(items.length, true)
  }, [hasMore, loadingMore, items.length, fetchPage])
  const refresh = useCallback(() => fetchPage(0, false), [fetchPage])

  return { items, total, hasMore, loading, loadingMore, error, loadMore, refresh }
}
