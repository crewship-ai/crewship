"use client"

import { useCallback, useEffect, useState } from "react"
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

  // Real-time: refresh on agent lifecycle events
  useRealtimeEvent("agent.status", useCallback(() => { refresh() }, [refresh]))
  useRealtimeEvent("agent.created", useCallback(() => { refresh() }, [refresh]))
  useRealtimeEvent("agent.deleted", useCallback(() => { refresh() }, [refresh]))
  useRealtimeEvent("run.started", useCallback(() => { refresh() }, [refresh]))
  useRealtimeEvent("run.completed", useCallback(() => { refresh() }, [refresh]))
  useRealtimeEvent("run.failed", useCallback(() => { refresh() }, [refresh]))

  return status
}
