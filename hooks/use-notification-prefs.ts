"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// PrefCell mirrors internal/notifyroute.PrefCell as serialized by
// GET/PUT /api/v1/me/notification-prefs (issue #1412).
export interface PrefCell {
  category: string // one of the 9 categories, or "*" (mute this channel entirely)
  channel_id: string
  state: "off" | "immediate" | "digest" // "digest" is schema-reserved; the UI never writes it (v2)
}

/**
 * Get/set the AUTHENTICATED CALLER's own category x channel notification
 * preference matrix. Self-scoped server-side — there is no "whose matrix"
 * parameter, the caller's session decides it.
 */
export function useNotificationPrefs(workspaceId: string | null | undefined) {
  const [cells, setCells] = useState<PrefCell[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setCells([])
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/me/notification-prefs?workspace_id=${encodeURIComponent(workspaceId)}`,
        { signal: ctrl.signal },
      )
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        setError(`notification prefs: ${res.status}`)
        return
      }
      const data = await res.json()
      if (ctrl.signal.aborted) return
      setCells(Array.isArray(data?.cells) ? data.cells : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  // setCell optimistically flips ONE cell and PUTs it, rolling back on
  // failure — this is what a matrix-cell click drives, so it must feel
  // instant rather than waiting a round-trip before the UI updates.
  const setCell = useCallback(
    async (cell: PrefCell): Promise<void> => {
      if (!workspaceId) return
      const prev = cells
      setCells((cur) => {
        const idx = cur.findIndex((c) => c.category === cell.category && c.channel_id === cell.channel_id)
        if (idx === -1) return [...cur, cell]
        const next = [...cur]
        next[idx] = cell
        return next
      })
      try {
        const res = await apiFetch(
          `/api/v1/me/notification-prefs?workspace_id=${encodeURIComponent(workspaceId)}`,
          {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ cells: [cell] }),
          },
        )
        if (!res.ok) {
          const errBody = await res.json().catch(() => null)
          throw new Error(errBody?.error ?? errBody?.detail ?? `set preference: ${res.status}`)
        }
      } catch (e) {
        setCells(prev) // roll back the optimistic update
        throw e
      }
    },
    [workspaceId, cells],
  )

  return { cells, loading, error, refresh, setCell }
}
