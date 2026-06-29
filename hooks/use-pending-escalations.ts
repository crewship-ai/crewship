"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

/**
 * Lightweight hook for pending escalation count (toolbar badge).
 * Auto-refreshes on escalation create/resolve events.
 */
export function usePendingEscalations(workspaceId: string | null): number {
  const [count, setCount] = useState(0)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/escalations/pending-count?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setCount(data.count ?? 0)
      }
    } catch { /* toolbar should never crash */ }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  // Real-time: debounced refresh when escalations change
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const debouncedRefresh = useCallback(() => {
    if (debounceRef.current !== null) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      void refresh()
    }, 150)
  }, [refresh])

  // Clear any pending timer on unmount / workspace change.
  useEffect(() => {
    return () => {
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [workspaceId])

  useRealtimeEvent("escalation.created", debouncedRefresh)
  useRealtimeEvent("escalation.resolved", debouncedRefresh)

  return count
}
