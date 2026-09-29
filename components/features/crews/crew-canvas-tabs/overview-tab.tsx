"use client"

import Link from "next/link"
import { Users, Activity } from "lucide-react"
import { HarborAgentCard } from "../agent-ring-avatar"
import { InlineEmpty } from "@/components/ui/inline-empty"
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
  /** Palette id or hex; rings the agent portraits. */
  crewColor?: string | null
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
  crewColor,
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
      <DashboardCard title="Team" icon={Users} action={<button onClick={onOpenTeam} className="text-primary-hover hover:underline">View team →</button>}>
        {teamError ? <p role="alert" className="mt-3 text-sm text-muted-foreground">Team could not be loaded. Open Team to retry.</p> : teamLoading && !agentsForCrew.length ? <p role="status" className="mt-3 text-sm text-muted-foreground">Loading team…</p> : !agentsForCrew.length ? <InlineEmpty icon={Users} className="mt-3" text="No agents yet. Add one with + Agent to start working together." /> : <ul className="mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">{agentsForCrew.slice(0, 6).map((agent) => <li key={agent.id}><HarborAgentCard agent={agent} crewName={crewName} crewColor={crewColor} avatarStyle={avatarStyle} href={`/crews?agent=${encodeURIComponent(agent.slug)}`} onSelect={onSelectAgent ? () => onSelectAgent(agent.slug) : undefined} /></li>)}</ul>}
      </DashboardCard>
      <RunMetrics workspaceId={workspaceId} crewId={crewId} />

      {/* Activity with per-agent filter chips */}
      <section className="space-y-3">
        <div className="flex items-baseline justify-between flex-wrap gap-2">
          <h2 className="eyebrow flex items-center gap-2"><Activity className="h-3.5 w-3.5" />Recent activity</h2>
          <div className="flex items-center gap-1.5 text-xs flex-wrap">
            <button
              type="button"
              onClick={() => setActivityFilter("all")}
              aria-pressed={activityFilter === "all"}
              className={cn(
                "px-2 py-0.5 rounded-full border transition-colors",
                activityFilter === "all"
                  ? "border-primary/45 bg-primary/15 text-primary-hover"
                  : "border-border text-muted-foreground hover:text-foreground",
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
                  "px-2 py-0.5 rounded-full border transition-colors",
                  activityFilter === a.id
                    ? "border-primary/45 bg-primary/15 text-primary-hover"
                    : "border-border text-muted-foreground hover:text-foreground",
                )}
              >
                {a.name}
              </button>
            ))}
          </div>
        </div>
        <div className="rounded-card border border-border bg-card max-h-[420px] overflow-hidden">
          <CrewActivityFeed
            limit={5}
            workspaceId={workspaceId}
            crewId={activityFilter === "all" ? crewId : undefined}
            agentId={activityFilter === "all" ? undefined : activityFilter}
          />
        </div>
        <Link className="text-sm text-primary-hover hover:underline" href={`/journal?crew_id=${encodeURIComponent(crewId)}`}>View all activity ↗</Link>
      </section>
      <AssignedConnected workspaceId={workspaceId} crewId={crewId} slug={crewSlug} name={crewName} />
    </div>
  )
}
