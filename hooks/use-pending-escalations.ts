"use client"

import { useCallback, useEffect, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

/**
 * Lightweight hook for pending escalation count (toolbar badge).
 * Auto-refreshes on escalation create/resolve events.
 */
export function usePendingEscalations(workspaceId: string | null): number {
  const [count, setCount] = useState(0)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/escalations/pending-count?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setCount(data.count ?? 0)
      }
    } catch { /* toolbar should never crash */ }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  // Real-time: refresh when escalations change
  useRealtimeEvent("escalation.created", useCallback(() => { refresh() }, [refresh]))
  useRealtimeEvent("escalation.resolved", useCallback(() => { refresh() }, [refresh]))

  return count
}
