"use client"

import Link from "next/link"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import { CircleDot, KeyRound, Plug, Workflow } from "lucide-react"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { useWorkspaceResource } from "./memory-workspace"
import { WorkspaceEmpty, WorkspaceGlyph } from "./workspace-visuals"
import type { AgentCredRow } from "./agent-canvas-tabs/types"
import type { CrewIntegration } from "./crew-canvas-tabs/types"

interface Props { workspaceId: string; agentId?: string; crewId?: string; slug: string; name: string; revision?: number }
interface Issue { id: string; identifier?: string; title: string; status: string }
interface Routine { id: string; slug: string; name: string; status: string }

export function AssignedConnected(props: Props) {
  const { workspaceId, agentId, crewId } = props
  const [retry, setRetry] = useState(0)
  const revision = (props.revision ?? 0) + retry
  const q = new URLSearchParams({ workspace_id: workspaceId, limit: "3", sort: "updated_at", ...(agentId ? { assignee_id: agentId } : { crew_id: crewId ?? "" }) })
  const issues = useWorkspaceResource<Issue[]>(`/api/v1/issues?${q}`, revision, true)
  return <section className="space-y-3"><div className="flex items-center justify-between gap-3"><h2 className="text-lg font-medium">Assigned &amp; connected</h2><Button variant="ghost" size="sm" onClick={() => setRetry(n => n + 1)}>Refresh assignments</Button></div><div className="grid gap-4 xl:grid-cols-3">
    <DashboardCard title="Issues & missions" icon={CircleDot} action={<Link className="text-primary" href={`/issues?${agentId ? `assignee_id=${encodeURIComponent(agentId)}` : `crew_id=${encodeURIComponent(crewId ?? "")}`}`}>View all ↗</Link>}>
      {issues.error ? <p role="alert" className="text-sm text-muted-foreground">Assigned work could not be loaded.</p> : !issues.data ? <p role="status" className="text-sm text-muted-foreground">Loading work…</p> : !issues.data.length ? <WorkspaceEmpty icon={CircleDot} title="No work assigned yet" /> : <ul className="divide-y divide-border/60">{issues.data.slice(0, 3).map(issue => <li key={issue.id} className="flex gap-3 py-3"><WorkspaceGlyph icon={CircleDot} /><div className="min-w-0"><Link className="text-sm line-clamp-2 hover:text-primary" href={`/issues/${encodeURIComponent(issue.identifier || issue.id)}`}>{issue.title}</Link><p className="text-xs text-muted-foreground mt-1">{issue.status.toLowerCase().replaceAll('_', ' ')}</p></div></li>)}</ul>}
    </DashboardCard>
    {crewId ? <CrewRoutines workspaceId={workspaceId} crewId={crewId} revision={revision} /> : <DashboardCard title="Crew routines" icon={Workflow}><WorkspaceEmpty icon={Workflow} title="No crew assigned" description="Join a crew to see its shared routines." /></DashboardCard>}
    {agentId ? <AgentAccess workspaceId={workspaceId} agentId={agentId} revision={revision} /> : crewId ? <CrewAccess workspaceId={workspaceId} crewId={crewId} revision={revision} /> : null}
  </div></section>
}

function CrewRoutines({ workspaceId, crewId, revision }: { workspaceId: string; crewId: string; revision: number }) {
  const { data, error } = useWorkspaceResource<Routine[]>(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines?author_crew_id=${encodeURIComponent(crewId)}&order=recent`, revision, true)
  return <DashboardCard title="Crew routines" icon={Workflow} action={<Link className="text-primary" href="/routines">Routines ↗</Link>}>
    <p className="text-xs text-muted-foreground mb-2">Owned by this crew; execution agents depend on each routine.</p>
    {error ? <p role="alert" className="text-sm text-muted-foreground">Routines could not be loaded.</p> : !data ? <p role="status" className="text-sm text-muted-foreground">Loading routines…</p> : !data.length ? <WorkspaceEmpty icon={Workflow} title="No crew routines yet" /> : <ul className="divide-y divide-border/60">{data.slice(0, 3).map(routine => <li key={routine.id} className="flex gap-3 py-3"><WorkspaceGlyph icon={Workflow} tone="purple" /><div className="min-w-0"><Link className="text-sm line-clamp-2 hover:text-primary" href={`/routines?routine=${encodeURIComponent(routine.slug)}`}>{routine.name || routine.slug}</Link><p className="mt-1 text-xs text-muted-foreground">{routine.status?.toLowerCase().replaceAll('_', ' ')}</p></div></li>)}</ul>}
  </DashboardCard>
}
function AgentAccess({ workspaceId, agentId, revision }: { workspaceId: string; agentId: string; revision: number }) {
  const credentials = useWorkspaceResource<AgentCredRow[]>(`/api/v1/agents/${encodeURIComponent(agentId)}/credentials?workspace_id=${encodeURIComponent(workspaceId)}`, revision, true)
  const integrations = useWorkspaceResource<{ server_id: string; name: string; display_name: string; scope: string; enabled: boolean }[]>(`/api/v1/agents/${encodeURIComponent(agentId)}/integrations/resolved?workspace_id=${encodeURIComponent(workspaceId)}`, revision, true)
  return <DashboardCard title="Access" icon={KeyRound} action={<Link className="text-primary" href="/credentials">Vault ↗</Link>}>
    {credentials.error && <p role="alert" className="text-xs text-muted-foreground">Credentials could not be loaded.</p>}
    {integrations.error && <p role="alert" className="text-xs text-muted-foreground">Integrations could not be loaded.</p>}
    {!credentials.data && !credentials.error && <p role="status" className="text-sm text-muted-foreground">Loading credentials…</p>}
    {!integrations.data && !integrations.error && <p role="status" className="text-sm text-muted-foreground">Loading integrations…</p>}
    {credentials.data?.length === 0 && integrations.data?.length === 0 && <WorkspaceEmpty icon={KeyRound} title="No access connected yet" />}
    <ul className="divide-y divide-border/60">{credentials.data?.slice(0, 3).map(credential => <li key={`${credential.credential_id}:${credential.source ?? credential.id}`} className="flex gap-3 py-3"><WorkspaceGlyph icon={KeyRound} tone="green" /><div className="min-w-0"><p className="text-sm truncate">{credential.credential_name}</p><p className="mt-1 text-xs text-muted-foreground">{credential.credential_status?.toLowerCase() || "Status unavailable"}{credential.source === "crew" ? " · From crew" : credential.source === "explicit" ? " · Assigned directly" : ""}</p></div></li>)}
    {integrations.data?.slice(0, 2).map(integration => <li key={`${integration.scope}:${integration.server_id}`} className="flex gap-3 py-3"><WorkspaceGlyph icon={Plug} /><div className="min-w-0"><p className="text-sm truncate">{integration.display_name || integration.name}</p><p className="mt-1 text-xs text-muted-foreground">{integration.enabled ? "Available" : "Disabled"} · {integration.scope}</p></div></li>)}</ul>
    <Link className="inline-block text-xs text-primary mt-3" href="/integrations">Tools &amp; integrations ↗</Link>
  </DashboardCard>
}
function CrewAccess({ workspaceId, crewId, revision }: { workspaceId: string; crewId: string; revision: number }) {
  const { data, error } = useWorkspaceResource<CrewIntegration[]>(`/api/v1/crews/${encodeURIComponent(crewId)}/integrations?workspace_id=${encodeURIComponent(workspaceId)}`, revision, true)
  return <DashboardCard title="Integrations" icon={Plug} action={<Link className="text-primary" href="/integrations">View all ↗</Link>}>
    {error ? <p role="alert" className="text-sm text-muted-foreground">Integrations could not be loaded.</p> : !data ? <p role="status" className="text-sm text-muted-foreground">Loading integrations…</p> : !data.length ? <WorkspaceEmpty icon={Plug} title="No integrations connected" /> : <ul className="divide-y divide-border/60">{data.slice(0, 3).map(integration => <li key={integration.id} className="flex gap-3 py-3"><WorkspaceGlyph icon={Plug} tone="green" /><div className="min-w-0"><p className="text-sm truncate">{integration.display_name || integration.name}</p><p className="mt-1 text-xs text-muted-foreground">{!integration.enabled ? "Disabled" : integration.auth_status === "none" ? "No authentication required" : integration.auth_status}</p></div></li>)}</ul>}
    <Link className="inline-block text-xs text-primary mt-3" href="/credentials">Credentials vault ↗</Link>
  </DashboardCard>
}
