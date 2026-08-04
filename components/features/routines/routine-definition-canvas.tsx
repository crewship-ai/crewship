"use client"

import * as React from "react"

import { cn } from "@/lib/utils"
import { TraceCanvas } from "@/components/features/activity/trace-canvas"
import type { HeatmapBucket } from "@/lib/trace/percentile-heatmap"
import type { PipelineDSL } from "@/lib/trace/types"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// The routine's shape, drawn by the code that draws live runs.
//
// Activity's TraceCanvas takes a (run, dsl) pair and derives node status
// from the run. A definition has no run, so it gets one that recorded
// nothing: no step outputs, no current step, a non-terminal status. Every
// node lands on `pending`, and the canvas shows shape without ever
// implying an outcome.
//
// Sharing the renderer is the point. A second diagram drawn by a second
// component is a second thing to keep in sync, and the one this replaces
// had already drifted: it laid a DAG out as a flat chain, so a routine
// with fan-in rendered as a straight line — the wrong shape, drawn
// confidently.

const NO_TOKENS: ReadonlyMap<string, string> = new Map<string, string>()
const NO_BUCKETS: ReadonlyMap<string, HeatmapBucket> = new Map<string, HeatmapBucket>()
const NO_METRICS: ReadonlyMap<string, { durationMs: number; costUsd: number }> = new Map<
  string,
  { durationMs: number; costUsd: number }
>()

/** A run that never ran, so buildTraceGraph paints everything pending. */
function definitionRun(slug: string, name: string): PipelineRun {
  return {
    id: `definition:${slug}`,
    pipeline_id: "definition",
    pipeline_slug: slug,
    pipeline_name: name,
    status: "queued",
    mode: "definition",
    started_at: "",
    ended_at: "",
    current_step_id: "",
    step_outputs: null,
    sub_spans: null,
    cost_usd: 0,
    duration_ms: 0,
    triggered_via: "schedule",
    triggered_by_id: "",
    invoking_crew_id: "",
    invoking_agent_id: "",
    invoking_user_id: "",
    error_message: "",
    failed_at_step: "",
    issue_identifier: "",
  }
}

interface Props {
  /** The stored definition — `routine.definition`, straight through. */
  definition: Record<string, unknown> | null | undefined
  slug: string
  name: string
  selectedStepId?: string | null
  onStepSelect?: (id: string | null) => void
  /** Bring a step into view without a click — driven by the editor caret. */
  focusStepId?: string | null
  className?: string
}

export function RoutineDefinitionCanvas({
  definition,
  slug,
  name,
  selectedStepId = null,
  onStepSelect,
  focusStepId = null,
  className,
}: Props) {
  // One run object per mounted canvas. React Flow keys node state off
  // run.id, and a fresh object each render would reset the graph.
  const run = React.useMemo(() => definitionRun(slug, name), [slug, name])
  const dsl = React.useMemo<PipelineDSL | null>(() => {
    if (!definition || typeof definition !== "object") return null
    const steps = (definition as { steps?: unknown }).steps
    return Array.isArray(steps) ? ({ steps } as PipelineDSL) : null
  }, [definition])

  const handleSelect = React.useCallback(
    (id: string | null) => onStepSelect?.(id),
    [onStepSelect],
  )

  return (
    <div className={cn("relative h-full w-full", className)}>
      <TraceCanvas
        run={run}
        dsl={dsl}
        selectedStepId={selectedStepId}
        onStepSelect={handleSelect}
        workspaceId=""
        waitpointTokensByStepId={NO_TOKENS}
        heatmapBuckets={NO_BUCKETS}
        stepMetrics={NO_METRICS}
        initialFocus="start"
        centerOnSelect
        focusStepId={focusStepId}
        recenterOnResize
      />
      <div className="pointer-events-none absolute left-3 top-3 rounded-md border border-border/60 bg-card/85 px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground backdrop-blur">
        Definition · not a run
      </div>
    </div>
  )
}
