"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { journalListResponseSchema, type JournalEntry } from "@/lib/types/journal"

interface UseJournalListOptions {
  workspaceId: string | null
  params?: Record<string, string | undefined>
  /** Page size; backend caps at 500. */
  limit?: number
  enabled?: boolean
  /**
   * Cap the in-memory entries buffer. When set, `prependLive` trims the
   * tail past `maxEntries` so a chatty SSE stream doesn't grow memory
   * unbounded. Pagination via `loadMore` ignores the cap (user-driven).
   */
  maxEntries?: number
}

interface UseJournalListResult {
  entries: JournalEntry[]
  nextCursor: string | null
  loading: boolean
  loadingMore: boolean
  error: string | null
  /** Replace head — call when filters change. */
  refresh: () => Promise<void>
  /** Append next page. */
  loadMore: () => Promise<void>
  /** Prepend a live entry (dedupes by id). */
  prependLive: (entry: JournalEntry) => void
}

/**
 * Paginated fetch of `/api/v1/journal` with keyset cursor + live-prepend API.
 * The hook keeps filter changes cheap by resetting state locally when
 * `paramsKey` changes rather than requiring callers to remount.
 */
export function useJournalList(opts: UseJournalListOptions): UseJournalListResult {
  // Backend caps page size at 500. The default used to be 100 but that
  // triggered scroll-load round-trips on every glance through the list.
  // Grafana / Elastic Discover behaviour: pick a time range, fetch
  // everything in it. 500 per request × eager pagination at the page
  // level → user sees all events for the active window.
  const { workspaceId, params, limit = 500, enabled = true, maxEntries } = opts
  const [entries, setEntries] = useState<JournalEntry[]>([])
  const [nextCursor, setNextCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const reqIdRef = useRef(0)

  const paramsKey = params
    ? Object.entries(params)
        .filter(([, v]) => v !== undefined && v !== "")
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([k, v]) => `${k}=${v}`)
        .join("&")
    : ""

  const buildParams = useCallback(
    (cursor?: string) => {
      const qp = new URLSearchParams()
      if (workspaceId) qp.set("workspace_id", workspaceId)
      qp.set("limit", String(limit))
      if (cursor) qp.set("cursor", cursor)
      if (paramsKey) {
        for (const kv of paramsKey.split("&")) {
          const idx = kv.indexOf("=")
          if (idx === -1) continue
          qp.set(kv.slice(0, idx), kv.slice(idx + 1))
        }
      }
      return qp
    },
    [workspaceId, limit, paramsKey],
  )

  const refresh = useCallback(async () => {
    if (!enabled || !workspaceId) return
    const requestId = ++reqIdRef.current
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(`/api/v1/journal?${buildParams().toString()}`)
      if (!res.ok) {
        if (reqIdRef.current !== requestId) return
        // 404 before handler ships — treat as empty rather than surfacing an
        // error the user can't act on.
        if (res.status === 404) {
          setEntries([])
          setNextCursor(null)
          return
        }
        setError(`Failed to load journal (${res.status})`)
        return
      }
      const json = await res.json()
      const parsed = journalListResponseSchema.safeParse(json)
      if (reqIdRef.current !== requestId) return
      if (!parsed.success) {
        setEntries([])
        setNextCursor(null)
        return
      }
      setEntries(parsed.data.entries)
      setNextCursor(parsed.data.next_cursor ?? null)
    } catch {
      if (reqIdRef.current === requestId) {
        setEntries([])
        setNextCursor(null)
      }
    } finally {
      if (reqIdRef.current === requestId) setLoading(false)
    }
  }, [enabled, workspaceId, buildParams])

  const loadMore = useCallback(async () => {
    if (!nextCursor || loadingMore || !workspaceId) return
    setLoadingMore(true)
    try {
      const res = await fetch(`/api/v1/journal?${buildParams(nextCursor).toString()}`)
      if (!res.ok) return
      const json = await res.json()
      const parsed = journalListResponseSchema.safeParse(json)
      if (!parsed.success) return
      setEntries((prev) => {
        const seen = new Set(prev.map((e) => e.id))
        const merged = [...prev]
        for (const e of parsed.data.entries) if (!seen.has(e.id)) merged.push(e)
        return merged
      })
      setNextCursor(parsed.data.next_cursor ?? null)
    } catch {
      // Ignore; user can retry by scrolling again.
    } finally {
      setLoadingMore(false)
    }
  }, [nextCursor, loadingMore, workspaceId, buildParams])

  const prependLive = useCallback((entry: JournalEntry) => {
    setEntries((prev) => {
      if (prev.some((e) => e.id === entry.id)) return prev
      const next = [entry, ...prev]
      if (maxEntries && next.length > maxEntries) next.length = maxEntries
      return next
    })
  }, [maxEntries])

  useEffect(() => {
    if (!enabled || !workspaceId) {
      setEntries([])
      setNextCursor(null)
      return
    }
    refresh()
  }, [enabled, workspaceId, paramsKey, refresh])

  return { entries, nextCursor, loading, loadingMore, error, refresh, loadMore, prependLive }
}
