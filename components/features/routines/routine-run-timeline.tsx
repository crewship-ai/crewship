"use client"

// The run as a waterfall (Harbor 2 RT-RUN › Timeline): one row per step, its
// bar placed on the run's own clock from the recorded executions. A step that
// is still going reaches to now.

import { cn } from "@/lib/utils"
import { formatDurationMs } from "@/lib/activity-stream"
import type { TimelineBar } from "@/lib/routine-page"

const BAR: Record<string, string> = {
  completed: "bg-success",
  succeeded: "bg-success",
  failed: "bg-destructive",
  running: "bg-primary",
  waiting: "bg-warn",
  cancelled: "bg-muted-foreground/50",
  skipped: "bg-muted-foreground/30",
}

export function RoutineRunTimeline({ bars, nameOf }: { bars: TimelineBar[]; nameOf: (stepId: string) => string }) {
  if (bars.length === 0) return <p className="text-xs text-muted-foreground">No step has started yet.</p>
  return (
    <ol className="flex flex-col gap-1.5 rounded-card border border-hairline bg-card p-3" aria-label="Timeline" data-testid="run-timeline">
      {bars.map((b) => (
        <li key={b.stepId} className="grid grid-cols-[minmax(0,10rem)_minmax(0,1fr)_4.5rem] items-center gap-3 text-xs sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)_5rem]">
          <span className="truncate" title={b.stepId}>{nameOf(b.stepId)}</span>
          <span className="relative h-3 rounded-sm bg-foreground/[0.04]">
            <i
              className={cn("absolute inset-y-0 rounded-sm", BAR[b.status] ?? "bg-primary")}
              style={{ left: `${b.left}%`, width: `${Math.min(b.width, 100 - b.left)}%` }}
              aria-hidden
            />
          </span>
          <span className="text-right font-mono tabular-nums text-muted-foreground">{b.durationMs != null ? formatDurationMs(b.durationMs) : "…"}</span>
        </li>
      ))}
    </ol>
  )
}
