"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Activity, Bot } from "lucide-react"
import {
  BarMenu,
  BarMenuBody,
  BarMenuEmpty,
  BarMenuFooter,
  BarMenuFooterLink,
  BarMenuHeader,
  BarMenuRow,
  BarMenuSection,
} from "@/components/layout/bar-menu"
import { Pill } from "@/components/ui/detail"
import {
  LiveRunRow,
  MAX_LIVE_ROWS,
  useCancelRoutineRun,
} from "@/components/features/routines/live-run-row"
import { formatStepCost } from "@/components/features/routines/routine-cost-format"
import { SourcePill } from "@/components/features/activity/source-pill"
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
//
// The chrome is the shared top-bar kit (components/layout/bar-menu.tsx). This
// was a 400px Radix dropdown with its own uppercase text-[9px] section labels
// and no header/footer contract, sitting one icon away from a 380px Inbox that
// had all three. Same panel now, same rows, same footer.
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
  // Amber the moment anything waits on a human approval, blue while routines
  // run, emerald preserved for agent-only activity.
  const badgeTone = awaitingApproval > 0 ? "urgent" : activeCount > 0 ? "active" : "live"

  const ariaLabel =
    liveTotal > 0
      ? `Activity: ${liveTotal} live` +
        (awaitingApproval > 0 ? `, ${awaitingApproval} awaiting approval` : "")
      : "Activity"

  return (
    <BarMenu
      icon={Activity}
      ariaLabel={ariaLabel}
      badge={{ count: liveTotal, tone: badgeTone }}
      open={open}
      onOpenChange={setOpen}
      testId="activity"
    >
      {open && (
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
      )}
    </BarMenu>
  )
}

// Body is a separate component, mounted only while the panel is open, so the
// 1s elapsed tick does not run behind a closed popover.
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

  const hidden = Math.max(0, liveRuns.length + agentRuns.length - visibleRoutine.length - visibleAgents.length)

  return (
    <>
      <BarMenuHeader
        title="Activity"
        // The pill carries the one fact that changes what you do next: work is
        // parked on a human. Same slot the Inbox uses for an expiring deadline.
        pill={
          awaitingApproval > 0 ? (
            <Pill tone="warn">
              {awaitingApproval} awaiting approval
            </Pill>
          ) : undefined
        }
        meta={liveTotal > 0 ? `${liveTotal} live` : "nothing live"}
      />

      <BarMenuBody>
        {isEmpty ? (
          <BarMenuEmpty icon={Activity} message="Nothing running right now" />
        ) : (
          <>
            {liveTotal > 0 && (
              <BarMenuSection
                label="Live"
                count={liveTotal}
                tone={awaitingApproval > 0 ? "warn" : undefined}
                overflow={hidden > 0 ? `+${hidden} more running` : undefined}
              >
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
              </BarMenuSection>
            )}
            {recent.length > 0 && (
              <BarMenuSection label="Recent" count={recent.length}>
                {recent.map((run) => (
                  <RecentRunRow
                    key={run.id}
                    run={run}
                    onClick={() => onOpenItem(`/activity?run=${encodeURIComponent(run.id)}`)}
                  />
                ))}
              </BarMenuSection>
            )}
          </>
        )}
      </BarMenuBody>

      <BarMenuFooter>
        <BarMenuFooterLink asChild onClick={onNavigate}>
          <Link href={liveTotal > 0 ? "/activity?status=active" : "/activity"}>
            View all activity →
          </Link>
        </BarMenuFooterLink>
      </BarMenuFooter>
    </>
  )
}

// AgentRunRow — an in-flight agent run from the legacy feed. Same row
// skeleton as everything else in the bar; routine runs get the richer
// LiveRunRow (step, cost, actions) instead.
function AgentRunRow({ item, onClick }: { item: ActiveRunItem; onClick: () => void }) {
  return (
    <BarMenuRow
      onClick={onClick}
      leading={
        <span className="flex h-6 w-6 items-center justify-center rounded-md bg-primary/15">
          <Bot className="h-3.5 w-3.5 text-primary" />
        </span>
      }
      title={item.label}
      meta={
        <>
          <span className="h-1 w-1 shrink-0 rounded-full bg-success animate-pulse" />
          <span>Agent</span>
          {item.sublabel && <span className="truncate">· {item.sublabel}</span>}
        </>
      }
      trailing={
        item.startedAt ? (
          <span className="type-meta text-muted-foreground-soft">{relTime(item.startedAt)}</span>
        ) : undefined
      }
    />
  )
}

// RecentRunRow — one terminal routine run: status dot, name, then
// `status · Xm ago · $cost` in mono on the right. Click jumps to the
// run's trace.
function RecentRunRow({ run, onClick }: { run: PipelineRun; onClick: () => void }) {
  const failed = run.status === "failed"
  return (
    <BarMenuRow
      onClick={onClick}
      leading={
        <span className="flex h-6 w-6 items-center justify-center">
          <span
            className={cn("h-2 w-2 rounded-full", failed ? "bg-destructive" : "bg-success")}
          />
        </span>
      }
      title={run.pipeline_name || run.pipeline_slug}
      // Unlinked: this whole row is one control that opens the run's trace,
      // and the kit renders such a row as a <button>. See source-pill.tsx.
      meta={<SourcePill run={run} linked={false} />}
      trailing={
        <span
          className={cn(
            "type-meta font-mono tabular-nums",
            failed ? "text-destructive" : "text-muted-foreground-soft",
          )}
        >
          {run.status} · {relTime(run.ended_at || run.started_at)}
          {run.cost_usd > 0 ? ` · ${formatStepCost(run.cost_usd)}` : ""}
        </span>
      }
    />
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
