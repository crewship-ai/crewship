"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { usePipelineRuns, type PipelineRun } from "@/hooks/use-pipeline-runs"
import { usePipelineSchedules } from "@/hooks/use-pipeline-schedules"
import type { Pipeline } from "@/hooks/use-pipelines"
import { apiFetch } from "@/lib/api-fetch"
import { describeCron } from "@/lib/cron-describe"
import type { PendingWaitpoint } from "@/lib/api/waitpoints"

export const routineRunHref = (slug: string, id: string) => `/routines?${new URLSearchParams({ slug, run: id })}`
export function routineRunLabel(run: Pick<PipelineRun, "status" | "outcome">) {
  return run.outcome === "FAILED" ? "Result failed" : run.outcome === "NEEDS_HUMAN" ? "Needs your attention" : run.status === "waiting" ? "Waiting for a decision" : run.status
}
const active = (run: PipelineRun) => ["running", "queued", "waiting", "paused"].includes(run.status)
export function RoutinesWorkspace({ workspaceId, routines, loading, error, onSelect }: { workspaceId: string; routines: Pipeline[]; loading: boolean; error: string | null; onSelect: (slug: string) => void }) {
  const { runs, error: runError } = usePipelineRuns(workspaceId, "all", 200)
  const { schedules, error: scheduleError } = usePipelineSchedules(workspaceId)
  const [tab, setTab] = useState("routines")
  const [search, setSearch] = useState("")
  const [filter, setFilter] = useState("all")
  const [waitpoints, setWaitpoints] = useState<PendingWaitpoint[]>([])
  const [waitError, setWaitError] = useState(false)
  useEffect(() => {
    const controller = new AbortController()
    const refresh = async () => {
      try {
        const res = await apiFetch(`/api/v1/workspaces/${workspaceId}/pipelines/waitpoints`, { signal: controller.signal })
        if (!res.ok) throw new Error("waitpoints")
        const data = await res.json()
        if (!controller.signal.aborted) { setWaitpoints(data); setWaitError(false) }
      } catch { if (!controller.signal.aborted) setWaitError(true) }
    }
    void refresh(); const timer = setInterval(refresh, 10000)
    return () => { controller.abort(); clearInterval(timer) }
  }, [workspaceId])
  const latest = useMemo(() => {
    const map = new Map<string, PipelineRun>()
    for (const run of runs) if (!map.has(run.pipeline_slug) || Date.parse(run.started_at) > Date.parse(map.get(run.pipeline_slug)!.started_at)) map.set(run.pipeline_slug, run)
    return map
  }, [runs])
  const live = runs.filter(active)
  const attention = [...new Set([...waitpoints.map(w => w.pipeline_run_id), ...runs.filter(r => r.outcome === "NEEDS_HUMAN").map(r => r.id)])]
  const items = routines.filter(r => `${r.name} ${r.description ?? ""}`.toLowerCase().includes(search.toLowerCase()) && (filter === "all" || (filter === "disabled" ? r.status === "disabled" : filter === "running" ? live.some(run => run.pipeline_slug === r.slug) : attention.some(id => runs.find(run => run.id === id)?.pipeline_slug === r.slug))))
  return <div className="h-full overflow-auto"><div className="mx-auto max-w-[1600px] space-y-6 p-4 md:p-6">
    <header><h1 className="text-xl font-semibold">Prepared work, ready to run</h1><p className="mt-1 text-sm text-muted-foreground">Follow progress, review results and plan what runs next.</p></header>
    {(error || runError || scheduleError || waitError) && <p role="alert" className="rounded-lg border border-destructive/40 p-3 text-sm">Some information could not be loaded. {error || runError || scheduleError || "Decisions are unavailable."}</p>}
    {(attention.length > 0 || live.length > 0) && <div className="grid gap-4 md:grid-cols-2"><section className="space-y-2 rounded-xl border p-4"><h2 className="text-sm font-medium">Needs your attention · {attention.length}</h2>{attention.map(id => { const run = runs.find(r => r.id === id); const wait = waitpoints.find(w => w.pipeline_run_id === id); return <Link key={id} className="block rounded-lg bg-muted/40 p-3 text-sm" href={routineRunHref(run?.pipeline_slug ?? "", id)}>{run?.pipeline_name || "Open waiting run"}<p className="mt-1 text-xs text-muted-foreground">{wait?.prompt || "Review the result and decide what happens next."}</p></Link> })}{!attention.length && <p className="text-sm text-muted-foreground">No decisions pending.</p>}</section><section className="space-y-2 rounded-xl border p-4"><h2 className="text-sm font-medium">In progress · {live.length}</h2>{live.map(run => <Link key={run.id} className="flex justify-between gap-3 rounded-lg bg-muted/40 p-3 text-sm" href={routineRunHref(run.pipeline_slug, run.id)}><span>{run.pipeline_name}</span><span className="text-xs text-muted-foreground">{routineRunLabel(run)}</span></Link>)}</section></div>}
    <nav aria-label="Routines views" className="flex gap-2 border-b pb-2">{["routines", "calendar", "recent runs"].map(t => <Button key={t} size="sm" variant={t === tab ? "secondary" : "ghost"} onClick={() => setTab(t)} aria-pressed={t === tab}>{t[0].toUpperCase() + t.slice(1)}</Button>)}</nav>
    {tab === "routines" && <section className="space-y-4"><div className="flex flex-wrap gap-3"><Input aria-label="Search routines" className="max-w-sm" value={search} onChange={e => setSearch(e.target.value)} placeholder="Search by name or purpose…" /><select aria-label="Routine filter" className="rounded-md border bg-background px-3 text-sm" value={filter} onChange={e => setFilter(e.target.value)}><option value="all">All routines</option><option value="running">In progress</option><option value="attention">Needs attention</option><option value="disabled">Disabled</option></select></div>
    <div className="overflow-x-auto rounded-xl border"><table className="w-full text-left text-sm"><thead className="border-b text-xs text-muted-foreground"><tr><th className="p-4">Routine and purpose</th><th className="p-4">Status</th><th className="p-4">Schedule</th><th className="p-4">Latest result</th></tr></thead><tbody>{items.map(r => { const last = latest.get(r.slug); const plans = schedules.filter(s => s.target_pipeline_id === r.id); return <tr key={r.id} className="border-b last:border-0 hover:bg-muted/30"><td className="p-4"><button onClick={() => onSelect(r.slug)} className="text-left font-medium hover:text-primary">{r.name}</button><p className="mt-1 max-w-lg text-xs text-muted-foreground">{r.description || "No purpose described yet."}</p></td><td className="p-4 text-xs">{r.status === "disabled" ? "Disabled" : r.status === "proposed" ? "Awaiting approval" : live.some(run => run.pipeline_slug === r.slug) ? "In progress" : "Ready"}</td><td className="p-4 text-xs text-muted-foreground">{plans.length ? plans.map(s => <div key={s.id}>{s.enabled ? describeCron(s.cron_expr) : "Schedule paused"} · {s.timezone}</div>) : "Manual / event"}</td><td className="p-4">{last ? <Link className="text-xs hover:text-primary" href={routineRunHref(r.slug, last.id)}>{routineRunLabel(last)}<p className="mt-1 text-muted-foreground">{new Date(last.started_at).toLocaleString()}</p></Link> : <span className="text-xs text-muted-foreground">{r.invocation_count ? "Open history" : "Never run"}</span>}</td></tr> })}</tbody></table>{!items.length && <p className="p-8 text-center text-sm text-muted-foreground">{loading ? "Loading routines…" : search || filter !== "all" ? "No routines match this filter." : "Create your first routine to prepare a repeatable workflow."}</p>}</div></section>}
    {tab === "recent runs" && <section className="space-y-2"><p className="text-xs text-muted-foreground">Latest {runs.length} loaded runs</p>{runs.map(run => <Link key={run.id} href={routineRunHref(run.pipeline_slug, run.id)} className="flex flex-wrap justify-between gap-3 rounded-xl border p-4 text-sm"><span>{run.pipeline_name} · {new Date(run.started_at).toLocaleString()}</span><span>{routineRunLabel(run)}</span></Link>)}{!runs.length && <p className="text-sm text-muted-foreground">No runs yet.</p>}</section>}
    {tab === "calendar" && <RoutineCalendar workspaceId={workspaceId} />}
  </div></div>
}
interface CalendarEvent { id: string; kind: "planned" | "pending" | "run"; at: string; slug: string; name: string; status?: string }
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
  return <section className="space-y-4"><div className="flex flex-wrap items-center justify-between gap-3"><h2 className="font-medium">{month.toLocaleDateString(undefined, { month: "long", year: "numeric" })}</h2><div className="flex gap-2"><Button size="sm" variant="outline" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() - 1, 1))}>Previous</Button><Button size="sm" variant="outline" onClick={() => setMonth(new Date(month.getFullYear(), month.getMonth() + 1, 1))}>Next</Button></div></div><p className="text-xs text-muted-foreground">{Intl.DateTimeFormat().resolvedOptions().timeZone} · Future entries are planned starts. Past entries show actual runs.</p>{loading && <p role="status">Loading calendar…</p>}{error && <p role="alert">Calendar could not be loaded.</p>}{truncated && <p className="text-sm text-muted-foreground">High frequency schedules or a large history: only a limited set of occurrences is shown.</p>}<div className="grid grid-cols-1 gap-2 sm:grid-cols-7">{Array.from({ length: (month.getDay() + 6) % 7 }, (_, i) => <div key={`blank-${i}`} className="hidden sm:block" />)}{Array.from({ length: days }, (_, i) => i + 1).map(day => { const list = events.filter(e => new Date(e.at).getDate() === day).sort((a, b) => Date.parse(a.at) - Date.parse(b.at)); return <div key={day} className="min-w-0 rounded-lg border p-2 sm:min-h-28"><p className="mb-2 text-xs text-muted-foreground">{day} · {new Date(month.getFullYear(), month.getMonth(), day).toLocaleDateString(undefined, { weekday: "short" })}</p>{list.map(e => <Link key={e.id} href={e.kind === "run" ? routineRunHref(e.slug, e.id) : `/routines?slug=${encodeURIComponent(e.slug)}`} className="mb-1 block rounded bg-muted/60 p-1.5 text-[11px]"><span className="block truncate">{new Date(e.at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })} · {e.name}</span><span className="text-muted-foreground">{e.kind === "run" ? e.status : e.kind === "pending" ? "Scheduled once" : "Planned"}</span></Link>)}</div> })}</div></section>
}
