"use client"

import { useMemo } from "react"
import Link from "next/link"
import { AlertCircle, CalendarClock, Search, Workflow } from "lucide-react"

import { formatRoutineTime } from "@/lib/routine-time"
import { formatRelativeTime } from "@/lib/time"
import { describeCron } from "@/lib/cron-describe"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import { usePipelineSchedules, type PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import type { Pipeline } from "@/hooks/use-pipelines"
import { useActiveRoutineRuns, isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { Input } from "@/components/ui/input"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import {
  matchesRoutineFilters,
  routineFilterInput,
  type RoutineFilterState,
  type RoutineStatusFilter,
} from "@/lib/routine-filters"
import { cn } from "@/lib/utils"
import { RoutineCalendar } from "./routine-calendar"

export const routineRunHref = (slug: string, id: string) =>
  `/routines?${new URLSearchParams({ slug, run: id })}`
export function routineRunLabel(run: { status?: string; outcome?: string }) {
  return run.outcome === "FAILED"
    ? "Result failed"
    : run.outcome === "NEEDS_HUMAN"
      ? "Needs your attention"
      : run.status === "waiting"
        ? "Waiting for a decision"
        : run.status || "Recorded"
}

// One list, not two. The explorer sidebar used to repeat every routine
// beside this panel, with the status buckets as a second navigation; the
// filters now sit above the rows and the routine row itself answers the
// three questions a reader brings: what it does, how it went last time,
// and whether it needs them (#2519).

type ListTab = "routines" | "calendar" | "recent runs"
const TABS: readonly ListTab[] = ["routines", "calendar", "recent runs"]

const STATUS_CHIPS: { value: RoutineStatusFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "awaiting", label: "Need me" },
  { value: "running", label: "Running" },
  { value: "failed", label: "Failed" },
  { value: "never", label: "Never run" },
]

interface RoutinesWorkspaceProps {
  workspaceId: string
  routines: Pipeline[]
  loading: boolean
  error: string | null
  onSelect: (slug: string) => void
  search?: string
  onSearchChange?: (value: string) => void
  filters?: RoutineFilterState
  onFilter?: (status: RoutineStatusFilter) => void
}

/** The word and tone for a routine's last run, from the row plus the live feed. */
export function routineLastState(
  routine: Pipeline,
  live?: { status: string } | null,
): { status: string; label: string } {
  if (live && isAwaitingApproval(live.status)) return { status: "WAITING", label: "Waiting for you" }
  if (live) return { status: "RUNNING", label: "Running" }
  const status = routine.last_invocation_status
  if (!status) return { status: "PENDING", label: "Never run" }
  if (status === "failed") return { status: "FAILED", label: "Failed" }
  if (routine.last_run_outcome === "FAILED") return { status: "FAILED", label: "Result failed" }
  if (status === "cancelled") return { status: "CANCELLED", label: "Stopped" }
  if (status === "completed") return { status: "SUCCEEDED", label: "Completed" }
  return { status: status.toUpperCase(), label: status }
}

function lastResultText(routine: Pipeline, state: ReturnType<typeof routineLastState>) {
  if (state.status === "WAITING") return "Needs your decision"
  if (state.status === "RUNNING") return "Running now"
  if (!routine.last_invoked_at) return "Not run yet"
  const when = formatRelativeTime(routine.last_invoked_at)
  if (state.status === "FAILED") return `Could not finish · ${when}`
  if (state.status === "CANCELLED") return `Stopped · ${when}`
  return `Finished · ${when}`
}

/** "Every day at 09:00 · Europe/Prague" for the enabled plan of a routine, or "Manual". */
function whenItRuns(schedule?: PipelineSchedule): string {
  if (!schedule) return "Manual"
  return `${describeCron(schedule.cron_expr)} · ${schedule.timezone || "UTC"}`
}

export function RoutinesWorkspace(props: RoutinesWorkspaceProps) {
  const [selectedTab, setTab] = useUrlSelection("tab")
  const tab: ListTab = TABS.includes(selectedTab as ListTab) ? (selectedTab as ListTab) : "routines"
  const { bySlug, runs: activeRuns } = useActiveRoutineRuns()
  const { schedules } = usePipelineSchedules(props.workspaceId)
  const filters = props.filters
  const search = props.search ?? ""

  const scheduleBySlug = useMemo(() => {
    const map = new Map<string, PipelineSchedule>()
    for (const s of schedules) {
      if (!s.enabled || !s.target_pipeline_slug) continue
      const prev = map.get(s.target_pipeline_slug)
      if (!prev || (s.next_run_at && (!prev.next_run_at || s.next_run_at < prev.next_run_at)))
        map.set(s.target_pipeline_slug, s)
    }
    return map
  }, [schedules])

  const visible = props.routines
  const displayed = visible.filter(
    (routine) => !filters || matchesRoutineFilters(routineFilterInput(routine), filters, bySlug, search),
  )

  // What needs me, first (docs/ux/README.md §1): the newest run parked on
  // a decision for one of these routines.
  const waiting = activeRuns.find(
    (r) => isAwaitingApproval(r.status) && visible.some((p) => p.slug === r.pipeline_slug),
  )
  const waitingCount = activeRuns.filter(
    (r) => isAwaitingApproval(r.status) && visible.some((p) => p.slug === r.pipeline_slug),
  ).length
  const runningCount = visible.filter((p) => {
    const live = bySlug.get(p.slug)
    return live && !isAwaitingApproval(live.status)
  }).length
  const failedCount = visible.filter((p) => routineLastState(p, null).status === "FAILED").length
  const nextPlan = useMemo(() => {
    const upcoming = [...scheduleBySlug.values()]
      .filter((s) => s.next_run_at && visible.some((p) => p.slug === s.target_pipeline_slug))
      .sort((a, b) => (a.next_run_at ?? "").localeCompare(b.next_run_at ?? ""))
    return upcoming[0]
  }, [scheduleBySlug, visible])

  return (
    <div className="flex h-full min-w-0 flex-col">
      <nav aria-label="Routines views" className="flex shrink-0 gap-4 border-b border-border px-6">
        {TABS.map((view) => (
          <button
            key={view}
            type="button"
            aria-pressed={tab === view}
            onClick={() => setTab(view === "routines" ? null : view)}
            className={cn(
              "border-b-2 px-1 py-3 text-xs transition-colors",
              tab === view
                ? "border-primary text-primary"
                : "border-transparent text-muted-foreground hover:text-foreground",
            )}
          >
            {view[0].toUpperCase() + view.slice(1)}
          </button>
        ))}
      </nav>
      <div className="min-h-0 flex-1 overflow-auto">
        {tab === "routines" && (
          <section aria-label="Routine list" className="mx-auto max-w-[1160px] p-4 md:p-6">
            {waiting && (
              <div
                role="status"
                className="mb-4 flex flex-wrap items-center gap-3 rounded-xl border border-warn/30 bg-warn/10 px-4 py-3"
              >
                <AlertCircle className="h-4 w-4 shrink-0 text-warn" aria-hidden />
                <div className="min-w-0 flex-1 text-sm">
                  <span className="font-medium text-warn">
                    {waitingCount === 1
                      ? "1 run is waiting for your decision"
                      : `${waitingCount} runs are waiting for your decision`}
                  </span>
                  <span className="text-muted-foreground">
                    {" "}
                    · {waiting.pipeline_name || waiting.pipeline_slug}
                  </span>
                </div>
                <Link
                  href={routineRunHref(waiting.pipeline_slug, waiting.id)}
                  className="inline-flex h-8 items-center rounded-md border border-warn/40 px-3 text-xs font-medium hover:bg-warn/10"
                >
                  Review and decide →
                </Link>
              </div>
            )}

            <div className="mb-4 flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
              <Stat n={visible.length} label={visible.length === 1 ? "routine" : "routines"} />
              <Stat n={runningCount} label="running" />
              <Stat n={failedCount} label="failed last time" />
              <div className="flex items-baseline gap-1.5">
                <span className="tabular-nums font-medium text-foreground">
                  {nextPlan?.next_run_at
                    ? formatRoutineTime(nextPlan.next_run_at, nextPlan.timezone || undefined)
                    : "—"}
                </span>
                <span>
                  next planned start
                  {nextPlan?.target_pipeline_slug && (
                    <>
                      {" "}
                      ·{" "}
                      {visible.find((p) => p.slug === nextPlan.target_pipeline_slug)?.name ??
                        nextPlan.target_pipeline_slug}
                    </>
                  )}
                </span>
              </div>
            </div>

            <div className="mb-3 flex flex-wrap items-center gap-2">
              <div className="relative min-w-[220px] flex-1 sm:max-w-xs" data-routines-search>
                <Search
                  className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
                  aria-hidden
                />
                <Input
                  aria-label="Search routines"
                  placeholder="Search by name or purpose"
                  value={search}
                  onChange={(e) => props.onSearchChange?.(e.target.value)}
                  className="h-8 pl-8 text-xs"
                />
              </div>
              {filters && props.onFilter && (
                <div
                  role="group"
                  aria-label="Filter by last result"
                  className="flex overflow-hidden rounded-md border border-border"
                >
                  {STATUS_CHIPS.map((chip) => (
                    <button
                      key={chip.value}
                      type="button"
                      aria-pressed={filters.status === chip.value}
                      onClick={() => props.onFilter?.(chip.value)}
                      className={cn(
                        "px-2.5 py-1.5 text-xs transition-colors",
                        filters.status === chip.value
                          ? "bg-muted font-medium text-foreground"
                          : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      {chip.label}
                    </button>
                  ))}
                </div>
              )}
              <span className="ml-auto text-[11px] text-muted-foreground">
                {displayed.length} of {visible.length}
              </span>
            </div>

            {props.error && (
              <p role="alert" className="mb-3 text-sm text-destructive">
                Routines could not be loaded.
              </p>
            )}

            <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
              <div
                className="hidden gap-3 border-b border-border/60 px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:grid"
                style={{ gridTemplateColumns: "minmax(0,1fr) 150px 60px minmax(0,220px) 60px" }}
                aria-hidden
              >
                <span>Routine and purpose</span>
                <span>Last time</span>
                <span className="text-right">Runs</span>
                <span>Last result</span>
                <span />
              </div>
              <ul className="divide-y divide-border/60">
                {displayed.map((routine) => {
                  const state = routineLastState(routine, bySlug.get(routine.slug) ?? null)
                  const stepCount = routine.step_count
                  return (
                    <li key={routine.id}>
                      <button
                        type="button"
                        onClick={() => props.onSelect(routine.slug)}
                        className="grid w-full gap-3 px-4 py-3 text-left transition-colors hover:bg-muted/30 md:items-center"
                        style={{ gridTemplateColumns: "minmax(0,1fr) 150px 60px minmax(0,220px) 60px" }}
                      >
                        <span className="flex min-w-0 items-start gap-3">
                          <CrewIcon
                            icon={resolveRoutineIcon(routine)}
                            color={resolveRoutineColor(routine)}
                            size="sm"
                          />
                          <span className="min-w-0 flex-1">
                            <span className="block truncate text-sm font-medium">{routine.name}</span>
                            <span
                              className="block truncate text-[13px] text-muted-foreground"
                              title={routine.description || undefined}
                            >
                              {routine.description || <i>No purpose written yet</i>}
                            </span>
                            <span className="mt-0.5 block font-mono text-[11px] text-muted-foreground">
                              {stepCount != null && (
                                <>
                                  {stepCount} {stepCount === 1 ? "step" : "steps"} ·{" "}
                                </>
                              )}
                              {whenItRuns(scheduleBySlug.get(routine.slug))}
                            </span>
                          </span>
                        </span>
                        <span>
                          <StatusPill
                            status={state.status}
                            label={state.label}
                            live={state.status === "RUNNING"}
                          />
                        </span>
                        <span className="text-right font-mono text-xs tabular-nums text-muted-foreground">
                          {routine.invocation_count ?? 0}
                        </span>
                        <span className="truncate text-xs text-muted-foreground">
                          {lastResultText(routine, state)}
                        </span>
                        <span className="text-right text-xs text-primary">Open →</span>
                      </button>
                    </li>
                  )
                })}
              </ul>
              {!displayed.length && !props.error && (
                <div className="p-3">
                  <InlineEmpty
                    icon={Workflow}
                    text={
                      props.loading
                        ? "Loading routines…"
                        : search || (filters && filters.status !== "all")
                          ? "No routines match these filters."
                          : "No routines yet. Create one with New routine, or import a bundle."
                    }
                  />
                </div>
              )}
            </div>
          </section>
        )}
        {tab === "calendar" && (
          <div className="p-4 md:p-6">
            <RoutineCalendar workspaceId={props.workspaceId} routines={props.routines} />
          </div>
        )}
        {tab === "recent runs" && (
          <RecentRoutineRuns workspaceId={props.workspaceId} routines={props.routines} />
        )}
      </div>
    </div>
  )
}

function Stat({ n, label }: { n: number; label: string }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="tabular-nums font-medium text-foreground">{n}</span>
      <span>{label}</span>
    </div>
  )
}

function RecentRoutineRuns({
  workspaceId,
  routines,
}: {
  workspaceId: string
  routines: Pipeline[]
}) {
  const { runs, loading, error } = usePipelineRuns(workspaceId, "all", 200)
  const visibleRuns = runs.filter((run) => routines.some((r) => r.slug === run.pipeline_slug))
  return (
    <section className="mx-auto max-w-[1160px] p-4 md:p-6">
      <h1 className="text-lg font-medium">Recent runs</h1>
      <p className="mb-4 text-xs text-muted-foreground">
        Latest {visibleRuns.length} loaded runs · times in {formatRoutineTime(new Date()).split(" · ")[1]}
      </p>
      {error && (
        <p role="alert" className="mb-3 text-sm text-destructive">
          Run history could not be loaded.
        </p>
      )}
      <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
        {visibleRuns.map((run) => {
          const routine = routines.find((r) => r.slug === run.pipeline_slug)
          const tone =
            run.outcome === "FAILED" || run.status === "failed"
              ? "FAILED"
              : isAwaitingApproval(run.status)
                ? "WAITING"
                : run.status === "completed"
                  ? "SUCCEEDED"
                  : run.status.toUpperCase()
          return (
            <Link
              key={run.id}
              href={routineRunHref(run.pipeline_slug, run.id)}
              className="flex flex-wrap items-center gap-3 border-b border-border/60 px-4 py-3 text-xs last:border-0 hover:bg-muted/30"
            >
              <CrewIcon
                icon={resolveRoutineIcon(routine ?? { slug: run.pipeline_slug })}
                color={resolveRoutineColor(routine ?? { slug: run.pipeline_slug })}
                size="sm"
              />
              <span className="min-w-0 flex-1 text-sm font-medium">
                {run.pipeline_name || run.pipeline_slug}
              </span>
              <span className="font-mono text-[11px] text-muted-foreground">
                {formatRoutineTime(run.started_at).split(" · ")[0]}
              </span>
              <StatusPill status={tone} label={routineRunLabel(run)} />
            </Link>
          )
        })}
        {!visibleRuns.length && (
          <div className="p-3">
            <InlineEmpty
              icon={CalendarClock}
              text={loading ? "Loading runs…" : error ? "History unavailable." : "No runs yet."}
            />
          </div>
        )}
      </div>
    </section>
  )
}
