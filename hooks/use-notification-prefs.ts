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

interface CellEdits {
  previous?: PrefCell
  edits: { cell: PrefCell; settled: boolean; failed: boolean }[]
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
  const scopeRef = useRef<{ workspaceId: typeof workspaceId; pending: Map<string, CellEdits> } | null>(null)

  const refresh = useCallback(async () => {
    const scope = scopeRef.current
    if (!scope || scope.workspaceId !== workspaceId) return
    if (!workspaceId) {
      setCells([])
      setLoading(false)
      setError(null)
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
      scope.pending.clear()
      setCells(Array.isArray(data?.cells) ? data.cells : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    scopeRef.current = { workspaceId, pending: new Map() }
    setCells([])
    refresh()
    return () => {
      scopeRef.current = null
      abortRef.current?.abort()
    }
  }, [refresh, workspaceId])

  // setCell optimistically flips ONE cell and PUTs it, rolling back on
  // failure — this is what a matrix-cell click drives, so it must feel
  // instant rather than waiting a round-trip before the UI updates.
  const setCell = useCallback(
    async (cell: PrefCell): Promise<void> => {
      const scope = scopeRef.current
      if (!workspaceId || !scope || scope.workspaceId !== workspaceId) return
      const key = JSON.stringify([cell.category, cell.channel_id])
      const pending: CellEdits = scope.pending.get(key) ?? {
        previous: cells.find(c => c.category === cell.category && c.channel_id === cell.channel_id),
        edits: [],
      }
      const edit = { cell, settled: false, failed: false }
      pending.edits.push(edit)
      scope.pending.set(key, pending)
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
        edit.failed = true
        throw e
      } finally {
        edit.settled = true
        if (scopeRef.current === scope && scope.pending.get(key) === pending) {
          // Keep the last non-rejected intent. Two rejected overlapping edits
          // must return to saved data, not to each other's optimistic values.
          const replacement = pending.edits.findLast(item => !item.failed)?.cell ?? pending.previous
          if (pending.edits.every(item => item.settled)) scope.pending.delete(key)
          setCells(cur => {
            const index = cur.findIndex(c => c.category === cell.category && c.channel_id === cell.channel_id)
            // A later edit or refresh owns any replacement of this cell.
            if (!pending.edits.some(item => item.cell === cur[index])) return cur
            return replacement ? cur.map((value, i) => i === index ? replacement : value) : cur.filter((_, i) => i !== index)
          })
        }
      }
    },
    [workspaceId, cells],
  )

  return { cells, loading, error, refresh, setCell }
}
