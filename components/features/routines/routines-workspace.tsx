"use client"

import { useMemo, type ReactNode } from "react"
import Link from "next/link"
import { CalendarClock, ChevronRight, Workflow } from "lucide-react"

import { formatRoutineTime } from "@/lib/routine-time"
import { describeCron } from "@/lib/cron-describe"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import { usePipelineSchedules, type PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import type { Pipeline } from "@/hooks/use-pipelines"
import { useActiveRoutineRuns, isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import { useAutomations } from "@/hooks/use-automations"
import { automationsForRoutine } from "@/lib/automations"
import { CrewIcon } from "@/components/ui/crew-icon"
import { StatusPill } from "@/components/ui/status-pill"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import {
  matchesRoutineFilters,
  routineFilterInput,
  type RoutineFilterState,
} from "@/lib/routine-filters"
import { cn } from "@/lib/utils"
import { formatAgo, routineRunPresentation } from "@/lib/routine-run-presentation"
import { routineViewHref } from "./routine-navigation"
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

// The explorer sidebar owns navigation, search and the status buckets, as it
// does on every page. This panel does not repeat those controls: it answers
// "what needs me" first (three tiles, each opening the concrete run or plan),
// then lists what each routine does, how it starts and how it went last time
// (docs/ux/routines-operator-console-2026-09-15.md §3, screen 1).

type ListTab = "routines" | "calendar" | "recent runs"
const TABS: readonly ListTab[] = ["routines", "calendar", "recent runs"]

/** The list row's `draft` field (operator console contract, "Pipeline list
 * item"): present only when an unpublished draft exists for the routine. */
export interface RoutineDraftSummary {
  id?: string
  revision: number
  updated_at?: string
  updated_by?: string
}
type ListRoutine = Pipeline & { draft?: RoutineDraftSummary | null }

interface RoutinesWorkspaceProps {
  workspaceId: string
  routines: Pipeline[]
  loading: boolean
  error: string | null
  onSelect: (slug: string) => void
  /** Search and filters belong to the explorer sidebar; the list only applies them. */
  search?: string
  filters?: RoutineFilterState
}

/** StatusPill tone for a run presentation tone. */
const PILL_TONE = {
  success: "success",
  destructive: "danger",
  warn: "warn",
  blue: "blue",
  default: "muted",
} as const

/** The word and tone for a routine's last run, from the row plus the live feed. */
export function routineLastState(
  routine: Pipeline,
  live?: { status: string } | null,
): { status: string; label: string } {
  if (live && isAwaitingApproval(live.status))
    return { status: "WAITING", label: "Waiting for a person" }
  if (live) return { status: "RUNNING", label: "Running" }
  const status = routine.last_invocation_status
  if (!status) return { status: "PENDING", label: "Never run" }
  if (status === "failed") return { status: "FAILED", label: "Could not finish" }
  if (routine.last_run_outcome === "FAILED") return { status: "FAILED", label: "Result failed" }
  if (status === "cancelled") return { status: "CANCELLED", label: "Stopped" }
  if (status === "completed") return { status: "SUCCEEDED", label: "Completed" }
  // Anything else (queued, interrupted, dry_run …) gets the word and tone the
  // run page gives it, never the raw token.
  const p = routineRunPresentation({ status, outcome: routine.last_run_outcome })
  const tone =
    p.tone === "destructive"
      ? "FAILED"
      : p.tone === "warn"
        ? "WAITING"
        : p.tone === "blue"
          ? "RUNNING"
          : p.tone === "success"
            ? "SUCCEEDED"
            : "PENDING"
  return { status: tone, label: p.label }
}

/** "Every day at 09:00 · Europe/Prague" for the enabled plan, else what starts it. */
function whenItRuns(schedule: PipelineSchedule | undefined, automations: number): string {
  const plan = schedule ? `${describeCron(schedule.cron_expr)} · ${schedule.timezone || "UTC"}` : null
  const rules = automations > 0 ? `${automations} automation${automations === 1 ? "" : "s"}` : null
  return [plan, rules].filter(Boolean).join(" · ") || "Manual"
}

/** "08:00" in the schedule's zone — the big figure on the next-start tile. */
function clockIn(iso: string, timeZone?: string): string {
  try {
    return new Date(iso).toLocaleTimeString("en-GB", {
      hour: "2-digit",
      minute: "2-digit",
      timeZone: timeZone || undefined,
    })
  } catch {
    return new Date(iso).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" })
  }
}

export function RoutinesWorkspace(props: RoutinesWorkspaceProps) {
  const [selectedTab, setTab] = useUrlSelection("tab")
  const tab: ListTab = TABS.includes(selectedTab as ListTab) ? (selectedTab as ListTab) : "routines"
  const { bySlug, runs: activeRuns } = useActiveRoutineRuns()
  const { schedules } = usePipelineSchedules(props.workspaceId)
  const { automations } = useAutomations(props.workspaceId)
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

  const visible = props.routines as ListRoutine[]
  // Any explorer facet can empty the list, not only the status bucket.
  const narrowed =
    !!search ||
    (!!filters &&
      (filters.status !== "all" || filters.invocations !== "all" || filters.authorAgentId !== null))
  const displayed = visible.filter(
    (routine) => !filters || matchesRoutineFilters(routineFilterInput(routine), filters, bySlug, search),
  )

  // What needs me, first: the newest run parked on a decision for one of
  // these routines, the newest routine that could not finish, and the next
  // planned start. Each tile opens the concrete run or plan, not a list.
  const waitingRuns = activeRuns.filter(
    (r) => isAwaitingApproval(r.status) && visible.some((p) => p.slug === r.pipeline_slug),
  )
  const waiting = waitingRuns[0]
  const failedRoutines = visible
    .filter((p) => routineLastState(p, null).status === "FAILED")
    .sort((a, b) => (b.last_invoked_at ?? "").localeCompare(a.last_invoked_at ?? ""))
  const newestFailed = failedRoutines[0]
  const nextPlan = useMemo(() => {
    const upcoming = [...scheduleBySlug.values()]
      .filter((s) => s.next_run_at && visible.some((p) => p.slug === s.target_pipeline_slug))
      .sort((a, b) => (a.next_run_at ?? "").localeCompare(b.next_run_at ?? ""))
    return upcoming[0]
  }, [scheduleBySlug, visible])
  const nextRoutine = nextPlan
    ? visible.find((p) => p.slug === nextPlan.target_pipeline_slug)
    : undefined

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
            <div aria-label="Needs you" role="group" className="mb-4 grid gap-2.5 md:grid-cols-3">
              <NeedsTile
                figure={waitingRuns.length}
                tone="text-warn"
                title="Waiting for your decision"
                hint={
                  waiting
                    ? `Open the newest · ${waiting.pipeline_name || waiting.pipeline_slug}`
                    : "Nothing right now"
                }
                href={waiting ? routineRunHref(waiting.pipeline_slug, waiting.id) : undefined}
              />
              <NeedsTile
                figure={failedRoutines.length}
                tone="text-destructive"
                title="Could not finish last time"
                hint={newestFailed ? `Open the newest problem · ${newestFailed.name}` : "Nothing right now"}
                onClick={newestFailed ? () => props.onSelect(newestFailed.slug) : undefined}
              />
              <NeedsTile
                figure={
                  nextPlan?.next_run_at ? clockIn(nextPlan.next_run_at, nextPlan.timezone) : "—"
                }
                small
                title="Next planned start"
                hint={
                  nextPlan?.next_run_at
                    ? `${formatRoutineTime(nextPlan.next_run_at, nextPlan.timezone || undefined)} · ${nextRoutine?.name ?? nextPlan.target_pipeline_slug}`
                    : "No schedule is on"
                }
                href={
                  nextPlan?.target_pipeline_slug
                    ? routineViewHref(nextPlan.target_pipeline_slug, "plan")
                    : undefined
                }
              />
            </div>

            {displayed.length !== visible.length && (
              <div className="mb-3 flex items-center justify-end text-[11px] text-muted-foreground">
                {displayed.length} of {visible.length} match the explorer&apos;s filters
              </div>
            )}

            {props.error && (
              <p role="alert" className="mb-3 text-sm text-destructive">
                Routines could not be loaded.
              </p>
            )}

            <div className="overflow-hidden rounded-xl border border-border/60 bg-card">
              <div
                className="hidden gap-3 border-b border-border/60 px-4 py-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground md:grid md:grid-cols-[minmax(0,1fr)_200px_160px_24px]"
                aria-hidden
              >
                <span>Routine and purpose</span>
                <span>Runs how</span>
                <span>Last run</span>
                <span />
              </div>
              <ul className="divide-y divide-border/60">
                {displayed.map((routine) => {
                  const live = bySlug.get(routine.slug) ?? null
                  const state = routineLastState(routine, live)
                  const published = (routine.head_version ?? 0) > 0
                  const draft = routine.draft ?? null
                  const when = live
                    ? formatAgo(live.started_at)
                    : state.status === "PENDING"
                      ? ""
                      : formatAgo(routine.last_invoked_at)
                  return (
                    <li key={routine.id}>
                      <button
                        type="button"
                        onClick={() => props.onSelect(routine.slug)}
                        className="relative grid w-full grid-cols-1 gap-2 px-4 py-3 pr-10 text-left transition-colors hover:bg-muted/30 md:grid-cols-[minmax(0,1fr)_200px_160px_24px] md:items-center md:gap-3 md:pr-4"
                      >
                        <span className="flex min-w-0 items-start gap-3">
                          <CrewIcon
                            icon={resolveRoutineIcon(routine)}
                            color={resolveRoutineColor(routine)}
                            size="sm"
                          />
                          <span className="min-w-0 flex-1">
                            <span className="flex min-w-0 items-center gap-2">
                              <span className="truncate text-sm font-medium">{routine.name}</span>
                              {draft && (
                                <StatusPill
                                  tone="purple"
                                  label={published ? `Draft r${draft.revision}` : "Draft"}
                                  title={
                                    published
                                      ? `A draft newer than v${routine.head_version} is waiting to be published`
                                      : "Not published yet — nothing runs until it is"
                                  }
                                />
                              )}
                            </span>
                            <span
                              className="block truncate text-[13px] text-muted-foreground"
                              title={routine.description || undefined}
                            >
                              {routine.description || <i>No purpose written yet</i>}
                            </span>
                          </span>
                        </span>
                        <span className="hidden text-xs text-muted-foreground md:block">
                          {draft && !published
                            ? "Draft · not published"
                            : whenItRuns(
                                scheduleBySlug.get(routine.slug),
                                automationsForRoutine(automations, routine.slug).length,
                              )}
                        </span>
                        <span className="flex flex-col items-start gap-0.5">
                          <StatusPill
                            status={state.status}
                            label={state.label}
                            live={state.status === "RUNNING"}
                          />
                          {when && (
                            <span className="text-[11px] text-muted-foreground">{when}</span>
                          )}
                        </span>
                        <ChevronRight
                          aria-hidden
                          className="absolute right-4 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground md:static md:translate-y-0"
                        />
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
                        : narrowed
                          ? "No routines match the explorer's filters."
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

/** One "Needs you" tile: a figure, a title and where a click goes. A tile
 * with nothing behind it is plain text — a button that opens nothing is a lie. */
function NeedsTile({
  figure,
  small,
  tone,
  title,
  hint,
  href,
  onClick,
}: {
  figure: ReactNode
  small?: boolean
  tone?: string
  title: string
  hint: string
  href?: string
  onClick?: () => void
}) {
  const body = (
    <>
      <span
        className={cn(
          "min-w-[28px] shrink-0 tabular-nums font-semibold",
          small ? "text-sm" : "text-xl",
          tone,
        )}
      >
        {figure}
      </span>
      <span className="min-w-0 text-xs text-muted-foreground">
        <span className="block truncate font-medium text-foreground">{title}</span>
        <span className="block truncate">{hint}</span>
      </span>
    </>
  )
  const className = cn(
    "flex min-h-12 w-full items-center gap-3 rounded-xl border border-border/60 bg-card px-4 py-2.5 text-left",
    (href || onClick) && "transition-colors hover:border-muted-foreground/40",
  )
  if (href)
    return (
      <Link href={href} className={className}>
        {body}
      </Link>
    )
  if (onClick)
    return (
      <button type="button" onClick={onClick} className={className}>
        {body}
      </button>
    )
  return <div className={className}>{body}</div>
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
          const presentation = routineRunPresentation(run)
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
              <StatusPill tone={PILL_TONE[presentation.tone]} label={presentation.label} />
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
