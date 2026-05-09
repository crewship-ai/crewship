"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { PipelineDSL } from "@/lib/trace/types"

// useTrace — fetches everything the canvas needs for one run:
//   - the run row (status, step_outputs, error_message…)
//   - the pipeline DSL (steps, edges)
//
// Both come from the same backend in two requests. We polled approach
// instead of subscribing to a single SSE stream because the data is
// small (~5KB combined) and the realtime layer already broadcasts
// pipeline.step.* events that hot-path UI updates without a refetch.
//
// Refresh triggers:
//   - pipeline.step.* event for this run → refetch run
//   - pipeline.run.* event for this run → refetch run
//   - 3s poll while run is in active states (running/queued/paused)

interface PipelineDetailResponse {
  id: string
  slug: string
  name: string
  definition?: PipelineDSL
}

interface RunDetailResponse extends PipelineRun {
  // GET /pipeline-runs/{id} parses step_outputs_json server-side and
  // returns it as `step_outputs` (already in PipelineRun). The
  // response shape is identical to a list-row.
  inputs?: Record<string, unknown>
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
      const runRes = await fetch(
        `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}`,
        { signal: ctrl.signal },
      )
      if (ctrl.signal.aborted) return
      if (!runRes.ok) {
        // 404 == run not found. Don't treat as transient.
        setError(`run: ${runRes.status}`)
        setRun(null)
        setDsl(null)
        return
      }
      const runData: RunDetailResponse = await runRes.json()
      if (ctrl.signal.aborted) return
      setRun(runData)

      // 2) Fetch the pipeline DSL by slug — needed to render every
      //    step, including ones that haven't run yet.
      if (runData.pipeline_slug) {
        const dslRes = await fetch(
          `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(runData.pipeline_slug)}`,
          { signal: ctrl.signal },
        )
        if (ctrl.signal.aborted) return
        if (dslRes.ok) {
          const dslData: PipelineDetailResponse = await dslRes.json()
          setDsl(dslData.definition ?? null)
        } else {
          // Pipeline gone (e.g. deleted after run completed). Keep the
          // run loaded so user still sees outputs; canvas falls back
          // to outputs-only mode.
          setDsl(null)
        }
      } else {
        setDsl(null)
      }
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, runId])

  useEffect(() => {
    abortRef.current?.abort()
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
    (run.status === "running" || run.status === "queued" || run.status === "paused")
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
