"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

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
  // sub_spans — agent-internal tool calls per step, keyed by step id.
  // Each value is the raw wire array (bash/write/read/edit/mcp_tool/
  // http/tool/think spans, ordered by seq); mapSubSpans normalizes it.
  // GetRun returns `{}` for a run with none, and the list endpoint may
  // omit it entirely — both collapse to "no drill-down" in the UI.
  sub_spans?: Record<string, unknown> | null
  // chat_id — the agent session this run was authored in, when the run
  // originated from a chat. Drives the "open session" Context link.
  chat_id?: string
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

  // inFlightRef gates a poll-driven refresh from cancelling a still-
  // pending fetch from the previous tick. Aborting on every tick
  // could drop slow responses on a flaky network, leaving the runs
  // view stale. We only abort on workspace/filter change or unmount
  // (handled by the cleanup below + the explicit refresh on filter
  // change). Manual refresh (caller invokes refresh()) is rare and
  // can wait for the in-flight fetch to settle.
  const inFlightRef = useRef(false)

  const refresh = useCallback(async () => {
    if (!workspaceId) {
      setRuns([])
      return
    }
    if (inFlightRef.current) return
    inFlightRef.current = true
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams()
      if (filter !== "all") params.set("status", filter)
      params.set("limit", "100")
      const url = `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs?${params.toString()}`
      const res = await apiFetch(url, { signal: ctrl.signal })
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
      inFlightRef.current = false
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, filter])

  useEffect(() => {
    // workspace / filter change → abort any in-flight + start fresh
    abortRef.current?.abort()
    inFlightRef.current = false
    refresh()
    return () => {
      abortRef.current?.abort()
      inFlightRef.current = false
    }
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
