"use client"

import { formatRoutineTime } from "@/lib/routine-time"

import { useUrlSelection } from "@/hooks/use-issue-detail"
import { RoutineCalendar } from "./routine-calendar"
import Link from "next/link"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutinesOverview } from "./routines-overview"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { useActiveRoutineRuns } from "@/hooks/use-active-routine-runs"
import {
  matchesRoutineFilters,
  routineFilterInput,
  type RoutineFilterState,
} from "@/lib/routine-filters"
import { cn } from "@/lib/utils"

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

interface RoutinesWorkspaceProps {
  workspaceId: string
  routines: Pipeline[]
  loading: boolean
  error: string | null
  onSelect: (slug: string) => void
  onFilter?: (status: string) => void
  search?: string
  filters?: RoutineFilterState
}

export function RoutinesWorkspace(props: RoutinesWorkspaceProps) {
  const [selectedTab, setTab] = useUrlSelection("tab")
  const tab =
    selectedTab === "overview"
      ? "health"
      : ["health", "calendar", "recent runs"].includes(selectedTab ?? "")
        ? selectedTab
        : "routines"
  const { bySlug } = useActiveRoutineRuns()
  const displayed = props.routines.filter(
    (routine) =>
      !props.filters ||
      matchesRoutineFilters(routineFilterInput(routine), props.filters, bySlug, props.search),
  )
  return (
    <div className="flex h-full min-w-0 flex-col">
      <nav
        aria-label="Routines views"
        className="flex shrink-0 gap-4 border-b border-border px-6"
      >
        {["routines", "health", "calendar", "recent runs"].map((view) => (
          <button
            key={view}
            type="button"
            aria-pressed={tab === view}
            onClick={() => setTab(view)}
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
          <section aria-label="Routine list" className="p-4 md:p-6">
            <h1 className="mb-4 text-lg font-medium">Routines</h1>
            {props.error && (
              <p role="alert" className="mb-3 text-sm text-destructive">
                Routines could not be loaded.
              </p>
            )}
            <div className="divide-y divide-border rounded-xl border border-border">
              {displayed.map((routine) => (
                <button
                  type="button"
                  key={routine.id}
                  onClick={() => props.onSelect(routine.slug)}
                  className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-muted/30"
                >
                  <CrewIcon
                    icon={resolveRoutineIcon(routine)}
                    color={resolveRoutineColor(routine)}
                    size="sm"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block text-sm font-medium">{routine.name}</span>
                    <span className="mt-1 block text-sm text-muted-foreground">
                      {routine.description}
                    </span>
                  </span>
                  {routine.step_count != null && (
                    <span className="shrink-0 text-xs text-muted-foreground">
                      {routine.step_count} {routine.step_count === 1 ? "step" : "steps"}
                    </span>
                  )}
                </button>
              ))}
              {!displayed.length && !props.error && (
                <p className="p-4 text-sm text-muted-foreground">
                  {props.loading ? "Loading routines…" : "No routines match these filters."}
                </p>
              )}
            </div>
          </section>
        )}
        {tab === "health" && (
          <RoutinesOverview
            {...props}
            onFilter={(status) => {
              props.onFilter?.(status)
              setTab("routines")
            }}
          />
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
    <section className="p-4 md:p-6">
      <h1 className="text-lg font-medium">Recent runs</h1>
      <p className="mb-4 text-xs text-muted-foreground">
        Latest {visibleRuns.length} loaded runs
      </p>
      {error && <p role="alert">Run history could not be loaded.</p>}
      <div className="overflow-hidden rounded-3xl border border-white/[0.06] bg-card">
        {visibleRuns.map((run) => {
          const routine = routines.find((r) => r.slug === run.pipeline_slug)
          return (
            <Link
              key={run.id}
              href={routineRunHref(run.pipeline_slug, run.id)}
              className="flex flex-wrap items-center gap-3 border-b border-white/[0.04] px-4 py-3 text-xs last:border-0 hover:bg-muted/30"
            >
              <CrewIcon
                icon={resolveRoutineIcon(routine ?? { slug: run.pipeline_slug })}
                color={resolveRoutineColor(routine ?? { slug: run.pipeline_slug })}
                size="sm"
              />
              <span className="min-w-0 flex-1">{run.pipeline_name || run.pipeline_slug}</span>
              <span className="text-muted-foreground">
                {formatRoutineTime(run.started_at)}
              </span>
              <span>{routineRunLabel(run)}</span>
            </Link>
          )
        })}
        {!visibleRuns.length && (
          <p className="p-6 text-sm text-muted-foreground">
            {loading ? "Loading runs…" : error ? "History unavailable." : "No runs yet."}
          </p>
        )}
      </div>
    </section>
  )
}
