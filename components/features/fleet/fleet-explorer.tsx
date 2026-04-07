"use client"

import { useCallback, useMemo, useState } from "react"
import {
  ChevronRight, Search, PanelLeftClose, PanelLeftOpen,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

type StatusFilter = "all" | "RUNNING" | "IDLE" | "ERROR" | "STOPPED"

const STATUS_FILTERS: { value: StatusFilter; label: string; dot?: string }[] = [
  { value: "all", label: "All" },
  { value: "RUNNING", label: "Running", dot: "bg-emerald-500" },
  { value: "IDLE", label: "Idle", dot: "bg-gray-400" },
  { value: "ERROR", label: "Error", dot: "bg-red-500" },
  { value: "STOPPED", label: "Stopped", dot: "bg-amber-500" },
]

const STATUS_BADGE: Record<string, { label: string; className: string; pulse?: boolean }> = {
  RUNNING: { label: "Running", className: "text-emerald-400", pulse: true },
  IDLE: { label: "Idle", className: "text-muted-foreground" },
  ERROR: { label: "Error", className: "text-red-400" },
  STOPPED: { label: "Stopped", className: "text-amber-400" },
}

interface CrewData {
  id: string
  name: string
  slug: string
  color: string | null
  icon: string | null
  _count?: { agents: number }
}

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  crew_id: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { avatar_style?: string | null } | null
}

export interface FleetExplorerProps {
  crews: CrewData[]
  agents: AgentData[]
  selectedCrewId: string | null
  selectedAgentId: string | null
  collapsed: boolean
  onToggleCollapse: () => void
  onCrewSelect: (crewId: string) => void
  onAgentSelect: (agentId: string) => void
}

export function FleetExplorer({
  crews,
  agents,
  selectedCrewId,
  selectedAgentId,
  collapsed,
  onToggleCollapse,
  onCrewSelect,
  onAgentSelect,
}: FleetExplorerProps) {
  const [search, setSearch] = useState("")
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all")
  const [expandedCrews, setExpandedCrews] = useState<Set<string>>(() => new Set(crews.map((c) => c.id)))

  const toggleCrew = useCallback((crewId: string) => {
    setExpandedCrews((prev) => {
      const next = new Set(prev)
      if (next.has(crewId)) next.delete(crewId)
      else next.add(crewId)
      return next
    })
  }, [])

  const agentsByCrew = useMemo(() => {
    const map = new Map<string | null, AgentData[]>()
    for (const agent of agents) {
      const key = agent.crew_id
      if (!map.has(key)) map.set(key, [])
      map.get(key)!.push(agent)
    }
    return map
  }, [agents])

  const filteredAgents = useMemo(() => {
    let result = agents
    if (statusFilter !== "all") {
      result = result.filter((a) => a.status === statusFilter)
    }
    if (search.trim()) {
      const q = search.toLowerCase()
      result = result.filter(
        (a) => a.name.toLowerCase().includes(q) || a.slug.toLowerCase().includes(q) || a.role_title?.toLowerCase().includes(q),
      )
    }
    return new Set(result.map((a) => a.id))
  }, [agents, statusFilter, search])

  const filteredCrews = useMemo(() => {
    if (statusFilter === "all" && !search.trim()) return new Set(crews.map((c) => c.id))
    const crewIds = new Set<string>()
    for (const agent of agents) {
      if (filteredAgents.has(agent.id) && agent.crew_id) {
        crewIds.add(agent.crew_id)
      }
    }
    // Also include crews matching search by name
    if (search.trim()) {
      const q = search.toLowerCase()
      for (const crew of crews) {
        if (crew.name.toLowerCase().includes(q) || crew.slug.toLowerCase().includes(q)) {
          crewIds.add(crew.id)
        }
      }
    }
    return crewIds
  }, [crews, agents, filteredAgents, statusFilter, search])

  const unassigned = useMemo(() => {
    return (agentsByCrew.get(null) || []).filter((a) => filteredAgents.has(a.id))
  }, [agentsByCrew, filteredAgents])

  // Status summary dots for a crew
  const crewStatusDots = useCallback((crewId: string) => {
    const crewAgents = agentsByCrew.get(crewId) || []
    const running = crewAgents.filter((a) => a.status === "RUNNING").length
    const error = crewAgents.filter((a) => a.status === "ERROR").length
    const idle = crewAgents.filter((a) => a.status === "IDLE" || a.status === "STOPPED").length
    return { running, error, idle }
  }, [agentsByCrew])

  return (
    <div className="flex flex-col h-full bg-card border-r border-white/[0.1] overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-2 py-1.5 border-b border-border shrink-0">
        {!collapsed && (
          <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
            Explorer
          </span>
        )}
        <Button
          variant="ghost"
          size="icon-xs"
          className="text-muted-foreground/70 hover:text-foreground/70 ml-auto"
          onClick={onToggleCollapse}
        >
          {collapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
        </Button>
      </div>

      {!collapsed && (
        <div className="flex-1 min-h-0 flex flex-col">
          {/* Search */}
          <div className="px-2 pt-2 pb-1 shrink-0">
            <div className="relative">
              <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
              <Input
                placeholder="Filter crews & agents..."
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                className="h-7 pl-7 text-[12px] bg-white/[0.04] border-white/[0.1]"
              />
            </div>
          </div>

          {/* Status filters */}
          <div className="px-2 pb-2 flex items-center gap-1 flex-wrap shrink-0">
            {STATUS_FILTERS.map((f) => (
              <button
                key={f.value}
                onClick={() => setStatusFilter(f.value)}
                className={cn(
                  "inline-flex items-center gap-1 px-2 py-0.5 rounded text-[10px] font-medium transition-colors",
                  statusFilter === f.value
                    ? "bg-blue-500/15 border border-blue-500/35 text-blue-400"
                    : "bg-white/[0.04] border border-white/[0.08] text-muted-foreground hover:text-foreground/80",
                )}
              >
                {f.dot && <span className={cn("h-1.5 w-1.5 rounded-full", f.dot)} />}
                {f.label}
              </button>
            ))}
          </div>

          {/* Tree */}
          <div className="flex-1 overflow-y-auto px-1">
            {crews.filter((c) => filteredCrews.has(c.id)).map((crew) => {
              const expanded = expandedCrews.has(crew.id)
              const crewAgents = (agentsByCrew.get(crew.id) || []).filter((a) => filteredAgents.has(a.id))
              const dots = crewStatusDots(crew.id)
              const isSelected = selectedCrewId === crew.id && !selectedAgentId

              return (
                <div key={crew.id} className="mb-0.5">
                  {/* Crew row */}
                  <button
                    className={cn(
                      "w-full flex items-center gap-2 px-2 py-1.5 rounded-md text-left transition-colors group",
                      isSelected
                        ? "bg-blue-500/10 border-l-2 border-blue-400"
                        : "hover:bg-white/[0.04] border-l-2 border-transparent",
                    )}
                    onClick={() => {
                      onCrewSelect(crew.id)
                      if (!expanded) toggleCrew(crew.id)
                    }}
                  >
                    <ChevronRight
                      className={cn(
                        "h-3 w-3 text-muted-foreground/50 transition-transform shrink-0",
                        expanded && "rotate-90",
                      )}
                      onClick={(e) => { e.stopPropagation(); toggleCrew(crew.id) }}
                    />
                    <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" />
                    <span className="text-[12px] font-medium truncate flex-1">{crew.name}</span>
                    <span className="text-[10px] text-muted-foreground/50 tabular-nums shrink-0">
                      {crewAgents.length}
                    </span>
                    {/* Mini status dots */}
                    <div className="flex items-center gap-0.5 shrink-0">
                      {Array.from({ length: dots.running }).map((_, i) => (
                        <span key={`r${i}`} className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
                      ))}
                      {Array.from({ length: dots.error }).map((_, i) => (
                        <span key={`e${i}`} className="h-1.5 w-1.5 rounded-full bg-red-500" />
                      ))}
                      {Array.from({ length: Math.min(dots.idle, 3) }).map((_, i) => (
                        <span key={`i${i}`} className="h-1.5 w-1.5 rounded-full bg-gray-500/50" />
                      ))}
                    </div>
                  </button>

                  {/* Agent rows */}
                  {expanded && crewAgents.map((agent) => {
                    const badge = STATUS_BADGE[agent.status] || STATUS_BADGE.IDLE
                    const isAgentSelected = selectedAgentId === agent.id
                    return (
                      <button
                        key={agent.id}
                        className={cn(
                          "w-full flex items-center gap-2 pl-9 pr-2 py-1 rounded-md text-left transition-colors",
                          isAgentSelected
                            ? "bg-blue-500/10 border-l-2 border-blue-400"
                            : "hover:bg-white/[0.04] border-l-2 border-transparent",
                        )}
                        onClick={() => onAgentSelect(agent.id)}
                      >
                        <img
                          src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style || agent.crew?.avatar_style)}
                          alt=""
                          className="h-5 w-5 rounded-full shrink-0"
                        />
                        <div className="flex-1 min-w-0">
                          <span className="text-[11px] font-medium truncate block">{agent.name}</span>
                          <span className="text-[10px] text-muted-foreground/60 truncate block">
                            {agent.role_title || agent.agent_role}
                          </span>
                        </div>
                        <div className="flex items-center gap-1 shrink-0">
                          {badge.pulse && (
                            <span className="relative flex h-1.5 w-1.5">
                              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                              <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
                            </span>
                          )}
                          <span className={cn("text-[10px]", badge.className)}>{badge.label}</span>
                        </div>
                      </button>
                    )
                  })}
                </div>
              )
            })}

            {/* Unassigned */}
            {unassigned.length > 0 && (
              <div className="mt-2 pt-2 border-t border-border">
                <div className="px-2 py-1 text-[10px] font-semibold text-muted-foreground/40 uppercase tracking-wider">
                  Unassigned
                </div>
                {unassigned.map((agent) => {
                  const badge = STATUS_BADGE[agent.status] || STATUS_BADGE.IDLE
                  const isAgentSelected = selectedAgentId === agent.id
                  return (
                    <button
                      key={agent.id}
                      className={cn(
                        "w-full flex items-center gap-2 px-2 py-1 rounded-md text-left transition-colors",
                        isAgentSelected
                          ? "bg-blue-500/10 border-l-2 border-blue-400"
                          : "hover:bg-white/[0.04] border-l-2 border-transparent",
                      )}
                      onClick={() => onAgentSelect(agent.id)}
                    >
                      <img
                        src={getAgentAvatarUrl(agent.avatar_seed || agent.name, agent.avatar_style)}
                        alt=""
                        className="h-5 w-5 rounded-full shrink-0"
                      />
                      <div className="flex-1 min-w-0">
                        <span className="text-[11px] font-medium truncate block">{agent.name}</span>
                        <span className="text-[10px] text-muted-foreground/60 truncate block">
                          {agent.role_title || agent.agent_role}
                        </span>
                      </div>
                      <span className={cn("text-[10px]", badge.className)}>{badge.label}</span>
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
