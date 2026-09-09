"use client"

import { useUrlSelection } from "@/hooks/use-issue-detail"
import { RoutineCalendar } from "./routine-calendar"
import Link from "next/link"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutinesOverview } from "./routines-overview"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { cn } from "@/lib/utils"

export const routineRunHref = (slug: string, id: string) => `/routines?${new URLSearchParams({ slug, run: id })}`
export function routineRunLabel(run: { status?: string; outcome?: string }) {
  return run.outcome === "FAILED" ? "Result failed" : run.outcome === "NEEDS_HUMAN" ? "Needs your attention" : run.status === "waiting" ? "Waiting for a decision" : run.status || "Recorded"
}

export function RoutinesWorkspace(props: { workspaceId: string; routines: Pipeline[]; loading: boolean; error: string | null; onSelect: (slug: string) => void; onFilter?: (status: string) => void }) {
  const [selectedTab, setTab] = useUrlSelection("tab")
  const tab = selectedTab === "calendar" || selectedTab === "recent runs" ? selectedTab : "overview"
  return <div className="flex h-full min-w-0 flex-col">
    <nav aria-label="Routines views" className="flex shrink-0 gap-4 border-b border-border px-6">
      {["overview", "calendar", "recent runs"].map(view => <button key={view} type="button" aria-pressed={tab === view} onClick={() => setTab(view)} className={cn("border-b-2 px-1 py-3 text-xs transition-colors", tab === view ? "border-primary text-primary" : "border-transparent text-muted-foreground hover:text-foreground")}>{view[0].toUpperCase() + view.slice(1)}</button>)}
    </nav>
    <div className="min-h-0 flex-1 overflow-auto">
      {tab === "overview" && <RoutinesOverview {...props} />}
      {tab === "calendar" && <div className="p-4 md:p-6"><RoutineCalendar workspaceId={props.workspaceId} routines={props.routines} /></div>}
      {tab === "recent runs" && <RecentRoutineRuns workspaceId={props.workspaceId} routines={props.routines} />}
    </div>
  </div>
}

function RecentRoutineRuns({ workspaceId, routines }: { workspaceId: string; routines: Pipeline[] }) {
  const { runs, loading, error } = usePipelineRuns(workspaceId, "all", 200)
  const visibleRuns = runs.filter(run => routines.some(r => r.slug === run.pipeline_slug))
  return <section className="p-4 md:p-6">
    <h1 className="text-lg font-medium">Recent runs</h1>
    <p className="mb-4 text-xs text-muted-foreground">Latest {visibleRuns.length} loaded runs</p>
    {error && <p role="alert">Run history could not be loaded.</p>}
    <div className="overflow-hidden rounded-3xl border border-white/[0.06] bg-card">
      {visibleRuns.map(run => {
        const routine = routines.find(r => r.slug === run.pipeline_slug)
        return <Link key={run.id} href={routineRunHref(run.pipeline_slug, run.id)} className="flex flex-wrap items-center gap-3 border-b border-white/[0.04] px-4 py-3 text-xs last:border-0 hover:bg-muted/30">
          <CrewIcon icon={resolveRoutineIcon(routine ?? { slug: run.pipeline_slug })} color={resolveRoutineColor(routine ?? { slug: run.pipeline_slug })} size="sm" />
          <span className="min-w-0 flex-1">{run.pipeline_name || run.pipeline_slug}</span>
          <span className="text-muted-foreground">{new Date(run.started_at).toLocaleString("en-GB")}</span>
          <span>{routineRunLabel(run)}</span>
        </Link>
      })}
      {!visibleRuns.length && <p className="p-6 text-sm text-muted-foreground">{loading ? "Loading runs…" : error ? "History unavailable." : "No runs yet."}</p>}
    </div>
  </section>
}
