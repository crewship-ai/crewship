"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { usePipelineRuns } from "@/hooks/use-pipeline-runs"
import type { Pipeline } from "@/hooks/use-pipelines"
import { apiFetch } from "@/lib/api-fetch"
import { RoutinesOverview } from "./routines-overview"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { cn } from "@/lib/utils"

export const routineRunHref = (slug: string, id: string) => `/routines?${new URLSearchParams({ slug, run: id })}`
export function routineRunLabel(run: { status?: string; outcome?: string }) {
  return run.outcome === "FAILED" ? "Result failed" : run.outcome === "NEEDS_HUMAN" ? "Needs your attention" : run.status === "waiting" ? "Waiting for a decision" : run.status || "Recorded"
}

export function RoutinesWorkspace(props: { workspaceId: string; routines: Pipeline[]; loading: boolean; error: string | null; onSelect: (slug: string) => void; onFilter?: (status: string) => void }) {
  const [tab, setTab] = useState("overview")
  return <div className="flex h-full min-w-0 flex-col">
    <nav aria-label="Routines views" className="flex shrink-0 gap-4 border-b border-border px-6">
      {["overview", "calendar", "recent runs"].map(view => <button key={view} type="button" aria-pressed={tab === view} onClick={() => setTab(view)} className={cn("border-b-2 px-1 py-3 text-xs transition-colors", tab === view ? "border-primary text-primary" : "border-transparent text-muted-foreground hover:text-foreground")}>{view[0].toUpperCase() + view.slice(1)}</button>)}
    </nav>
    <div className="min-h-0 flex-1 overflow-auto">
      {tab === "overview" && <RoutinesOverview {...props} />}
      {tab === "calendar" && <div className="p-4 md:p-6"><RoutineCalendar workspaceId={props.workspaceId} /></div>}
      {tab === "recent runs" && <RecentRoutineRuns workspaceId={props.workspaceId} routines={props.routines} />}
    </div>
  </div>
}

function RecentRoutineRuns({ workspaceId, routines }: { workspaceId: string; routines: Pipeline[] }) {
  const { runs, loading, error } = usePipelineRuns(workspaceId, "all", 200)
  return <section className="p-4 md:p-6">
    <h1 className="text-lg font-medium">Recent runs</h1>
    <p className="mb-4 text-xs text-muted-foreground">Latest {runs.length} loaded runs</p>
    {error && <p role="alert">Run history could not be loaded.</p>}
    <div className="overflow-hidden rounded-3xl border border-white/[0.06] bg-card">
      {runs.map(run => {
        const routine = routines.find(r => r.slug === run.pipeline_slug)
        return <Link key={run.id} href={routineRunHref(run.pipeline_slug, run.id)} className="flex flex-wrap items-center gap-3 border-b border-white/[0.04] px-4 py-3 text-xs last:border-0 hover:bg-muted/30">
          <CrewIcon icon={resolveRoutineIcon(routine ?? { slug: run.pipeline_slug })} color={resolveRoutineColor(routine ?? { slug: run.pipeline_slug })} size="sm" />
          <span className="min-w-0 flex-1">{run.pipeline_name || run.pipeline_slug}</span>
          <span className="text-muted-foreground">{new Date(run.started_at).toLocaleString()}</span>
          <span>{routineRunLabel(run)}</span>
        </Link>
      })}
      {!runs.length && <p className="p-6 text-sm text-muted-foreground">{loading ? "Loading runs…" : error ? "History unavailable." : "No runs yet."}</p>}
    </div>
  </section>
}
interface CalendarEvent { id: string; kind: "planned" | "pending" | "run"; at: string; slug: string; name: string; status?: string; outcome?: string }
function RoutineCalendar({ workspaceId }: { workspaceId: string }) {
  const [month, setMonth] = useState(() => new Date(new Date().getFullYear(), new Date().getMonth(), 1))
  const [events, setEvents] = useState<CalendarEvent[]>([])
  const [error, setError] = useState(false)
  const [truncated, setTruncated] = useState(false)
  const [loading, setLoading] = useState(true)
  useEffect(() => {
    const controller = new AbortController()
    setLoading(true); setEvents([])
    const to = new Date(month.getFullYear(), month.getMonth() + 1, 1)
    void (async () => {
      try {
        const qs = new URLSearchParams({ from: month.toISOString(), to: to.toISOString() })
        const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/calendar?${qs}`, { signal: controller.signal })
        if (!res.ok) throw new Error("calendar")
        const data = await res.json()
        if (!controller.signal.aborted) { setEvents(data.events); setTruncated(data.truncated); setError(false) }
      } catch { if (!controller.signal.aborted) setError(true) }
      finally { if (!controller.signal.aborted) setLoading(false) }
    })()
    return () => controller.abort()
  }, [workspaceId, month])
  const days = new Date(month.getFullYear(), month.getMonth() + 1, 0).getDate()
  return <section className="space-y-4"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-medium">{month.toLocaleDateString(undefined, { month: "long", year: "numeric" })}</h2><div className="flex gap-2"><Button size="sm" variant="outline" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}>Previous</Button><Button size="sm" variant="outline" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}>Next</Button></div></div><p className="text-xs text-muted-foreground">{Intl.DateTimeFormat().resolvedOptions().timeZone} · Future entries are planned starts. Past entries show actual runs.</p>{loading && <p role="status">Loading calendar…</p>}{error && <p role="alert">Calendar could not be loaded.</p>}{truncated && <p className="text-sm text-muted-foreground">High frequency schedules or a large history: only a limited set of occurrences is shown.</p>}<div className="grid grid-cols-1 gap-2 sm:grid-cols-7">{Array.from({ length: (month.getDay() + 6) % 7 }, (_, i) => <div key={`blank-${i}`} className="hidden sm:block" />)}{Array.from({ length: days }, (_, i) => i + 1).map(day => { const list = events.filter(e => new Date(e.at).getDate() === day).sort((a, b) => Date.parse(a.at) - Date.parse(b.at)); return <div key={day} className="min-w-0 rounded-lg border p-2 sm:min-h-28"><p className="mb-2 text-xs text-muted-foreground">{day} · {new Date(month.getFullYear(), month.getMonth(), day).toLocaleDateString(undefined, { weekday: "short" })}</p><div className="max-h-40 overflow-y-auto">{list.map(e => <Link key={e.id} href={e.kind === "run" ? routineRunHref(e.slug, e.id) : `/routines?slug=${encodeURIComponent(e.slug)}`} className="mb-1 block rounded bg-muted/60 p-1.5 text-[11px]"><span className="block truncate">{new Date(e.at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })} · {e.name}</span><span className="text-muted-foreground">{e.kind === "run" ? routineRunLabel(e) : e.kind === "pending" ? "Scheduled once" : "Planned"}</span></Link>)}</div></div> })}</div></section>
}
