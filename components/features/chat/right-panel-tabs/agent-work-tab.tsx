"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { ArrowUpRight, CircleDot, RefreshCw, Workflow } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"

interface IssueRow { id: string; identifier?: string | null; title: string; status?: string | null }
interface RoutineRow { id: string; slug: string; name?: string | null; author_agent_id?: string | null }

export function AgentWorkTab({ agentId, workspaceId }: { agentId: string; workspaceId: string | null }) {
  const [issues, setIssues] = useState<IssueRow[]>([])
  const [routines, setRoutines] = useState<RoutineRow[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    const ws = encodeURIComponent(workspaceId)
    setIssues([])
    setRoutines([])
    setLoading(true)
    setError(false)
    void Promise.all([
      apiFetch(`/api/v1/issues?workspace_id=${ws}&assignee_id=${encodeURIComponent(agentId)}&limit=20&sort=updated_at`, { signal: controller.signal }),
      apiFetch(`/api/v1/workspaces/${ws}/pipelines?order=recent&limit=100`, { signal: controller.signal }),
    ]).then(async ([issueRes, routineRes]) => {
      if (!issueRes.ok || !routineRes.ok) throw new Error("Work unavailable")
      const [issueRows, routineRows] = await Promise.all([issueRes.json(), routineRes.json()])
      if (controller.signal.aborted) return
      setIssues(Array.isArray(issueRows) ? issueRows : [])
      setRoutines(Array.isArray(routineRows) ? routineRows.filter((row: RoutineRow) => row.author_agent_id === agentId) : [])
    }).catch(() => { if (!controller.signal.aborted) setError(true) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [agentId, workspaceId, revision])

  return <div className="space-y-4 p-3 text-xs">
    <div className="flex items-center justify-between text-[10px] uppercase tracking-wider text-muted-foreground"><span>Agent work</span><button type="button" onClick={() => setRevision((n) => n + 1)} aria-label="Refresh agent work" className="rounded p-1 hover:bg-accent"><RefreshCw className="size-3" /></button></div>
    {loading && <p role="status" className="text-muted-foreground">Loading agent work…</p>}
    {error && <p role="alert" className="text-muted-foreground">Work could not be loaded. Try refresh.</p>}
    {!loading && !error && <>
      <section><div className="mb-1.5 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground"><CircleDot className="size-3" />Assigned issues <span className="ml-auto">{issues.length}</span></div>
        {issues.length ? <ul className="space-y-1">{issues.slice(0, 8).map((issue) => <li key={issue.id}><Link href={`/issues/${encodeURIComponent(issue.identifier || issue.id)}`} className="group block rounded-md border bg-muted/20 p-2 hover:border-primary/40 hover:bg-accent"><span className="font-mono text-primary">{issue.identifier || issue.id}</span> <span className="line-clamp-2 font-medium">{issue.title}</span><span className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground">{issue.status?.toLowerCase().replaceAll("_", " ") || "Open"}<ArrowUpRight className="ml-auto size-3" /></span></Link></li>)}</ul> : <p className="rounded-md border border-dashed p-2 text-muted-foreground">No issues assigned to this agent.</p>}
        <Link href={`/issues?assignee_id=${encodeURIComponent(agentId)}`} className="mt-2 inline-block text-primary">View all issues ↗</Link>
      </section>
      <section><div className="mb-1.5 flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground"><Workflow className="size-3" />Authored routines <span className="ml-auto">{routines.length}</span></div>
        {routines.length ? <ul className="space-y-1">{routines.slice(0, 5).map((routine) => <li key={routine.id}><Link href={`/routines?routine=${encodeURIComponent(routine.slug)}`} className="flex items-center gap-1 rounded-md border bg-muted/20 p-2 hover:border-primary/40 hover:bg-accent"><span className="min-w-0 flex-1 truncate">{routine.name || routine.slug}</span><ArrowUpRight className="size-3 text-muted-foreground" /></Link></li>)}</ul> : <p className="rounded-md border border-dashed p-2 text-muted-foreground">No routines authored by this agent.</p>}
      </section>
    </>}
  </div>
}
