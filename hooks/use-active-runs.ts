"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// ActiveRun mirrors the wire shape from GET /pipelines/runs/active.
// Single-instance scope: a multi-replica deployment would only see
// runs on the queried replica until the registry becomes shared.
export interface ActiveRun {
  run_id: string
  workspace_id: string
  pipeline_id: string
  pipeline_slug: string
  concurrency_key: string
  started_at: string
  cancel_requested: boolean
}

// useActiveRuns subscribes to the workspace's in-flight pipeline run
// list. Refreshes on:
//   - mount / workspaceId change
//   - pipeline.run.started / .completed / .failed (covers most state)
//
// We don't poll on a timer — the realtime channel covers
// transitions; if a frontend wants a "stuck run" indicator, it can
// trigger refresh() manually on a long-press or admin action.
export function useActiveRuns(workspaceId: string | null | undefined) {
  const [runs, setRuns] = useState<ActiveRun[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setRuns([])
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const res = await fetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/runs/active`,
        { signal: controller.signal },
      )
      if (controller.signal.aborted) return
      if (!res.ok) {
        setError(`active runs: ${res.status}`)
        setLoading(false)
        return
      }
      const data: ActiveRun[] = await res.json()
      if (controller.signal.aborted) return
      setRuns(Array.isArray(data) ? data : [])
    } catch (e) {
      if (controller.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!controller.signal.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    refresh()
    return () => abortRef.current?.abort()
  }, [refresh])

  // Live refresh on run lifecycle events.
  useRealtimeEvent("pipeline.run.started", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)

  // Cancel POSTs to the cancel endpoint. The registry trips the run
  // ctx; the run loop notices between steps + records CANCELLED.
  // This callback returns immediately — caller renders an optimistic
  // "Cancelling…" badge and the realtime refresh removes the row.
  const cancel = useCallback(
    async (runId: string): Promise<void> => {
      if (!workspaceId) return
      const res = await fetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/runs/${runId}/cancel`,
        { method: "POST" },
      )
      if (!res.ok && res.status !== 404) {
        throw new Error(`cancel run failed: ${res.status}`)
      }
      // Optimistic update — mark cancel_requested locally so the
      // button disables before the next refresh lands.
      setRuns((prev) =>
        prev.map((r) =>
          r.run_id === runId ? { ...r, cancel_requested: true } : r,
        ),
      )
      await refresh()
    },
    [workspaceId, refresh],
  )

  return { runs, loading, error, refresh, cancel }
}
