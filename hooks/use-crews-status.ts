"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

/** Aggregate agent counts by status for the crews toolbar badge.
 *
 * `queued` counts ASSIGNMENTS in QUEUED state — the per-crew admission
 * queue (PR #396) parks dispatches when a crew's slot budget is
 * saturated. Before this field existed, queued dispatches showed up
 * as `error` in the toolbar (because the agent itself wasn't running,
 * the dispatcher just hadn't claimed a slot yet), giving the
 * misleading "12 errors" reading on a healthy-but-busy workspace.
 *
 * `queued` is independent of `running` / `idle` / `error` — those
 * count agents, this counts in-flight dispatches. A workspace can
 * show "0 running, 12 queued" if every crew is at capacity but
 * agents themselves are still IDLE between assignments. Old servers
 * (no QUEUED support) omit the field; the hook normalises that to 0
 * so consumers can treat the count as a plain number.
 */
export interface CrewsStatus {
  total: number
  running: number
  error: number
  idle: number
  queued: number
}

/**
 * Lightweight hook for toolbar crews status.
 * Fetches agent counts by status and auto-refreshes on real-time events.
 */
/** Matches use-active-runs' POLL_MS so the toolbar's two live surfaces
 *  cannot fall more than one tick out of step. */
const CREWS_STATUS_POLL_MS = 6000

export function useCrewsStatus(workspaceId: string | null): CrewsStatus | null {
  const [status, setStatus] = useState<CrewsStatus | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/agents/crews-status?workspace_id=${workspaceId}`)
      if (res.ok) {
        const raw = (await res.json()) as Partial<CrewsStatus> | null
        // Normalise: server may omit `queued` on older builds, and a
        // malformed payload shouldn't blow up downstream string
        // building. Coerce every count to a finite number so the
        // tooltip never renders "NaN running".
        setStatus({
          total: Number(raw?.total ?? 0) || 0,
          running: Number(raw?.running ?? 0) || 0,
          error: Number(raw?.error ?? 0) || 0,
          idle: Number(raw?.idle ?? 0) || 0,
          queued: Number(raw?.queued ?? 0) || 0,
        })
      }
    } catch { /* toolbar should never crash */ }
  }, [workspaceId])

  useEffect(() => { refresh() }, [refresh])

  // Poll as well as listen.
  //
  // Events alone were the whole refresh story here, and they are not a
  // guarantee: a reconnect, a dropped frame or a tab backgrounded across a
  // short run all cost one, and the pill then reported "Crews idle" through an
  // agent that was working — indefinitely, because nothing else would correct
  // it. The Activity panel a few pixels away polls on top of the same events
  // and so healed itself, which is exactly how the two came to disagree on
  // screen while the SERVER agreed with itself the whole time.
  //
  // Same interval as that panel (hooks/use-active-runs.ts), so the two cannot
  // drift by more than one tick. Events still drive the immediate update; this
  // is the floor, not the mechanism.
  useEffect(() => {
    if (!workspaceId) return
    const t = setInterval(() => { void refresh() }, CREWS_STATUS_POLL_MS)
    return () => clearInterval(t)
  }, [workspaceId, refresh])

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
  // Queue lifecycle (PR #396 — Phase 1B). Without these the toolbar's
  // "queued" count goes stale until the next agent/run event nudges a
  // refresh. The 150ms debounce above coalesces a burst of unqueue
  // events during a queue drain into a single server hit.
  useRealtimeEvent("assignment_queued", debouncedRefresh)
  useRealtimeEvent("assignment_unqueued", debouncedRefresh)

  return status
}
