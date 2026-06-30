"use client"

import { useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import type { MiniRun } from "@/lib/routine-mini-trace"

// useRunSubSpans — best-effort fetch of one run's detail (GET
// /api/v1/workspaces/{ws}/pipeline-runs/{id}), which carries the
// `sub_spans` map + step_outputs the list endpoint omits. Used by the
// routine "Last Run" card to render the mini-trace's tool calls.
//
// Deliberately tiny: no realtime, no polling. The last completed run is
// terminal, so a single fetch on (workspace, run) change is enough. Any
// failure resolves to `run: null` so the card still renders the flow
// from the list-row record without the drill-down calls.
export function useRunSubSpans(
  workspaceId: string | null | undefined,
  runId: string | null | undefined,
) {
  const [run, setRun] = useState<MiniRun | null>(null)
  const [loading, setLoading] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!workspaceId || !runId) {
      // Abort any in-flight request and fully reset. Without clearing
      // loading here (and the aborted fetch skips the finally's
      // setLoading(false)), loading would latch true forever once the
      // inputs go empty.
      abortRef.current?.abort()
      abortRef.current = null
      setRun(null)
      setLoading(false)
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    apiFetch(
      `/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipeline-runs/${encodeURIComponent(runId)}`,
      { signal: ctrl.signal },
    )
      .then(async (res) => {
        if (ctrl.signal.aborted) return
        if (!res.ok) {
          setRun(null)
          return
        }
        const data = (await res.json()) as MiniRun
        if (!ctrl.signal.aborted) setRun(data)
      })
      .catch(() => {
        // Best-effort: a fetch failure just renders the flow without
        // sub-spans. Never surface an error here.
        if (!ctrl.signal.aborted) setRun(null)
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false)
      })
    return () => ctrl.abort()
  }, [workspaceId, runId])

  return { run, loading }
}
