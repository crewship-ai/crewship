"use client"

import Link from "next/link"
import { Users, Activity } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { AssignedConnected } from "../assigned-connected"
import { RunMetrics } from "../workspace-overview"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { cn } from "@/lib/utils"

import type { AgentSummary, IssuesSnapshot, MissionData } from "./types"

export interface OverviewTabProps {
  workspaceId: string
  crewId: string
  crewSlug?: string
  crewName?: string
  avatarStyle?: string | null
  agentsForCrew: AgentSummary[]
  onSelectAgent?: (slug: string) => void
  onOpenTeam?: () => void
  teamLoading?: boolean
  teamError?: string | null
  missions?: MissionData[]
  issues?: IssuesSnapshot | null
  health?: {
    running: number
    errored: number
    openIssues: number | null
    activeMissions: number
  }
  activityFilter: "all" | string
  setActivityFilter: (filter: "all" | string) => void
  onOpenFiles: () => void
  applyAvatarStyle: (resetOverrides: boolean) => void
}

export function OverviewTab({
  workspaceId,
  crewId,
  crewSlug = "",
  crewName = "Crew",
  avatarStyle,
  agentsForCrew,
  onOpenTeam,
  onSelectAgent,
  teamLoading,
  teamError,
  activityFilter,
  setActivityFilter,
}: OverviewTabProps) {
  return (
    <div className="space-y-7">
      <DashboardCard title="Team" icon={Users} action={<button onClick={onOpenTeam} className="text-primary">View team →</button>}>
        {teamError ? <p role="alert" className="mt-3 text-sm text-muted-foreground">Team could not be loaded. Open Team to retry.</p> : teamLoading && !agentsForCrew.length ? <p role="status" className="mt-3 text-sm text-muted-foreground">Loading team…</p> : !agentsForCrew.length ? <p className="mt-3 text-sm text-muted-foreground">Add an agent to start working together.</p> : <ul className="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-3">{agentsForCrew.slice(0, 6).map((agent) => <li key={agent.id}><Link href={`/crews?agent=${encodeURIComponent(agent.slug)}`} onClick={event => { if (onSelectAgent && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && event.button === 0) { event.preventDefault(); onSelectAgent(agent.slug) } }} className="flex items-center gap-3 rounded-xl border border-border/60 p-3 hover:bg-muted transition-colors"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style || avatarStyle} avatarUrl={agent.avatar_url} className="h-10 w-10" /><div className="min-w-0"><p className="text-sm font-medium">{agent.name}</p><p className="text-xs text-muted-foreground mt-1">{agent.role_title || (agent.agent_role === "LEAD" ? "Lead" : "Agent")} · {agent.status.toLowerCase()}</p></div></Link></li>)}</ul>}
      </DashboardCard>
      <RunMetrics workspaceId={workspaceId} crewId={crewId} />

      {/* Activity with per-agent filter chips */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between flex-wrap gap-2">
          <h2 className="text-lg font-semibold flex items-center gap-2"><Activity className="h-4 w-4 text-muted-foreground" />Recent activity</h2>
          <div className="flex items-center gap-1.5 text-xs flex-wrap">
            <button
              type="button"
              onClick={() => setActivityFilter("all")}
              aria-pressed={activityFilter === "all"}
              className={cn(
                "px-2 py-0.5 rounded border transition-colors",
                activityFilter === "all"
                  ? "border-primary/45 bg-primary/15 text-primary"
                  : "border-white/10 text-muted-foreground hover:text-foreground/80",
              )}
            >
              All
            </button>
            {agentsForCrew.slice(0, 6).map((a) => (
              <button
                key={a.id}
                type="button"
                onClick={() => setActivityFilter(a.id)}
                aria-pressed={activityFilter === a.id}
                className={cn(
                  "px-2 py-0.5 rounded border transition-colors",
                  activityFilter === a.id
                    ? "border-primary/45 bg-primary/15 text-primary"
                    : "border-white/10 text-muted-foreground hover:text-foreground/80",
                )}
              >
                {a.name}
              </button>
            ))}
          </div>
        </div>
        <div className="rounded-xl border border-white/8 bg-card max-h-[420px] overflow-hidden">
          <CrewActivityFeed
            limit={5}
            workspaceId={workspaceId}
            crewId={activityFilter === "all" ? crewId : undefined}
            agentId={activityFilter === "all" ? undefined : activityFilter}
          />
        </div>
        <Link className="text-sm text-primary" href={`/journal?crew_id=${encodeURIComponent(crewId)}`}>View all activity ↗</Link>
      </section>
      <AssignedConnected workspaceId={workspaceId} crewId={crewId} slug={crewSlug} name={crewName} />
    </div>
  )
}
