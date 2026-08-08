"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

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
  // Mirrors internal/pipeline/runs.go RunStatus. "waiting" is
  // non-terminal — the run is parked on a human approval (wait step).
  status: "queued" | "running" | "waiting" | "completed" | "failed" | "cancelled" | "dry_run" | "interrupted"
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
  // Mirrors internal/pipeline/runs.go TriggeredVia. "issue" and "wake_check"
  // were missing here while the backend has emitted both for some time — a
  // run started from an issue's Run button rendered as an unhandled value.
  triggered_via: "manual" | "schedule" | "webhook" | "call_pipeline" | "issue" | "wake_check"
  triggered_by_id?: string
  idempotency_key?: string
  // chain_depth is how many COMPOSED hops separate this run from whatever a
  // person did: 0 = somebody started it, 1 = a routine called it, 2 = an
  // automation fired it from an event that run emitted. Server-capped at 8.
  // Always present on a current server; optional here so a page rendered
  // against an older one degrades to "not composed" instead of NaN.
  chain_depth?: number
  chain_origin?: string
  // The RULE behind a rule-fired run. NOT derivable from triggered_via: every
  // deferred run — cron and automation alike — is stored as "schedule", so
  // this is the only thing that tells them apart. See the run-provenance
  // section of docs/api-reference/pipelines.
  automation_id?: string
  automation_name?: string
  trigger_event_type?: string
}

// isActiveRunStatus mirrors the backend's in-flight set — RunStore
// queries scope "active" to status IN ('queued','running','waiting')
// (internal/pipeline/runs.go). Only these runs are cancellable via
// POST /pipelines/runs/{runId}/cancel; terminal runs 404 there.
export function isActiveRunStatus(status: string): boolean {
  return status === "queued" || status === "running" || status === "waiting"
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
      setLegacy(false)
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
      const res = await apiFetch(url, { signal: ctrl.signal })
      if (ctrl.signal.aborted) return
      if (res.status === 503) {
        // Server doesn't have runStore wired — UI falls back to /runs.
        // We don't surface this as an error since the legacy path is
        // still functional.
        setLegacy(true)
        setRecords([])
        return
      }
      // Any other non-503 outcome means /run-records IS the right
      // surface; clear legacy so a previous 503 doesn't pin the UI to
      // the journal fallback after the server recovers.
      setLegacy(false)
      if (!res.ok) {
        setError(`run-records: ${res.status}`)
        return
      }
      const data: PipelineRunRecord[] = await res.json()
      if (ctrl.signal.aborted) return
      setRecords(Array.isArray(data) ? data : [])
    } catch (e) {
      if (ctrl.signal.aborted) return
      setLegacy(false)
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

  // Refresh on the lifecycle events the backend actually emits over
  // WebSocket. Cancelled runs surface as pipeline.run.failed with a
  // "cancelled" reason; dry-run completion surfaces as
  // pipeline.run.completed; interrupted transitions only happen at
  // boot recovery (no live event), so the next manual refresh or
  // page reload picks them up.
  useRealtimeEvent("pipeline.run.started", refresh)
  useRealtimeEvent("pipeline.run.completed", refresh)
  useRealtimeEvent("pipeline.run.failed", refresh)

  return { records, loading, error, legacy, refresh }
}
