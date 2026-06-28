"use client"

import { useEffect, useState } from "react"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"

import type { BottomPanelContext, LogEntry } from "./types"
import { EmptyState, formatTime } from "./shared"

/**
 * Logs — the exec log of a single run.
 *  • run     → the run in context (Activity page)
 *  • routine → the routine's most recent run (resolved via run-records)
 * Reads GET /api/v1/workspaces/{ws}/pipeline-runs/{runId}/logs.
 */
export function LogsTab({ workspaceId, context }: { workspaceId: string; context: BottomPanelContext }) {
  const [runId, setRunId] = useState<string | null>(null)
  const [logs, setLogs] = useState<LogEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const isRun = context?.kind === "run"
  const isRoutine = context?.kind === "routine"

  // Resolve which run to tail.
  useEffect(() => {
    let cancelled = false
    setRunId(null)
    setLogs(null)
    setError(null)
    if (isRun) {
      setRunId(context.runId)
      return
    }
    if (isRoutine) {
      // Latest run for this routine = first record in run-records.
      apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/${encodeURIComponent(context.slug)}/run-records?limit=1`)
        .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
        .then((data) => {
          if (cancelled) return
          // run-records may come back bare or wrapped as { runs: [...] } —
          // accept both, same as RunsTab.
          const list = Array.isArray(data) ? data : (data?.runs ?? [])
          setRunId(list[0]?.id ?? "")
        })
        .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    }
    return () => { cancelled = true }
  }, [isRun, isRoutine, context, workspaceId])

  // Fetch the resolved run's logs.
  useEffect(() => {
    if (!runId) return
    let cancelled = false
    setLogs(null)
    apiFetch(`/api/v1/workspaces/${workspaceId}/pipeline-runs/${encodeURIComponent(runId)}/logs`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
      .then((data) => {
        if (cancelled) return
        const list = Array.isArray(data) ? data : (data?.logs ?? [])
        setLogs(list)
      })
      .catch((err) => { if (!cancelled) setError(err instanceof Error ? err.message : String(err)) })
    return () => { cancelled = true }
  }, [runId, workspaceId])

  if (!context) return <EmptyState>Select a run or routine to see its logs.</EmptyState>
  if (!isRun && !isRoutine) return <EmptyState>Logs are shown per run or routine.</EmptyState>
  if (error) return <EmptyState><span className="text-red-300">Failed to load: {error}</span></EmptyState>
  if (runId === "") return <EmptyState>No runs yet to show logs for.</EmptyState>
  if (logs === null) return <EmptyState>Loading…</EmptyState>
  if (logs.length === 0) return <EmptyState>No log output for this run.</EmptyState>

  return (
    <div className="h-full overflow-y-auto p-3 text-[11px] leading-relaxed font-mono text-foreground/80">
      {logs.map((l, i) => {
        const ts = l.ts ?? l.timestamp ?? ""
        const msg = l.message ?? l.msg ?? l.text ?? JSON.stringify(l)
        const level = String(l.level ?? "").toLowerCase()
        const levelColor =
          level.includes("error") || level.includes("fatal") ? "text-red-300" :
          level.includes("warn") ? "text-amber-300" :
          level.includes("info") ? "text-blue-300" :
          "text-muted-foreground"
        return (
          <div key={i} className="flex gap-2 hover:bg-white/[0.03] px-1 -mx-1 rounded">
            {ts && <span className="text-muted-foreground shrink-0">{formatTime(String(ts))}</span>}
            {level && <span className={cn("shrink-0 uppercase", levelColor)}>{level}</span>}
            <span className="break-all">{String(msg)}</span>
          </div>
        )
      })}
    </div>
  )
}
