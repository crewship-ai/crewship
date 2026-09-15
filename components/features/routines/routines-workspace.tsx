"use client"

import Link from "next/link"
import { ArrowUpRight, Workflow } from "lucide-react"

import { useUrlSelection } from "@/hooks/use-issue-detail"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import type { Pipeline } from "@/hooks/use-pipelines"
import { useActiveRoutineRuns, isAwaitingApproval } from "@/hooks/use-active-routine-runs"
import { InlineEmpty } from "@/components/ui/inline-empty"
import {
  matchesRoutineFilters,
  routineFilterInput,
  type RoutineFilterState,
} from "@/lib/routine-filters"
import { cn } from "@/lib/utils"
import { routineRunPresentation } from "@/lib/routine-run-presentation"
import { RoutineCalendar } from "./routine-calendar"
import { RoutinesDashboard } from "./routines-dashboard"

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

type ListTab = "routines" | "calendar"
const TABS: readonly ListTab[] = ["routines", "calendar"]

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
  const [selectedTab, setTab] = useUrlSelection("tab")
  const tab: ListTab = TABS.includes(selectedTab as ListTab) ? (selectedTab as ListTab) : "routines"
  const { bySlug } = useActiveRoutineRuns()
  const { schedules } = usePipelineSchedules(props.workspaceId)
  const { runs: dashboardRuns, loading: dashboardRunsLoading } = usePipelineRuns(
    props.workspaceId,
    "all",
    200,
  )
  const filters = props.filters
  const search = props.search ?? ""

  const visible = props.routines as ListRoutine[]
  const displayed = visible.filter(
    (routine) => !filters || matchesRoutineFilters(routineFilterInput(routine), filters, bySlug, search),
  )

  // What needs me, first: the newest run parked on a decision for one of
  // these routines, the newest routine that could not finish, and the next
  // planned start. Each tile opens the concrete run or plan, not a list.
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
            {view === "routines" ? "Overview" : view[0].toUpperCase() + view.slice(1)}
          </button>
        ))}
        {/* Runs are not a third tab: Activity already lists every routine run
            with its steps and files, so the tab was a second, poorer copy. */}
        <Link
          href="/activity?lens=routines"
          className="ml-auto inline-flex items-center gap-1 border-b-2 border-transparent px-1 py-3 text-xs text-muted-foreground hover:text-foreground"
        >
          Runs in Activity <ArrowUpRight className="h-3 w-3" />
        </Link>
      </nav>
      <div className="min-h-0 flex-1 overflow-auto">
        {tab === "routines" && (
          <section aria-label="Routine list" className="mx-auto max-w-[1160px] p-4 md:p-6">
            {props.error && (
              <p role="alert" className="mb-3 text-sm text-destructive">
                Routines could not be loaded.
              </p>
            )}

            {/* The catalog lives in the sidebar; this pane is the week at a
                glance. An empty workspace still needs the way in. */}
            {!visible.length && !props.loading && !props.error ? (
              <div className="rounded-xl border border-border/60 bg-card p-3">
                <InlineEmpty
                  icon={Workflow}
                  text="No routines yet. Create one with New routine, or import a bundle."
                />
              </div>
            ) : (
              <RoutinesDashboard
                routines={displayed}
                runs={dashboardRuns}
                runsLoading={dashboardRunsLoading}
                schedules={schedules}
                onSelect={props.onSelect}
              />
            )}
          </section>
        )}
        {tab === "calendar" && (
          <div className="p-4 md:p-6">
            <RoutineCalendar workspaceId={props.workspaceId} routines={props.routines} />
          </div>
        )}
      </div>
    </div>
  )
}
