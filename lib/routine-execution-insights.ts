import type { RunExecution } from "@/hooks/use-run-executions"

/** Rank completed top-level attempts only: do not add parent/child times or
 * infer total progress from execution rows. Scope is the currently loaded page set. */
export function routineExecutionInsights(rows: RunExecution[]) {
  let longest: { row: RunExecution; durationMs: number } | null = null
  const repeatedPaths = new Set<string>()
  const retained = new Set<string>()
  for (const row of rows) {
    if (row.attempt > 1) repeatedPaths.add(row.execution_path)
    if (row.output_bytes > 0) retained.add(row.step_id)
    if (row.parent_execution_id || row.status !== "completed" || !row.ended_at) continue
    const durationMs = Date.parse(row.ended_at) - Date.parse(row.started_at)
    if (!Number.isFinite(durationMs) || durationMs < 0) continue
    if (!longest || durationMs > longest.durationMs) longest = { row, durationMs }
  }
  return {
    longest,
    repeatedPaths: repeatedPaths.size,
    retainedSteps: retained.size,
  }
}
