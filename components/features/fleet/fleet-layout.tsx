"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Bot, Plus, LayoutGrid, Activity, Play, Square, FileJson,
  Share2, HeartPulse, ChevronUp, ChevronDown, Layers, Download,
  ChevronLeft, PanelLeftOpen, CheckSquare,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { getCrewDotColor } from "@/lib/crew-icon"
import { FleetExplorer } from "@/components/features/fleet/fleet-explorer"
import { FleetCrewDetail } from "@/components/features/fleet/fleet-crew-detail"
import { FleetAgentDetail } from "@/components/features/fleet/fleet-agent-detail"
import { CrewActivityFeed } from "@/components/features/crews/crew-activity-feed"
import { useIsMobile } from "@/hooks/use-mobile"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { timeAgo } from "@/lib/time"
import Link from "next/link"

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
  description: string | null
  role_title: string | null
  agent_role: string
  llm_provider: string
  llm_model: string
  cli_adapter: string
  crew_id: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  crew?: { name: string; slug: string; color: string | null; avatar_style?: string | null } | null
  _count?: { skills: number; credentials: number; chats: number }
  last_active_at?: string | null
}

interface MissionData {
  id: string
  title: string
  status: string
  crew_id: string
  tasks?: { id: string; status: string }[]
  created_at: string
}

type TabId = "overview" | "activity" | "connections" | "health"

export interface FleetLayoutProps {
  crews: CrewData[]
  agents: AgentData[]
  missions: MissionData[]
  workspaceId: string
  onRefresh: () => void
}

const FLEET_TABS = [
  { id: "overview" as const, label: "Overview", icon: LayoutGrid },
  { id: "activity" as const, label: "Activity", icon: Activity },
  { id: "connections" as const, label: "Connections", icon: Share2 },
  { id: "health" as const, label: "Health", icon: HeartPulse },
]

const FLEET_BOTTOM_TABS = [
  { id: "activity" as const, label: "Activity", icon: Activity },
  { id: "bulk" as const, label: "Bulk Actions", icon: Layers },
  { id: "export" as const, label: "Export", icon: Download },
]

export function FleetLayout({ crews, agents, missions, workspaceId, onRefresh }: FleetLayoutProps) {
  const isMobile = useIsMobile()

  // Panel state
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [drawerTab, setDrawerTab] = useState<"activity" | "bulk" | "export">("activity")
  const [activeTab, setActiveTab] = useState<TabId>("overview")

  // Selection state
  const [selectedCrewId, setSelectedCrewId] = useState<string | null>(null)
  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null)

  // Auto-collapse on mobile
  useEffect(() => {
    if (isMobile) setLeftCollapsed(true)
  }, [isMobile])

  // Derived data
  const selectedCrew = useMemo(
    () => (selectedCrewId ? crews.find((c) => c.id === selectedCrewId) || null : null),
    [crews, selectedCrewId],
  )

  const selectedAgent = useMemo(
    () => (selectedAgentId ? agents.find((a) => a.id === selectedAgentId) || null : null),
    [agents, selectedAgentId],
  )

  const crewAgents = useMemo(
    () => (selectedCrewId ? agents.filter((a) => a.crew_id === selectedCrewId) : []),
    [agents, selectedCrewId],
  )

  // Stats
  const stats = useMemo(() => ({
    crews: crews.length,
    agents: agents.length,
    running: agents.filter((a) => a.status === "RUNNING").length,
    error: agents.filter((a) => a.status === "ERROR").length,
  }), [crews, agents])

  // Handlers
  const handleCrewSelect = useCallback((crewId: string) => {
    setSelectedCrewId(crewId)
    setSelectedAgentId(null)
  }, [])

  const handleAgentSelect = useCallback((agentId: string) => {
    setSelectedAgentId((prev) => prev === agentId ? null : agentId)
    // Also select the agent's crew
    const agent = agents.find((a) => a.id === agentId)
    if (agent?.crew_id) setSelectedCrewId(agent.crew_id)
  }, [agents])

  const handleAgentClose = useCallback(() => {
    setSelectedAgentId(null)
  }, [])

  const showRightPanel = selectedAgent !== null

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* Toolbar: tabs | actions */}
      <div className="shrink-0 z-20 flex items-stretch h-8 bg-card border-b border-white/[0.08] px-3 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {/* Left: tabs */}
        <div className="flex items-stretch">
          {FLEET_TABS.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              onClick={() => setActiveTab(id)}
              className={cn(
                "flex items-center gap-1.5 px-3 text-[12px] font-medium border-b-2 transition-all duration-100 relative top-px",
                activeTab === id
                  ? "border-blue-400 text-blue-400"
                  : "border-transparent text-muted-foreground hover:text-foreground/80",
              )}
            >
              <Icon className="h-3 w-3 opacity-75" />
              {label}
            </button>
          ))}
        </div>

        {/* Right: actions */}
        <div className="flex items-center gap-1.5 ml-auto shrink-0">
          <Button size="sm" className="h-[22px] px-2 text-[11.5px] font-medium gap-1" asChild>
            <Link href="/crews/new">
              <Plus className="h-3 w-3" />
              Crew
            </Link>
          </Button>
          <Button variant="outline" size="sm" className="h-[22px] px-2 text-[11.5px] font-medium gap-1 bg-white/[0.04] border-white/[0.1]" asChild>
            <Link href="/agents/new">
              <Plus className="h-3 w-3" />
              Agent
            </Link>
          </Button>
        </div>
      </div>

      {/* Main 3-column layout */}
      <div
        className="flex-1 min-h-0 grid transition-all duration-200 relative"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${leftCollapsed ? "48px" : "280px"} 1fr ${showRightPanel ? "380px" : "0px"}`,
          gridTemplateRows: "1fr auto",
        }}
      >
        {/* Left panel */}
        {isMobile ? (
          <>
            {leftCollapsed && (
              <button
                className="absolute top-2 left-2 z-20 h-8 w-8 min-h-[44px] min-w-[44px] rounded-md bg-card border border-white/[0.1] flex items-center justify-center text-muted-foreground hover:text-foreground"
                onClick={() => setLeftCollapsed(false)}
              >
                <PanelLeftOpen className="h-3.5 w-3.5" />
              </button>
            )}
            <AnimatePresence>
              {!leftCollapsed && (
                <>
                  <motion.div
                    className="fixed inset-0 bg-black/50 z-30"
                    initial={{ opacity: 0 }}
                    animate={{ opacity: 1 }}
                    exit={{ opacity: 0 }}
                    onClick={() => setLeftCollapsed(true)}
                  />
                  <motion.div
                    className="fixed left-0 top-0 bottom-0 w-[280px] z-40"
                    initial={{ x: -280 }}
                    animate={{ x: 0 }}
                    exit={{ x: -280 }}
                    transition={{ type: "spring", damping: 25, stiffness: 300 }}
                  >
                    <FleetExplorer
                      crews={crews}
                      agents={agents}
                      selectedCrewId={selectedCrewId}
                      selectedAgentId={selectedAgentId}
                      collapsed={false}
                      onToggleCollapse={() => setLeftCollapsed(true)}
                      onCrewSelect={(id) => { handleCrewSelect(id); setLeftCollapsed(true) }}
                      onAgentSelect={(id) => { handleAgentSelect(id); setLeftCollapsed(true) }}
                    />
                  </motion.div>
                </>
              )}
            </AnimatePresence>
          </>
        ) : (
          <div className="row-span-1 min-h-0 overflow-hidden">
            <FleetExplorer
              crews={crews}
              agents={agents}
              selectedCrewId={selectedCrewId}
              selectedAgentId={selectedAgentId}
              collapsed={leftCollapsed}
              onToggleCollapse={() => setLeftCollapsed(!leftCollapsed)}
              onCrewSelect={handleCrewSelect}
              onAgentSelect={handleAgentSelect}
            />
          </div>
        )}

        {/* Center content */}
        <div className="row-span-1 relative overflow-hidden min-h-0">
          <AnimatePresence mode="wait">
            {activeTab === "overview" && (
              <motion.div
                key={`overview-${selectedCrewId || "none"}`}
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="h-full"
              >
                {selectedCrew ? (
                  <FleetCrewDetail
                    crew={selectedCrew}
                    agents={crewAgents}
                    missions={missions}
                    onAgentClick={handleAgentSelect}
                  />
                ) : (
                  <AllCrewsOverview
                    crews={crews}
                    agents={agents}
                    onCrewSelect={handleCrewSelect}
                    onAgentSelect={handleAgentSelect}
                  />
                )}
              </motion.div>
            )}

            {activeTab === "activity" && (
              <motion.div
                key="activity"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="p-4 h-full overflow-auto"
              >
                <CrewActivityFeed workspaceId={workspaceId} />
              </motion.div>
            )}

            {activeTab === "connections" && (
              <motion.div
                key="connections"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="flex items-center justify-center h-full text-muted-foreground/50"
              >
                <div className="text-center">
                  <Share2 className="h-8 w-8 mx-auto mb-2 opacity-50" />
                  <p className="text-[13px] font-medium">Crew Connections</p>
                  <p className="text-[11px] text-muted-foreground/40 mt-1">
                    Configure inter-crew communication in{" "}
                    <Link href="/orchestration" className="text-blue-400 hover:underline">Orchestration</Link>
                  </p>
                </div>
              </motion.div>
            )}

            {activeTab === "health" && (
              <motion.div
                key="health"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className="p-4 h-full overflow-auto"
              >
                <HealthOverview crews={crews} agents={agents} />
              </motion.div>
            )}
          </AnimatePresence>
        </div>

        {/* Right panel */}
        {isMobile ? (
          <AnimatePresence>
            {showRightPanel && selectedAgent && (
              <motion.div
                className="fixed inset-0 z-40 bg-card flex flex-col"
                initial={{ x: "100%" }}
                animate={{ x: 0 }}
                exit={{ x: "100%" }}
                transition={{ type: "spring", damping: 25, stiffness: 300 }}
              >
                <div className="flex items-center gap-2 px-3 py-2 border-b border-white/[0.1] shrink-0">
                  <button
                    onClick={handleAgentClose}
                    className="h-8 w-8 min-h-[44px] min-w-[44px] flex items-center justify-center text-muted-foreground hover:text-foreground"
                  >
                    <ChevronLeft className="h-4 w-4" />
                  </button>
                  <span className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Agent Detail</span>
                </div>
                <div className="flex-1 overflow-y-auto">
                  <FleetAgentDetail agent={selectedAgent} workspaceId={workspaceId} onClose={handleAgentClose} />
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        ) : (
          <div className={cn(
            "row-span-1 transition-all duration-200 overflow-hidden min-h-0",
            showRightPanel ? "w-[380px]" : "w-0",
          )}>
            <AnimatePresence mode="wait">
              {showRightPanel && selectedAgent && (
                <FleetAgentDetail agent={selectedAgent} workspaceId={workspaceId} onClose={handleAgentClose} />
              )}
            </AnimatePresence>
          </div>
        )}

        {/* Bottom drawer */}
        <motion.div
          className={cn("border-t border-white/[0.1] bg-card flex flex-col overflow-hidden", isMobile ? "col-span-1" : "col-span-3")}
          animate={{ height: drawerOpen ? 220 : 32 }}
          transition={{ duration: 0.2, ease: "easeInOut" }}
        >
          {/* Drawer tab bar */}
          <div
            className="flex items-center gap-0 px-2 shrink-0 h-8 cursor-pointer select-none"
            onClick={() => { if (!drawerOpen) setDrawerOpen(true) }}
          >
            {FLEET_BOTTOM_TABS.map(({ id, label, icon: Icon }) => (
              <button
                key={id}
                className={cn(
                  "flex items-center gap-1.5 px-3 py-1 text-[11px] font-medium rounded-t transition-colors",
                  drawerOpen && drawerTab === id
                    ? "text-foreground bg-accent/50"
                    : "text-muted-foreground hover:text-foreground/70",
                )}
                onClick={(e) => {
                  e.stopPropagation()
                  setDrawerTab(id)
                  setDrawerOpen(true)
                }}
              >
                <Icon className="h-3 w-3" />
                {!isMobile && label}
              </button>
            ))}

            <div className="ml-auto">
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground/70 hover:text-foreground/70"
                onClick={(e) => { e.stopPropagation(); setDrawerOpen(!drawerOpen) }}
              >
                {drawerOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />}
              </Button>
            </div>
          </div>

          {/* Drawer content */}
          {drawerOpen && (
            <div className="flex-1 min-h-0 border-t border-border overflow-auto">
              {drawerTab === "activity" && (
                <FleetActivityFeed agents={agents} />
              )}
              {drawerTab === "bulk" && (
                <div className="p-4 space-y-3">
                  <p className="text-[12px] text-muted-foreground mb-3">Select agents from the explorer, then apply bulk operations.</p>
                  <div className="flex items-center gap-2 flex-wrap">
                    <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" disabled>
                      <Play className="h-3 w-3" /> Start All Idle
                    </Button>
                    <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" disabled>
                      <Square className="h-3 w-3" /> Stop All Running
                    </Button>
                    <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" disabled>
                      <CheckSquare className="h-3 w-3" /> Assign Crew
                    </Button>
                  </div>
                  <p className="text-[10px] text-muted-foreground/40">Multi-select coming in Phase 2</p>
                </div>
              )}
              {drawerTab === "export" && (
                <div className="p-4 space-y-3">
                  <p className="text-[12px] text-muted-foreground mb-3">Export your workspace configuration.</p>
                  <div className="flex items-center gap-2">
                    <Button variant="outline" size="sm" className="h-7 text-[11px] gap-1.5" onClick={() => {
                      const data = { crews: crews.map((c) => ({ name: c.name, slug: c.slug, color: c.color, icon: c.icon })), agents: agents.map((a) => ({ name: a.name, slug: a.slug, role: a.agent_role, crew_id: a.crew_id, llm_provider: a.llm_provider, llm_model: a.llm_model })) }
                      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" })
                      const url = URL.createObjectURL(blob)
                      const a = document.createElement("a"); a.href = url; a.download = "fleet-export.json"; a.click()
                      URL.revokeObjectURL(url)
                    }}>
                      <FileJson className="h-3 w-3" /> Export JSON
                    </Button>
                  </div>
                  <p className="text-[10px] text-muted-foreground/40">Exports crews and agents configuration (no credentials)</p>
                </div>
              )}
            </div>
          )}
        </motion.div>
      </div>
    </div>
  )
}

/** All crews overview — shown when no crew is selected */
function AllCrewsOverview({
  crews,
  agents,
  onCrewSelect,
  onAgentSelect,
}: {
  crews: CrewData[]
  agents: AgentData[]
  onCrewSelect: (id: string) => void
  onAgentSelect: (id: string) => void
}) {
  // Pre-compute agents grouped by crew to avoid O(crews × agents) per render
  const agentsByCrew = useMemo(() => {
    const map: Record<string, AgentData[]> = {}
    for (const agent of agents) {
      if (agent.crew_id) {
        ;(map[agent.crew_id] ||= []).push(agent)
      }
    }
    return map
  }, [agents])

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
          const crewAgents = agentsByCrew[crew.id] || []
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
                  className="h-8 w-8 rounded-lg flex items-center justify-center text-white text-[12px] font-bold shrink-0"
                  style={{ backgroundColor: getCrewDotColor(crew.color) }}
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

/** Health overview tab */
function HealthOverview({ crews, agents }: { crews: CrewData[]; agents: AgentData[] }) {
  const agentsByCrew = useMemo(() => {
    const map: Record<string, AgentData[]> = {}
    for (const agent of agents) {
      if (agent.crew_id) {
        ;(map[agent.crew_id] ||= []).push(agent)
      }
    }
    return map
  }, [agents])

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
            const crewAgents = agentsByCrew[crew.id] || []
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

/** Live activity feed for bottom drawer — shows agent status changes via WebSocket */
function FleetActivityFeed({ agents }: { agents: AgentData[] }) {
  interface ActivityEntry { ts: string; agent: string; avatarSeed: string; avatarStyle?: string | null; content: string; type: "status" | "run" | "mission" }
  const [entries, setEntries] = useState<ActivityEntry[]>([])
  const endRef = useRef<HTMLDivElement>(null)

  const handleAgentStatus = useCallback((ev: RealtimeEvent) => {
    const slug = (ev.payload.agent_slug ?? ev.payload.slug ?? "") as string
    const status = (ev.payload.status ?? "") as string
    if (!slug || !status) return
    const agent = agents.find((a) => a.slug === slug)
    setEntries((prev) => [...prev.slice(-100), {
      ts: new Date().toISOString(), agent: slug,
      avatarSeed: agent?.avatar_seed || slug, avatarStyle: agent?.avatar_style,
      content: `Status → ${status}`, type: "status",
    }])
  }, [agents])

  const handleRunEvent = useCallback((ev: RealtimeEvent) => {
    const slug = (ev.payload.agent_slug ?? "") as string
    const status = (ev.payload.status ?? ev.type?.split(".")[1] ?? "") as string
    if (!slug) return
    const agent = agents.find((a) => a.slug === slug)
    setEntries((prev) => [...prev.slice(-100), {
      ts: new Date().toISOString(), agent: slug,
      avatarSeed: agent?.avatar_seed || slug, avatarStyle: agent?.avatar_style,
      content: `Run ${status}`, type: "run",
    }])
  }, [agents])

  const handleMissionUpdate = useCallback((ev: RealtimeEvent) => {
    const title = (ev.payload.title ?? "") as string
    const status = (ev.payload.status ?? "") as string
    if (!title) return
    setEntries((prev) => [...prev.slice(-100), {
      ts: new Date().toISOString(), agent: (ev.payload.lead_agent_slug ?? "") as string,
      avatarSeed: (ev.payload.lead_agent_slug ?? "mission") as string, avatarStyle: null,
      content: `Mission "${title}" → ${status}`, type: "mission",
    }])
  }, [])

  useRealtimeEvent("agent.status", handleAgentStatus)
  useRealtimeEvent("run.started", handleRunEvent)
  useRealtimeEvent("run.completed", handleRunEvent)
  useRealtimeEvent("run.failed", handleRunEvent)
  useRealtimeEvent("mission.updated", handleMissionUpdate)

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" })
  }, [entries])

  const typeColors: Record<string, string> = {
    status: "text-blue-400", run: "text-emerald-400", mission: "text-purple-400",
  }

  if (entries.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground/50">
        <Activity className="h-5 w-5 mb-1.5" />
        <p className="text-[11px]">Waiting for activity...</p>
        <p className="text-[10px] text-muted-foreground/30 mt-0.5">Agent status changes and run events appear here in real-time</p>
      </div>
    )
  }

  return (
    <div className="font-mono text-[11px] px-3 py-1 h-full overflow-y-auto">
      {entries.map((entry, i) => (
        <div key={i} className="flex items-center gap-2 py-0.5 hover:bg-white/[0.02]">
          <span className="text-muted-foreground/40 tabular-nums shrink-0 w-[52px]">{entry.ts.slice(11, 19)}</span>
          <span className={cn("text-[10px] px-1 rounded bg-white/[0.03] shrink-0", typeColors[entry.type] || "text-muted-foreground")}>
            {entry.type}
          </span>
          <img src={getAgentAvatarUrl(entry.avatarSeed, entry.avatarStyle)} alt="" className="w-3.5 h-3.5 rounded-full shrink-0" />
          <span className="text-muted-foreground shrink-0 w-[60px] truncate">@{entry.agent}</span>
          <span className="text-foreground/80 truncate">{entry.content}</span>
        </div>
      ))}
      <div ref={endRef} />
    </div>
  )
}
