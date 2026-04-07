"use client"

import { useState, useMemo } from "react"
import { ChevronRight, ChevronDown, Network, Users } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Badge } from "@/components/ui/badge"
import { ScrollArea } from "@/components/ui/scroll-area"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import type { CrewSummary, AgentSummary } from "@/lib/types/orchestration"

const crewColorMap: Record<string, string> = {
  blue: "#3b82f6",
  emerald: "#10b981",
  violet: "#8b5cf6",
  amber: "#f59e0b",
  rose: "#f43f5e",
  cyan: "#06b6d4",
  lime: "#84cc16",
  fuchsia: "#d946ef",
}

function resolveColor(color: string | null): string {
  if (!color) return "#64748b"
  return crewColorMap[color] || color
}

export interface HierarchyTreeProps {
  crews: CrewSummary[]
  agents: AgentSummary[]
  selectedCrewId: string | null
  selectedAgentSlug: string | null
  onCrewSelect: (crewId: string) => void
  onAgentSelect: (agentSlug: string) => void
}

export function HierarchyTree({
  crews,
  agents,
  selectedCrewId,
  selectedAgentSlug,
  onCrewSelect,
  onAgentSelect,
}: HierarchyTreeProps) {
  const [expandedCrews, setExpandedCrews] = useState<Set<string>>(
    () => new Set(crews.map((c) => c.id)),
  )

  const agentsByCrew = useMemo(() => {
    const map = new Map<string, AgentSummary[]>()
    for (const agent of agents) {
      if (!agent.crew_id) continue
      const list = map.get(agent.crew_id) || []
      list.push(agent)
      map.set(agent.crew_id, list)
    }
    return map
  }, [agents])

  function toggleCrew(crewId: string) {
    setExpandedCrews((prev) => {
      const next = new Set(prev)
      if (next.has(crewId)) {
        next.delete(crewId)
      } else {
        next.add(crewId)
      }
      return next
    })
  }

  return (
    <ScrollArea className="h-full">
      <div className="p-2 space-y-0.5">
        {/* Workspace root */}
        <div className="flex items-center gap-2 px-2 py-1.5 rounded-md text-muted-foreground">
          <Network className="h-3.5 w-3.5 shrink-0" />
          <span className="text-[11px] font-semibold uppercase tracking-wider">
            Coordinator
          </span>
        </div>

        {/* Crews */}
        {crews.map((crew) => {
          const crewAgents = agentsByCrew.get(crew.id) || []
          const agentCount = crew._count?.agents ?? crewAgents.length
          const isExpanded = expandedCrews.has(crew.id)
          const accent = resolveColor(crew.color)

          return (
            <Collapsible
              key={crew.id}
              open={isExpanded}
            >
              <div
                className={cn(
                  "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left transition-colors",
                  "hover:bg-accent/50 cursor-pointer",
                  selectedCrewId === crew.id &&
                    "bg-accent ring-1 ring-border",
                )}
                onClick={() => onCrewSelect(crew.id)}
              >
                {/* Chevron — only this toggles expand/collapse */}
                <CollapsibleTrigger asChild>
                  <button
                    className="shrink-0 text-muted-foreground/70 hover:text-foreground transition-colors p-0.5 -m-0.5 rounded"
                    onClick={(e) => {
                      e.stopPropagation()
                      toggleCrew(crew.id)
                    }}
                  >
                    {isExpanded ? (
                      <ChevronDown className="h-3 w-3" />
                    ) : (
                      <ChevronRight className="h-3 w-3" />
                    )}
                  </button>
                </CollapsibleTrigger>

                  {crew.icon ? (
                    <CrewIcon
                      icon={crew.icon}
                      color={crew.color}
                      size="sm"
                      className="!h-5 !w-5 !rounded-md"
                    />
                  ) : (
                    <span
                      className="w-2.5 h-2.5 rounded-full shrink-0"
                      style={{ backgroundColor: accent }}
                    />
                  )}

                  <span className="text-xs font-medium text-foreground truncate flex-1">
                    {crew.name}
                  </span>

                  <Badge
                    variant="secondary"
                    className="h-4 min-w-4 px-1 text-[10px] bg-accent text-muted-foreground border-0"
                  >
                    <Users className="h-2.5 w-2.5 mr-0.5" />
                    {agentCount}
                  </Badge>
              </div>

              <CollapsibleContent>
                <div className="ml-4 pl-2.5 border-l border-border space-y-px">
                  {crewAgents.map((agent) => (
                    <button
                      key={agent.id}
                      className={cn(
                        "w-full flex items-center gap-2 px-2 py-1 rounded-md text-left transition-colors",
                        "hover:bg-accent/50",
                        selectedAgentSlug === agent.slug &&
                          "bg-accent ring-1 ring-border",
                      )}
                      onClick={() => onAgentSelect(agent.slug)}
                    >
                      <img src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)} alt="" className="w-4 h-4 rounded-full shrink-0" />
                      <span className="text-[11px] text-foreground/80 truncate flex-1">
                        {agent.name}
                      </span>
                      <span className="text-[10px] font-mono text-muted-foreground/70 truncate">
                        @{agent.slug}
                      </span>
                    </button>
                  ))}
                  {crewAgents.length === 0 && (
                    <div className="px-2 py-1 text-[10px] text-muted-foreground/50 italic">
                      No agents
                    </div>
                  )}
                </div>
              </CollapsibleContent>
            </Collapsible>
          )
        })}

        {crews.length === 0 && (
          <div className="px-2 py-4 text-center text-xs text-muted-foreground/50">
            No crews yet
          </div>
        )}
      </div>
    </ScrollArea>
  )
}
