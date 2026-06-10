"use client"

import Link from "next/link"
import { cn } from "@/lib/utils"
import { Progress } from "@/components/ui/progress"
import type { Mission } from "@/lib/types/mission"
import { formatRelativeTime } from "@/lib/time"

interface RecentMissionsTableProps {
  missions: Mission[]
}

const STATUS_STYLES: Record<string, string> = {
  BACKLOG: "text-muted-foreground border-border bg-muted/20",
  TODO: "text-cyan-400 border-cyan-500/30 bg-cyan-500/10",
  PLANNING: "text-muted-foreground border-border bg-muted/20",
  IN_PROGRESS: "text-blue-400 border-blue-500/30 bg-blue-500/10",
  REVIEW: "text-amber-400 border-amber-500/30 bg-amber-500/10",
  COMPLETED: "text-emerald-400 border-emerald-500/30 bg-emerald-500/10",
  DONE: "text-emerald-400 border-emerald-500/30 bg-emerald-500/10",
  FAILED: "text-red-400 border-red-500/30 bg-red-500/10",
  CANCELLED: "text-muted-foreground border-border bg-muted/20",
}

// Indicator fill classes for the inline Progress bar, one per mission status.
const PROGRESS_INDICATOR: Record<string, string> = {
  COMPLETED: "bg-emerald-400",
  DONE: "bg-emerald-400",
  FAILED: "bg-red-400",
  IN_PROGRESS: "bg-blue-400",
  REVIEW: "bg-amber-400",
}

// Statuses that count as "work done" for the per-mission task progress ratio.
// Matches the dashboard-wide semantics change that includes REVIEW alongside
// COMPLETED/DONE so missions waiting for approval are not artificially
// undercounted on the recent list.
const DONE_TASK_STATUSES = new Set(["COMPLETED", "DONE", "REVIEW"])

function formatCost(cost: number | null | undefined): string {
  if (cost == null) return "—"
  if (cost === 0) return "$0.00"
  if (cost < 0.01) return "<$0.01"
  return `$${cost.toFixed(2)}`
}

export function RecentMissionsTable({ missions }: RecentMissionsTableProps) {
  if (missions.length === 0) {
    return (
      <div className="flex items-center justify-center h-[120px] text-[11px] text-muted-foreground-soft">
        No missions yet
      </div>
    )
  }

  return (
    <div className="overflow-x-auto -mx-1 px-1">
      <div className="min-w-[640px]">
        {missions.map((m) => {
          const taskTotal = m.tasks?.length ?? 0
          const taskDone = m.tasks?.filter((t) => DONE_TASK_STATUSES.has(t.status)).length ?? 0
          const rawPct = taskTotal > 0
            ? Math.round((taskDone / taskTotal) * 100)
            : (m.status === "COMPLETED" || m.status === "DONE" ? 100 : 0)
          // Clamp in case of stale aggregates or task/mission status drift.
          const pct = Math.max(0, Math.min(100, rawPct))
          const statusCls = STATUS_STYLES[m.status] ?? STATUS_STYLES.BACKLOG
          const progressCls = PROGRESS_INDICATOR[m.status] ?? "bg-blue-400"
          return (
            <Link
              key={m.id}
              href={m.identifier ? `/issues/${m.identifier}` : "/issues"}
              className="grid items-center gap-3 px-1.5 py-2 text-[11px] border-b border-border/60 last:border-b-0 hover:bg-white/[0.02] rounded grid-cols-[56px_minmax(0,1fr)_110px_78px_64px_72px]"
            >
              <span className="font-mono text-[10px] text-muted-foreground truncate">{m.identifier ?? "—"}</span>
              <span className="text-foreground/80 truncate">{m.title}</span>
              <div className="flex items-center gap-1.5">
                <Progress value={pct} className="h-1 flex-1 bg-white/[0.06]" indicatorClassName={progressCls} />
                <span className="text-[9px] font-mono text-muted-foreground tabular-nums w-8 text-right shrink-0">{pct}%</span>
              </div>
              <span className={cn("inline-flex items-center justify-center px-1.5 py-0.5 rounded text-[9px] font-semibold uppercase tracking-wide border", statusCls)}>
                {m.status.replace("_", " ").toLowerCase()}
              </span>
              <span className="font-mono text-[10px] text-muted-foreground text-right tabular-nums">
                {formatCost(m.total_estimated_cost)}
              </span>
              <span className="text-[10px] text-muted-foreground text-right">
                {formatRelativeTime(m.updated_at)}
              </span>
            </Link>
          )
        })}
      </div>
    </div>
  )
}
