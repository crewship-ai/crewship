"use client"

import Link from "next/link"
import { RunMetrics } from "../workspace-overview"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { cn } from "@/lib/utils"

import type { AgentSummary, IssuesSnapshot, MissionData } from "./types"

export interface OverviewTabProps {
  workspaceId: string
  crewId: string
  agentsForCrew: AgentSummary[]
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
  agentsForCrew,
  onOpenTeam,
  teamLoading,
  teamError,
  activityFilter,
  setActivityFilter,
}: OverviewTabProps) {
  return (
    <div className="space-y-7">
      <section className="rounded-2xl border border-border bg-card p-5"><div className="flex justify-between gap-3"><h2 className="font-medium">Team</h2><button onClick={onOpenTeam} className="text-sm text-primary">View team →</button></div>
        {teamError ? <p role="alert" className="mt-3 text-sm text-muted-foreground">Team could not be loaded. Open Team to retry.</p> : teamLoading && !agentsForCrew.length ? <p role="status" className="mt-3 text-sm text-muted-foreground">Loading team…</p> : !agentsForCrew.length ? <p className="mt-3 text-sm text-muted-foreground">Add an agent to start working together.</p> : <ul className="mt-3 grid gap-2 sm:grid-cols-2 xl:grid-cols-3">{agentsForCrew.slice(0, 6).map((agent) => <li key={agent.id}><Link href={`/crews?agent=${encodeURIComponent(agent.slug)}`} className="block rounded-xl border border-border p-3 hover:bg-muted"><p className="text-sm font-medium">{agent.name}</p><p className="text-xs text-muted-foreground mt-1">{agent.role_title || (agent.agent_role === "LEAD" ? "Lead" : "Agent")} · {agent.status.toLowerCase()}</p></Link></li>)}</ul>}
      </section>
      <RunMetrics workspaceId={workspaceId} crewId={crewId} />

      {/* Activity with per-agent filter chips */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between flex-wrap gap-2">
          <h2 className="text-lg font-semibold">Recent activity</h2>
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


    </div>
  )
}
