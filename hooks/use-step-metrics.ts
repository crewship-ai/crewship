"use client"

import { useCallback, useEffect, useState } from "react"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

// useStepMetrics — per-step duration + cost for a single run, sourced
// from journal entries (`pipeline.step.completed`). Used by the
// heatmap toggle on the trace canvas.
//
// We fetch the journal once on mount + subscribe to live
// `pipeline.step.completed` events for the same run. The journal
// fetch covers historical (already-completed) runs; the live
// subscription covers in-flight runs that complete more steps while
// the canvas is open.
//
// Shape returned: a Map<stepId, { durationMs, costUsd }>. Steps that
// haven't completed yet are not in the map; the heatmap renderer
// treats missing entries as "no shading".

export interface StepMetric {
  durationMs: number
  costUsd: number
}

interface JournalEntry {
  id: string
  ts: string
  entry_type: string
  payload?: {
    step_id?: string
    duration_ms?: number
    cost_usd?: number
    run_id?: string
    pipeline_run_id?: string
  } | null
}

export function useStepMetrics(
  workspaceId: string | null | undefined,
  pipelineSlug: string | null | undefined,
  runId: string | null | undefined,
) {
  const [metrics, setMetrics] = useState<Map<string, StepMetric>>(() => new Map())
  const [loading, setLoading] = useState(false)

  // Initial fetch from the journal — one shot.
  useEffect(() => {
    if (!workspaceId || !pipelineSlug || !runId) {
      setMetrics(new Map())
      // Clear loading too — without this a hook that started
      // loading, then had its inputs cleared (run switch to null,
      // workspace switch), would stay stuck on loading=true.
      setLoading(false)
      return
    }
    let cancelled = false
    // The previous scope's heatmap must not shade the newly selected run
    // while its history is still loading.
    setMetrics(new Map())
    setLoading(true)
    apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(pipelineSlug)}/runs?limit=200&include_steps=1`,
    )
      .then(async (res) => (res.ok ? ((await res.json()) as JournalEntry[]) : []))
      .then((entries) => {
        if (cancelled) return
        const next = new Map<string, StepMetric>()
        for (const e of entries) {
          if (e.entry_type !== "pipeline.step.completed") continue
          const p = e.payload
          if (!p) continue
          const eventRunId = p.run_id ?? p.pipeline_run_id
          if (eventRunId !== runId) continue
          if (!p.step_id) continue
          // ListRuns returns journal entries ORDER BY ts DESC. Keep the
          // first completion so an older retry cannot replace the latest.
          if (next.has(p.step_id)) continue
          next.set(p.step_id, {
            durationMs: typeof p.duration_ms === "number" ? p.duration_ms : 0,
            costUsd: typeof p.cost_usd === "number" ? p.cost_usd : 0,
          })
        }
        // Live completions observed during this request are newer than
        // its history snapshot and must survive its eventual response.
        setMetrics((live) => new Map([...next, ...live]))
      })
      .catch(() => {
        // History is best-effort. The map was cleared on scope change;
        // retain any live completions already observed for this scope.
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [workspaceId, pipelineSlug, runId])

  // Live updates — one entry at a time as steps complete.
  const handleStepCompleted = useCallback(
    (event: RealtimeEvent) => {
      if (!workspaceId || !pipelineSlug || !runId) return
      const p = event.payload as JournalEntry["payload"]
      if (!p) return
      const eventRunId = p.run_id ?? p.pipeline_run_id
      if (eventRunId !== runId) return
      if (!p.step_id) return
      setMetrics((prev) => {
        const next = new Map(prev)
        next.set(p.step_id!, {
          durationMs: typeof p.duration_ms === "number" ? p.duration_ms : 0,
          costUsd: typeof p.cost_usd === "number" ? p.cost_usd : 0,
        })
        return next
      })
    },
    [workspaceId, pipelineSlug, runId],
  )

  useRealtimeEvent("pipeline.step.completed", handleStepCompleted)

  return { metrics, loading }
}
