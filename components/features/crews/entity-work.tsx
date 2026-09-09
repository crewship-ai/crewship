"use client"
import { useEffect, useState } from "react"
import Link from "next/link"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { ArrowUpRight, RefreshCw } from "lucide-react"
import { CONCEPT_ICON } from "@/lib/concept-icons"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { WorkspaceEmpty, WorkspaceGlyph } from "./workspace-visuals"
import { CrewRoutines } from "./assigned-connected"
import { withReturnTo } from "@/lib/return-to"

interface WorkRow { id: string; identifier?: string | null; title: string; status: string }
interface WorkPage { rows: WorkRow[]; total: number | null; counts: Record<string, number> | null }
export function EntityWork({ workspaceId, crewId, agentId, slug, name }: { workspaceId: string; crewId?: string; agentId?: string; slug: string; name: string }) {
  const [revision, setRevision] = useState(0)
  return <div className="space-y-5">
    <div className="flex items-center justify-between gap-3">
      <div><h2 className="text-lg font-medium">Work</h2><p className="text-sm text-muted-foreground">Track issues, missions and shared routines for {name}.</p></div>
      <Button size="sm" variant="outline" onClick={() => setRevision((n) => n + 1)}><RefreshCw aria-hidden="true" />Refresh</Button>
    </div>
    <div className="grid gap-4 xl:grid-cols-2">
      <WorkList key={`issue:${workspaceId}:${agentId || crewId}`} type="issue" workspaceId={workspaceId} agentId={agentId} crewId={crewId} slug={slug} name={name} revision={revision} />
      <WorkList key={`mission:${workspaceId}:${agentId || crewId}`} type="mission" workspaceId={workspaceId} agentId={agentId} crewId={crewId} slug={slug} name={name} revision={revision} />
    </div>
    {crewId ? <CrewRoutines key={`${workspaceId}:${crewId}`} workspaceId={workspaceId} crewId={crewId} revision={revision} title="Routines" /> : <DashboardCard title="Routines" icon={CONCEPT_ICON.routines} action={<Link className="inline-flex items-center gap-1 text-primary" href="/routines">Open Routines<ArrowUpRight className="h-3 w-3" aria-hidden="true" /></Link>}><WorkspaceEmpty icon={CONCEPT_ICON.routines} title="No crew assigned" description="Join a crew to see its shared routines. Browse all workflows in Routines." /></DashboardCard>}
  </div>
}
function WorkList({ workspaceId, crewId, agentId, slug, name, type, revision }: { workspaceId: string; crewId?: string; agentId?: string; slug: string; name: string; type: string; revision: number }) {
  const query = new URLSearchParams({ workspace_id: workspaceId, mission_type: type, limit: "5", counts: "1", sort: "updated_at" })
  if (agentId) query.set("assignee_id", agentId)
  if (crewId && !agentId) query.set("crew_id", crewId)
  const url = `/api/v1/issues?${query}`
  const [data, setData] = useState<WorkPage | null>(null)
  const [error, setError] = useState(false)
  useEffect(() => {
    const controller = new AbortController(); setData(null); setError(false)
    void apiFetch(url, { signal: controller.signal }).then(async (response) => {
      if (!response.ok) throw new Error("Work unavailable")
      const rows: WorkRow[] = await response.json()
      if (!Array.isArray(rows)) throw new Error("Invalid work data")
      const count = response.headers.get('X-Total-Count'); const counts = response.headers.get('X-Status-Counts')
      if (!controller.signal.aborted) setData({ rows, total: count === null ? null : Number(count), counts: counts ? JSON.parse(counts) : null })
    }).catch(() => { if (!controller.signal.aborted) setError(true) })
    return () => controller.abort()
  }, [url, revision])
  const href = agentId ? `/issues?assignee_id=${encodeURIComponent(agentId)}` : `/issues?crew_id=${encodeURIComponent(crewId ?? "")}`
  const label = type === "issue" ? "Issues" : "Missions"
  const Icon = type === "issue" ? CONCEPT_ICON.issues : CONCEPT_ICON.missions
  return <DashboardCard title={<>{label}{data?.total != null && <span className="ml-2 rounded-md bg-muted px-1.5 py-0.5 tabular-nums text-muted-foreground">{data.total}</span>}</>} icon={Icon} action={<Link aria-label={`View all ${label.toLowerCase()}`} className="inline-flex items-center gap-1 text-primary" href={`${href}&mission_type=${type}`}>View all<ArrowUpRight className="h-3 w-3" aria-hidden="true" /></Link>}>
    {data?.counts && <div className="mb-3 flex flex-wrap gap-2">{Object.entries(data.counts).map(([status, count]) => <span key={status} className="rounded-md bg-muted/60 px-2 py-1 text-xs text-muted-foreground"><span className="font-medium tabular-nums text-foreground">{count}</span> {status.toLowerCase().replaceAll('_', ' ')}</span>)}</div>}
    {error ? <p role="alert" className="mt-4 text-sm text-destructive">Work could not be loaded. Use Refresh to try again.</p> : !data ? <p role="status" className="mt-4 text-sm text-muted-foreground">Loading work…</p> : !data.rows.length ? <WorkspaceEmpty icon={Icon} title={`No ${label.toLowerCase()} assigned here yet.`} /> : <>
      <p className="text-xs text-muted-foreground">Recently updated · up to 5</p>
      <ul className="mt-1 divide-y divide-border/60">{data.rows.map((row) => <li key={row.id}><Link className="group flex items-start gap-3 rounded-lg px-1 py-3 hover:bg-muted/40" href={withReturnTo(`/issues/${encodeURIComponent(row.identifier || row.id)}`, `/crews?${agentId ? 'agent' : 'crew'}=${encodeURIComponent(slug)}`, name)}>
        <WorkspaceGlyph icon={Icon} tone={type === "issue" ? "blue" : "purple"} />
        <div className="min-w-0 flex-1"><p className="text-sm group-hover:text-primary">{row.identifier && <span className="mr-2 font-mono text-xs text-muted-foreground">{row.identifier}</span>}{row.title}</p><p className="mt-1 text-xs text-muted-foreground">{row.status.toLowerCase().replaceAll('_', ' ')}</p></div>
        <ArrowUpRight aria-hidden="true" className="mt-1 h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      </Link></li>)}</ul>
    </>}
  </DashboardCard>
}
