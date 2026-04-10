"use client"

import { Bot } from "lucide-react"
import { cn } from "@/lib/utils"
import { getCrewBgClass } from "@/lib/colors"

interface CrewData {
  id: string
  name: string
  slug: string
  description: string | null
  color: string | null
  icon: string | null
  created_at: string
  _count: { agents: number; members: number }
}

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  crew_id: string | null
}

interface AllCrewsOverviewProps {
  crews: CrewData[]
  agents: AgentData[]
  onCrewSelect: (id: string) => void
  onAgentSelect: (id: string) => void
}

export function AllCrewsOverview({ crews, agents, onCrewSelect, onAgentSelect }: AllCrewsOverviewProps) {
  return (
    <div className="p-4 sm:p-6 h-full overflow-y-auto space-y-5">
      <div>
        <h2 className="text-lg font-semibold">All Crews</h2>
        <p className="text-[12px] text-muted-foreground mt-0.5">
          Select a crew from the explorer or below to see details
        </p>
      </div>

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {crews.map((crew) => {
          const crewAgents = agents.filter((a) => a.crew_id === crew.id)
          const running = crewAgents.filter((a) => a.status === "RUNNING").length
          const error = crewAgents.filter((a) => a.status === "ERROR").length

          return (
            <button
              key={crew.id}
              onClick={() => onCrewSelect(crew.id)}
              className="text-left rounded-xl border border-border/80 bg-card p-4 hover:border-primary/50 hover:bg-accent/30 hover:shadow-md transition-all duration-150 cursor-pointer"
            >
              <div className="flex items-start gap-3">
                <div
                  className={cn(
                    "h-8 w-8 rounded-lg flex items-center justify-center text-white text-[12px] font-bold shrink-0",
                    getCrewBgClass(crew.color),
                  )}
                >
                  {crew.name.charAt(0)}
                </div>
                <div className="flex-1 min-w-0">
                  <h3 className="text-[13px] font-semibold truncate">{crew.name}</h3>
                  <p className="text-[11px] text-muted-foreground mt-0.5 line-clamp-2">
                    {crew.description || "No description"}
                  </p>
                </div>
              </div>
              <div className="mt-3 pt-2 border-t border-border/50 flex items-center gap-3 text-[11px] text-muted-foreground">
                <span className="flex items-center gap-1">
                  <Bot className="h-3 w-3" />
                  {crew._count.agents} agents
                </span>
                {running > 0 && (
                  <span className="text-emerald-400">{running} running</span>
                )}
                {error > 0 && (
                  <span className="text-red-400">{error} error</span>
                )}
              </div>
            </button>
          )
        })}
      </div>

      {/* Unassigned agents */}
      {agents.filter((a) => !a.crew_id).length > 0 && (
        <div>
          <h3 className="text-[13px] font-semibold mb-3 text-muted-foreground">Unassigned Agents</h3>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {agents.filter((a) => !a.crew_id).map((agent) => (
              <button
                key={agent.id}
                onClick={() => onAgentSelect(agent.id)}
                className="text-left rounded-xl border border-border/80 bg-card p-3 hover:border-primary/50 hover:bg-accent/30 transition-all duration-150 cursor-pointer"
              >
                <div className="flex items-center gap-2">
                  <Bot className="h-4 w-4 text-muted-foreground" />
                  <span className="text-[12px] font-medium truncate">{agent.name}</span>
                  <span className={cn(
                    "text-[10px] ml-auto",
                    agent.status === "RUNNING" ? "text-emerald-400" : "text-muted-foreground/50",
                  )}>
                    {agent.status.toLowerCase()}
                  </span>
                </div>
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
