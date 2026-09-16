import type { RunExecution } from "@/hooks/use-run-executions"
import { routineExecutionInsights } from "@/lib/routine-execution-insights"
import { formatDurationMs } from "@/lib/activity-stream"
import { describeStep, isRecord } from "@/lib/routine-step-describe"

export function RoutineExecutionInsights({
  rows,
  partial,
  definition,
}: {
  rows: RunExecution[] | null
  partial: boolean
  definition: unknown
}) {
  if (!rows?.length) return null
  const insight = routineExecutionInsights(rows)
  const steps = isRecord(definition) && Array.isArray(definition.steps) ? definition.steps : []
  const name = (id: string) => {
    const step = steps.find((s) => isRecord(s) && s.id === id)
    return step ? describeStep(step, 1).title : id
  }
  return (
    <details className="rounded-xl border border-border/60 bg-card px-4 py-3 text-xs">
      <summary className="cursor-pointer font-medium">
        Time and attempts{partial ? " · partial history" : ""}
      </summary>
      <div className="mt-3 space-y-2" data-testid="routine-execution-insights">
        <p className="text-muted-foreground">
          {partial
            ? "Based only on loaded executions. Load more in the step list before comparing the whole run."
            : "Based on recorded executions."}{" "}
          Parent and child durations are not added together.
        </p>
        {insight.longest ? (
          <p>
            Longest completed top-level attempt:{" "}
            <strong>{name(insight.longest.row.step_id)}</strong> ·{" "}
            {formatDurationMs(insight.longest.durationMs)} · attempt{" "}
            {insight.longest.row.attempt}.
          </p>
        ) : (
          <p>No completed top-level duration is available in these records.</p>
        )}
        <p>
          {insight.repeatedPaths} execution paths have a recorded attempt beyond the first. This
          includes agent and review paths; it is not a count of failed steps.
        </p>
        <p>{insight.retainedSteps} step identifiers have stored output in these records.</p>
      </div>
    </details>
  )
}
