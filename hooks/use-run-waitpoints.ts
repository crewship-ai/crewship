"use client"

import { useCallback, useEffect, useState } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { listPendingWaitpoints, type PendingWaitpoint } from "@/lib/api/waitpoints"

// useRunWaitpoints — pending waitpoints scoped to one run.
//
// The /pipelines/waitpoints endpoint returns workspace-wide pending
// rows; we filter to the active run on the client. Cheap because the
// list is capped at 200 server-side and a healthy workspace usually
// has 0–5 pending at any given moment.
//
// Refresh triggers: realtime `pipeline.waitpoint.created` event for
// this run, plus a single fetch on run change. The approve flow is
// fire-and-forget — the action endpoint returns 200, then a
// `pipeline.run.completed` realtime event eventually fires which
// triggers a refresh elsewhere. We don't manually refresh here on
// approve to keep the hook stateless w.r.t. user actions.

export function useRunWaitpoints(
  workspaceId: string | null | undefined,
  runId: string | null | undefined,
) {
  const [waitpoints, setWaitpoints] = useState<PendingWaitpoint[]>([])

  const refresh = useCallback(async () => {
    if (!workspaceId || !runId) {
      setWaitpoints([])
      return
    }
    const all = await listPendingWaitpoints(workspaceId)
    setWaitpoints(all.filter((w) => w.pipeline_run_id === runId))
  }, [workspaceId, runId])

  useEffect(() => {
    refresh()
  }, [refresh])

  const handleWaitpointEvent = useCallback(
    (event: RealtimeEvent) => {
      if (!runId) return
      const payload = event.payload as Record<string, unknown> | undefined
      const eventRunId =
        (payload?.run_id as string | undefined) ??
        (payload?.pipeline_run_id as string | undefined)
      if (eventRunId === runId) refresh()
    },
    [runId, refresh],
  )

  useRealtimeEvent("pipeline.waitpoint.created", handleWaitpointEvent)
  // A run that completes/fails clears its waitpoints implicitly;
  // refresh so the canvas no longer shows stale pending pills.
  useRealtimeEvent("pipeline.run.completed", handleWaitpointEvent)
  useRealtimeEvent("pipeline.run.failed", handleWaitpointEvent)

  return { waitpoints, refresh }
}
