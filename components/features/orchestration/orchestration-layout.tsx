"use client"

import { useCallback, useMemo, useRef, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Workflow, Clock, Activity, RefreshCw, Focus, LayoutTemplate,
  Settings2, FileText, PanelLeftClose, PanelLeftOpen,
  MessageSquare, Terminal, FileCode2, Container,
  ChevronUp, ChevronDown, Play, Square, Loader2,
} from "lucide-react"
// Tabs replaced with custom nav for orchestration toolbar
import { Button } from "@/components/ui/button"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { WorkflowGraph, type WorkflowGraphRef } from "@/components/features/orchestration/workflow-graph"
import { MissionTimeline } from "@/components/features/orchestration/mission-timeline"
import { OrchestrationActivity } from "@/components/features/orchestration/orchestration-activity"
import { TemplateGallery } from "@/components/features/orchestration/template-gallery"
// MissionControlBar replaced by inline info strip in unified toolbar
import { CreateMissionWizard } from "@/components/features/orchestration/create-mission-wizard"
import { CrewConnections } from "@/components/features/orchestration/crew-connections"
import { ProposalReview } from "@/components/features/orchestration/proposal-review"
import { HierarchyTree } from "@/components/features/orchestration/hierarchy-tree"
import { UnifiedInbox } from "@/components/features/orchestration/unified-inbox"
import { ConnectionMap } from "@/components/features/orchestration/connection-map"
import { ContextDetailPanel, type DetailContext } from "@/components/features/orchestration/context-detail-panel"
import { A2AMessageStream } from "@/components/features/orchestration/a2a-message-stream"
import { MissionYamlEditor } from "@/components/features/orchestration/mission-yaml-editor"
import { DockerOverview } from "@/components/features/orchestration/docker-overview"
import type { Mission, MissionTask } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"

import { toast } from "sonner"

type DrawerTab = "messages" | "exec" | "yaml" | "docker"

/** Compact action button for the toolbar (Start/Cancel) */
function MissionActionButton({ mission, action, workspaceId, onDone }: {
  mission: Mission; action: "start" | "cancel"; workspaceId: string; onDone: () => void
}) {
  const [loading, setLoading] = useState(false)
  const handleClick = useCallback(async () => {
    setLoading(true)
    try {
      const qs = `?workspace_id=${encodeURIComponent(workspaceId)}`
      const res = action === "start"
        ? await fetch(`/api/v1/crews/${mission.crew_id}/missions/${mission.id}/start${qs}`, { method: "POST" })
        : await fetch(`/api/v1/crews/${mission.crew_id}/missions/${mission.id}${qs}`, {
            method: "PATCH", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ status: "CANCELLED" }),
          })
      if (!res.ok) { const b = await res.json().catch(() => null); toast.error(b?.detail ?? `Failed to ${action}`); return }
      toast.success(action === "start" ? "Mission started" : "Mission cancelled")
      onDone()
    } catch { toast.error(`Failed to ${action}`) } finally { setLoading(false) }
  }, [mission.id, mission.crew_id, workspaceId, action, onDone])

  if (action === "start") {
    return (
      <button onClick={handleClick} disabled={loading} className="inline-flex items-center gap-1 h-[22px] px-2 rounded-[3px] text-[11.5px] font-medium bg-blue-500/15 border border-blue-500/35 text-blue-400 hover:bg-blue-500/25 transition-colors disabled:opacity-50">
        {loading ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
        Start
      </button>
    )
  }
  return (
    <button onClick={handleClick} disabled={loading} className="inline-flex items-center gap-1 h-[22px] px-2 rounded-[3px] text-[11.5px] font-medium bg-red-500/10 border border-red-500/30 text-red-400 hover:bg-red-500/20 transition-colors disabled:opacity-50">
      {loading ? <Loader2 className="h-3 w-3 animate-spin" /> : <Square className="h-3 w-3" />}
      Cancel
    </button>
  )
}

export interface OrchestrationLayoutProps {
  missions: Mission[]
  crews: CrewSummary[]
  agents: AgentSummary[]
  connections: CrewConnection[]
  workspaceId: string
  selectedMissionId: string
  onMissionChange: (missionId: string) => void
  onRefresh: () => void
  onMissionCreated: () => void
}

export function OrchestrationLayout({
  missions,
  crews,
  agents,
  connections,
  workspaceId,
  selectedMissionId,
  onMissionChange,
  onRefresh,
  onMissionCreated,
}: OrchestrationLayoutProps) {
  // Panel state
  const [leftCollapsed, setLeftCollapsed] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [drawerTab, setDrawerTab] = useState<DrawerTab>("messages")

  // Content state
  const [activeTab, setActiveTab] = useState("graph")
  const [_selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const [selectedCrewId, setSelectedCrewId] = useState<string | null>(null)
  const [selectedAgentSlug, setSelectedAgentSlug] = useState<string | null>(null)
  const [detailContext, setDetailContext] = useState<DetailContext>({ type: "none" })

  // A2A message filter
  const [a2aCrewFilter, setA2aCrewFilter] = useState<string | null>(null)

  const graphRef = useRef<WorkflowGraphRef>(null)

  // Derived data
  const filteredMissions = useMemo(() => {
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [missions, selectedMissionId])

  const selectedMission = useMemo(() => {
    if (selectedMissionId === "all") return null
    return missions.find((m) => m.id === selectedMissionId) || null
  }, [missions, selectedMissionId])

  const stats = useMemo(() => ({
    active: missions.filter((m) => m.status === "IN_PROGRESS").length,
    planning: missions.filter((m) => m.status === "PLANNING").length,
    completed: missions.filter((m) => m.status === "COMPLETED").length,
    failed: missions.filter((m) => m.status === "FAILED").length,
  }), [missions])

  // Left panel filtered by selected mission
  const panelCrews = useMemo(() => {
    if (selectedMissionId === "all") return crews
    const mission = missions.find((m) => m.id === selectedMissionId)
    if (!mission) return crews
    const crewIds = new Set<string>()
    crewIds.add(mission.crew_id)
    for (const task of mission.tasks || []) {
      const agent = agents.find((a) => a.slug === task.agent_slug)
      if (agent?.crew_id) crewIds.add(agent.crew_id)
    }
    return crews.filter((c) => crewIds.has(c.id))
  }, [selectedMissionId, missions, crews, agents])

  const panelAgents = useMemo(() => {
    if (selectedMissionId === "all") return agents
    const crewIds = new Set(panelCrews.map((c) => c.id))
    return agents.filter((a) => a.crew_id && crewIds.has(a.crew_id))
  }, [selectedMissionId, panelCrews, agents])

  const panelConnections = useMemo(() => {
    if (selectedMissionId === "all") return connections
    const crewIds = new Set(panelCrews.map((c) => c.id))
    return connections.filter((c) => crewIds.has(c.from_crew_id) && crewIds.has(c.to_crew_id))
  }, [selectedMissionId, panelCrews, connections])

  const panelMissions = useMemo(() => {
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [selectedMissionId, missions])

  // Handlers
  const handleNodeClick = useCallback((task: MissionTask) => {
    setSelectedTask(task)
    const mission = missions.find((m) => m.tasks?.some((t) => t.id === task.id))
    if (mission) {
      setDetailContext({
        type: "task",
        task,
        mission,
        allTasks: mission.tasks || [],
      })
    }
  }, [missions])

  const handleCrewSelect = useCallback((crewId: string) => {
    setSelectedCrewId(crewId)
    setSelectedAgentSlug(null)
    const crew = crews.find((c) => c.id === crewId)
    if (crew) {
      setDetailContext({
        type: "crew",
        crew,
        agents,
        connections,
      })
    }
  }, [crews, agents, connections])

  const handleAgentSelect = useCallback((agentSlug: string) => {
    setSelectedAgentSlug(agentSlug)
  }, [])

  const handleInboxTaskSelect = useCallback((task: MissionTask, mission: Mission) => {
    setSelectedTask(task)
    setDetailContext({
      type: "task",
      task,
      mission,
      allTasks: mission.tasks || [],
    })
  }, [])

  const handleDetailClose = useCallback(() => {
    setDetailContext({ type: "none" })
    setSelectedTask(null)
  }, [])

  const handleDrawerTabClick = useCallback((tab: DrawerTab) => {
    if (drawerOpen && drawerTab === tab) {
      setDrawerOpen(false)
    } else {
      setDrawerTab(tab)
      setDrawerOpen(true)
    }
  }, [drawerOpen, drawerTab])

  const showRightPanel = detailContext.type !== "none"

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Unified toolbar ---- */}
      <div className="shrink-0 z-20 bg-card">
        {/* Row 1: Mission selector | Tabs | Utility buttons (32px) */}
        <div className="flex items-center h-8 border-b border-white/[0.08]">
          {/* Mission selector — single dot, no border trigger */}
          <Select value={selectedMissionId} onValueChange={onMissionChange}>
            <SelectTrigger className="h-full w-auto max-w-[280px] text-[12.5px] font-medium bg-transparent border-none shadow-none rounded-none text-foreground hover:bg-white/[0.04] transition-colors px-3 gap-2 shrink-0">
              <SelectValue placeholder="All missions" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All missions</SelectItem>
              {missions.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  <div className="flex items-center gap-2">
                    <div className={cn(
                      "w-1.5 h-1.5 rounded-full shrink-0",
                      m.status === "IN_PROGRESS" && "bg-blue-500",
                      m.status === "PLANNING" && "bg-purple-500",
                      m.status === "COMPLETED" && "bg-green-500",
                      m.status === "FAILED" && "bg-red-500",
                      m.status === "REVIEW" && "bg-amber-500",
                    )} />
                    <span className="truncate">{m.title}</span>
                  </div>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <div className="w-px h-4 bg-white/[0.08] shrink-0" />

          {/* Tab navigation */}
          <nav className="flex items-stretch h-full flex-1 pl-1">
            {([
              { id: "graph", label: "Graph", icon: Workflow },
              { id: "timeline", label: "Timeline", icon: Clock },
              { id: "activity", label: "Activity", icon: Activity },
              { id: "templates", label: "Templates", icon: LayoutTemplate },
              { id: "proposals", label: "Proposals", icon: FileText },
              { id: "connections", label: "Connections", icon: Settings2 },
            ] as const).map(({ id, label, icon: Icon }) => (
              <button
                key={id}
                onClick={() => setActiveTab(id)}
                className={cn(
                  "flex items-center gap-1.5 px-2.5 text-[12px] font-medium border-b-2 transition-all duration-100 relative top-px",
                  activeTab === id
                    ? "border-blue-400 text-blue-400"
                    : "border-transparent text-muted-foreground hover:text-foreground/80",
                )}
              >
                <Icon className="h-3 w-3 opacity-75" />
                {label}
              </button>
            ))}
          </nav>

          {/* Utility buttons */}
          <div className="flex items-center gap-1 px-2 shrink-0">
            <Button variant="ghost" size="sm" className="h-6 w-6 p-0 text-muted-foreground hover:text-foreground/80" onClick={() => graphRef.current?.focusActive()}>
              <Focus className="h-3 w-3" />
            </Button>
            <Button variant="ghost" size="sm" className="h-6 w-6 p-0 text-muted-foreground hover:text-foreground/80" onClick={onRefresh}>
              <RefreshCw className="h-3 w-3" />
            </Button>
            <CreateMissionWizard workspaceId={workspaceId} onCreated={onMissionCreated} />
          </div>
        </div>

        {/* Row 2: Info strip (24px) — stats or mission detail + actions */}
        <div className="flex items-center justify-between h-6 border-b border-white/[0.06] px-4 font-mono text-[11px] text-muted-foreground overflow-hidden">
          <div className="flex items-center gap-0">
            {!selectedMission ? (
              <>
                {[
                  { label: "Active", value: stats.active, color: "bg-blue-500", tc: stats.active > 0 ? "text-blue-400" : "" },
                  { label: "Planning", value: stats.planning, color: "bg-purple-500", tc: stats.planning > 0 ? "text-purple-400" : "" },
                  { label: "Done", value: stats.completed, color: "bg-green-500", tc: stats.completed > 0 ? "text-green-400" : "" },
                  { label: "Failed", value: stats.failed, color: "bg-red-500", tc: stats.failed > 0 ? "text-red-400" : "" },
                ].map(({ label, value, color, tc }, i) => (
                  <div key={label} className={cn("flex items-center gap-1.5 shrink-0", i > 0 && "ml-4 pl-4 border-l border-white/[0.06]")}>
                    <div className={cn("w-1.5 h-1.5 rounded-full", color, value === 0 && "opacity-30")} />
                    <span className={cn("tabular-nums", tc)}>{value}</span>
                    <span className="text-muted-foreground/50 font-sans">{label}</span>
                  </div>
                ))}
              </>
            ) : (
              <>
                <span className="font-sans text-muted-foreground/50 mr-1">Lead</span>
                <span className="pr-3 mr-3 border-r border-white/[0.06]">@{selectedMission.lead_agent_slug}</span>
                <div className="w-[52px] h-1 bg-white/[0.08] overflow-hidden mr-1.5">
                  <div className="h-full bg-blue-400 transition-all" style={{ width: `${selectedMission.tasks?.length ? ((selectedMission.tasks.filter(t => t.status === "COMPLETED").length / selectedMission.tasks.length) * 100) : 0}%` }} />
                </div>
                <span className="tabular-nums pr-3 mr-3 border-r border-white/[0.06]">
                  {selectedMission.tasks?.filter(t => t.status === "COMPLETED").length || 0}/{selectedMission.tasks?.length || 0}
                  {(selectedMission.tasks?.filter(t => t.status === "IN_PROGRESS").length || 0) > 0 && (
                    <span className="text-blue-400 ml-1">({selectedMission.tasks?.filter(t => t.status === "IN_PROGRESS").length} running)</span>
                  )}
                </span>
                {(() => { const t = selectedMission.tasks || []; const tok = t.reduce((s, x) => s + (x.token_count || 0), 0); const cost = t.reduce((s, x) => s + (x.estimated_cost || 0), 0); return tok > 0 ? (
                  <span className="tabular-nums">{(tok / 1000).toFixed(1)}k tok{cost > 0 && ` · $${cost.toFixed(2)}`}</span>
                ) : null })()}
              </>
            )}
          </div>
          {/* Mission actions in info strip */}
          {selectedMission && (
            <div className="flex items-center gap-1 shrink-0">
              {selectedMission.status === "PLANNING" && (
                <MissionActionButton mission={selectedMission} action="start" workspaceId={workspaceId} onDone={onRefresh} />
              )}
              {(selectedMission.status === "PLANNING" || selectedMission.status === "IN_PROGRESS") && (
                <MissionActionButton mission={selectedMission} action="cancel" workspaceId={workspaceId} onDone={onRefresh} />
              )}
            </div>
          )}
        </div>
      </div>

      {/* ---- Main 3-column layout ---- */}
      <div
        className="flex-1 min-h-0 grid transition-all duration-200"
        style={{
          gridTemplateColumns: `${leftCollapsed ? "48px" : "260px"} 1fr ${showRightPanel ? "380px" : "0px"}`,
          gridTemplateRows: "1fr auto",
        }}
      >
        {/* ---- Left panel ---- */}
        <div className={cn(
          "row-span-1 border-r border-white/[0.1] bg-card flex flex-col min-h-0 transition-all duration-200 overflow-hidden",
        )}>
          {/* Toggle */}
          <div className="flex items-center justify-between px-2 py-1.5 border-b border-border shrink-0">
            {!leftCollapsed && (
              <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
                Explorer
              </span>
            )}
            <Button
              variant="ghost"
              size="icon-xs"
              className="text-muted-foreground/70 hover:text-foreground/70 ml-auto"
              onClick={() => setLeftCollapsed(!leftCollapsed)}
            >
              {leftCollapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
            </Button>
          </div>

          <AnimatePresence mode="wait">
            {!leftCollapsed && (
              <motion.div
                key={selectedMissionId}
                initial={{ opacity: 0, x: -8 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: -8 }}
                transition={{ duration: 0.2, ease: "easeOut" }}
                className="flex-1 min-h-0 flex flex-col"
              >
                {/* Hierarchy tree */}
                <div className="border-b border-border shrink-0 max-h-[40%] overflow-y-auto">
                  <HierarchyTree
                    crews={panelCrews}
                    agents={panelAgents}
                    selectedCrewId={selectedCrewId}
                    selectedAgentSlug={selectedAgentSlug}
                    onCrewSelect={handleCrewSelect}
                    onAgentSelect={handleAgentSelect}
                  />
                </div>

                {/* Unified Inbox */}
                <div className="border-b border-border flex-1 min-h-0 flex flex-col">
                  <UnifiedInbox
                    missions={panelMissions}
                    onTaskSelect={handleInboxTaskSelect}
                  />
                </div>

                {/* Connection Map */}
                <div className="p-2 shrink-0">
                  <div className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider px-1 mb-1">
                    Connections
                  </div>
                  <ConnectionMap
                    crews={panelCrews}
                    connections={panelConnections}
                  />
                </div>
              </motion.div>
            )}
          </AnimatePresence>
        </div>

        {/* ---- Center content area ---- */}
        <div className="row-span-1 relative overflow-hidden min-h-0">
          {activeTab === "graph" && (
            <>
              <WorkflowGraph
                ref={graphRef}
                missions={filteredMissions}
                crews={crews}
                agents={agents}
                connections={connections}
                onTaskClick={handleNodeClick}
              />

            </>
          )}

          <AnimatePresence mode="wait">
            {activeTab === "timeline" && (
              <motion.div key="timeline" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <MissionTimeline missions={filteredMissions} />
              </motion.div>
            )}

            {activeTab === "activity" && (
              <motion.div key="activity" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <OrchestrationActivity missions={filteredMissions} />
              </motion.div>
            )}

            {activeTab === "templates" && (
              <motion.div key="templates" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <TemplateGallery workspaceId={workspaceId} />
              </motion.div>
            )}

            {activeTab === "proposals" && (
              <motion.div key="proposals" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <ProposalReview workspaceId={workspaceId} />
              </motion.div>
            )}

            {activeTab === "connections" && (
              <motion.div key="connections" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.15 }} className="p-4 h-full overflow-auto">
                <CrewConnections workspaceId={workspaceId} />
              </motion.div>
            )}
          </AnimatePresence>
        </div>

        {/* ---- Right panel ---- */}
        <div className={cn(
          "row-span-1 transition-all duration-200 overflow-hidden min-h-0",
          showRightPanel ? "w-[380px]" : "w-0",
        )}>
          <AnimatePresence mode="wait">
            {showRightPanel && (
              <motion.div
                key={detailContext.type === "task" ? `task-${(detailContext as { task: MissionTask }).task.id}` : detailContext.type}
                initial={{ opacity: 0, x: 12 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: 12 }}
                transition={{ duration: 0.15, ease: "easeOut" }}
                className="h-full"
              >
                <ContextDetailPanel
                  context={detailContext}
                  onClose={handleDetailClose}
                />
              </motion.div>
            )}
          </AnimatePresence>
        </div>

        {/* ---- Bottom drawer ---- */}
        <motion.div
          className="col-span-3 border-t border-white/[0.1] bg-card flex flex-col overflow-hidden"
          animate={{ height: drawerOpen ? 240 : 32 }}
          transition={{ duration: 0.2, ease: "easeInOut" }}
        >
          {/* Drawer tab bar */}
          <div
            className="flex items-center gap-0 px-2 shrink-0 h-8 cursor-pointer select-none"
            onClick={() => {
              if (!drawerOpen) setDrawerOpen(true)
            }}
          >
            {([
              { id: "messages" as const, label: "Messages", icon: MessageSquare },
              { id: "exec" as const, label: "Exec Log", icon: Terminal },
              { id: "yaml" as const, label: "YAML", icon: FileCode2 },
              { id: "docker" as const, label: "Docker", icon: Container },
            ]).map(({ id, label, icon: Icon }) => (
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
                  handleDrawerTabClick(id)
                }}
              >
                <Icon className="h-3 w-3" />
                {label}
              </button>
            ))}

            <div className="ml-auto">
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground/70 hover:text-foreground/70"
                onClick={(e) => {
                  e.stopPropagation()
                  setDrawerOpen(!drawerOpen)
                }}
              >
                {drawerOpen ? <ChevronDown className="h-3 w-3" /> : <ChevronUp className="h-3 w-3" />}
              </Button>
            </div>
          </div>

          {/* Drawer content */}
          <AnimatePresence mode="wait">
            {drawerOpen && (
              <motion.div
                key={drawerTab}
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                exit={{ opacity: 0, y: 8 }}
                transition={{ duration: 0.15 }}
                className="flex-1 min-h-0 border-t border-border"
              >
                {drawerTab === "messages" && (
                  <A2AMessageStream
                    messages={[]}
                    crewFilter={a2aCrewFilter}
                    onFilterChange={setA2aCrewFilter}
                  />
                )}

                {drawerTab === "exec" && (
                  <div className="flex flex-col items-center justify-center h-full text-muted-foreground/70">
                    <Terminal className="h-6 w-6 mb-2" />
                    <p className="text-xs">Exec log coming soon</p>
                  </div>
                )}

                {drawerTab === "yaml" && (
                  <MissionYamlEditor
                    mission={selectedMission}
                    readOnly
                  />
                )}

                {drawerTab === "docker" && (
                  <DockerOverview crews={crews} />
                )}
              </motion.div>
            )}
          </AnimatePresence>
        </motion.div>
      </div>
    </div>
  )
}
