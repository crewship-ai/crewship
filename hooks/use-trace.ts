"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { PipelineDSL } from "@/lib/trace/types"

// useTrace — fetches everything the canvas needs for one run:
//   - the run row (status, step_outputs, error_message…)
//   - the pipeline DSL (steps, edges)
//
// Both come from the run endpoint, pinned to its saved version. We poll instead of
// subscribing to a single SSE stream because the realtime layer already
// broadcasts pipeline.step.* events that hot-path UI updates, and the run
// row itself is small.
//
// This poll is INTENTIONALLY light on sub-span I/O (#863): GetRun omits each
// agent sub-span's input/output (up to 16 KB output/span) unless asked, so the
// payload stays flat as live actions accumulate on a long watched run. Only
// span metadata (kind/name/detail/status/duration + truncated flags) comes
// through here — enough for the canvas waterfall. The OPENED step's I/O is
// fetched separately on demand by useStepIO (`?io_step=<id>`).
//
// Refresh triggers:
//   - pipeline.step.* event for this run → refetch run
//   - pipeline.run.* event for this run → refetch run
//   - 3s poll while run is in active states (running/queued/paused)

interface RunDetailResponse extends PipelineRun {
  // GET /pipeline-runs/{id} parses step_outputs_json server-side and
  // returns it as `step_outputs` (already in PipelineRun). The
  // response shape is identical to a list-row.
  inputs?: Record<string, unknown>
  definition?: PipelineDSL | null
  definition_status?: "available" | "unavailable" | "error"
  pipeline_version?: number | null
  definition_hash?: string
  step_outputs_available?: boolean
  output?: string
  outcome?: string
}

export function useTrace(workspaceId: string | null | undefined, runId: string | null) {
  const [run, setRun] = useState<RunDetailResponse | null>(null)
  const [dsl, setDsl] = useState<PipelineDSL | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId || !runId) {
      setRun(null)
      setDsl(null)
      return
    }
    // Cancel any in-flight fetch and start a fresh one. We
    // deliberately don't gate on an "in-flight" flag — realtime
    // events arriving mid-poll were silently dropped that way, so
    // step.completed events between polls never showed up. Aborting
    // is correct: if a step finishes while a previous fetch is
    // still flying, the abort guarantees the next fetch wins.
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      // 1) Fetch the run. The endpoint is workspace-scoped: GET
      //    /api/v1/workspaces/{ws}/pipeline-runs/{id}. Response is the
      //    same shape used by the list endpoint plus an `inputs` map.
      const runRes = await apiFetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}`,
        { signal: ctrl.signal },
      )
      if (ctrl.signal.aborted) return
      if (!runRes.ok) {
        // 404 == run not found. Don't treat as transient.
        setError(`run: ${runRes.status}`)
        if (runRes.status === 404) { setRun(null); setDsl(null) }
        return
      }
      const runData: RunDetailResponse = await runRes.json()
      if (ctrl.signal.aborted) return
      setRun(runData)

      // The server resolves the immutable definition of THIS run. Never use HEAD
      // as a fallback: it would redraw historical work using a different recipe.
      setDsl(runData.definition ?? null)

    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, runId])

  useEffect(() => {
    // Clear stale graph state when the run identity flips. Without
    // this, switching from a healthy run to one whose DSL fetch fails
    // would render the previous run's DSL against the new run row —
    // step nodes from one pipeline shown on top of another's outputs.
    abortRef.current?.abort()
    setRun(null)
    setDsl(null)
    setError(null)
    refresh()
    return () => {
      abortRef.current?.abort()
    }
  }, [refresh])

  // 3s poll while active — same rule as usePipelineRuns. When the run
  // is terminal (completed/failed/cancelled) we stop and let realtime
  // cover the next state change.
  const isActive =
    run !== null &&
    (run.status === "running" || run.status === "queued" || run.status === "paused" || run.status === "waiting")
  useEffect(() => {
    if (!isActive) return
    const t = setInterval(refresh, 3_000)
    return () => clearInterval(t)
  }, [isActive, refresh])

  // Realtime — refresh whenever an event names this run. The
  // backend's WS payload uses `run_id` for run-scoped events and
  // `pipeline_run_id` for some legacy ones; check both.
  const handleRunEvent = useCallback(
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

  useRealtimeEvent("pipeline.run.started", handleRunEvent)
  useRealtimeEvent("pipeline.run.completed", handleRunEvent)
  useRealtimeEvent("pipeline.run.failed", handleRunEvent)
  useRealtimeEvent("pipeline.step.started", handleRunEvent)
  useRealtimeEvent("pipeline.step.completed", handleRunEvent)
  useRealtimeEvent("pipeline.step.failed", handleRunEvent)
  useRealtimeEvent("pipeline.waitpoint.created", handleRunEvent)

  return { run, dsl, loading, error, refresh }
}
