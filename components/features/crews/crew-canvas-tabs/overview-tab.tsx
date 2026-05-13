"use client"

import { Files, RotateCcw } from "lucide-react"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { cn } from "@/lib/utils"

import { HealthCard, QuickAction } from "../crew-canvas-cards"
import type { AgentSummary, IssuesSnapshot, MissionData } from "./types"

export interface OverviewTabProps {
  workspaceId: string
  crewId: string
  agentsForCrew: AgentSummary[]
  missions: MissionData[]
  issues: IssuesSnapshot | null
  health: {
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
  issues,
  health,
  activityFilter,
  setActivityFilter,
  onOpenFiles,
  applyAvatarStyle,
}: OverviewTabProps) {
  return (
    <div className="space-y-7">
      {/* Health 3-card strip — derived stats, no extra fetches */}
      <section className="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <HealthCard
          label="Agents"
          value={`${agentsForCrew.length}`}
          hint={
            health.errored > 0
              ? `${health.errored} error${health.errored === 1 ? "" : "s"} · ${health.running} running`
              : `${health.running} running · ${agentsForCrew.length - health.running} idle`
          }
          tone={health.errored > 0 ? "danger" : health.running > 0 ? "active" : "neutral"}
        />
        <HealthCard
          label="Open issues"
          value={health.openIssues !== null ? String(health.openIssues) : "–"}
          hint={
            issues
              ? `${issues.InProgress} in progress · ${issues.InReview} in review`
              : "loading…"
          }
          tone={(health.openIssues ?? 0) > 0 ? "active" : "neutral"}
          href="/issues"
        />
        <HealthCard
          label="Missions"
          value={`${health.activeMissions}`}
          hint={
            health.activeMissions > 0
              ? "active missions running"
              : "no active missions"
          }
          tone={health.activeMissions > 0 ? "active" : "neutral"}
        />
      </section>

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
                  ? "border-blue-500/45 bg-blue-500/15 text-blue-300"
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
                    ? "border-blue-500/45 bg-blue-500/15 text-blue-300"
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
            workspaceId={workspaceId}
            crewId={activityFilter === "all" ? crewId : undefined}
            agentId={activityFilter === "all" ? undefined : activityFilter}
          />
        </div>
      </section>

      {/* Quick actions */}
      <section className="grid grid-cols-2 lg:grid-cols-4 gap-2">
        <QuickAction
          icon={<Files className="h-3.5 w-3.5" />}
          label="Open Files"
          onClick={onOpenFiles}
        />
        <QuickAction
          icon={<RotateCcw className="h-3.5 w-3.5" />}
          label="Apply avatar style"
          onClick={() => applyAvatarStyle(false)}
          disabled={agentsForCrew.length === 0}
        />
        <QuickAction
          icon={<RotateCcw className="h-3.5 w-3.5" />}
          label="Reset avatar overrides"
          onClick={() => applyAvatarStyle(true)}
          disabled={agentsForCrew.length === 0}
        />
      </section>
    </div>
  )
}
