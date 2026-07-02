"use client"

import { useState } from "react"
import Link from "next/link"
import { Pause, Play, Square } from "lucide-react"
import { toast } from "sonner"
import { Spinner } from "@/components/ui/spinner"
import { isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import { apiFetch } from "@/lib/api-fetch"
import { routineHref } from "@/lib/routine-href"
import { cn } from "@/lib/utils"
import { formatElapsedSince, formatStepCost } from "./routine-cost-format"

// LiveRunRow — one active routine run inside the header Activity
// dropdown's LIVE section: pulse dot (amber while parked on a human),
// routine name, elapsed + cost (mono, right), current step or the
// awaiting-approval hint, and per-row Review / Trace / Cancel actions.
//
// Extracted from the retired header LiveRoutinesChip (the popover
// rows moved wholesale into the Activity dropdown, feedback
// 2026-07-02) so the row rendering + cancel contract live in exactly
// one place.

// SCALE: the LIVE section shows at most this many rows; overflow goes
// through the dropdown footer into /activity?status=active. The
// dropdown stays a counter + gateway, never an unbounded list.
export const MAX_LIVE_ROWS = 6

// useCancelRoutineRun — same cancel contract as RoutineRunsTab:
// workspace-scoped POST, 403 surfaced as a permission toast, refresh
// on success.
export function useCancelRoutineRun(workspaceId: string | null, refresh: () => void) {
  const [cancellingRunId, setCancellingRunId] = useState<string | null>(null)

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

  return { cancellingRunId, cancelRun }
}

export function LiveRunRow({
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
