"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  ChevronRight, Clock,
} from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { isGhost, effectiveStatus } from "@/lib/agent-ephemeral"
import { SidebarToolbar, SidebarSearch, SidebarRow, SidebarCollapseButton } from "@/components/layout/sidebar-kit"

const STATUS_BADGE: Record<string, { label: string; className: string; pulse?: boolean }> = {
  RUNNING: { label: "Running", className: "text-emerald-400", pulse: true },
  IDLE: { label: "Idle", className: "text-muted-foreground" },
  ERROR: { label: "Error", className: "text-red-400" },
  STOPPED: { label: "Stopped", className: "text-amber-400" },
  PENDING_REVIEW: { label: "Pending", className: "text-amber-300" },
  EXPIRED: { label: "Expired", className: "text-slate-400" },
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
  // PR-D F5 ephemeral lifecycle (server returns these; absent on permanent agents).
  ephemeral?: boolean
  expires_at?: string | null
  expired_at?: string | null
}

export interface CrewsExplorerProps {
  crews: CrewData[]
  agents: AgentData[]
  selectedCrewId: string | null
  selectedAgentId: string | null
  collapsed: boolean
  onToggleCollapse: () => void
  onCrewSelect: (crewId: string) => void
  onAgentSelect: (agentId: string) => void
}

export function CrewsExplorer({
  crews,
  agents,
  selectedCrewId,
  selectedAgentId,
  collapsed,
  onToggleCollapse,
  onCrewSelect,
  onAgentSelect,
}: CrewsExplorerProps) {
  const [search, setSearch] = useState("")
  const [expandedCrews, setExpandedCrews] = useState<Set<string>>(() => new Set(crews.map((c) => c.id)))

  // Auto-expand newly added crews
  useEffect(() => {
    setExpandedCrews((prev) => {
      const next = new Set(prev)
      let changed = false
      for (const crew of crews) {
        if (!next.has(crew.id)) { next.add(crew.id); changed = true }
      }
      return changed ? next : prev
    })
  }, [crews])

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
    if (search.trim()) {
      const q = search.toLowerCase()
      result = result.filter(
        (a) => a.name.toLowerCase().includes(q) || a.slug.toLowerCase().includes(q) || a.role_title?.toLowerCase().includes(q),
      )
    }
    return new Set(result.map((a) => a.id))
  }, [agents, search])

  const filteredCrews = useMemo(() => {
    if (!search.trim()) return new Set(crews.map((c) => c.id))
    const crewIds = new Set<string>()
    for (const agent of agents) {
      if (filteredAgents.has(agent.id) && agent.crew_id) {
        crewIds.add(agent.crew_id)
      }
    }
    const q = search.toLowerCase()
    for (const crew of crews) {
      if (crew.name.toLowerCase().includes(q) || crew.slug.toLowerCase().includes(q)) {
        crewIds.add(crew.id)
      }
    }
    return crewIds
  }, [crews, agents, filteredAgents, search])

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

  const collapseToggle = <SidebarCollapseButton collapsed={collapsed} onToggle={onToggleCollapse} />

  return (
    <div className="flex flex-col h-full bg-card border-r border-white/[0.1] overflow-hidden">
      {collapsed ? (
        <div className="flex items-center justify-center px-2 py-2 shrink-0">
          {collapseToggle}
        </div>
      ) : (
        <div className="flex-1 min-h-0 flex flex-col">
          {/* Toolbar — search + collapse toggle (status/role filtering driven by sub-bar) */}
          <SidebarToolbar>
            <SidebarSearch
              value={search}
              onValueChange={setSearch}
              placeholder="Search agents, crews…"
            />
            {collapseToggle}
          </SidebarToolbar>

          {/* Tree */}
          <div className="flex-1 overflow-y-auto px-1">
            {crews.filter((c) => filteredCrews.has(c.id)).map((crew) => {
              const expanded = expandedCrews.has(crew.id)
              const crewAgents = (agentsByCrew.get(crew.id) || []).filter((a) => filteredAgents.has(a.id))
              const dots = crewStatusDots(crew.id)
              const isSelected = selectedCrewId === crew.id && !selectedAgentId

              return (
                <div
                  key={crew.id}
                  className="mb-0.5"
                  onKeyDown={(e) => {
                    if (e.key === "ArrowRight" && !expanded) { e.preventDefault(); toggleCrew(crew.id) }
                    if (e.key === "ArrowLeft" && expanded) { e.preventDefault(); toggleCrew(crew.id) }
                  }}
                >
                  {/* Crew row */}
                  <SidebarRow
                    as="div"
                    selected={isSelected}
                    aria-label={crew.name}
                    onSelect={() => {
                      onCrewSelect(crew.id)
                      if (!expanded) toggleCrew(crew.id)
                    }}
                  >
                    <span
                      role="button"
                      tabIndex={-1}
                      aria-expanded={expanded}
                      className="shrink-0"
                      onClick={(e) => { e.stopPropagation(); toggleCrew(crew.id) }}
                      onKeyDown={(e) => { if (e.key === "Enter") { e.stopPropagation(); toggleCrew(crew.id) } }}
                    >
                      <ChevronRight
                        className={cn(
                          "h-3 w-3 text-muted-foreground-soft transition-transform",
                          expanded && "rotate-90",
                        )}
                      />
                    </span>
                    <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" />
                    <span className="text-[12px] font-medium truncate flex-1">{crew.name}</span>
                    <span className="text-[10px] text-muted-foreground-soft tabular-nums shrink-0">
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
                  </SidebarRow>

                  {/* Agent rows */}
                  {expanded && crewAgents.map((agent) => {
                    const ghost = isGhost(agent)
                    const badge = STATUS_BADGE[effectiveStatus(agent)] || STATUS_BADGE.IDLE
                    const isAgentSelected = selectedAgentId === agent.id
                    return (
                      <SidebarRow
                        key={agent.id}
                        as="div"
                        indent
                        selected={isAgentSelected}
                        aria-label={agent.name}
                        onSelect={() => onAgentSelect(agent.id)}
                        className={cn(
                          ghost && "opacity-55 grayscale-[0.4] hover:opacity-90 hover:grayscale-0",
                        )}
                      >
                        <AgentAvatar
                          seed={agent.avatar_seed || agent.name}
                          style={agent.avatar_style || agent.crew?.avatar_style}
                          className="h-5 w-5 rounded-full shrink-0"
                        />
                        <div className="flex-1 min-w-0">
                          <span className="text-[11px] font-medium truncate block">{agent.name}</span>
                          <span className="text-[10px] text-muted-foreground truncate block">
                            {agent.role_title || agent.agent_role}
                          </span>
                        </div>
                        <div className="flex items-center gap-1 shrink-0">
                          {agent.ephemeral && !ghost && (
                            <Clock className="h-2.5 w-2.5 text-cyan-300/80" aria-label="Ephemeral hire" />
                          )}
                          {badge.pulse && (
                            <span className="relative flex h-1.5 w-1.5">
                              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-emerald-400 opacity-75" />
                              <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-emerald-500" />
                            </span>
                          )}
                          <span className={cn("text-[10px]", badge.className)}>{badge.label}</span>
                        </div>
                      </SidebarRow>
                    )
                  })}
                </div>
              )
            })}

            {/* Unassigned */}
            {unassigned.length > 0 && (
              <div className="mt-2 pt-2 border-t border-border">
                <div className="px-2 py-1 text-[10px] font-semibold text-muted-foreground-soft uppercase tracking-wider">
                  Unassigned
                </div>
                {unassigned.map((agent) => {
                  const ghost = isGhost(agent)
                  const badge = STATUS_BADGE[effectiveStatus(agent)] || STATUS_BADGE.IDLE
                  const isAgentSelected = selectedAgentId === agent.id
                  return (
                    <SidebarRow
                      key={agent.id}
                      as="div"
                      selected={isAgentSelected}
                      aria-label={agent.name}
                      onSelect={() => onAgentSelect(agent.id)}
                      className={cn(
                        ghost && "opacity-55 grayscale-[0.4] hover:opacity-90 hover:grayscale-0",
                      )}
                    >
                      <AgentAvatar
                        seed={agent.avatar_seed || agent.name}
                        style={agent.avatar_style}
                        className="h-5 w-5 rounded-full shrink-0"
                      />
                      <div className="flex-1 min-w-0">
                        <span className="text-[11px] font-medium truncate block">{agent.name}</span>
                        <span className="text-[10px] text-muted-foreground truncate block">
                          {agent.role_title || agent.agent_role}
                        </span>
                      </div>
                      <div className="flex items-center gap-1 shrink-0">
                        {agent.ephemeral && !ghost && (
                          <Clock className="h-2.5 w-2.5 text-cyan-300/80" aria-label="Ephemeral hire" />
                        )}
                        <span className={cn("text-[10px]", badge.className)}>{badge.label}</span>
                      </div>
                    </SidebarRow>
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
