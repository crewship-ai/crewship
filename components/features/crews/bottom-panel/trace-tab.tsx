"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

import type { BottomPanelContext } from "./types"
import { EmptyState, statusColor } from "./shared"

// Subset of GetRun (pipeline_runs.go) we render.
interface RunDetail {
  id: string
  status: string
  mode?: string
  current_step_id?: string
  step_outputs?: Record<string, unknown>
  cost_usd?: number
  duration_ms?: number
  error_message?: string
  failed_at_step?: string
}

function preview(val: unknown): string {
  if (val == null) return ""
  const s = typeof val === "string" ? val : JSON.stringify(val)
  return s.length > 160 ? s.slice(0, 160) + "…" : s
}

/**
 * Trace — the step-by-step execution of the selected run: which steps ran,
 * their outputs, where it currently sits / failed. Lighter companion to the
 * full Activity trace canvas; reads GET /api/v1/workspaces/{ws}/pipeline-runs/{runId}.
 */
export function TraceTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [run, setRun] = useState<RunDetail | null>(null)
  const [error, setError] = useState<string | null>(null)

  const isRun = context?.kind === "run"
  const runId = isRun ? context.runId : null

  useEffect(() => {
    if (!runId) return
    let cancelled = false
    setRun(null)
    setError(null)
    apiFetch(`/api/v1/workspaces/${workspaceId}/pipeline-runs/${encodeURIComponent(runId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((d) => { if (!cancelled) setRun(d) })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [runId, workspaceId])

  if (!context) return <EmptyState>Select a run to see its trace.</EmptyState>
  if (context.kind !== "run") return <EmptyState>Trace is shown per run.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (run === null) return <EmptyState>Loading…</EmptyState>

  const steps = Object.entries(run.step_outputs ?? {})

  return (
    <div className="h-full overflow-y-auto p-4 text-xs">
      <div className="flex items-center gap-3 mb-4 text-[11px] text-muted-foreground">
        <span className={cn("font-medium", statusColor(run.status))}>{run.status}</span>
        {run.mode && <><span>·</span><span>{run.mode}</span></>}
        {typeof run.duration_ms === "number" && run.duration_ms > 0 && (
          <><span>·</span><span>{Math.round(run.duration_ms / 1000)}s</span></>
        )}
        {typeof run.cost_usd === "number" && run.cost_usd > 0 && (
          <><span>·</span><span>${run.cost_usd.toFixed(4)}</span></>
        )}
      </div>

      {run.error_message && (
        <div className="mb-3 text-red-300 border border-red-500/20 bg-red-500/5 rounded-md px-3 py-2">
          {run.failed_at_step && <span className="font-mono mr-2">{run.failed_at_step}:</span>}
          {run.error_message}
        </div>
      )}

      {steps.length === 0 ? (
        <EmptyState>No step output recorded yet.</EmptyState>
      ) : (
        <div className="relative pl-5 before:absolute before:left-[5px] before:top-1 before:bottom-1 before:w-px before:bg-white/10">
          {steps.map(([stepId, out]) => {
            const current = stepId === run.current_step_id
            const failed = stepId === run.failed_at_step
            return (
              <div key={stepId} className="relative pb-4 pl-3.5">
                <span className={cn(
                  "absolute -left-[15px] top-0.5 h-2.5 w-2.5 rounded-full bg-card border-2",
                  failed ? "border-red-400" : current ? "border-blue-400" : "border-emerald-400",
                )} />
                <div className="text-foreground font-mono">
                  {stepId}
                  {current && <span className="ml-2 text-blue-300 text-[10px]">current</span>}
                </div>
                {preview(out) && (
                  <div className="text-muted-foreground mt-0.5 break-all">{preview(out)}</div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
