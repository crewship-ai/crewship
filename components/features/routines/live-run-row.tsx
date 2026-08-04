"use client"

import { useState } from "react"
import Link from "next/link"
import { Pause, Play, Square } from "lucide-react"
import { toast } from "sonner"
import { BarMenuRow, BarMenuRowAction } from "@/components/layout/bar-menu"
import { Spinner } from "@/components/ui/spinner"
import { isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import { SourcePill } from "@/components/features/activity/source-pill"
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
//
// The row's shape now comes from the shared top-bar kit
// (components/layout/bar-menu.tsx): identity on the left, what it IS on the
// first line, what it is ABOUT on the second, figures on the right, actions
// beneath. It had been the one row in the bar with its own three-line layout
// and its own text-[10px]/[11px] pair; the Inbox's ladder wins because the
// Inbox is the surface that was designed rather than grown.

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
    <BarMenuRow
      leading={
        // The identity slot carries a status dot rather than a face: a routine
        // run has no subject to show, and what you want off it at a glance is
        // "moving" vs "parked on a human".
        <span className="flex h-6 w-6 items-center justify-center">
          <span
            className={cn(
              "h-2 w-2 rounded-full animate-pulse",
              awaiting ? "bg-warn" : "bg-primary",
            )}
          />
        </span>
      }
      title={run.pipeline_name || run.pipeline_slug}
      meta={
        <>
          {/* provenance first — "this run happened because X" (#1418 follow-up) */}
          <SourcePill run={run} />
          {awaiting ? (
            <>
              <Pause className="h-3 w-3 shrink-0 text-warn" />
              <span className="text-warn">awaiting approval</span>
            </>
          ) : (
            <>
              <Play className="h-3 w-3 shrink-0 text-primary" />
              {/* current_step_id is the step's id/slug — the list feed
                  has no step totals, so no "2/3" here by design. */}
              <span className="truncate font-mono text-foreground/85">
                {run.current_step_id || "starting…"}
              </span>
            </>
          )}
        </>
      }
      trailing={
        <span className="type-meta font-mono tabular-nums text-muted-foreground-soft">
          {elapsed}
          {run.cost_usd > 0 ? `${elapsed ? " · " : ""}${formatStepCost(run.cost_usd)}` : ""}
        </span>
      }
      actions={
        <>
          {awaiting && (
            <BarMenuRowAction asChild>
              <Link href={routineHref(run.pipeline_slug)} onClick={onNavigate}>
                Review →
              </Link>
            </BarMenuRowAction>
          )}
          <BarMenuRowAction asChild>
            <Link href={`/activity?run=${encodeURIComponent(run.id)}`} onClick={onNavigate}>
              Open trace ↗
            </Link>
          </BarMenuRowAction>
          <BarMenuRowAction danger onClick={onCancel} disabled={cancelling} ariaLabel="Cancel run" title="Cancel this run">
            {cancelling ? <Spinner className="h-3 w-3" /> : <Square className="h-3 w-3" />}
            Cancel
          </BarMenuRowAction>
        </>
      }
    />
  )
}
