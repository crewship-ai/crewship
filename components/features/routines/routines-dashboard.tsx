"use client"

import * as React from "react"
import Link from "next/link"
import { AlarmClock, CalendarClock, ChevronRight, FileEdit, Hourglass, Play, Radio, XCircle } from "lucide-react"

import { cn } from "@/lib/utils"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { RunVolumeChart, type RunVolumeBucket, type RunVolumeSeries } from "@/components/features/dashboard/run-volume-chart"
import { AttentionStrip, OutcomeKpis, UpNext, type AttentionItem, type OutcomeKpiData } from "@/components/features/dashboard/dashboard-overview"
import { CREW_PALETTE, foldRunVolumeSeries, RUN_VOLUME_OTHER_KEY } from "@/app/(dashboard)/dashboard-helpers"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { routineRunPresentation, formatAgo, formatUntil } from "@/lib/routine-run-presentation"
import type { OverviewRun } from "@/lib/routines-overview"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { routineRunHref } from "./routines-workspace"
import { routineViewHref } from "./routine-navigation"

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
    successPct: finished.length ? Math.round((completed / finished.length) * 100) : null,
    p95Ms: p95,
    spendUsd: week.reduce((sum, r) => sum + (typeof r.cost_usd === "number" ? r.cost_usd : 0), 0),
  }
}

/** One bucket per day for the window, one series per routine, as the
 * dashboard's run-volume chart draws crews. */
export function runVolumeByRoutine(
  runs: DashboardRun[],
  routines: Pipeline[],
  now = new Date(),
): { buckets: RunVolumeBucket[]; series: RunVolumeSeries[] } {
  const dayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const days: Date[] = Array.from({ length: WINDOW_DAYS }, (_, i) => {
    const d = new Date(dayStart)
    d.setDate(dayStart.getDate() - (WINDOW_DAYS - 1 - i))
    return d
  })
  const slugs = new Set<string>()
  const buckets: RunVolumeBucket[] = days.map((d) => ({ ts: d.toISOString() }))
  for (const run of runs) {
    const t = Date.parse(run.started_at)
    if (!Number.isFinite(t)) continue
    const runDay = new Date(t)
    const index = days.findIndex(
      (d) => d.getFullYear() === runDay.getFullYear() && d.getMonth() === runDay.getMonth() && d.getDate() === runDay.getDate(),
    )
    if (index < 0) continue
    slugs.add(run.pipeline_slug)
    buckets[index][run.pipeline_slug] = Number(buckets[index][run.pipeline_slug] ?? 0) + 1
  }
  // Hues in the dashboard's fixed categorical order, assigned by slug so a
  // routine keeps its hue whatever the filters show beside it. Most routines
  // carry no colour of their own, and the ones that do share a handful, so
  // painting by routine colour made eight series the same pink.
  const hues = Object.values(CREW_PALETTE)
  const series: RunVolumeSeries[] = [...slugs].sort().map((slug, index) => {
    const routine = routines.find((r) => r.slug === slug)
    return { key: slug, label: routine?.name ?? slug, color: hues[index % hues.length] }
  })
  // Five named routines and Other: the legend has to fit under the chart on
  // a phone, where the dashboard's eight crews would run to eight lines.
  const folded = foldRunVolumeSeries(buckets, series, 5)
  return {
    buckets: folded.buckets,
    series: folded.series.map((s) => (s.key === RUN_VOLUME_OTHER_KEY ? { ...s, label: `Other (${folded.folded} routines)` } : s)),
  }
}

export function RoutinesDashboard({ routines, runs, runsLoading, schedules, onSelect }: Props) {
  const now = React.useMemo(() => new Date(), [])
  const visibleRuns = React.useMemo(
    () => runs.filter((r) => routines.some((p) => p.slug === r.pipeline_slug)),
    [runs, routines],
  )
  const kpis = React.useMemo(() => outcomeKpis(visibleRuns, now), [visibleRuns, now])
  const volume = React.useMemo(() => runVolumeByRoutine(visibleRuns, routines, now), [visibleRuns, routines, now])
  const routineOf = (slug: string) => routines.find((p) => p.slug === slug)

  const newest = (list: DashboardRun[]) =>
    [...list].sort((a, b) => (Date.parse(b.started_at) || 0) - (Date.parse(a.started_at) || 0))
  const waiting = newest(visibleRuns.filter((r) => ["waiting", "paused"].includes((r.status ?? "").toLowerCase())))
  const running = newest(visibleRuns.filter((r) => ["running", "queued"].includes((r.status ?? "").toLowerCase())))
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
    <div className="flex flex-col gap-3" data-testid="routines-dashboard">
      <AttentionStrip items={attention} />

      <div className="flex flex-wrap items-center gap-2">
        <Radio className="h-3.5 w-3.5 text-primary-hover" aria-hidden />
        <h2 className="text-[11px] font-semibold uppercase tracking-wider text-foreground/70">Routine run summary</h2>
        <span className="font-mono text-[10px] text-muted-foreground">
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

      <div className="grid grid-cols-1 gap-3 xl:grid-cols-5">
        <div className="xl:col-span-3">
          <DashboardCard
            title={`Run volume · ${WINDOW_DAYS}d · by routine`}
            icon={Radio}
            hint={`${kpis.total} runs`}
            action={
              <Link href="/activity?lens=routines" className="text-primary-hover hover:underline">
                Activity →
              </Link>
            }
          >
            <RunVolumeChart buckets={volume.buckets} series={volume.series} window="7d" />
          </DashboardCard>
        </div>

        <div className="flex min-w-0 flex-col gap-3 xl:col-span-2">
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

          {failing.length > 0 && (
            <DashboardCard
              title="Could not finish last time"
              icon={XCircle}
              hint={`${failing.length} ${failing.length === 1 ? "routine" : "routines"}`}
            >
              <div>
                {failing.slice(0, 5).map((r) => (
                  <button
                    key={r.slug}
                    type="button"
                    onClick={() => onSelect(r.slug)}
                    className="group grid w-full grid-cols-[auto_minmax(0,1fr)_auto_auto] items-center gap-2.5 rounded-md border-b border-border/50 px-2 py-2 text-left last:border-0 hover:bg-foreground/[0.025]"
                  >
                    <CrewIcon icon={resolveRoutineIcon(r)} color={resolveRoutineColor(r)} size="sm" />
                    <span className="truncate text-body font-medium text-foreground/90">{r.name}</span>
                    <span className="font-mono text-label tabular-nums text-muted-foreground">{r.last_invoked_at ? formatAgo(r.last_invoked_at) : ""}</span>
                    <span className="inline-flex items-center gap-1 text-label font-medium text-primary-hover">
                      Inspect <ChevronRight className="h-3.5 w-3.5" />
                    </span>
                  </button>
                ))}
              </div>
            </DashboardCard>
          )}

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
                    <CrewIcon icon={resolveRoutineIcon(r)} color={resolveRoutineColor(r)} size="sm" />
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

      <p className="text-label text-muted-foreground">
        <AlarmClock className="mr-1 inline h-3.5 w-3.5 align-[-2px]" aria-hidden />
        Every run, with its steps and files, is in{" "}
        <Link href="/activity?lens=routines" className="text-primary-hover hover:underline">
          Activity → Routines
        </Link>
        .
      </p>
    </div>
  )
}

const PILL = { success: "success", destructive: "danger", warn: "warn", blue: "blue", default: "muted" } as const

/** One running or waiting run, as the dashboard's Routines running now draws it. */
function RunRow({ run, routine }: { run: DashboardRun; routine?: Pipeline }) {
  const p = routineRunPresentation({ status: run.status, outcome: run.outcome })
  return (
    <Link
      href={routineRunHref(run.pipeline_slug, run.id)}
      className="group grid grid-cols-[auto_minmax(0,1fr)_auto_auto] items-center gap-2.5 rounded-md border-b border-border/50 px-2 py-2 last:border-0 hover:bg-foreground/[0.025]"
    >
      <CrewIcon icon={resolveRoutineIcon(routine ?? { slug: run.pipeline_slug })} color={resolveRoutineColor(routine ?? { slug: run.pipeline_slug })} size="sm" />
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

