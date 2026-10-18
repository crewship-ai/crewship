"use client"

import { useState } from "react"
import Link from "next/link"
import { useGuardedRouter as useRouter } from "@/hooks/use-navigation-guard"
import { Activity, AlertTriangle, Bot, Hand, Play, Users } from "lucide-react"
import {
  BarMenu,
  BarMenuBody,
  BarMenuChips,
  BarMenuEmpty,
  BarMenuFooter,
  BarMenuFooterLink,
  BarMenuHeader,
  BarMenuRow,
  BarMenuRowAction,
  BarMenuSection,
} from "@/components/layout/bar-menu"
import { StatusPill } from "@/components/ui/status-pill"
import { MAX_LIVE_ROWS } from "@/components/features/routines/live-run-row"
import { formatElapsedSince } from "@/components/features/routines/routine-cost-format"
import { isAwaitingApproval, useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import { useActiveRuns, type ActiveRunItem } from "@/hooks/use-active-runs"
import { useAgentSummaries, useCrewSummaries } from "@/hooks/use-dashboard-data"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import { useTick } from "@/hooks/use-tick"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"
import { runStatusLabel, runTone } from "@/lib/activity-run"

// ActivityBell — the global "what's running now" surface in the toolbar, as the
// v67 wireframe draws it (`H.POPS.actbell`, PAR-145):
//
//   badge   — count of live runs on the Activity icon; amber the moment
//             anything waits on a human approval, blue while routines run,
//             emerald preserved for agent-only activity. Hidden at zero.
//   panel   — 480 px (a sheet under md): the header, one fleet line (crews,
//             agents online, working, idle), the chips All / Waiting /
//             Running / Could not finish, then the work in those groups with
//             the action each one needs inline (Decide, Open, Fix
//             credential), and a footer with today's tally and the way out.
//   icon    — tinted (BarMenu `active`) while anything is live.
//
// What the panel does not draw, and why:
//   · "Now | Changes" — the Changes feed (issue, routine, artefact and access
//     changes) has no source in the product yet, so there is no second tab.
//   · A step progress bar and "Step 3 of 5" — the run feed carries only
//     current_step_id, no step index and no total; a bar would be invented
//     data. The row names the step and the elapsed time instead.
//   · Cancel — it moved to the run's page in v67 (Stop on the run peek).
//
// Routine runs come from the shared useActiveRoutineRuns provider (one
// fetch/poll/WS stream for every live surface); the legacy useActiveRuns feed
// still contributes agent runs — its routine rows are dropped here to avoid
// duplicates.
//
// The chrome is the shared top-bar kit (components/layout/bar-menu.tsx).
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
    recentDashboardRuns,
  } = useActiveRoutineRuns()

  const agentRuns = activeItems.filter((i) => i.kind === "agent")
  const liveTotal = activeCount + agentRuns.length

  // Live-run semantics win the badge tone; the count merges both feeds.
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
      // 480 px on a desktop and a tablet; a sheet between the viewport's edges
      // on a phone, where the button is too near the right edge for 480 px.
      widthClass="w-[480px] max-md:fixed max-md:inset-x-2 max-md:top-12 max-md:w-auto"
    >
      {open && (
        <ActivityPanelBody
          workspaceId={workspaceId}
          liveRuns={liveRuns}
          agentRuns={agentRuns}
          liveTotal={liveTotal}
          awaitingApproval={awaitingApproval}
          // The wider (12-run) terminal window, not the 3-row one: the Could
          // not finish chip and today's tally count across it. Falls back for
          // a provider that predates the wider slice.
          recentRuns={recentDashboardRuns ?? recentRuns}
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

export type ActivityChip = "all" | "waiting" | "running" | "failed"

/** The panel opens on All, whatever is live: the chips are the way to narrow. */
export function defaultActivityChip(_liveTotal: number): ActivityChip {
  return "all"
}

/** The terminal runs the Could not finish chip shows: runTone failed, in feed order. */
export function failedRuns(runs: PipelineRun[]): PipelineRun[] {
  return runs.filter((r) => runTone(r.status) === "failed")
}

/**
 * Where "Open Activity →" goes. The approval wins whatever the chip — a run
 * parked on a human is the reason to leave the panel — then the chip the
 * reader chose, then the live bucket. /activity accepts `failed` as a scope
 * (lib/activity-deeplink.ts SCOPES).
 */
export function activityFooterHref(chip: ActivityChip, liveTotal: number, awaitingApproval: number): string {
  if (awaitingApproval > 0) return "/activity?status=waiting"
  if (chip === "failed") return "/activity?status=failed"
  if ((chip === "all" || chip === "running") && liveTotal > 0) return "/activity?status=active"
  return "/activity"
}

/** How many rows one group shows; overflow exits through the footer. */
const MAX_GROUP_ROWS = MAX_LIVE_ROWS
/** The terminal window is 12 runs; a full window of today's is "12+". */
const TERMINAL_WINDOW = 12

const sameDay = (iso: string | undefined, now: Date) => {
  if (!iso) return false
  const d = new Date(iso)
  return !Number.isNaN(d.getTime()) && d.toDateString() === now.toDateString()
}

/** Pure: today's completed runs and what they cost, from the terminal window. */
export function completedToday(runs: PipelineRun[], now: Date = new Date()): { count: number; costUsd: number; more: boolean } {
  const done = runs.filter((r) => runTone(r.status) === "done" && sameDay(r.ended_at || r.started_at, now))
  return {
    count: done.length,
    costUsd: done.reduce((sum, r) => sum + (r.cost_usd > 0 ? r.cost_usd : 0), 0),
    more: runs.length >= TERMINAL_WINDOW && done.length >= TERMINAL_WINDOW,
  }
}

/** Pure: the fleet line — crews, agents that are up, how many work, how many wait. */
export function fleetSummary(crewCount: number, agents: { status: string }[]): string {
  const online = agents.filter((a) => a.status !== "STOPPED" && a.status !== "OFFLINE")
  const working = online.filter((a) => a.status === "RUNNING").length
  const idle = online.filter((a) => a.status === "IDLE").length
  const n = (count: number, one: string, many: string) => `${count} ${count === 1 ? one : many}`
  return [n(crewCount, "crew", "crews"), `${n(online.length, "agent", "agents")} online`, `${working} working`, `${idle} idle`].join(" · ")
}

// Body is a separate component, mounted only while the panel is open, so the
// 1 s elapsed tick and the two list fetches do not run behind a closed
// popover. That also makes the chip choice per-opening.
function ActivityPanelBody({
  workspaceId,
  liveRuns,
  agentRuns,
  liveTotal,
  awaitingApproval,
  recentRuns,
  onNavigate,
  onOpenItem,
}: {
  workspaceId: string | null
  liveRuns: PipelineRun[]
  agentRuns: ActiveRunItem[]
  liveTotal: number
  awaitingApproval: number
  recentRuns: PipelineRun[]
  onNavigate: () => void
  onOpenItem: (href: string) => void
}) {
  useTick(1000) // re-render each second so elapsed times tick
  const crewsQ = useCrewSummaries(workspaceId)
  const agentsQ = useAgentSummaries(workspaceId)

  const waiting = liveRuns.filter((r) => isAwaitingApproval(r.status))
  const running = liveRuns.filter((r) => !isAwaitingApproval(r.status))
  const failed = failedRuns(recentRuns)
  const total = waiting.length + running.length + agentRuns.length + failed.length

  const [picked, setPicked] = useState<ActivityChip | null>(null)
  const chip = picked ?? defaultActivityChip(liveTotal)
  const today = completedToday(recentRuns)

  const crewCount = crewsQ.data?.length ?? 0
  const agents = agentsQ.data ?? []
  const showFleet = crewCount > 0 || agents.length > 0

  const groups: { id: ActivityChip; label: string; tone?: "warn"; rows: React.ReactNode[]; hidden: number; hiddenLabel: string }[] = []
  const cap = <T,>(rows: T[]) => rows.slice(0, MAX_GROUP_ROWS)
  if (chip === "all" || chip === "waiting") {
    groups.push({
      id: "waiting", label: "Waiting for a person", tone: "warn", hidden: Math.max(0, waiting.length - MAX_GROUP_ROWS), hiddenLabel: "waiting",
      rows: cap(waiting).map((run) => <WorkRow key={run.id} state="waiting" run={run} onNavigate={onNavigate} />),
    })
  }
  if (chip === "all" || chip === "running") {
    const rows = [
      ...running.map((run) => <WorkRow key={run.id} state="running" run={run} onNavigate={onNavigate} />),
      ...agentRuns.map((item) => <AgentRunRow key={item.id} item={item} onClick={() => onOpenItem(item.href)} />),
    ]
    groups.push({ id: "running", label: "Running", hidden: Math.max(0, rows.length - MAX_GROUP_ROWS), hiddenLabel: "running", rows: rows.slice(0, MAX_GROUP_ROWS) })
  }
  if (chip === "all" || chip === "failed") {
    groups.push({
      id: "failed", label: "Could not finish", hidden: Math.max(0, failed.length - MAX_GROUP_ROWS), hiddenLabel: "could not finish",
      rows: cap(failed).map((run) => <WorkRow key={run.id} state="failed" run={run} onNavigate={onNavigate} />),
    })
  }
  const shown = groups.filter((g) => g.rows.length > 0)

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

      {showFleet && (
        <div data-testid="activity-fleet" className="flex items-center gap-1.5 border-b border-foreground/[0.06] px-3 py-1.5 type-meta text-muted-foreground">
          <Users className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span className="tabular-nums">{fleetSummary(crewCount, agents)}</span>
        </div>
      )}

      {total > 0 && (
        <BarMenuChips<ActivityChip>
          ariaLabel="Show"
          testId="activity-chip"
          value={chip}
          onChange={setPicked}
          chips={[
            { id: "all", label: "All", count: total },
            { id: "waiting", label: "Waiting", count: waiting.length, tone: waiting.length > 0 ? "warn" : undefined },
            { id: "running", label: "Running", count: running.length + agentRuns.length },
            { id: "failed", label: "Could not finish", count: failed.length },
          ]}
        />
      )}

      <BarMenuBody>
        {total === 0 ? (
          <BarMenuEmpty icon={Activity} message="Nothing running right now" />
        ) : shown.length === 0 ? (
          <BarMenuEmpty icon={Activity} message={chip === "failed" ? "Nothing failed lately" : chip === "waiting" ? "Nobody is waiting on you" : "Nothing running right now"} />
        ) : (
          shown.map((g) => (
            <BarMenuSection
              key={g.id}
              label={g.label}
              count={g.rows.length + g.hidden}
              tone={g.tone}
              overflow={g.hidden > 0 ? `+${g.hidden} more ${g.hiddenLabel}` : undefined}
            >
              {g.rows}
            </BarMenuSection>
          ))
        )}
      </BarMenuBody>

      <BarMenuFooter>
        <span data-testid="activity-footer-tally" className="type-meta tabular-nums text-muted-foreground">
          {today.more ? `${TERMINAL_WINDOW}+` : today.count} completed today
          {today.costUsd > 0 ? ` · $${today.costUsd.toFixed(2)}` : ""}
        </span>
        <BarMenuFooterLink asChild onClick={onNavigate}>
          <Link href={activityFooterHref(chip, liveTotal, awaitingApproval)}>Open Activity →</Link>
        </BarMenuFooterLink>
      </BarMenuFooter>
    </>
  )
}

const STATE_ICON = {
  waiting: { Icon: Hand, tile: "bg-chip-warn-bg text-chip-warn-fg" },
  running: { Icon: Play, tile: "bg-chip-info-bg text-chip-info-fg" },
  failed: { Icon: AlertTriangle, tile: "bg-chip-danger-bg text-chip-danger-fg" },
} as const

function StateTile({ state }: { state: keyof typeof STATE_ICON }) {
  const { Icon, tile } = STATE_ICON[state]
  return (
    <span className={cn("grid h-7 w-7 place-items-center rounded-md", tile)}>
      <Icon className="h-3.5 w-3.5" aria-hidden />
    </span>
  )
}

/** The first line of a failed run's error, short enough for a one-line sub. */
function failureReason(run: PipelineRun): string {
  const line = (run.error_message ?? "").trim().split("\n")[0]
  if (!line) return runStatusLabel(run.status)
  return line.length > 48 ? `${line.slice(0, 47)}…` : line
}

/**
 * One piece of work in the panel (v67 `bellRow`): a state tile, the title over
 * what it needs, the figure on the right, and the one action beneath. The row
 * opens the run; the action is a link to the same place, so the row is a
 * clickable region (never a <button> holding a link).
 */
function WorkRow({ state, run, onNavigate }: { state: keyof typeof STATE_ICON; run: PipelineRun; onNavigate: () => void }) {
  const router = useRouter()
  const href = `/activity?run=${encodeURIComponent(run.id)}`
  const open = () => {
    onNavigate()
    router.push(href)
  }
  const credential = state === "failed" && /credential/i.test(run.error_message ?? "")
  const sub =
    state === "waiting"
      ? "Waits for your approval"
      : state === "running"
        ? run.current_step_id || "Starting…"
        : `${failureReason(run)} · ${relTime(run.ended_at || run.started_at)}`
  return (
    <BarMenuRow
      testId="activity-row"
      onClick={open}
      leading={<StateTile state={state} />}
      title={run.pipeline_name || run.pipeline_slug}
      meta={<span className="truncate">{sub}</span>}
      trailing={
        state === "running" ? (
          <span className="type-meta tabular-nums text-muted-foreground">{formatElapsedSince(run.started_at)}</span>
        ) : undefined
      }
      actions={
        state === "waiting" ? (
          <BarMenuRowAction primary asChild>
            <Link href={href} onClick={(e) => { e.stopPropagation(); onNavigate() }}>Decide</Link>
          </BarMenuRowAction>
        ) : state === "failed" ? (
          <BarMenuRowAction asChild>
            <Link href={credential ? "/credentials" : href} onClick={(e) => { e.stopPropagation(); onNavigate() }}>
              {credential ? "Fix credential" : "Open"}
            </Link>
          </BarMenuRowAction>
        ) : undefined
      }
    />
  )
}

// AgentRunRow — an in-flight agent run from the legacy feed. Same row
// skeleton as everything else in the bar.
function AgentRunRow({ item, onClick }: { item: ActiveRunItem; onClick: () => void }) {
  return (
    <BarMenuRow
      testId="activity-row"
      onClick={onClick}
      leading={
        <span className="grid h-7 w-7 place-items-center rounded-md bg-chip-info-bg text-chip-info-fg">
          <Bot className="h-3.5 w-3.5" aria-hidden />
        </span>
      }
      title={item.label}
      meta={<span className="truncate">Agent{item.sublabel ? ` · ${item.sublabel}` : ""}</span>}
      trailing={
        item.startedAt ? (
          <span className="type-meta tabular-nums text-muted-foreground-soft">{relTime(item.startedAt)}</span>
        ) : undefined
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
