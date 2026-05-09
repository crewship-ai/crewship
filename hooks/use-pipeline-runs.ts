"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// PipelineRun mirrors the wire shape from /api/v1/workspaces/{ws}/
// pipeline-runs. The backend enriches each row with pipeline_name +
// issue_identifier so the UI doesn't fan out to per-row lookups.
export interface PipelineRun {
  id: string
  pipeline_id: string
  pipeline_slug: string
  pipeline_name: string
  status: "running" | "queued" | "paused" | "completed" | "failed" | "cancelled" | "interrupted" | string
  mode: string
  started_at: string
  ended_at: string
  current_step_id: string
  step_outputs: Record<string, unknown> | null
  cost_usd: number
  duration_ms: number
  triggered_via: "manual" | "schedule" | "webhook" | "call_pipeline" | "issue" | string
  triggered_by_id: string
  invoking_crew_id: string
  invoking_agent_id: string
  invoking_user_id: string
  error_message: string
  failed_at_step: string
  // issue_identifier is only populated when triggered_via === "issue"
  // (LEFT JOIN missions on triggered_by_id). Empty string otherwise.
  issue_identifier: string
}

type StatusFilter = "all" | "active" | "completed" | "failed"

interface ListResponse {
  rows: PipelineRun[]
  count: number
}

// usePipelineRuns drives the /activity Runs sub-tab. Hot-paths:
//   - filter: all | active | completed | failed
//   - polling: every 3s while there's at least one active run, otherwise
//     paused so an idle workspace doesn't burn requests
//   - realtime: pipeline.run.* events kick a refresh independent of
//     the poll cadence so the UI updates within a couple of seconds
//     of any state change
export function usePipelineRuns(
  workspaceId: string | null | undefined,
  filter: StatusFilter = "all",
) {
  const [runs, setRuns] = useState<PipelineRun[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setRuns([])
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams()
      if (filter !== "all") params.set("status", filter)
      params.set("limit", "100")
      const url = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs?${params.toString()}`
      const res = await fetch(url, { signal: ctrl.signal })
      if (ctrl.signal.aborted) return
      if (!res.ok) {
        setError(`runs: ${res.status}`)
        setLoading(false)
        return
      }
      const data: ListResponse = await res.json()
      if (ctrl.signal.aborted) return
      setRuns(data.rows ?? [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, filter])

  useEffect(() => {
    refresh()
    return () => { abortRef.current?.abort() }
  }, [refresh])

  // Poll cadence is gated on whether anything is active. When the
  // workspace is idle (no runs in active states) we don't bother
  // refreshing — the realtime events below cover the "new run
  // started" gap.
  const hasActive = runs.some((r) =>
    r.status === "running" || r.status === "queued" || r.status === "paused",
  )
  useEffect(() => {
    if (!hasActive) return
    const t = setInterval(refresh, 3_000)
    return () => clearInterval(t)
  }, [hasActive, refresh])

  // Realtime: any pipeline run state change kicks a refresh so we
  // catch "new run started" events that the active-only polling
  // would otherwise miss.
  useRealtimeEvent("pipeline.run.started", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)

  return { runs, loading, error, refresh }
}
