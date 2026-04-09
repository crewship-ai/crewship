"use client"

import { useMemo } from "react"
import { Bot } from "lucide-react"
import { cn } from "@/lib/utils"
import { getCrewDotColor } from "@/lib/crew-icon"

interface CrewData {
  id: string
  name: string
  color: string | null
}

interface AgentData {
  id: string
  name: string
  status: string
  crew_id: string | null
  crew?: { name: string } | null
}

interface HealthOverviewProps {
  crews: CrewData[]
  agents: AgentData[]
}

export function HealthOverview({ crews, agents }: HealthOverviewProps) {
  const statusGroups = useMemo(() => {
    const groups: Record<string, AgentData[]> = { RUNNING: [], IDLE: [], ERROR: [], STOPPED: [] }
    for (const agent of agents) {
      const key = agent.status in groups ? agent.status : "IDLE"
      groups[key].push(agent)
    }
    return groups
  }, [agents])

  const statusColors: Record<string, string> = {
    RUNNING: "text-emerald-400",
    IDLE: "text-muted-foreground",
    ERROR: "text-red-400",
    STOPPED: "text-amber-400",
  }

  return (
    <div className="space-y-4">
      <h2 className="text-lg font-semibold">Agent Health</h2>
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
        {Object.entries(statusGroups).map(([status, group]) => (
          <div
            key={status}
            className="rounded-xl border border-border/80 bg-card p-4 text-center"
          >
            <p className={cn("text-2xl font-bold tabular-nums", statusColors[status])}>{group.length}</p>
            <p className="text-[11px] text-muted-foreground mt-0.5">{status.toLowerCase()}</p>
          </div>
        ))}
      </div>

      {statusGroups.ERROR.length > 0 && (
        <div>
          <h3 className="text-[13px] font-semibold text-red-400 mb-2">Agents with Errors</h3>
          <div className="space-y-1.5">
            {statusGroups.ERROR.map((agent) => (
              <div
                key={agent.id}
                className="flex items-center gap-3 px-3 py-2 rounded-md bg-red-500/5 border border-red-500/10"
              >
                <Bot className="h-4 w-4 text-red-400 shrink-0" />
                <span className="text-[12px] font-medium flex-1">{agent.name}</span>
                <span className="text-[11px] text-muted-foreground">{agent.crew?.name || "Unassigned"}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Per-crew health */}
      <div>
        <h3 className="text-[13px] font-semibold mb-2">By Crew</h3>
        <div className="space-y-1.5">
          {crews.map((crew) => {
            const crewAgents = agents.filter((a) => a.crew_id === crew.id)
            const running = crewAgents.filter((a) => a.status === "RUNNING").length
            const error = crewAgents.filter((a) => a.status === "ERROR").length
            const total = crewAgents.length
            const healthPct = total > 0 ? Math.round(((total - error) / total) * 100) : 100

            return (
              <div key={crew.id} className="flex items-center gap-3 px-3 py-2 rounded-md hover:bg-white/[0.04]">
                <div
                  className="h-3 w-3 rounded-sm shrink-0"
                  style={{ backgroundColor: getCrewDotColor(crew.color) }}
                />
                <span className="text-[12px] font-medium flex-1">{crew.name}</span>
                <span className="text-[10px] text-muted-foreground tabular-nums">{total} agents</span>
                {running > 0 && <span className="text-[10px] text-emerald-400 tabular-nums">{running} up</span>}
                {error > 0 && <span className="text-[10px] text-red-400 tabular-nums">{error} err</span>}
                <div className="w-12 h-1 bg-white/[0.08] overflow-hidden rounded-full">
                  <div
                    className={cn("h-full rounded-full transition-all", error > 0 ? "bg-red-400" : "bg-emerald-400")}
                    style={{ width: `${healthPct}%` }}
                  />
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
