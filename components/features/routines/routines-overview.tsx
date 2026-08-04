"use client"

// The routines landing pane.
//
// What was here: a table of every routine in the workspace. On the
// instance this was designed against it was 38 rows, 37 of which read
// "never invoked · 0 · — · 8d ago", under four tiles of which one
// repeated the page header and another reported "PASS RATE 100%" off a
// single run.
//
// The sidebar to the left is already the catalog — iconed, searchable,
// filtered by status. So the main pane was the same list a second
// time, and the second copy was the one that could not be searched.
//
// It answers, in the order someone asks on arrival: did anything run
// today, is the catalog healthy, what fires next, what ran, what did
// it cost, what is broken. Selecting a routine in the sidebar replaces
// this with that routine's card.
//
// Shares its shell components with /dashboard rather than imitating
// them, so a green arc means the same thing on both pages and neither
// drifts from the other.

import * as React from "react"
import Link from "next/link"
import {
  Activity,
  AlarmClock,
  AlertTriangle,
  Banknote,
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  PieChart,
  RefreshCw,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Appear } from "@/components/ui/detail"
import { Skeleton } from "@/components/ui/skeleton"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { StatusDonut } from "@/components/features/dashboard/status-donut"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { describeCron } from "@/lib/cron-describe"
import { formatUsd } from "@/lib/routines-insights"
import { RoutineBudgetSummaryCard } from "./routine-budget-summary-card"
import { formatDurationDecimal, relTime } from "@/lib/time"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import type { Pipeline } from "@/hooks/use-pipelines"
import {
  catalogBuckets,
  isLiveStatus,
  needsAttention,
  nextScheduled,
  recentRuns,
  runsToday,
  spendByDay,
  successRate,
  upcomingSchedules,
} from "@/lib/routines-overview"

const SUCCESS_WINDOW_DAYS = 7
const RECENT_RUN_LIMIT = 10
const UPCOMING_LIMIT = 6
const FAILING_LIMIT = 5

interface Props {
  workspaceId: string
  routines: Pipeline[]
  loading: boolean
  error: string | null
  onSelect: (slug: string) => void
  /** Sets the sidebar's status filter — the donut's click-through. */
  onFilter?: (filter: string) => void
  onRefresh: () => void
}

export function RoutinesOverview({
  workspaceId,
  routines,
  loading,
  error,
  onSelect,
  onFilter,
  onRefresh,
}: Props) {
  const { runs } = usePipelineRuns(workspaceId, "all")
  const { schedules } = usePipelineSchedules(workspaceId)
  const { bySlug: liveBySlug } = useActiveRoutineRuns()

  // One clock for the whole render. Deriving `new Date()` inside each
  // helper would let two tiles disagree about what "today" is across a
  // midnight boundary, and the bug would appear once a day for one
  // second.
  const [now, setNow] = React.useState(() => new Date())
  React.useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 30_000)
    return () => clearInterval(t)
  }, [])

  const routineBySlug = React.useMemo(
    () => new Map(routines.map((r) => [r.slug, r])),
    [routines],
  )
  const liveSlugs = React.useMemo(() => new Set(liveBySlug.keys()), [liveBySlug])

  const today = React.useMemo(() => runsToday(runs, now), [runs, now])
  const success = React.useMemo(() => successRate(runs, now, SUCCESS_WINDOW_DAYS), [runs, now])
  const next = React.useMemo(() => nextScheduled(schedules, now), [schedules, now])
  const attention = React.useMemo(() => needsAttention(routines), [routines])
  const buckets = React.useMemo(() => catalogBuckets(routines, liveSlugs), [routines, liveSlugs])
  const upcoming = React.useMemo(
    () => upcomingSchedules(schedules, now, UPCOMING_LIMIT),
    [schedules, now],
  )
  const recent = React.useMemo(() => recentRuns(runs, RECENT_RUN_LIMIT), [runs])
  const spend = React.useMemo(() => spendByDay(runs, now, SUCCESS_WINDOW_DAYS), [runs, now])
  const spendTotal = React.useMemo(() => spend.reduce((s, d) => s + d.usd, 0), [spend])
  const failing = React.useMemo(
    () =>
      routines
        .filter((r) => {
          const s = r.last_invocation_status?.toLowerCase()
          return s === "failed" || s === "error"
        })
        .sort((a, b) => Date.parse(b.last_invoked_at ?? "") - Date.parse(a.last_invoked_at ?? ""))
        .slice(0, FAILING_LIMIT),
    [routines],
  )

  const nextRoutine = next?.target_pipeline_slug
    ? routineBySlug.get(next.target_pipeline_slug)
    : undefined

  if (loading && routines.length === 0) return <OverviewSkeleton />

  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
        <Appear order={0}>
          <div className="flex items-center justify-between gap-3">
            <div>
              <h1 className="text-lg font-semibold tracking-tight">Overview</h1>
              <p className="text-xs text-muted-foreground">
                {routines.length} {routines.length === 1 ? "routine" : "routines"} in this workspace
              </p>
            </div>
            <button
              type="button"
              onClick={onRefresh}
              className="inline-flex items-center gap-1.5 rounded-md border border-border/60 px-2.5 py-1.5 text-[11px] font-medium text-muted-foreground transition-colors hover:text-foreground"
            >
              <RefreshCw className="h-3.5 w-3.5" />
              Refresh
            </button>
          </div>
        </Appear>

        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {/* ── What happened today, and what happens next ───────────── */}
        <Appear order={1}>
          <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <KpiCard
              label="Runs today"
              value={today.total}
              subtitle={today.total === 0 ? "nothing yet" : `${today.failed} failed`}
              valueColor={today.failed > 0 ? "rgb(248, 113, 113)" : undefined}
            />
            {/* The rate carries its denominator. "100%" off one run is
                not a health signal, and a reader shown "1 of 1" can
                discount it without being told to. */}
            <KpiCard
              label={`Success · ${SUCCESS_WINDOW_DAYS}d`}
              value={success.pct === null ? "—" : `${success.pct}%`}
              subtitle={success.total === 0 ? "no finished runs" : `${success.ok} of ${success.total}`}
            />
            <KpiCard
              label="Next run"
              value={next?.next_run_at ? relTime(next.next_run_at) : "—"}
              subtitle={
                next
                  ? nextRoutine?.name || next.target_pipeline_slug || next.name
                  : "nothing scheduled"
              }
            />
            <KpiCard
              label="Needs attention"
              value={attention.total}
              valueColor={attention.total > 0 ? "rgb(251, 191, 36)" : undefined}
              subtitle={
                attention.total === 0
                  ? "all clear"
                  : [
                      attention.failing > 0 ? `${attention.failing} failing` : null,
                      attention.awaitingApproval > 0 ? `${attention.awaitingApproval} to approve` : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")
              }
            />
          </div>
        </Appear>

        {/* ── The catalog as a shape, and the week ahead ───────────── */}
        <Appear order={2}>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <DashboardCard
              title="Catalog health"
              icon={PieChart}
              hint={`${routines.length} total`}
            >
              {/* The arcs sum to the catalog, so the number in the
                  centre is the number in the header. A donut whose
                  slices do not add up is worse than no donut. */}
              <StatusDonut
                data={buckets}
                centerLabel="routines"
                onSelect={
                  onFilter
                    ? (key) => {
                        const b = buckets.find((x) => x.key === key)
                        if (b) onFilter(b.filter)
                      }
                    : undefined
                }
              />
            </DashboardCard>

            <DashboardCard
              title="Firing next"
              icon={CalendarClock}
              hint={upcoming.length > 0 ? `${upcoming.length} upcoming` : "none scheduled"}
            >
              {upcoming.length === 0 ? (
                <Empty icon={AlarmClock}>
                  No schedule is due. Open a routine and add one under Triggers.
                </Empty>
              ) : (
                <div className="flex flex-col">
                  {upcoming.map((s) => {
                    const r = s.target_pipeline_slug
                      ? routineBySlug.get(s.target_pipeline_slug)
                      : undefined
                    return (
                      <button
                        key={s.id}
                        type="button"
                        onClick={() => s.target_pipeline_slug && onSelect(s.target_pipeline_slug)}
                        className="group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
                      >
                        <span className="w-[62px] shrink-0 font-mono text-[11px] tabular-nums text-primary">
                          {relTime(s.next_run_at)}
                        </span>
                        {r ? (
                          <CrewIcon
                            icon={resolveRoutineIcon(r)}
                            color={resolveRoutineColor(r)}
                            size="sm"
                            className="!h-5 !w-5 !rounded-md shrink-0"
                          />
                        ) : (
                          <span className="h-5 w-5 shrink-0 rounded-md bg-muted" />
                        )}
                        <span className="min-w-0 flex-1 truncate text-[12px] text-foreground/85">
                          {r?.name || s.target_pipeline_slug || s.name}
                        </span>
                        <span className="shrink-0 text-[10px] text-muted-foreground">
                          {describeCron(s.cron_expr)}
                        </span>
                        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft opacity-0 transition-opacity group-hover:opacity-100" />
                      </button>
                    )
                  })}
                </div>
              )}
            </DashboardCard>
          </div>
        </Appear>

        {/* ── What actually ran ───────────────────────────────────── */}
        <Appear order={3}>
          <DashboardCard
            title="Recent runs"
            icon={Activity}
            hint={recent.length > 0 ? `last ${recent.length}` : "no runs yet"}
            action={
              recent.length > 0 ? (
                <Link href="/activity" className="text-primary hover:underline">
                  Activity →
                </Link>
              ) : undefined
            }
          >
            {recent.length === 0 ? (
              <Empty icon={Activity}>
                Nothing has run yet. Pick a routine on the left and press Run, or give it a
                schedule.
              </Empty>
            ) : (
              <div className="flex flex-col">
                {recent.map((run) => {
                  const r = routineBySlug.get(run.pipeline_slug)
                  const live = isLiveStatus(run.status)
                  return (
                    <Link
                      key={run.id}
                      href={`/activity?run=${encodeURIComponent(run.id)}`}
                      className="group grid grid-cols-[auto_1fr_auto] items-center gap-3 rounded-md px-1.5 py-2 transition-colors hover:bg-white/[0.03] sm:grid-cols-[auto_1fr_auto_auto_auto_auto]"
                    >
                      <span className="relative shrink-0">
                        {r ? (
                          <CrewIcon
                            icon={resolveRoutineIcon(r)}
                            color={resolveRoutineColor(r)}
                            size="sm"
                            className="!h-6 !w-6 !rounded-md"
                          />
                        ) : (
                          <span className="h-6 w-6 rounded-md bg-muted" />
                        )}
                        <span
                          aria-hidden
                          className={cn(
                            "absolute -bottom-0.5 -right-0.5 h-2 w-2 rounded-full ring-2 ring-card",
                            statusDot(run.status),
                          )}
                        />
                      </span>
                      <span className="min-w-0">
                        <span className="block truncate text-[12px] text-foreground/90">
                          {run.pipeline_name || r?.name || run.pipeline_slug}
                        </span>
                        {live && (
                          <span className="block truncate text-[10px] text-primary">
                            ▶ {run.current_step_id || "starting…"}
                          </span>
                        )}
                      </span>
                      <span className="hidden shrink-0 text-[10px] text-muted-foreground sm:block">
                        {triggerLabel(run.triggered_via)}
                      </span>
                      <span className="hidden w-14 shrink-0 text-right font-mono text-[11px] tabular-nums text-muted-foreground sm:block">
                        {live ? "—" : formatDurationDecimal(run.duration_ms ?? 0)}
                      </span>
                      <span className="hidden w-16 shrink-0 text-right font-mono text-[11px] tabular-nums text-muted-foreground sm:block">
                        {formatUsd(run.cost_usd ?? 0)}
                      </span>
                      <span className="shrink-0 text-right text-[10px] text-muted-foreground-soft">
                        {relTime(run.started_at)}
                      </span>
                    </Link>
                  )
                })}
              </div>
            )}
          </DashboardCard>
        </Appear>

        {/* ── Money, and breakage ─────────────────────────────────── */}
        <Appear order={4}>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <DashboardCard
              title={`Spend · ${SUCCESS_WINDOW_DAYS} days`}
              icon={Banknote}
              hint={formatUsd(spendTotal)}
            >
              {/* Real per-run cost_usd, or nothing. This card's
                  ancestor had decorative sparklines drawn from a mock
                  function; they were removed for promising a trend that
                  did not exist, and the lesson stands. */}
              {spendTotal === 0 ? (
                <Empty icon={Banknote}>No run in this window carried a cost.</Empty>
              ) : (
                <div className="flex h-[120px] items-end gap-1.5">
                  {spend.map((d, i) => {
                    const max = Math.max(...spend.map((x) => x.usd), 0.0000001)
                    const pct = Math.max(2, Math.round((d.usd / max) * 100))
                    return (
                      <div key={i} className="flex flex-1 flex-col items-center gap-1.5">
                        <div className="flex w-full flex-1 items-end">
                          <div
                            title={`${d.label}: ${formatUsd(d.usd)}`}
                            style={{ height: `${pct}%` }}
                            className={cn(
                              "w-full rounded-sm transition-[height] duration-500 ease-out",
                              d.usd > 0 ? "bg-primary/70" : "bg-muted",
                              d.isToday && "bg-primary",
                            )}
                          />
                        </div>
                        <span className="text-[9px] text-muted-foreground-soft">{d.label}</span>
                      </div>
                    )
                  })}
                </div>
              )}
            </DashboardCard>

            {/* Budgets came from the Insights tab, and is the one
                thing on it that was a capability rather than a second
                view of one: a routine can carry a monthly cap, and
                this is where you find out one is over it. It keeps its
                own card shell — it is a list with its own tone, not a
                tile. */}
            <RoutineBudgetSummaryCard workspaceId={workspaceId} onSelect={onSelect} />

            <DashboardCard
              title="Recently failing"
              icon={AlertTriangle}
              hint={failing.length > 0 ? `${failing.length}` : "all clean"}
            >
              {failing.length === 0 ? (
                <Empty icon={CheckCircle2}>Nothing has failed. Nice.</Empty>
              ) : (
                <div className="flex flex-col">
                  {failing.map((r) => (
                    <button
                      key={r.id}
                      type="button"
                      onClick={() => onSelect(r.slug)}
                      className="group flex items-center gap-2.5 rounded-md px-1.5 py-2 text-left transition-colors hover:bg-white/[0.03]"
                    >
                      <CrewIcon
                        icon={resolveRoutineIcon(r)}
                        color={resolveRoutineColor(r)}
                        size="sm"
                        className="!h-5 !w-5 !rounded-md shrink-0"
                      />
                      <span className="min-w-0 flex-1 truncate text-[12px] text-foreground/85">
                        {r.name || r.slug}
                      </span>
                      <span className="shrink-0 text-[10px] text-destructive">failed</span>
                      <span className="shrink-0 text-[10px] text-muted-foreground-soft">
                        {relTime(r.last_invoked_at)}
                      </span>
                    </button>
                  ))}
                </div>
              )}
            </DashboardCard>
          </div>
        </Appear>
      </div>
    </div>
  )
}

function Empty({ icon: Icon, children }: { icon: typeof Activity; children: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-1.5 py-7 text-center">
      <Icon className="h-4 w-4 text-muted-foreground-soft" />
      <p className="max-w-[280px] text-[11px] text-muted-foreground-soft">{children}</p>
    </div>
  )
}

function statusDot(status: string): string {
  const s = status.toLowerCase()
  if (s === "running" || s === "queued") return "bg-primary animate-pulse"
  if (s === "waiting" || s === "paused") return "bg-warn animate-pulse"
  if (s === "completed" || s === "succeeded") return "bg-success"
  if (s === "failed" || s === "error") return "bg-destructive"
  return "bg-muted-foreground/40"
}

/** Where the run came from, in a word a person would use. */
function triggerLabel(via: string | undefined): string {
  switch (via) {
    case "schedule":
      return "scheduled"
    case "webhook":
      return "webhook"
    case "call_pipeline":
      return "called"
    case "issue":
      return "issue"
    case "manual":
      return "manual"
    default:
      return via || "—"
  }
}

/**
 * Skeleton in the final geometry.
 *
 * Placeholders that do not match what replaces them make the page
 * reflow on load, which reads as a second, unexplained render.
 */
function OverviewSkeleton() {
  return (
    <div className="h-full overflow-auto">
      <div className="mx-auto flex max-w-[1800px] flex-col gap-4 p-4 md:p-6">
        <Skeleton className="h-9 w-48" />
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          {Array.from({ length: 4 }, (_, i) => (
            <Skeleton key={i} className="h-[104px] rounded-xl" />
          ))}
        </div>
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <Skeleton className="h-[228px] rounded-xl" />
          <Skeleton className="h-[228px] rounded-xl" />
        </div>
        <Skeleton className="h-[300px] rounded-xl" />
      </div>
    </div>
  )
}
