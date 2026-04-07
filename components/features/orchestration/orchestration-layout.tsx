"use client"

import { useCallback, useMemo, useRef, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  Workflow, Clock, Activity, RefreshCw, Focus, LayoutTemplate,
  Settings2, FileText, PanelLeftClose, PanelLeftOpen,
  MessageSquare, Terminal, FileCode2, Container,
  ChevronUp, ChevronDown,
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
import { MissionControlBar } from "@/components/features/orchestration/mission-control-bar"
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

type DrawerTab = "messages" | "exec" | "yaml" | "docker"

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
    <div className="dark flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Top toolbar ---- */}
      <div className="flex items-center justify-between px-4 py-1.5 border-b border-border bg-card shrink-0 z-20">
        <div className="flex items-center gap-3">
          <h1 className="text-sm font-semibold text-foreground">Orchestration</h1>
          <nav className="flex items-center gap-0.5 bg-accent/50 rounded-lg p-0.5">
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
                  "flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium transition-all duration-150",
                  activeTab === id
                    ? "bg-blue-500/15 text-blue-400 shadow-sm"
                    : "text-muted-foreground hover:text-foreground/80 hover:bg-accent",
                )}
              >
                <Icon className="h-3 w-3" />
                {label}
              </button>
            ))}
          </nav>
        </div>

        <div className="flex items-center gap-2">
          <Select value={selectedMissionId} onValueChange={onMissionChange}>
            <SelectTrigger className="w-[180px] h-7 text-xs bg-accent/50 border-border">
              <SelectValue placeholder="All missions" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All missions</SelectItem>
              {missions.map((m) => (
                <SelectItem key={m.id} value={m.id}>
                  <span className="truncate">{m.title}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-muted-foreground hover:text-foreground/80"
            onClick={() => graphRef.current?.focusActive()}
          >
            <Focus className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-muted-foreground hover:text-foreground/80"
            onClick={onRefresh}
          >
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
          <CreateMissionWizard workspaceId={workspaceId} onCreated={onMissionCreated} />
        </div>
      </div>

      {/* ---- Mission control bar ---- */}
      {selectedMission && (
        <div className="shrink-0">
          <MissionControlBar
            mission={selectedMission}
            workspaceId={workspaceId}
            onMissionChanged={onRefresh}
          />
        </div>
      )}

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
          "row-span-1 border-r border-border bg-card flex flex-col min-h-0 transition-all duration-200 overflow-hidden",
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

              {/* Floating stats overlay */}
              <div className="absolute top-3 left-3 flex items-center gap-1.5 z-10">
                {[
                  { key: "active", value: stats.active, label: "Active", color: "blue" },
                  { key: "planning", value: stats.planning, label: "Planning", color: "purple" },
                  { key: "completed", value: stats.completed, label: "Done", color: "green" },
                  { key: "failed", value: stats.failed, label: "Failed", color: "red" },
                ].map(({ key, value, label, color }) => (
                  <div
                    key={key}
                    className={cn(
                      "flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium",
                      "bg-card/90 backdrop-blur-sm border border-border",
                      "transition-all cursor-default",
                      value > 0 && color === "blue" && "border-blue-500/30 text-blue-400",
                      value > 0 && color === "green" && "border-green-500/30 text-green-400",
                      value > 0 && color === "red" && "border-red-500/30 text-red-400",
                      value > 0 && color === "purple" && "border-purple-500/30 text-purple-400",
                      value === 0 && "text-muted-foreground/70",
                    )}
                  >
                    <div className={cn(
                      "w-1.5 h-1.5 rounded-full",
                      color === "blue" && (value > 0 ? "bg-blue-500 animate-pulse" : "bg-blue-500/30"),
                      color === "purple" && (value > 0 ? "bg-purple-500" : "bg-purple-500/30"),
                      color === "green" && (value > 0 ? "bg-green-500" : "bg-green-500/30"),
                      color === "red" && (value > 0 ? "bg-red-500" : "bg-red-500/30"),
                    )} />
                    <span>{value}</span>
                    <span className="text-muted-foreground/70">{label}</span>
                  </div>
                ))}
              </div>
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
          className="col-span-3 border-t border-border bg-card flex flex-col overflow-hidden"
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
