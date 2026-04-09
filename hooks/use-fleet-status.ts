"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

/** Aggregate agent counts by status for the fleet toolbar badge. */
export interface FleetStatus {
  total: number
  running: number
  error: number
  idle: number
}

/**
 * Lightweight hook for toolbar fleet status.
 * Fetches agent counts by status and auto-refreshes on real-time events.
 */
export function useFleetStatus(workspaceId: string | null): FleetStatus | null {
  const [status, setStatus] = useState<FleetStatus | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/agents/fleet-status?workspace_id=${workspaceId}`)
      if (res.ok) {
        setStatus(await res.json())
      }
    } catch { /* toolbar should never crash */ }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  // Real-time: debounced refresh on agent lifecycle events
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined)
  const debouncedRefresh = useCallback(() => {
    clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => refresh(), 150)
  }, [refresh])

  useRealtimeEvent("agent.status", debouncedRefresh)
  useRealtimeEvent("agent.created", debouncedRefresh)
  useRealtimeEvent("agent.deleted", debouncedRefresh)
  useRealtimeEvent("run.started", debouncedRefresh)
  useRealtimeEvent("run.completed", debouncedRefresh)
  useRealtimeEvent("run.failed", debouncedRefresh)

  return status
}
