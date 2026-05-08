"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"

// PipelineRunRecord mirrors the wire shape returned by the v83
// pipeline_runs-backed endpoint:
//   GET /api/v1/workspaces/{ws}/pipelines/{slug}/run-records
//
// One row per run (vs. usePipelineRuns which returns one row per
// journal event). Use this hook for the list-runs view; use
// usePipelineRuns when you need per-step events for the waterfall.
export interface PipelineRunRecord {
  id: string
  pipeline_id: string
  pipeline_slug: string
  status: "queued" | "running" | "completed" | "failed" | "cancelled" | "dry_run" | "interrupted"
  mode: "run" | "test_run" | "dry_run"
  started_at: string
  ended_at?: string
  current_step_id?: string
  output?: string
  cost_usd: number
  duration_ms: number
  error_message?: string
  failed_at_step?: string
  error_fingerprint?: string
  triggered_via: "manual" | "schedule" | "webhook" | "call_pipeline"
  triggered_by_id?: string
  idempotency_key?: string
}

// usePipelineRunRecords fetches the pipeline_runs projection with
// stable wire shape + AbortController-based stale-fetch protection.
// Same ergonomic shape as usePipelineRuns so swap-in is one-line.
//
// Falls back gracefully when the server returns 503 (runStore not
// wired): records becomes [] + error stays null + a `legacy` flag
// flips so the caller can fall back to /runs (journal-backed).
export function usePipelineRunRecords(
  workspaceId: string | null | undefined,
  slug: string | null,
  status?: PipelineRunRecord["status"],
) {
  const [records, setRecords] = useState<PipelineRunRecord[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // legacy=true means the server returned 503 (no runStore wired);
  // callers fall back to /runs without surfacing this as an error.
  const [legacy, setLegacy] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId || !slug) {
      setRecords([])
      return
    }
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setLoading(true)
    setError(null)
    try {
      let url = `/api/v1/workspaces/${workspaceId}/pipelines/${slug}/run-records?limit=50`
      if (status) url += `&status=${encodeURIComponent(status)}`
      const res = await fetch(url, { signal: ctrl.signal })
      if (ctrl.signal.aborted) return
      if (res.status === 503) {
        // Server doesn't have runStore wired — UI falls back to /runs.
        // We don't surface this as an error since the legacy path is
        // still functional.
        setLegacy(true)
        setRecords([])
        return
      }
      if (!res.ok) {
        setError(`run-records: ${res.status}`)
        return
      }
      const data: PipelineRunRecord[] = await res.json()
      if (ctrl.signal.aborted) return
      setLegacy(false)
      setRecords(Array.isArray(data) ? data : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!ctrl.signal.aborted) setLoading(false)
    }
  }, [workspaceId, slug, status])

  useEffect(() => {
    refresh()
    return () => {
      abortRef.current?.abort()
    }
  }, [refresh])

  // Refresh on any pipeline run lifecycle event for this routine —
  // started events appear as new records, completed/failed transition
  // status. Cheap because list endpoint is small + typically cached.
  useRealtimeEvent("pipeline.run.started", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)

  return { records, loading, error, legacy, refresh }
}
