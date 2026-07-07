"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

// useStepIO — fetches the OPENED step's sub-span input/output on demand (#863).
//
// The main trace poll (useTrace) omits per-span I/O so its payload stays flat
// as live actions accumulate — it refetches GetRun on every pipeline.step.*
// event + a 3s tick, and the canvas waterfall only needs span metadata. Only
// the SELECTED step's drill-down needs the (up to 16 KB/span) input/output, so
// this fetches GetRun scoped to that one step (`?io_step=<id>`) once when the
// step is selected. Returns that step's raw sub_spans (with I/O), or undefined
// while loading / when no step is selected — the caller falls back to the light
// poll's metadata-only spans in the meantime.
export function useStepIO(
  workspaceId: string | null | undefined,
  runId: string | null,
  stepId: string | null,
): { spans: unknown[] | undefined; loading: boolean } {
  const [spans, setSpans] = useState<unknown[] | undefined>(undefined)
  const [loading, setLoading] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    abortRef.current?.abort()
    if (!workspaceId || !runId || !stepId) {
      setSpans(undefined)
      setLoading(false)
      return
    }
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}?io_step=${encodeURIComponent(stepId)}`,
      { signal: ctrl.signal },
    )
      .then(async (res) => {
        if (ctrl.signal.aborted) return
        if (!res.ok) {
          setSpans(undefined)
          return
        }
        const data = await res.json()
        if (ctrl.signal.aborted) return
        const raw = (data?.sub_spans as Record<string, unknown> | undefined)?.[stepId]
        setSpans(Array.isArray(raw) ? raw : undefined)
      })
      .catch(() => {
        if (!ctrl.signal.aborted) setSpans(undefined)
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })

    return () => ctrl.abort()
  }, [workspaceId, runId, stepId])

  return { spans, loading }
}
