"use client"

import * as React from "react"
import Link from "next/link"
import { CalendarClock, ChevronRight, FileEdit, Hourglass, Play, Radio, XCircle } from "lucide-react"

import { cn } from "@/lib/utils"
import { formatDurationMs } from "@/lib/activity-stream"
import { StatusPill } from "@/components/ui/status-pill"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { honestPct } from "@/lib/honest-pct"
import { RunVolumeChart, type RunVolumeBucket, type RunVolumeSeries } from "@/components/features/dashboard/run-volume-chart"
import { AttentionStrip, OutcomeKpis, UpNext, type AttentionItem, type OutcomeKpiData } from "@/components/features/dashboard/dashboard-overview"
import { STATUS_PALETTE } from "@/app/(dashboard)/dashboard-helpers"
import { routineRunPresentation, formatAgo, formatUntil } from "@/lib/routine-run-presentation"
import type { OverviewRun } from "@/lib/routines-overview"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { routineRunHref } from "./routines-workspace"
import { routineViewHref } from "./routine-navigation"
import { RoutineGlyph } from "./routine-glyph"

// routines-dashboard — what the main pane answers when no routine is
// selected, built from the SAME pieces as /dashboard so the two read as one
// application: the attention strip, the outcome KPI tiles, the run-volume
// chart, Up next, and DashboardCard for the rest. The sidebar is the catalog
// already; this pane says what happened in the last seven days, what runs
// now, what comes next and what needs a person.
//
// Every figure derives from the run list the page already loads
// (`usePipelineRuns`, 200 newest rows) and the schedules; nothing is fetched
// twice or estimated. Runs of routines that the explorer's filters hide are
// left out, so the pane follows the filters like the sidebar does.

export const WINDOW_DAYS = 7

export interface DashboardRun extends OverviewRun {
  outcome?: string
  pipeline_name?: string
}

interface Props {
  routines: Pipeline[]
  runs: DashboardRun[]
  runsLoading?: boolean
  schedules: PipelineSchedule[]
  onSelect: (slug: string) => void
}

const LIVE = new Set(["running", "queued", "paused", "waiting"])
const DONE_OK = new Set(["completed", "succeeded", "success"])
const STOPPED = new Set(["cancelled", "canceled", "interrupted"])

/** A run judged by its result, not only the engine status: a run that
 * completed with a failed result is a failure for a reader. */
export function effectiveStatus(run: DashboardRun): string {
  const status = (run.status ?? "").toLowerCase()
  if (run.outcome === "FAILED" && !STOPPED.has(status) && !LIVE.has(status)) return "failed"
  return status
}

function within(run: DashboardRun, sinceMs: number): boolean {
  const t = Date.parse(run.started_at)
  return Number.isFinite(t) && t >= sinceMs
}

export { honestPct } from "@/lib/honest-pct"

/** The outcome tiles' numbers for the window, in the shape /dashboard uses. */
export function outcomeKpis(runs: DashboardRun[], now = new Date()): OutcomeKpiData & { spendUsd: number; total: number } {
  const since = now.getTime() - WINDOW_DAYS * 86_400_000
  const week = runs.filter((r) => within(r, since))
  const finished = week.filter((r) => !LIVE.has(effectiveStatus(r)) && !STOPPED.has(effectiveStatus(r)))
  const completed = finished.filter((r) => DONE_OK.has(effectiveStatus(r))).length
  const durations = finished
    .map((r) => r.duration_ms)
    .filter((d): d is number => typeof d === "number" && d > 0)
    .sort((a, b) => a - b)
  const p95 = durations.length ? durations[Math.min(durations.length - 1, Math.floor(durations.length * 0.95))] : 0
  return {
    total: week.length,
    completed,
    successOk: completed,
    successTotal: finished.length,
    successPct: honestPct(completed, finished.length),
    p95Ms: p95,
    spendUsd: week.reduce((sum, r) => sum + (typeof r.cost_usd === "number" ? r.cost_usd : 0), 0),
  }
}

/** One bucket per day for the window, one series per outcome, drawn with
 * the dashboard's run-volume chart and its status palette. Outcomes, not
 * routines: how many runs ended well says something about the week, which
 * routine produced them is what the sidebar and Activity are for. */
export const OUTCOME_SERIES: RunVolumeSeries[] = [
  { key: "completed", label: "Completed", color: STATUS_PALETTE.COMPLETED },
  { key: "failed", label: "Could not finish", color: STATUS_PALETTE.FAILED },
  { key: "stopped", label: "Stopped", color: STATUS_PALETTE.CANCELLED },
  { key: "live", label: "Still going", color: STATUS_PALETTE.IN_PROGRESS },
]

export function runOutcomesByDay(runs: DashboardRun[], now = new Date()): { buckets: RunVolumeBucket[]; series: RunVolumeSeries[] } {
  const dayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const days: Date[] = Array.from({ length: WINDOW_DAYS }, (_, i) => {
    const d = new Date(dayStart)
    d.setDate(dayStart.getDate() - (WINDOW_DAYS - 1 - i))
    return d
  })
  const buckets: RunVolumeBucket[] = days.map((d) => ({ ts: d.toISOString(), completed: 0, failed: 0, stopped: 0, live: 0 }))
  for (const run of runs) {
    const t = Date.parse(run.started_at)
    if (!Number.isFinite(t)) continue
    const runDay = new Date(t)
    const index = days.findIndex(
      (d) => d.getFullYear() === runDay.getFullYear() && d.getMonth() === runDay.getMonth() && d.getDate() === runDay.getDate(),
    )
    if (index < 0) continue
    const status = effectiveStatus(run)
    const key = LIVE.has(status) ? "live" : STOPPED.has(status) ? "stopped" : DONE_OK.has(status) ? "completed" : "failed"
    buckets[index][key] = Number(buckets[index][key]) + 1
  }
  // Only the outcomes that occurred, so the legend names what the bars show.
  const used = OUTCOME_SERIES.filter((s) => buckets.some((b) => Number(b[s.key]) > 0))
  return { buckets, series: used }
}

/** How much of a week the run list actually covers. The list is the 200
 * newest runs, so a busy routine can fill it inside one day — and a seven-day
 * chart of one bar says nothing a sentence would not say better. */
export function outcomeVolumeShape(buckets: RunVolumeBucket[], keys: string[]): "empty" | "single-day" | "chart" {
  const days = buckets.filter((b) => keys.some((k) => Number(b[k]) > 0)).length
  return days === 0 ? "empty" : days === 1 ? "single-day" : "chart"
}

export interface LatestResultGroup {
  latest: DashboardRun
  count: number
}

const SEVERITY: Record<string, number> = { destructive: 0, warn: 1 }

/** Finished runs as rows: runs of one routine that ended the same way fold
 * into one row with a count (the newest run opens), and results that need a
 * person — failed first, then those waiting on review — sort above the rest.
 * A routine that ingests every five minutes otherwise fills every row. */
export function groupLatestResults(runs: DashboardRun[], limit = 8): LatestResultGroup[] {
  const groups = new Map<string, LatestResultGroup & { order: number; severity: number }>()
  for (const run of runs) {
    const p = routineRunPresentation({ status: run.status, outcome: run.outcome })
    const key = `${run.pipeline_slug}\u0000${p.label}`
    const group = groups.get(key)
    if (group) group.count += 1
    else groups.set(key, { latest: run, count: 1, order: groups.size, severity: SEVERITY[p.tone] ?? 2 })
  }
  return [...groups.values()]
    .sort((a, b) => a.severity - b.severity || a.order - b.order)
    .slice(0, limit)
    .map(({ latest, count }) => ({ latest, count }))
}

export function RoutinesDashboard({ routines, runs, runsLoading, schedules, onSelect }: Props) {
  const now = React.useMemo(() => new Date(), [])
  const visibleRuns = React.useMemo(
    () => runs.filter((r) => routines.some((p) => p.slug === r.pipeline_slug)),
    [runs, routines],
  )
  const kpis = React.useMemo(() => outcomeKpis(visibleRuns, now), [visibleRuns, now])
  const volume = React.useMemo(() => runOutcomesByDay(visibleRuns, now), [visibleRuns, now])
  const volumeShape = outcomeVolumeShape(volume.buckets, volume.series.map((s) => s.key))
  const routineOf = (slug: string) => routines.find((p) => p.slug === slug)

  const newest = (list: DashboardRun[]) =>
    [...list].sort((a, b) => (Date.parse(b.started_at) || 0) - (Date.parse(a.started_at) || 0))
  const waiting = newest(visibleRuns.filter((r) => ["waiting", "paused"].includes((r.status ?? "").toLowerCase())))
  const running = newest(visibleRuns.filter((r) => ["running", "queued"].includes((r.status ?? "").toLowerCase())))
  const since = now.getTime() - WINDOW_DAYS * 86_400_000
  const finished = newest(visibleRuns.filter((r) => !LIVE.has((r.status ?? "").toLowerCase()) && within(r, since)))
  const resultGroups = groupLatestResults(finished)
  const failing = routines
    .filter((r) => r.last_invocation_status === "failed" || r.last_run_outcome === "FAILED")
    .sort((a, b) => (b.last_invoked_at ?? "").localeCompare(a.last_invoked_at ?? ""))
  const drafts = routines.filter((r) => r.draft)
  const mySchedules = schedules.filter((s) => routines.some((p) => p.slug === s.target_pipeline_slug))
  const nextStart = mySchedules
    .filter((s) => s.enabled && s.next_run_at && Date.parse(s.next_run_at) > now.getTime())
    .sort((a, b) => Date.parse(a.next_run_at!) - Date.parse(b.next_run_at!))[0]

  // The same strip as /dashboard, with the routine-shaped items in a fixed
  // order: what needs a person, what could not finish, the next planned
  // start; drafts to publish come after and, like the dashboard's fourth
  // item, are named below the strip when the three slots are taken.
  const attention: AttentionItem[] = []
  if (waiting.length)
    attention.push({
      id: "approvals",
      label: `${waiting.length} ${waiting.length === 1 ? "decision" : "decisions"} waiting`,
      detail: `Newest · ${waiting[0].pipeline_name || routineOf(waiting[0].pipeline_slug)?.name || waiting[0].pipeline_slug}`,
      href: routineRunHref(waiting[0].pipeline_slug, waiting[0].id),
      tone: "warn",
      icon: Hourglass,
    })
  if (failing.length)
    attention.push({
      id: "failures",
      label: `${failing.length} could not finish`,
      detail: `Newest · ${failing[0].name}`,
      href: `/routines?${new URLSearchParams({ slug: failing[0].slug })}`,
      tone: "danger",
      icon: XCircle,
    })
  if (nextStart?.target_pipeline_slug)
    attention.push({
      id: "schedules",
      label: `Next start in ${formatUntil(nextStart.next_run_at!)}`,
      detail: routineOf(nextStart.target_pipeline_slug)?.name ?? nextStart.target_pipeline_slug,
      href: routineViewHref(nextStart.target_pipeline_slug, "plan"),
      tone: "blue",
      icon: CalendarClock,
    })
  if (drafts.length)
    attention.push({
      id: "drafts",
      label: `${drafts.length} ${drafts.length === 1 ? "draft" : "drafts"} to publish`,
      detail: drafts.map((r) => r.name).slice(0, 3).join(" · "),
      href: `/routines?${new URLSearchParams({ slug: drafts[0].slug, view: "versions" })}`,
      tone: "purple",
      icon: FileEdit,
    })

  return (
    <div className="@container/overview flex flex-col gap-3" data-testid="routines-dashboard">
      <AttentionStrip items={attention} />

      <div className="flex flex-wrap items-center gap-2">
        <Radio className="h-3.5 w-3.5 text-primary-hover" aria-hidden />
        <h2 className="eyebrow">Routine run summary</h2>
        <span className="font-mono text-[11px] tabular-nums text-muted-foreground-soft">
          {WINDOW_DAYS}d · {kpis.total} {kpis.total === 1 ? "run" : "runs"}
          {runsLoading ? " · loading" : ""}
        </span>
      </div>

      <OutcomeKpis
        data={kpis}
        window="7d"
        spendUsd={kpis.spendUsd}
        spendPerRun={kpis.successTotal > 0 ? kpis.spendUsd / kpis.successTotal : null}
      />

      {/* The dashboard's shape: the main list on the left, the live column on
          the right, the chart as its own row. Columns split by this pane's
          width, not the viewport's — beside the explorer a 1280px window
          leaves ~930px here. */}
      <div className="grid grid-cols-1 gap-3 @5xl/overview:grid-cols-5">
        <div className="@5xl/overview:col-span-3">
          <DashboardCard
            title="Latest results"
            icon={Radio}
            hint={`${WINDOW_DAYS}d · ${finished.length} finished`}
            action={
              <Link href="/activity?lens=routines" className="text-primary-hover hover:underline">
                All runs →
              </Link>
            }
            className="h-full"
          >
            {finished.length === 0 ? (
              <InlineEmpty icon={Radio} text={runsLoading ? "Loading runs…" : "Nothing finished in the last 7 days."} />
            ) : (
              <div>
                {resultGroups.map(({ latest, count }) => (
                  <ResultRow key={latest.id} run={latest} count={count} routine={routineOf(latest.pipeline_slug)} />
                ))}
              </div>
            )}
          </DashboardCard>
        </div>

        <div className="flex min-w-0 flex-col gap-3 @5xl/overview:col-span-2">
          <DashboardCard
            title="Routines running now"
            icon={Play}
            hint={running.length ? `${running.length} running` : "none running"}
            action={
              <Link href="/activity?lens=routines" className="text-primary-hover hover:underline">
                Activity →
              </Link>
            }
          >
            {running.length === 0 ? (
              <InlineEmpty icon={Play} text="No routines are running right now." />
            ) : (
              <div>
                {running.slice(0, 5).map((run) => (
                  <RunRow key={run.id} run={run} routine={routineOf(run.pipeline_slug)} />
                ))}
              </div>
            )}
            {waiting.length > 0 && (
              <Link
                href={routineRunHref(waiting[0].pipeline_slug, waiting[0].id)}
                className="mt-3 block rounded-lg bg-warn/10 px-3 py-2 text-label text-warn"
              >
                {waiting.length} waiting for a decision · Decide →
              </Link>
            )}
          </DashboardCard>

          <UpNext schedules={mySchedules} />

          {drafts.length > 0 && (
            <DashboardCard title="Drafts to publish" icon={FileEdit} hint={String(drafts.length)}>
              <div>
                {drafts.slice(0, 5).map((r) => (
                  <button
                    key={r.slug}
                    type="button"
                    onClick={() => onSelect(r.slug)}
                    className="group grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2.5 rounded-md border-b border-border/50 px-2 py-2 text-left last:border-0 hover:bg-foreground/[0.025]"
                  >
                    <RoutineGlyph routine={r} />
                    <span className="min-w-0">
                      <span className="block truncate text-body font-medium text-foreground/90">{r.name}</span>
                      <span className="block truncate text-label text-muted-foreground">
                        {(r.head_version ?? 0) > 0 ? `Draft r${r.draft!.revision} · published v${r.head_version}` : "Not published yet"}
                        {r.draft?.updated_at ? ` · ${formatAgo(r.draft.updated_at)}` : ""}
                      </span>
                    </span>
                    <span className="inline-flex items-center gap-1 text-label font-medium text-primary-hover">
                      Publish <ChevronRight className="h-3.5 w-3.5" />
                    </span>
                  </button>
                ))}
              </div>
            </DashboardCard>
          )}
        </div>
      </div>

      <DashboardCard
        title={`Run outcomes · ${WINDOW_DAYS}d · by day`}
        icon={Radio}
        hint={`${kpis.total} runs`}
        action={
          <Link href="/activity?lens=routines" className="text-primary-hover hover:underline">
            Activity →
          </Link>
        }
      >
        {volumeShape === "single-day" ? (
          <SingleDayOutcomes buckets={volume.buckets} series={volume.series} now={now} />
        ) : (
          <RunVolumeChart buckets={volume.buckets} series={volume.series} window="7d" />
        )}
      </DashboardCard>
    </div>
  )
}

const PILL = { success: "success", destructive: "danger", warn: "warn", blue: "blue", default: "muted" } as const

/** One finished run, as the dashboard's Results & review draws a row: icon,
 * state pill, name, meta on the right, then the verb. */
function ResultRow({ run, count = 1, routine }: { run: DashboardRun; count?: number; routine?: Pipeline }) {
  const p = routineRunPresentation({ status: run.status, outcome: run.outcome })
  return (
    <Link
      href={routineRunHref(run.pipeline_slug, run.id)}
      className="group grid grid-cols-[auto_auto_minmax(0,1fr)_auto] items-center gap-2.5 rounded-[10px] border-b border-border/50 px-2 py-2 transition-colors last:border-0 hover:bg-foreground/[0.03] coarse:min-h-12 @3xl/overview:grid-cols-[auto_auto_minmax(0,1fr)_auto_auto]"
    >
      <RoutineGlyph routine={routine ?? { slug: run.pipeline_slug }} />
      <StatusPill tone={PILL[p.tone]} label={p.label} />
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate text-body font-medium text-foreground">{run.pipeline_name || routine?.name || run.pipeline_slug}</span>
        {count > 1 && (
          <span
            className="shrink-0 rounded-md bg-foreground/[0.06] px-1.5 font-mono text-micro font-semibold tabular-nums text-muted-foreground"
            title={`${count} runs with this result in the window; the newest opens`}
          >
            ×{count}
          </span>
        )}
      </span>
      <span className="hidden font-mono text-label tabular-nums text-muted-foreground-soft @3xl/overview:inline">
        {count > 1 ? "latest " : ""}
        {formatAgo(run.started_at)}
        {run.duration_ms ? ` · ${formatDurationMs(run.duration_ms)}` : ""}
      </span>
      <span className="inline-flex items-center gap-1 text-label font-medium text-primary-hover">
        {/* The verb stays for screen readers on a phone, where the name needs the width. */}
        <span className="sr-only @3xl/overview:not-sr-only">Open run</span> <ChevronRight className="h-3.5 w-3.5" />
      </span>
    </Link>
  )
}

/** One running or waiting run, as the dashboard's Routines running now draws it. */
function RunRow({ run, routine }: { run: DashboardRun; routine?: Pipeline }) {
  const p = routineRunPresentation({ status: run.status, outcome: run.outcome })
  return (
    <Link
      href={routineRunHref(run.pipeline_slug, run.id)}
      className="group grid grid-cols-[auto_minmax(0,1fr)_auto_auto] items-center gap-2.5 rounded-md border-b border-border/50 px-2 py-2 last:border-0 hover:bg-foreground/[0.025]"
    >
      <RoutineGlyph routine={routine ?? { slug: run.pipeline_slug }} />
      <span className="min-w-0">
        <span className="block truncate text-body font-medium text-foreground/90">{run.pipeline_name || routine?.name || run.pipeline_slug}</span>
        <span className="block truncate text-label text-muted-foreground">
          started {formatAgo(run.started_at)}
          {run.current_step_id ? ` · ${run.current_step_id.replaceAll("_", " ")}` : ""}
        </span>
      </span>
      <StatusPill tone={PILL[p.tone]} label={p.label} live={p.tone === "blue"} />
      <span className={cn("inline-flex items-center gap-1 text-label font-medium text-primary-hover")}>
        View <ChevronRight className="h-3.5 w-3.5" />
      </span>
    </Link>
  )
}

/** A window whose runs all started on one day, said as a sentence with one
 * proportion bar — the seven-day chart would draw a single slab. */
function SingleDayOutcomes({ buckets, series, now }: { buckets: RunVolumeBucket[]; series: RunVolumeSeries[]; now: Date }) {
  const day = buckets.find((b) => series.some((s) => Number(b[s.key]) > 0))
  if (!day) return null
  const counts = series.map((s) => ({ ...s, n: Number(day[s.key]) })).filter((s) => s.n > 0)
  const total = counts.reduce((sum, s) => sum + s.n, 0)
  const date = new Date(day.ts)
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  const when =
    date.toDateString() === now.toDateString()
      ? "today"
      : date.toDateString() === yesterday.toDateString()
        ? "yesterday"
        : `on ${date.toLocaleDateString(undefined, { month: "short", day: "numeric" })}`
  return (
    <div data-testid="run-outcomes-single-day" className="flex flex-col gap-3">
      <p className="text-body text-muted-foreground">
        All <span className="font-mono tabular-nums text-foreground">{total}</span> {total === 1 ? "run" : "runs"} in the window started {when}.
      </p>
      <div className="flex h-2 w-full overflow-hidden rounded-full bg-foreground/[0.06]" role="img" aria-label={counts.map((s) => `${s.n} ${s.label}`).join(", ")}>
        {counts.map((s) => (
          <span key={s.key} className="h-full" style={{ width: `${(s.n / total) * 100}%`, minWidth: 4, backgroundColor: s.color }} />
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
        {counts.map((s) => (
          <span key={s.key} className="inline-flex items-center gap-1.5 text-label text-muted-foreground">
            <span className="h-2 w-2 rounded-sm" style={{ backgroundColor: s.color }} aria-hidden />
            <span className="font-mono tabular-nums text-foreground">{s.n}</span> {s.label}
          </span>
        ))}
      </div>
    </div>
  )
}
