"use client"

import { useMemo } from "react"
import { usePipelineRuns } from "@/hooks/use-pipelines"
import type { JournalEntry } from "@/lib/types/journal"
import { humanizeRun, isRunInFlight, withAwaitingApproval } from "@/lib/run-activity"
import { RunActivityRail } from "./run-activity-timeline"

// PipelineRunActivity — the readable rail for ONE routine (pipeline) run.
// Routine runs don't use journal trace_id; they group by payload.run_id and
// carry pipeline.run.* / pipeline.step.* entry types. usePipelineRuns already
// fetches those (include_steps=1) and subscribes to live pipeline.* events, so
// we reuse it and render the same shared rail the issue timeline uses.

interface PipelineRunActivityProps {
  workspaceId: string
  slug: string
  /** Restrict to one run. When omitted, shows the most recent run. */
  runId?: string | null
  /**
   * When this run is parked on a human approval, pass the waitpoint's step +
   * created_at so the rail pins an amber "awaiting your decision" row and
   * shows the "Waiting for approval" header state. Null when not waiting.
   */
  awaiting?: { stepId?: string; ts: string } | null
  title?: string
  className?: string
}

export function PipelineRunActivity({
  workspaceId,
  slug,
  runId,
  awaiting,
  title = "Run activity",
  className,
}: PipelineRunActivityProps) {
  const { runs, loading } = usePipelineRuns(workspaceId, slug)

  // Resolve which run to show: the requested run, else the newest one present.
  const targetRunId = useMemo(() => {
    if (runId) return runId
    let newest: { id: string; ts: string } | null = null
    for (const r of runs) {
      if (!r.run_id) continue
      if (!newest || r.ts > newest.ts) newest = { id: r.run_id, ts: r.ts }
    }
    return newest?.id ?? null
  }, [runId, runs])

  // Adapt the flattened pipeline entries to JournalEntry shape for the
  // shared humanizer (it only reads entry_type / payload / ts / summary).
  const entries = useMemo<JournalEntry[]>(() => {
    if (!targetRunId) return []
    return runs
      .filter((r) => r.run_id === targetRunId)
      .map((r) => ({
        id: r.id,
        workspace_id: workspaceId,
        ts: r.ts,
        entry_type: r.entry_type,
        severity: r.severity,
        actor_type: "orchestrator",
        summary: r.summary,
        payload: (r.payload as Record<string, unknown>) ?? undefined,
      }))
  }, [runs, targetRunId, workspaceId])

  const rows = useMemo(
    () => withAwaitingApproval(humanizeRun(entries), awaiting),
    [entries, awaiting],
  )
  const running = useMemo(() => isRunInFlight(entries.map((e) => e.entry_type)), [entries])

  // Nothing triggered yet for this routine — stay out of the way.
  if (!targetRunId && !loading) return null

  return (
    <RunActivityRail
      rows={rows}
      running={running}
      waiting={!!awaiting}
      loading={loading}
      title={title}
      emptyLabel="Waiting for the run to start…"
      className={className}
    />
  )
}
