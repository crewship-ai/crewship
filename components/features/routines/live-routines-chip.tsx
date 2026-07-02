"use client"

import { useState } from "react"
import Link from "next/link"
import { Pause, Play, Square } from "lucide-react"
import { toast } from "sonner"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Spinner } from "@/components/ui/spinner"
import {
  isAwaitingApproval,
  useActiveRoutineRuns,
} from "@/hooks/use-active-routine-runs"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import { useTick } from "@/hooks/use-tick"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"
import { routineHref } from "@/lib/routine-href"
import { cn } from "@/lib/utils"
import { formatElapsedSince, formatStepCost } from "./routine-cost-format"

// LiveRoutinesChip — the global "what routine is doing something right
// now?" surface in the toolbar, next to the Online / Crews pills.
// Hidden while nothing is active (no noise); when runs are in flight
// it shows a pulsing count, turns amber if any run is parked on a
// human approval, and opens a popover with the newest active runs:
// current step, elapsed, cost, trace deep-link and cancel.
//
// SCALE: the popover shows at most MAX_POPOVER_ROWS newest runs; more
// than that adds a "View all N running →" footer into the /activity
// rail pre-filtered to the active bucket (?status=active). The chip
// stays a counter + gateway, never an unbounded list.
const MAX_POPOVER_ROWS = 6

export function LiveRoutinesChip() {
  const { workspaceId } = useWorkspace()
  const { runs, activeCount, awaitingApproval, refresh } = useActiveRoutineRuns()
  const [open, setOpen] = useState(false)

  if (activeCount === 0) return null

  const label =
    `${activeCount} ${activeCount === 1 ? "routine" : "routines"} running` +
    (awaitingApproval > 0 ? ` · ${awaitingApproval} awaiting approval` : "")

  // Amber accent when a run waits on a human — that's the state the
  // user needs to notice (the run makes zero progress until they act).
  const tone = awaitingApproval > 0
    ? {
        bg: "bg-amber-50 dark:bg-amber-950/30 border-amber-200 dark:border-amber-800",
        dot: "bg-amber-500",
        text: "text-amber-700 dark:text-amber-400",
      }
    : {
        bg: "bg-blue-50 dark:bg-blue-950/30 border-blue-200 dark:border-blue-800",
        dot: "bg-blue-500",
        text: "text-blue-700 dark:text-blue-400",
      }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`Live routine runs: ${label}`}
          className={cn(
            "flex items-center gap-1.5 px-2.5 py-1 rounded-full border transition-colors",
            tone.bg,
          )}
        >
          <span className={cn("h-1.5 w-1.5 rounded-full animate-pulse", tone.dot)} />
          <span className={cn("text-micro font-medium", tone.text)}>{label}</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[400px] p-0">
        <LiveRunsPopoverBody
          workspaceId={workspaceId}
          runs={runs}
          activeCount={activeCount}
          refresh={refresh}
          onNavigate={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  )
}

// Body is a separate component so the 1s elapsed tick only exists
// while the popover is mounted (Radix unmounts closed content).
function LiveRunsPopoverBody({
  workspaceId,
  runs,
  activeCount,
  refresh,
  onNavigate,
}: {
  workspaceId: string | null
  runs: PipelineRun[]
  activeCount: number
  refresh: () => void
  onNavigate: () => void
}) {
  useTick(1000) // re-render each second so elapsed times tick
  const [cancellingRunId, setCancellingRunId] = useState<string | null>(null)

  // Same cancel contract as RoutineRunsTab: workspace-scoped POST,
  // 403 surfaced as a permission toast, refresh on success.
  const cancelRun = async (runId: string) => {
    if (!workspaceId) return
    setCancellingRunId(runId)
    try {
      const res = await apiFetch(
        `/api/v1/workspaces/${workspaceId}/pipelines/runs/${runId}/cancel`,
        { method: "POST" },
      )
      if (!res.ok) {
        if (res.status === 403) {
          throw new Error("You don't have permission to cancel runs (manager role or above required)")
        }
        const t = await res.text().catch(() => "")
        throw new Error(`${res.status}: ${t || res.statusText}`)
      }
      toast.success("Cancel requested", {
        description: `Run ${runId.slice(0, 12)}… will stop at the next step boundary.`,
      })
      refresh()
    } catch (e) {
      toast.error("Cancel failed", {
        description: e instanceof Error ? e.message : String(e),
      })
    } finally {
      setCancellingRunId(null)
    }
  }

  const visible = runs.slice(0, MAX_POPOVER_ROWS)

  return (
    <div>
      <div className="flex items-center justify-between border-b border-white/[0.06] px-3 py-2">
        <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
          Active routine runs
        </span>
        <span className="text-[10px] text-muted-foreground tabular-nums">{activeCount} live</span>
      </div>
      <ul className="divide-y divide-white/[0.05]">
        {visible.map((run) => (
          <LiveRunRow
            key={run.id}
            run={run}
            cancelling={cancellingRunId === run.id}
            onCancel={() => cancelRun(run.id)}
            onNavigate={onNavigate}
          />
        ))}
      </ul>
      {activeCount > MAX_POPOVER_ROWS && (
        <div className="border-t border-white/[0.06] p-2">
          <Link
            href="/activity?status=active"
            onClick={onNavigate}
            className="block w-full rounded px-2 py-1.5 text-center text-xs text-blue-400 hover:bg-white/[0.04]"
          >
            View all {activeCount} running →
          </Link>
        </div>
      )}
    </div>
  )
}

function LiveRunRow({
  run,
  cancelling,
  onCancel,
  onNavigate,
}: {
  run: PipelineRun
  cancelling: boolean
  onCancel: () => void
  onNavigate: () => void
}) {
  const awaiting = isAwaitingApproval(run.status)
  const elapsed = formatElapsedSince(run.started_at)

  return (
    <li className="px-3 py-2.5">
      <div className="flex items-center gap-2">
        <span
          className={cn(
            "h-1.5 w-1.5 shrink-0 rounded-full animate-pulse",
            awaiting ? "bg-amber-500" : "bg-blue-500",
          )}
        />
        <span className="min-w-0 flex-1 truncate text-xs font-semibold text-foreground">
          {run.pipeline_name || run.pipeline_slug}
        </span>
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
          {run.id.slice(0, 10)}…{elapsed ? ` · ${elapsed}` : ""}
          {run.cost_usd > 0 ? ` · ${formatStepCost(run.cost_usd)}` : ""}
        </span>
      </div>
      <div className="mt-1 flex items-center gap-1.5 pl-3.5 text-[11px] text-muted-foreground">
        {awaiting ? (
          <>
            <Pause className="h-3 w-3 shrink-0 text-amber-400" />
            <span className="text-amber-400">awaiting approval</span>
            <Link
              href={routineHref(run.pipeline_slug)}
              onClick={onNavigate}
              className="ml-1 rounded border border-white/[0.08] px-1.5 py-0.5 text-[10px] text-foreground/80 hover:bg-white/[0.05]"
            >
              Review →
            </Link>
          </>
        ) : (
          <>
            <Play className="h-3 w-3 shrink-0 text-blue-400" />
            {/* current_step_id is the step's id/slug — the list feed
                has no step totals, so no "2/3" here by design. */}
            <span className="truncate font-mono text-foreground/85">
              {run.current_step_id || "starting…"}
            </span>
          </>
        )}
      </div>
      <div className="mt-1.5 flex items-center gap-2 pl-3.5">
        <Link
          href={`/activity?run=${encodeURIComponent(run.id)}`}
          onClick={onNavigate}
          className="rounded-md border border-white/[0.08] bg-white/[0.03] px-2 py-0.5 text-[11px] text-foreground/85 hover:bg-white/[0.06]"
        >
          Open trace ↗
        </Link>
        <button
          type="button"
          onClick={onCancel}
          disabled={cancelling}
          aria-label="Cancel run"
          title="Cancel this run"
          className="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-rose-500/10 hover:text-rose-400 disabled:opacity-50"
        >
          {cancelling ? <Spinner className="h-3 w-3" /> : <Square className="h-3 w-3" />}
          Cancel
        </button>
      </div>
    </li>
  )
}
