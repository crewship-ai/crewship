"use client"

import { useState } from "react"
import Link from "next/link"
import { useGuardedRouter as useRouter } from "@/hooks/use-navigation-guard"
import { Activity, Bot } from "lucide-react"
import {
  BarMenu,
  BarMenuBody,
  BarMenuChips,
  BarMenuEmpty,
  BarMenuFooter,
  BarMenuFooterLink,
  BarMenuHeader,
  BarMenuRow,
  BarMenuSection,
} from "@/components/layout/bar-menu"
import { StatusPill } from "@/components/ui/status-pill"
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
import { RUN_TONE_DOT, runStatusLabel, runTone } from "@/lib/activity-run"
import { roleAtLeast } from "@/lib/routine-governance"

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
//   FAILED  — the terminal runs whose runTone is failed, on their own.
//   chips   — Live (n) / Recent / Failed (n) under the header; the panel
//             shows the selected section only. Default is Live while
//             anything is live, else Recent. A failure used to be one
//             red word among three Recent rows and scrolled off the
//             moment two newer runs finished — the Failed chip is the
//             place it cannot be pushed out of.
//   icon    — tinted (BarMenu `active`) while anything is live, on top
//             of the badge: the badge says how many and how bad, the
//             tint says "something is moving" at a glance.
//   footer  — View all activity →, pre-filtered to the bucket the
//             panel is showing (waiting / active / failed).
//
// No step x/y progress bar on live rows: the list feed carries
// current_step_id only — no step index and no step total on the run
// (step_count lives on the pipeline definition, not the run) — so a
// bar would be invented data. See live-run-row.tsx.
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
  const { workspaceId, role } = useWorkspace()
  const [open, setOpen] = useState(false)
  const { runs: activeItems } = useActiveRuns(workspaceId)
  const {
    runs: liveRuns,
    activeCount,
    awaitingApproval,
    recentRuns,
    recentDashboardRuns,
    refresh,
  } = useActiveRoutineRuns()

  const agentRuns = activeItems.filter((i) => i.kind === "agent")
  const liveTotal = activeCount + agentRuns.length

  // Live-run semantics win the badge tone; the count merges both feeds.
  // Amber the moment anything waits on a human approval, blue while routines
  // run, emerald preserved for agent-only activity.
  //
  // A tone token, not raw classes. This branch carried the raw form —
  // `bg-warn text-white` — from the sweep that fixed eight invisible
  // buttons; main then replaced the whole call with BarMenu's
  // badge={{count, tone}} API, which routes the same decision through
  // the shared kit. Keeping the local classes would have meant styling
  // a prop that no longer exists.
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
      active={liveTotal > 0}
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
          // The wider (12-run) terminal window, not the 3-row one: the Failed
          // chip counts failures across it, and a window of three would
          // report "Failed (0)" as soon as three runs succeeded after one
          // failed — the exact loss the chip exists to prevent. Falls back
          // for a provider that predates the wider slice.
          recentRuns={recentDashboardRuns ?? recentRuns}
          refresh={refresh}
          canCancel={roleAtLeast(role, "ADMIN")}
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

export type ActivityChip = "live" | "recent" | "failed"

/**
 * Which chip the panel opens on. Live while anything is live — that is the
 * question the icon's badge just raised — otherwise Recent. Never Failed by
 * default: the chip carries its count, and opening on old failures every time
 * would make the panel read as an alarm when nothing is happening.
 */
export function defaultActivityChip(liveTotal: number): ActivityChip {
  return liveTotal > 0 ? "live" : "recent"
}

/** The terminal runs the Failed chip shows: runTone failed, in feed order. */
export function failedRuns(runs: PipelineRun[]): PipelineRun[] {
  return runs.filter((r) => runTone(r.status) === "failed")
}

/**
 * Where "View all activity →" goes. The approval wins whatever the chip —
 * a run parked on a human is the reason to leave the panel — then the chip
 * the reader chose, then the live bucket. /activity accepts `failed` as a
 * scope (lib/activity-deeplink.ts SCOPES).
 */
export function activityFooterHref(chip: ActivityChip, liveTotal: number, awaitingApproval: number): string {
  if (awaitingApproval > 0) return "/activity?status=waiting"
  if (chip === "failed") return "/activity?status=failed"
  if (chip === "live" && liveTotal > 0) return "/activity?status=active"
  return "/activity"
}

// How many terminal rows a chip shows. Recent was three while it shared the
// panel with Live; with a chip of its own it gets the LIVE budget.
const MAX_TERMINAL_ROWS = MAX_LIVE_ROWS

// Body is a separate component, mounted only while the panel is open, so the
// 1s elapsed tick does not run behind a closed popover. That also makes the
// chip choice per-opening: each open starts from defaultActivityChip, so a
// panel last left on Failed does not reopen there while runs are live.
function ActivityDropdownBody({
  workspaceId,
  liveRuns,
  agentRuns,
  liveTotal,
  awaitingApproval,
  recentRuns,
  refresh,
  canCancel,
  onNavigate,
  onOpenItem,
}: {
  workspaceId: string | null
  canCancel: boolean
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
  const failed = failedRuns(recentRuns)
  const isEmpty = liveTotal === 0 && recentRuns.length === 0

  const [picked, setPicked] = useState<ActivityChip | null>(null)
  const chip = picked ?? defaultActivityChip(liveTotal)

  const hidden = Math.max(0, liveRuns.length + agentRuns.length - visibleRoutine.length - visibleAgents.length)

  return (
    <>
      <BarMenuHeader
        title="Activity"
        // The pill carries the one fact that changes what you do next: work is
        // parked on a human. Same slot the Inbox uses for an expiring deadline.
        pill={
          awaitingApproval > 0 ? (
            <StatusPill size="md" tone="warn" label={`${awaitingApproval} waiting for you`} />
          ) : undefined
        }
        meta={liveTotal > 0 ? `${liveTotal} live` : "nothing live"}
      />

      {!isEmpty && (
        <BarMenuChips<ActivityChip>
          ariaLabel="Show"
          testId="activity-chip"
          value={chip}
          onChange={setPicked}
          chips={[
            { id: "live", label: "Live", count: liveTotal, tone: awaitingApproval > 0 ? "warn" : undefined },
            { id: "recent", label: "Recent" },
            { id: "failed", label: "Failed", count: failed.length },
          ]}
        />
      )}

      <BarMenuBody>
        {isEmpty ? (
          <BarMenuEmpty icon={Activity} message="Nothing running right now" />
        ) : chip === "live" ? (
          liveTotal > 0 ? (
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
                  canCancel={canCancel}
                />
              ))}
              {visibleAgents.map((item) => (
                <AgentRunRow key={item.id} item={item} onClick={() => onOpenItem(item.href)} />
              ))}
            </BarMenuSection>
          ) : (
            <BarMenuEmpty icon={Activity} message="Nothing running right now" />
          )
        ) : (
          <TerminalSection
            label={chip === "failed" ? "Failed" : "Recent"}
            runs={chip === "failed" ? failed : recentRuns}
            emptyMessage={chip === "failed" ? "No recent failures" : "No recent runs"}
            onOpenItem={onOpenItem}
          />
        )}
      </BarMenuBody>

      <BarMenuFooter>
        <BarMenuFooterLink asChild onClick={onNavigate}>
          <Link href={activityFooterHref(chip, liveTotal, awaitingApproval)}>
            View all activity →
          </Link>
        </BarMenuFooterLink>
      </BarMenuFooter>
    </>
  )
}

// TerminalSection — the Recent or the Failed chip's list: one section, capped
// at MAX_TERMINAL_ROWS, overflow pointing at /activity like Live's does.
function TerminalSection({
  label, runs, emptyMessage, onOpenItem,
}: {
  label: string
  runs: PipelineRun[]
  emptyMessage: string
  onOpenItem: (href: string) => void
}) {
  if (runs.length === 0) return <BarMenuEmpty icon={Activity} message={emptyMessage} />
  const rows = runs.slice(0, MAX_TERMINAL_ROWS)
  return (
    <BarMenuSection
      label={label}
      count={rows.length}
      overflow={runs.length > rows.length ? `+${runs.length - rows.length} more in activity` : undefined}
    >
      {rows.map((run) => (
        <RecentRunRow
          key={run.id}
          run={run}
          onClick={() => onOpenItem(`/activity?run=${encodeURIComponent(run.id)}`)}
        />
      ))}
    </BarMenuSection>
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
  // The rail's words and colours (#2988): a cancelled or interrupted run is
  // Stopped-grey, not finished-green.
  const tone = runTone(run.status)
  const failed = tone === "failed"
  return (
    <BarMenuRow
      onClick={onClick}
      leading={
        <span className="flex h-6 w-6 items-center justify-center">
          <span className={cn("h-2 w-2 rounded-full", RUN_TONE_DOT[tone])} />
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
          {runStatusLabel(run.status)} · {relTime(run.ended_at || run.started_at)}
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
