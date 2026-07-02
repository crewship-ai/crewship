"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Activity, Bot } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Button } from "@/components/ui/button"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  LiveRunRow,
  MAX_LIVE_ROWS,
  useCancelRoutineRun,
} from "@/components/features/routines/live-run-row"
import { formatStepCost } from "@/components/features/routines/routine-cost-format"
import { useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import { useActiveRuns, type ActiveRunItem } from "@/hooks/use-active-runs"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import { useTick } from "@/hooks/use-tick"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"

// ActivityBell — the global "what's running now" surface in the
// toolbar. The single home for live routine visibility (the header
// LiveRoutinesChip was retired in favour of this dropdown, feedback
// 2026-07-02):
//
//   badge   — count of live runs on the Activity icon; amber the
//             moment anything waits on a human approval, blue while
//             routines run, emerald preserved for agent-only activity
//             (the badge's historical meaning). Hidden at zero.
//   LIVE    — up to MAX_LIVE_ROWS active routine runs with current
//             step, elapsed + cost and Review / Trace / Cancel
//             actions (rows shared with nothing else — see
//             live-run-row.tsx), plus any in-flight agent runs.
//   RECENT  — the last few terminal routine runs (completed/failed)
//             so "did my run just finish?" doesn't need a page hop.
//   footer  — View all activity →, pre-filtered to the active bucket
//             while anything is live.
//
// Routine runs come from the shared useActiveRoutineRuns provider
// (one fetch/poll/WS stream for every live surface); the legacy
// useActiveRuns feed still contributes agent runs — its routine rows
// are dropped here to avoid duplicates.
export function ActivityBell() {
  const router = useRouter()
  const { workspaceId } = useWorkspace()
  const [open, setOpen] = useState(false)
  const { runs: activeItems } = useActiveRuns(workspaceId)
  const {
    runs: liveRuns,
    activeCount,
    awaitingApproval,
    recentRuns,
    refresh,
  } = useActiveRoutineRuns()

  const agentRuns = activeItems.filter((i) => i.kind === "agent")
  const liveTotal = activeCount + agentRuns.length

  // Live-run semantics win the badge tone; the count merges both feeds.
  const badgeClass =
    awaitingApproval > 0
      ? "bg-amber-500 text-amber-950"
      : activeCount > 0
        ? "bg-blue-500 text-white"
        : "bg-emerald-500 text-white"

  const ariaLabel =
    liveTotal > 0
      ? `Activity: ${liveTotal} live` +
        (awaitingApproval > 0 ? `, ${awaitingApproval} awaiting approval` : "")
      : "Activity"

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative h-8 w-8"
          aria-label={ariaLabel}
        >
          <Activity className="h-4 w-4" />
          <AnimatePresence>
            {liveTotal > 0 && (
              <motion.span
                initial={{ scale: 0 }}
                animate={{ scale: 1 }}
                exit={{ scale: 0 }}
                data-testid="activity-live-badge"
                className={cn(
                  "absolute -right-0.5 -top-0.5 flex h-4 min-w-[16px] items-center justify-center rounded-full px-1 text-[9px] font-semibold",
                  badgeClass,
                )}
              >
                {liveTotal > 99 ? "99+" : liveTotal}
              </motion.span>
            )}
          </AnimatePresence>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-[400px] p-0">
        <ActivityDropdownBody
          workspaceId={workspaceId}
          liveRuns={liveRuns}
          agentRuns={agentRuns}
          liveTotal={liveTotal}
          awaitingApproval={awaitingApproval}
          recentRuns={recentRuns}
          refresh={refresh}
          onNavigate={() => setOpen(false)}
          onOpenItem={(href) => {
            setOpen(false)
            router.push(href)
          }}
        />
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// Body is a separate component so the 1s elapsed tick only exists
// while the dropdown is mounted (Radix unmounts closed content).
function ActivityDropdownBody({
  workspaceId,
  liveRuns,
  agentRuns,
  liveTotal,
  awaitingApproval,
  recentRuns,
  refresh,
  onNavigate,
  onOpenItem,
}: {
  workspaceId: string | null
  liveRuns: PipelineRun[]
  agentRuns: ActiveRunItem[]
  liveTotal: number
  awaitingApproval: number
  recentRuns: PipelineRun[]
  refresh: () => void
  onNavigate: () => void
  onOpenItem: (href: string) => void
}) {
  useTick(1000) // re-render each second so elapsed times tick
  const { cancellingRunId, cancelRun } = useCancelRoutineRun(workspaceId, refresh)

  // Routine rows first (they carry step/cost/actions); agent runs fill
  // whatever remains of the LIVE budget. Overflow exits via the footer.
  const visibleRoutine = liveRuns.slice(0, MAX_LIVE_ROWS)
  const visibleAgents = agentRuns.slice(0, Math.max(0, MAX_LIVE_ROWS - visibleRoutine.length))
  const recent = recentRuns.slice(0, 3)
  const isEmpty = liveTotal === 0 && recent.length === 0

  return (
    <div>
      <div className="flex items-center justify-between border-b border-white/[0.06] px-3 py-2">
        <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
          Activity
        </span>
        {liveTotal > 0 && (
          <span className="inline-flex items-center gap-1.5 text-[10px] tabular-nums text-muted-foreground">
            <span
              className={cn(
                "h-1.5 w-1.5 rounded-full animate-pulse",
                awaitingApproval > 0 ? "bg-amber-500" : "bg-blue-500",
              )}
            />
            {liveTotal} live
            {awaitingApproval > 0 ? ` · ${awaitingApproval} awaiting approval` : ""}
          </span>
        )}
      </div>
      <ScrollArea className="max-h-[420px]">
        {isEmpty ? (
          <div className="flex flex-col items-center justify-center gap-2 p-6 text-center">
            <Activity className="h-6 w-6 text-muted-foreground/30" />
            <span className="text-xs text-muted-foreground">Nothing running right now</span>
          </div>
        ) : (
          <>
            {liveTotal > 0 && (
              <>
                <div className="px-3 pb-0.5 pt-2 text-[9px] font-semibold uppercase tracking-widest text-muted-foreground/60">
                  Live
                </div>
                <ul className="divide-y divide-white/[0.05]">
                  {visibleRoutine.map((run) => (
                    <LiveRunRow
                      key={run.id}
                      run={run}
                      cancelling={cancellingRunId === run.id}
                      onCancel={() => cancelRun(run.id)}
                      onNavigate={onNavigate}
                    />
                  ))}
                  {visibleAgents.map((item) => (
                    <AgentRunRow key={item.id} item={item} onClick={() => onOpenItem(item.href)} />
                  ))}
                </ul>
              </>
            )}
            {recent.length > 0 && (
              <>
                <div className="border-t border-white/[0.06] px-3 pb-0.5 pt-2 text-[9px] font-semibold uppercase tracking-widest text-muted-foreground/60">
                  Recent
                </div>
                <ul className="divide-y divide-white/[0.04]">
                  {recent.map((run) => (
                    <RecentRunRow key={run.id} run={run} onClick={() => onOpenItem(`/activity?run=${encodeURIComponent(run.id)}`)} />
                  ))}
                </ul>
              </>
            )}
          </>
        )}
      </ScrollArea>
      <div className="border-t border-white/[0.06] p-2">
        <Link
          href={liveTotal > 0 ? "/activity?status=active" : "/activity"}
          onClick={onNavigate}
          className="block w-full rounded px-2 py-1.5 text-center text-xs text-emerald-400 hover:bg-white/[0.04]"
        >
          View all activity →
        </Link>
      </div>
    </div>
  )
}

// AgentRunRow — an in-flight agent run from the legacy feed. Keeps
// the pre-redesign row shape (icon + label + relative time); routine
// runs get the richer LiveRunRow instead.
function AgentRunRow({ item, onClick }: { item: ActiveRunItem; onClick: () => void }) {
  return (
    <li
      onClick={onClick}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onClick()
        }
      }}
      className="flex cursor-pointer items-start gap-2 px-3 py-2 hover:bg-white/[0.04]"
    >
      <Bot className="mt-0.5 h-3.5 w-3.5 shrink-0 text-blue-300" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-xs font-medium text-foreground">{item.label}</div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground">
          <span className="h-1 w-1 rounded-full bg-emerald-500 animate-pulse" />
          Agent
          {item.sublabel ? ` · ${item.sublabel}` : ""}
          {item.startedAt ? ` · ${relTime(item.startedAt)}` : ""}
        </div>
      </div>
    </li>
  )
}

// RecentRunRow — one terminal routine run: status dot, name, then
// `status · Xm ago · $cost` in mono on the right. Click jumps to the
// run's trace.
function RecentRunRow({ run, onClick }: { run: PipelineRun; onClick: () => void }) {
  const failed = run.status === "failed"
  return (
    <li
      onClick={onClick}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onClick()
        }
      }}
      className="flex cursor-pointer items-center gap-2 px-3 py-2 hover:bg-white/[0.04]"
    >
      <span
        className={cn(
          "h-1.5 w-1.5 shrink-0 rounded-full",
          failed ? "bg-rose-500" : "bg-emerald-500",
        )}
      />
      <span className="min-w-0 flex-1 truncate text-xs font-medium text-muted-foreground">
        {run.pipeline_name || run.pipeline_slug}
      </span>
      <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
        {run.status} · {relTime(run.ended_at || run.started_at)}
        {run.cost_usd > 0 ? ` · ${formatStepCost(run.cost_usd)}` : ""}
      </span>
    </li>
  )
}

function relTime(iso?: string) {
  if (!iso) return ""
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ""
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  // Floor so a run never reads "1h ago" at 59.5 min — labels stay monotonic.
  const mins = Math.floor(Math.abs(diff) / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}
