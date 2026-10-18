"use client"

import { useMemo } from "react"
import { Workflow } from "lucide-react"

import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import { usePendingStarts } from "@/hooks/use-pending-starts"
import type { Pipeline } from "@/hooks/use-pipelines"
import { useActiveRoutineRuns, isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import { PageEmpty, PageError } from "@/components/layout/page-state"
import {
  matchesRoutineFilters,
  routineFilterInput,
  type RoutineFilterState,
} from "@/lib/routine-filters"
import { routineRunPresentation } from "@/lib/routine-run-presentation"
import { RoutinesDashboard, outcomeKpis, windowCoverage } from "./routines-dashboard"
import { RoutinesOverviewBoard } from "./routines-overview-board"
import { useAbilities } from "@/hooks/use-abilities"
import { useWorkspaceWaitpoints } from "@/hooks/use-workspace-waitpoints"
import { useBudgetSummary } from "@/hooks/use-routine-budget"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"
import { RoutinesIntro } from "./routines-intro"

// The server caps a run list at 200 rows.
const DASHBOARD_RUN_LIMIT = 200

/** The history rows plus every active run they miss: a run that started
 * before the newest rows is still going and belongs on the dashboard. An
 * active row replaces its older recorded copy, since the active feed is
 * the fresher one for a run in flight. */
export function withActiveRuns<T extends { id: string }>(recorded: T[], active: T[]): T[] {
  if (active.length === 0) return recorded
  const live = new Map(active.map((r) => [r.id, r]))
  const merged = recorded.map((r) => live.get(r.id) ?? r)
  const seen = new Set(recorded.map((r) => r.id))
  for (const r of active) if (!seen.has(r.id)) merged.push(r)
  return merged
}

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
  /** Load the list again after an error. */
  onRetry?: () => void
  /** Open a run page (from Needs you and Running now). */
  onOpenRun?: (slug: string, runId: string) => void
  /** Open New routine; absent when the person cannot create one. */
  onNew?: () => void
}

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

export function RoutinesWorkspace(props: RoutinesWorkspaceProps) {
  const { bySlug, runs: activeRuns } = useActiveRoutineRuns()
  const { schedules } = usePipelineSchedules(props.workspaceId)
  const { starts: pendingStarts } = usePendingStarts(props.workspaceId)
  const { runs: recordedRuns, loading: dashboardRunsLoading } = usePipelineRuns(
    props.workspaceId,
    "all",
    DASHBOARD_RUN_LIMIT,
  )
  const coveredSince = useMemo(() => windowCoverage(recordedRuns, DASHBOARD_RUN_LIMIT), [recordedRuns])
  const dashboardRuns = useMemo(() => withActiveRuns(recordedRuns, activeRuns), [recordedRuns, activeRuns])
  const filters = props.filters
  const search = props.search ?? ""

  const visible = props.routines as ListRoutine[]
  const displayed = visible.filter(
    (routine) => !filters || matchesRoutineFilters(routineFilterInput(routine), filters, bySlug, search),
  )
  const { role } = useAbilities()
  const { waitpoints, decide } = useWorkspaceWaitpoints(props.workspaceId)
  const { summary: spend } = useBudgetSummary(props.workspaceId)
  // The 7-day figure counts the history rows of the routines on screen; an
  // older active run is not part of "the newest N runs" (routines-dashboard).
  const boardKpis = useMemo(() => {
    const from = coveredSince ? Date.parse(coveredSince) : NaN
    const rows = recordedRuns.filter(
      (r) => displayed.some((p) => p.slug === r.pipeline_slug) && (!Number.isFinite(from) || Date.parse(r.started_at) >= from),
    )
    const k = outcomeKpis(rows)
    return { total: k.total, successPct: k.successPct }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [recordedRuns, coveredSince, displayed.map((p) => p.slug).join(",")])

  // What needs me, first: the newest run parked on a decision for one of
  // these routines, the newest routine that could not finish, and the next
  // planned start. Each tile opens the concrete run or plan, not a list.
  return (
    <div className="flex h-full min-w-0 flex-col">
      <div className="min-h-0 flex-1 overflow-auto">
          <section aria-label="Routines overview" className="p-4 md:p-6">
            {props.error && (
              <PageError
                className="mb-3"
                title="Routines could not load"
                description="The filters stay as they are. Try again in a moment."
                onRetry={props.onRetry}
              />
            )}

            {/* The catalog lives in the panel; this pane is the week at a
                glance. An empty workspace still needs the way in. */}
            {!visible.length && !props.loading && !props.error ? (
              <>
                <PageEmpty
                  icon={Workflow}
                  title="No routines yet"
                  description="Describe a job once; a crew runs it on a schedule or a trigger."
                  primary={props.onNew ? { label: "New routine", onClick: props.onNew } : undefined}
                  cli="crewship routine init"
                />
                {/* RT-INTRO as the empty state's guide (no templates, no figures). */}
                <RoutinesIntro compact />
              </>
            ) : (
              <div className="flex flex-col gap-4">
              {/* RT-OV: figures, Needs you, Running now, Next 24 hours. */}
              <RoutinesOverviewBoard
                routines={displayed}
                activeRuns={activeRuns.filter((r) => displayed.some((p) => p.slug === r.pipeline_slug))}
                waitpoints={waitpoints}
                schedules={schedules}
                pendingStarts={pendingStarts}
                kpis={boardKpis}
                coveredSince={coveredSince}
                spend={spend}
                role={role}
                onDecide={async (runId, approved) => {
                  const ok = await decide(runId, approved)
                  if (ok) toast.success(approved ? "Approved · the run continues" : "Rejected")
                  else toast.error("The decision was not accepted", { description: "It may have been answered already. Open the run to see." })
                  return ok
                }}
                onStop={async (runId) => {
                  const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(props.workspaceId)}/pipelines/runs/${encodeURIComponent(runId)}/cancel`, { method: "POST" })
                  if (!res.ok) toast.error("Could not stop this run. It may already have finished.")
                  else toast.success("Stop requested")
                  return res.ok
                }}
                onOpenRun={(slug, runId) => props.onOpenRun?.(slug, runId)}
                onSelect={props.onSelect}
              />
              <RoutinesDashboard
                routines={displayed}
                runs={dashboardRuns}
                runsLoading={dashboardRunsLoading}
                coveredSince={coveredSince}
                pendingStarts={pendingStarts}
                schedules={schedules}
                spend={spend}
                onSelect={props.onSelect}
              />
              </div>
            )}
          </section>
      </div>
    </div>
  )
}
