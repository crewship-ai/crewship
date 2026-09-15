"use client"

import * as React from "react"
import Link from "next/link"
import { Activity, AlarmClock, FileEdit, XCircle } from "lucide-react"

import { cn } from "@/lib/utils"
import { formatRoutineTime } from "@/lib/routine-time"
import { formatDurationMs } from "@/lib/activity-stream"
import { describeCron } from "@/lib/cron-describe"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { DetailCard } from "@/components/ui/detail"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { routineRunPresentation, formatAgo } from "@/lib/routine-run-presentation"
import { recentRuns, runOutcomesByDay, upcomingSchedules, type OverviewRun } from "@/lib/routines-overview"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { routineRunHref } from "./routines-workspace"
import { routineViewHref } from "./routine-navigation"

// routines-dashboard — what the main pane answers when no routine is
// selected. The sidebar is the catalog already (iconed, searched, filtered);
// repeating it here was the same list a second time. This pane says what
// happened in the last seven days, what happens next and what needs a
// person, in one screen: a strip of one-line figures, the week as seven
// small stacked bars, then recent runs beside what is coming up.
//
// Every figure is derived from the run list the page already loads
// (`usePipelineRuns`, 200 newest rows) and the schedules; nothing here is
// fetched twice or estimated.

const WINDOW_DAYS = 7

export interface DashboardRun extends OverviewRun {
  outcome?: string
}

interface Props {
  routines: Pipeline[]
  runs: DashboardRun[]
  runsLoading?: boolean
  schedules: PipelineSchedule[]
  onSelect: (slug: string) => void
  /** The Recent runs tab of this page. */
  onShowRuns: () => void
}

/** Runs judged by their result, not only by the engine status: a run that
 * completed with a failed result is a failure for a reader. */
function effectiveStatus(run: DashboardRun): string {
  if (run.outcome === "FAILED" && !["cancelled", "canceled", "interrupted"].includes((run.status ?? "").toLowerCase()))
    return "failed"
  return (run.status ?? "").toLowerCase()
}

export function weekFigures(runs: DashboardRun[], now = new Date()) {
  const since = now.getTime() - WINDOW_DAYS * 86_400_000
  const week = runs.filter((r) => {
    const t = Date.parse(r.started_at)
    return Number.isFinite(t) && t >= since
  })
  const by = (pred: (s: string) => boolean) => week.filter((r) => pred(effectiveStatus(r))).length
  const durations = week
    .map((r) => r.duration_ms)
    .filter((d): d is number => typeof d === "number" && d > 0)
    .sort((a, b) => a - b)
  const median = durations.length ? durations[Math.floor(durations.length / 2)] : null
  const cost = week.reduce((sum, r) => sum + (typeof r.cost_usd === "number" ? r.cost_usd : 0), 0)
  return {
    total: week.length,
    completed: by((s) => ["completed", "succeeded", "success"].includes(s)),
    failed: by((s) => ["failed", "error"].includes(s)),
    stopped: by((s) => ["cancelled", "canceled", "interrupted"].includes(s)),
    waiting: runs.filter((r) => ["waiting", "paused"].includes((r.status ?? "").toLowerCase())).length,
    running: runs.filter((r) => ["running", "queued"].includes((r.status ?? "").toLowerCase())).length,
    cost,
    median,
  }
}

const money = (usd: number) => (usd <= 0 ? "$0" : usd < 0.01 ? "<$0.01" : `$${usd.toFixed(2)}`)

export function RoutinesDashboard({ routines, runs, runsLoading, schedules, onSelect, onShowRuns }: Props) {
  const now = React.useMemo(() => new Date(), [])
  const visibleRuns = React.useMemo(
    () => runs.filter((r) => routines.some((p) => p.slug === r.pipeline_slug)),
    [runs, routines],
  )
  const figures = React.useMemo(() => weekFigures(visibleRuns, now), [visibleRuns, now])
  const days = React.useMemo(
    () => runOutcomesByDay(visibleRuns.map((r) => ({ ...r, status: effectiveStatus(r) })), now, WINDOW_DAYS),
    [visibleRuns, now],
  )
  const peak = Math.max(1, ...days.map((d) => d.passed + d.failed + d.pending + d.other))
  const recent = React.useMemo(() => recentRuns(visibleRuns, 8), [visibleRuns])
  const upcoming = React.useMemo(
    () => upcomingSchedules(schedules.filter((s) => routines.some((p) => p.slug === s.target_pipeline_slug)), now, 5),
    [schedules, routines, now],
  )
  const drafts = routines.filter((r) => r.draft)
  const failing = routines
    .filter((r) => r.last_invocation_status === "failed" || r.last_run_outcome === "FAILED")
    .sort((a, b) => (b.last_invoked_at ?? "").localeCompare(a.last_invoked_at ?? ""))
    .slice(0, 5)
  const routineOf = (slug: string) => routines.find((p) => p.slug === slug)

  return (
    <div className="flex flex-col gap-4" data-testid="routines-dashboard">
      {/* ── The week in one line ─────────────────────────────────── */}
      <div className="rounded-xl border border-border/60 bg-card px-4 py-3">
        <div className="flex flex-wrap items-baseline gap-x-6 gap-y-1.5 text-xs text-muted-foreground">
          <span className="text-[11px] font-semibold uppercase tracking-wider">Last {WINDOW_DAYS} days</span>
          <Figure n={figures.total} label={figures.total === 1 ? "run" : "runs"} onClick={onShowRuns} />
          <Figure n={figures.completed} label="completed" tone="text-success" />
          <Figure n={figures.failed} label="could not finish" tone="text-destructive" />
          <Figure n={figures.stopped} label="stopped" />
          <Figure n={figures.waiting} label="waiting for a person now" tone="text-warn" />
          {figures.running > 0 && <Figure n={figures.running} label="running now" tone="text-primary" />}
          <Figure text={money(figures.cost)} label="spent" />
          <Figure text={figures.median != null ? formatDurationMs(figures.median) : "—"} label="typical run" />
          {runsLoading && <span className="text-muted-foreground-soft">loading…</span>}
        </div>
        {/* Seven stacked bars: passed / could not finish / stopped / still going,
            2px gaps between segments, height by the busiest day. Status colours
            are the app's own; the legend names them so colour is never alone. */}
        <div className="mt-3 flex items-end gap-2" role="img" aria-label={`Runs per day for the last ${WINDOW_DAYS} days`}>
          {days.map((d) => {
            const total = d.passed + d.failed + d.pending + d.other
            const seg = (n: number, cls: string, what: string) =>
              n > 0 ? (
                <span
                  key={what}
                  title={`${n} ${what}`}
                  className={cn("block w-full rounded-[2px]", cls)}
                  style={{ height: `${Math.max(3, (n / peak) * 36)}px` }}
                />
              ) : null
            return (
              <div key={d.label} className="flex min-w-0 flex-1 flex-col items-center gap-1" title={`${d.label}: ${total} ${total === 1 ? "run" : "runs"}`}>
                <div className="flex h-10 w-full flex-col-reverse justify-start gap-[2px]">
                  {seg(d.passed, "bg-success/80", "completed")}
                  {seg(d.failed, "bg-destructive/80", "could not finish")}
                  {seg(d.other, "bg-muted-foreground/40", "stopped")}
                  {seg(d.pending, "bg-primary/70", "still going")}
                </div>
                <span className={cn("text-[10px] tabular-nums", d.isToday ? "font-semibold text-foreground" : "text-muted-foreground-soft")}>
                  {d.label}
                </span>
              </div>
            )
          })}
        </div>
        <div className="mt-1.5 flex flex-wrap gap-x-3 text-[10px] text-muted-foreground-soft">
          <Legend cls="bg-success/80" label="completed" />
          <Legend cls="bg-destructive/80" label="could not finish" />
          <Legend cls="bg-muted-foreground/40" label="stopped" />
          <Legend cls="bg-primary/70" label="still going" />
        </div>
      </div>

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
        {/* ── Recent runs ─────────────────────────────────────────── */}
        <DetailCard
          title="Recent runs"
          icon={Activity}
          bare
          action={
            <button type="button" onClick={onShowRuns} className="text-[11px] text-primary hover:underline">
              All runs
            </button>
          }
        >
          {recent.length === 0 ? (
            <p className="px-4 py-3 text-xs text-muted-foreground">{runsLoading ? "Loading runs…" : "No runs yet."}</p>
          ) : (
            <ul className="divide-y divide-border/40">
              {recent.map((run) => {
                const routine = routineOf(run.pipeline_slug)
                const p = routineRunPresentation({ status: run.status, outcome: (run as DashboardRun).outcome })
                return (
                  <li key={run.id}>
                    <Link
                      href={routineRunHref(run.pipeline_slug, run.id)}
                      className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-3 px-4 py-2 text-xs hover:bg-muted/30 md:grid-cols-[auto_minmax(0,1fr)_120px_64px]"
                    >
                      <CrewIcon icon={resolveRoutineIcon(routine ?? { slug: run.pipeline_slug })} color={resolveRoutineColor(routine ?? { slug: run.pipeline_slug })} size="sm" />
                      <span className="min-w-0">
                        <span className="block truncate text-[13px] font-medium text-foreground">{run.pipeline_name || routine?.name || run.pipeline_slug}</span>
                        <span className="block text-[11px] text-muted-foreground">{formatAgo(run.started_at)}</span>
                      </span>
                      <StatusPill tone={PILL[p.tone]} label={p.label} live={p.tone === "blue"} />
                      <span className="hidden text-right font-mono text-[11px] tabular-nums text-muted-foreground md:block">
                        {run.duration_ms ? formatDurationMs(run.duration_ms) : ""}
                      </span>
                    </Link>
                  </li>
                )
              })}
            </ul>
          )}
        </DetailCard>

        <div className="flex flex-col gap-4">
          {/* ── Coming up ───────────────────────────────────────── */}
          <DetailCard title="Coming up" icon={AlarmClock} bare>
            {upcoming.length === 0 ? (
              <p className="px-4 py-3 text-xs text-muted-foreground">No schedule is on. Plans start from a routine's Plan tab.</p>
            ) : (
              <ul className="divide-y divide-border/40">
                {upcoming.map((s) => {
                  const routine = s.target_pipeline_slug ? routineOf(s.target_pipeline_slug) : undefined
                  return (
                    <li key={s.id}>
                      <Link
                        href={s.target_pipeline_slug ? routineViewHref(s.target_pipeline_slug, "plan") : "/routines"}
                        className="flex items-center gap-3 px-4 py-2 text-xs hover:bg-muted/30"
                      >
                        <CrewIcon icon={resolveRoutineIcon(routine ?? { slug: s.target_pipeline_slug ?? s.id })} color={resolveRoutineColor(routine ?? { slug: s.target_pipeline_slug ?? s.id })} size="sm" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-[13px] font-medium text-foreground">{routine?.name ?? s.target_pipeline_slug ?? s.name}</span>
                          <span className="block truncate text-[11px] text-muted-foreground">
                            {s.next_run_at ? formatRoutineTime(s.next_run_at, s.timezone || undefined) : describeCron(s.cron_expr)}
                          </span>
                        </span>
                      </Link>
                    </li>
                  )
                })}
              </ul>
            )}
          </DetailCard>

          {/* ── Drafts to publish ───────────────────────────────── */}
          {drafts.length > 0 && (
            <DetailCard title="Drafts to publish" icon={FileEdit} subtitle={String(drafts.length)} bare>
              <ul className="divide-y divide-border/40">
                {drafts.slice(0, 5).map((r) => (
                  <li key={r.slug}>
                    <button type="button" onClick={() => onSelect(r.slug)} className="flex w-full items-center gap-3 px-4 py-2 text-left text-xs hover:bg-muted/30">
                      <CrewIcon icon={resolveRoutineIcon(r)} color={resolveRoutineColor(r)} size="sm" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-[13px] font-medium text-foreground">{r.name}</span>
                        <span className="block text-[11px] text-muted-foreground">
                          {(r.head_version ?? 0) > 0 ? `Draft r${r.draft!.revision} · published v${r.head_version}` : "Not published yet"}
                          {r.draft?.updated_at ? ` · ${formatAgo(r.draft.updated_at)}` : ""}
                        </span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </DetailCard>
          )}

          {/* ── Recently failing ────────────────────────────────── */}
          {failing.length > 0 && (
            <DetailCard title="Could not finish last time" icon={XCircle} subtitle={String(failing.length)} bare>
              <ul className="divide-y divide-border/40">
                {failing.map((r) => (
                  <li key={r.slug}>
                    <button type="button" onClick={() => onSelect(r.slug)} className="flex w-full items-center gap-3 px-4 py-2 text-left text-xs hover:bg-muted/30">
                      <CrewIcon icon={resolveRoutineIcon(r)} color={resolveRoutineColor(r)} size="sm" />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-[13px] font-medium text-foreground">{r.name}</span>
                        <span className="block text-[11px] text-muted-foreground">{r.last_invoked_at ? formatAgo(r.last_invoked_at) : ""}</span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </DetailCard>
          )}
        </div>
      </div>
    </div>
  )
}

const PILL = { success: "success", destructive: "danger", warn: "warn", blue: "blue", default: "muted" } as const

function Figure({ n, text, label, tone, onClick }: { n?: number; text?: string; label: string; tone?: string; onClick?: () => void }) {
  const body = (
    <>
      <span className={cn("text-sm font-semibold tabular-nums text-foreground", tone)}>{text ?? n}</span>
      <span> {label}</span>
    </>
  )
  return onClick ? (
    <button type="button" onClick={onClick} className="hover:underline">
      {body}
    </button>
  ) : (
    <span>{body}</span>
  )
}

function Legend({ cls, label }: { cls: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span aria-hidden className={cn("h-2 w-2 rounded-[2px]", cls)} />
      {label}
    </span>
  )
}
