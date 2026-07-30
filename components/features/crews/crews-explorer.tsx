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
  RUNNING: { label: "Running", className: "text-success", pulse: true },
  IDLE: { label: "Idle", className: "text-muted-foreground" },
  ERROR: { label: "Error", className: "text-destructive" },
  STOPPED: { label: "Stopped", className: "text-warn" },
  PENDING_REVIEW: { label: "Pending", className: "text-warn" },
  EXPIRED: { label: "Expired", className: "text-muted-foreground" },
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
  /** Stored avatar render (#1297); null means generate from the seed. */
  avatar_url?: string | null
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
                    className="group/crew"
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
                      {/* Visible on hover, on focus, and while collapsed.
                          It is nearly redundant — clicking the row selects and
                          expands — but it is the ONLY way to collapse with a
                          mouse, so it cannot simply go. Quiet at rest, there
                          when reached for. */}
                      <ChevronRight
                        className={cn(
                          "h-3 w-3 text-muted-foreground-soft transition-all",
                          expanded
                            ? "rotate-90 opacity-0 group-hover/crew:opacity-100 group-focus-within/crew:opacity-100"
                            : "opacity-100",
                        )}
                      />
                    </span>
                    <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" />
                    <span className="type-nav font-semibold truncate flex-1">{crew.name}</span>
                    <span className="type-nav-sub text-muted-foreground-soft tabular-nums shrink-0">
                      {crewAgents.length}
                    </span>
                    {/* One dot per STATE, not one per agent.
                        It used to draw a dot for every agent — a crew of 35
                        drew 35 dots, and idle was capped at 3 while running and
                        error were not, so the row grew with the roster and the
                        count beside it became unreadable.
                        The count already says how many. What it cannot say is
                        that something is running or broken, which is the only
                        reason to glance at a collapsed crew at all — so that
                        keeps a mark, once, and idle gets none: idle is the
                        normal state and needs no ink. */}
                    <div className="flex items-center gap-1 shrink-0">
                      {dots.error > 0 && (
                        <span
                          className="type-meta inline-flex items-center gap-1 text-destructive"
                          title={`${dots.error} in error`}
                        >
                          <span className="h-1.5 w-1.5 rounded-full bg-destructive" />
                          {dots.error > 1 && <span className="tabular-nums">{dots.error}</span>}
                        </span>
                      )}
                      {dots.running > 0 && (
                        <span
                          className="type-meta inline-flex items-center gap-1 text-success"
                          title={`${dots.running} running`}
                        >
                          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-success" />
                          {dots.running > 1 && <span className="tabular-nums">{dots.running}</span>}
                        </span>
                      )}
                    </div>
                  </SidebarRow>

                  {/* Agent rows, hung off a guide line.
                      They were already indented by 24px, and 24px of empty
                      space says nothing — the eye read two rounded blocks
                      side by side, not a parent and its children. The rule
                      runs under the crew's own chevron, so the nesting is
                      stated rather than implied, and a selected agent's
                      highlight now starts to the RIGHT of it instead of
                      competing with the crew's row for the same left edge. */}
                  {expanded && (
                  <div className="relative ml-[1.1rem] border-l border-border/70 pl-1">
                  {crewAgents.map((agent) => {
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
                          style={agent.avatar_style || agent.crew?.avatar_style}
                          agentId={agent.id}
                          avatarUrl={agent.avatar_url}
                          className="h-8 w-8 rounded-lg shrink-0"
                        />
                        {/* The role line only on the row you are on.
                            Measured: the two text lines were 38px against a
                            29px portrait, so the TEXT set the row height and
                            the face — the thing you actually scan by — was the
                            smaller half. Dropping the second line for every
                            other row lets the portrait grow and the row shrink
                            at the same time, and the role is still there the
                            moment you need it to confirm. */}
                        <div className="flex-1 min-w-0">
                          <span className="type-nav font-medium truncate block">{agent.name}</span>
                          {isAgentSelected && (
                            <span className="type-nav-sub text-muted-foreground truncate block">
                              {agent.role_title || agent.agent_role}
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-1 shrink-0">
                          {agent.ephemeral && !ghost && (
                            <Clock className="h-2.5 w-2.5 text-notice/80" aria-label="Ephemeral hire" />
                          )}
                          {badge.pulse && (
                            <span className="relative flex h-1.5 w-1.5">
                              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-success opacity-75" />
                              <span className="relative inline-flex rounded-full h-1.5 w-1.5 bg-success" />
                            </span>
                          )}
                          <span className={cn("type-nav-sub", badge.className)}>{badge.label}</span>
                        </div>
                      </SidebarRow>
                    )
                  })}
                  </div>
                  )}
                </div>
              )
            })}

            {/* Unassigned */}
            {unassigned.length > 0 && (
              <div className="mt-2 pt-2 border-t border-border">
                <div className="type-nav-sub px-2 py-1 font-semibold uppercase tracking-wider text-muted-foreground-soft">
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
                        agentId={agent.id}
                        avatarUrl={agent.avatar_url}
                        className="h-8 w-8 rounded-lg shrink-0"
                      />
                      <div className="flex-1 min-w-0">
                        <span className="type-nav font-medium truncate block">{agent.name}</span>
                        <span className="type-nav-sub text-muted-foreground truncate block">
                          {agent.role_title || agent.agent_role}
                        </span>
                      </div>
                      <div className="flex items-center gap-1 shrink-0">
                        {agent.ephemeral && !ghost && (
                          <Clock className="h-2.5 w-2.5 text-notice/80" aria-label="Ephemeral hire" />
                        )}
                        <span className={cn("type-nav-sub", badge.className)}>{badge.label}</span>
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
