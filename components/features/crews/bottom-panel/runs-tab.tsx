"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"

import type { BottomPanelContext } from "./types"
import { EmptyState, formatRelative, statusColor } from "./shared"

// Normalised run row rendered by both modes. The routine endpoint returns
// runRecordDTO (pipelines_exec.go) and the issue endpoint returns the same
// shape plus optional agent fields, so one renderer covers both.
interface RunRow {
  id: string
  status: string
  started_at?: string
  ended_at?: string
  duration_ms?: number
  cost_usd?: number
  triggered_via?: string
  error_message?: string
}

function fmtDuration(ms?: number): string {
  if (!ms || ms <= 0) return "—"
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return `${m}m ${s % 60}s`
}

/**
 * Runs — every execution tied to the selected entity:
 *  • issue/mission → GET /api/v1/crews/{crewId}/issues/{identifier}/runs
 *  • routine       → GET /api/v1/workspaces/{ws}/pipelines/{slug}/run-records
 * Both return the same row shape; click-through to the run's log lives in
 * the Logs tab / Activity page.
 */
export function RunsTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [runs, setRuns] = useState<RunRow[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  let url: string | null = null
  if (context?.kind === "mission") {
    url = `/api/v1/crews/${context.crewId}/issues/${encodeURIComponent(context.identifier)}/runs?workspace_id=${workspaceId}`
  } else if (context?.kind === "routine") {
    url = `/api/v1/workspaces/${workspaceId}/pipelines/${encodeURIComponent(context.slug)}/run-records?limit=50`
  }

  useEffect(() => {
    if (!url) return
    let cancelled = false
    setRuns(null)
    setError(null)
    fetch(url)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        const list = Array.isArray(data) ? data : (data?.runs ?? [])
        setRuns(list)
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [url])

  if (!context) return <EmptyState>Select an issue or routine to see its runs.</EmptyState>
  if (context.kind !== "mission" && context.kind !== "routine") {
    return <EmptyState>Runs are shown per issue or routine.</EmptyState>
  }
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (runs === null) return <EmptyState>Loading…</EmptyState>
  if (runs.length === 0) return <EmptyState>No runs yet.</EmptyState>

  return (
    <div className="h-full overflow-y-auto p-3 text-xs">
      <table className="w-full border-collapse">
        <thead>
          <tr className="text-muted-foreground-soft text-[10px] uppercase tracking-wide">
            <th className="text-left font-medium pb-2 pr-3">Run</th>
            <th className="text-left font-medium pb-2 pr-3">Status</th>
            <th className="text-left font-medium pb-2 pr-3">Started</th>
            <th className="text-left font-medium pb-2 pr-3">Duration</th>
            <th className="text-left font-medium pb-2 pr-3">Trigger</th>
            <th className="text-left font-medium pb-2">Result</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id} className="border-t border-white/5 hover:bg-white/[0.02]">
              <td className="py-2 pr-3 font-mono text-blue-300/90 truncate max-w-[120px]">{run.id.slice(0, 12)}</td>
              <td className={cn("py-2 pr-3", statusColor(run.status))}>{run.status}</td>
              <td className="py-2 pr-3 text-muted-foreground">{run.started_at ? formatRelative(run.started_at) : "—"}</td>
              <td className="py-2 pr-3 text-muted-foreground">{fmtDuration(run.duration_ms)}</td>
              <td className="py-2 pr-3 text-muted-foreground">{run.triggered_via || "—"}</td>
              <td className="py-2 text-muted-foreground truncate max-w-[200px]">
                {run.error_message
                  ? <span className="text-red-300">{run.error_message}</span>
                  : (run.cost_usd ? `$${run.cost_usd.toFixed(4)}` : "—")}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
