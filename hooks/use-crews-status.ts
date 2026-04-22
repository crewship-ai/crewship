"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

/** Aggregate agent counts by status for the crews toolbar badge. */
export interface CrewsStatus {
  total: number
  running: number
  error: number
  idle: number
}

/**
 * Lightweight hook for toolbar crews status.
 * Fetches agent counts by status and auto-refreshes on real-time events.
 */
export function useCrewsStatus(workspaceId: string | null): CrewsStatus | null {
  const [status, setStatus] = useState<CrewsStatus | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/crews-status?workspace_id=${workspaceId}`)
      if (res.ok) {
        setStatus(await res.json())
      }
    } catch { /* toolbar should never crash */ }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  // Real-time: debounced refresh on agent lifecycle events
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const debouncedRefresh = useCallback(() => {
    if (debounceRef.current !== null) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      void refresh()
    }, 150)
  }, [refresh])

  // Clear any pending timer on unmount / workspace change to avoid
  // stale setStatus after the component is gone.
  useEffect(() => {
    return () => {
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [workspaceId])

  useRealtimeEvent("agent.status", debouncedRefresh)
  useRealtimeEvent("agent.created", debouncedRefresh)
  useRealtimeEvent("agent.deleted", debouncedRefresh)
  useRealtimeEvent("run.started", debouncedRefresh)
  useRealtimeEvent("run.completed", debouncedRefresh)
  useRealtimeEvent("run.failed", debouncedRefresh)

  return status
}
