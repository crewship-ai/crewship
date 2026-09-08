"use client"
import { useEffect, useState } from "react"
import Link from "next/link"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { withReturnTo } from "@/lib/return-to"

interface WorkRow { id: string; identifier?: string | null; title: string; status: string }
interface WorkPage { rows: WorkRow[]; total: number | null; counts: Record<string, number> | null }
export function EntityWork({ workspaceId, crewId, agentId, slug, name }: { workspaceId: string; crewId?: string; agentId?: string; slug: string; name: string }) {
  const [revision, setRevision] = useState(0)
  return <div className="space-y-5"><div className="flex items-center justify-between gap-3"><div><h2 className="text-lg font-medium">Work</h2><p className="text-sm text-muted-foreground">Issues, missions and recurring work linked to {name}.</p></div><Button size="sm" variant="outline" onClick={() => setRevision((n) => n + 1)}>Refresh</Button></div><div className="grid gap-4 xl:grid-cols-2"><WorkList key={`issue:${workspaceId}:${agentId || crewId}`} type="issue" workspaceId={workspaceId} agentId={agentId} crewId={crewId} slug={slug} name={name} revision={revision} /><WorkList key={`mission:${workspaceId}:${agentId || crewId}`} type="mission" workspaceId={workspaceId} agentId={agentId} crewId={crewId} slug={slug} name={name} revision={revision} /></div><div className="rounded-xl border border-border p-4"><h3 className="text-sm font-medium">Automations</h3><p className="text-sm text-muted-foreground mt-1">Manage schedules, triggers and recurring workflows in Routines.</p><Button asChild variant="ghost" size="sm" className="mt-2"><Link href="/routines">Open Routines ↗</Link></Button></div></div>
}
function WorkList({ workspaceId, crewId, agentId, slug, name, type, revision }: { workspaceId: string; crewId?: string; agentId?: string; slug: string; name: string; type: string; revision: number }) {
  const query = new URLSearchParams({ workspace_id: workspaceId, mission_type: type, limit: "5", counts: "1", sort: "updated_at" })
  if (agentId) query.set("assignee_id", agentId)
  if (crewId) query.set("crew_id", crewId)
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
  return <section className="rounded-2xl border border-border bg-card p-5"><div className="flex items-center justify-between gap-3"><h3 className="font-medium">{type === 'issue' ? 'Issues' : 'Missions'}{data?.total != null && <span className="text-sm text-muted-foreground ml-2">{data.total}</span>}</h3><Link className="text-sm text-primary" href={`${href}&mission_type=${type}`}>View all ↗</Link></div>{data?.counts && <p className="mt-3 text-xs text-muted-foreground">{Object.entries(data.counts).map(([status, count]) => `${count} ${status.toLowerCase().replaceAll('_', ' ')}`).join(' · ')}</p>}{error ? <p role="alert" className="mt-4 text-sm text-destructive">Work could not be loaded. Use Refresh to try again.</p> : !data ? <p role="status" className="mt-4 text-sm text-muted-foreground">Loading work…</p> : !data.rows.length ? <p className="mt-4 text-sm text-muted-foreground">No {type === 'issue' ? 'issues' : 'missions'} assigned here yet.</p> : <><p className="mt-4 text-xs text-muted-foreground">Recently updated</p><ul className="divide-y divide-border">{data.rows.map((row) => <li key={row.id} className="py-3"><Link className="text-sm" href={withReturnTo(`/issues/${encodeURIComponent(row.identifier || row.id)}`, `/crews?${agentId ? 'agent' : 'crew'}=${encodeURIComponent(slug)}`, name)}>{row.identifier && `${row.identifier} `}{row.title}</Link><p className="mt-1 text-xs text-muted-foreground">{row.status.toLowerCase().replaceAll('_', ' ')}</p></li>)}</ul></>}</section>
}
