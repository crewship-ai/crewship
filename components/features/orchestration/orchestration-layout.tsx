"use client"

import { useCallback, useMemo, useRef, useState } from "react"
import {
  Workflow, Clock, Activity, RefreshCw, Focus, LayoutTemplate,
  Settings2, FileText, PanelLeftClose, PanelLeftOpen,
  MessageSquare, Terminal, FileCode2, Container,
  ChevronUp, ChevronDown,
} from "lucide-react"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { ScrollArea } from "@/components/ui/scroll-area"
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
    <div className="flex flex-col h-[calc(100vh-48px)] bg-[#0a0c10]">
      {/* ---- Top toolbar ---- */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-white/[0.06] bg-[#0a0c10]/80 backdrop-blur-sm shrink-0 z-20">
        <div className="flex items-center gap-3">
          <h1 className="text-sm font-semibold text-white/80">Orchestration</h1>
          <Tabs value={activeTab} onValueChange={setActiveTab}>
            <TabsList className="h-7">
              <TabsTrigger value="graph" className="text-xs h-6 px-2.5 gap-1">
                <Workflow className="h-3 w-3" /> Graph
              </TabsTrigger>
              <TabsTrigger value="timeline" className="text-xs h-6 px-2.5 gap-1">
                <Clock className="h-3 w-3" /> Timeline
              </TabsTrigger>
              <TabsTrigger value="activity" className="text-xs h-6 px-2.5 gap-1">
                <Activity className="h-3 w-3" /> Activity
              </TabsTrigger>
              <TabsTrigger value="templates" className="text-xs h-6 px-2.5 gap-1">
                <LayoutTemplate className="h-3 w-3" /> Templates
              </TabsTrigger>
              <TabsTrigger value="proposals" className="text-xs h-6 px-2.5 gap-1">
                <FileText className="h-3 w-3" /> Proposals
              </TabsTrigger>
              <TabsTrigger value="connections" className="text-xs h-6 px-2.5 gap-1">
                <Settings2 className="h-3 w-3" /> Connections
              </TabsTrigger>
            </TabsList>
          </Tabs>
        </div>

        <div className="flex items-center gap-2">
          <Select value={selectedMissionId} onValueChange={onMissionChange}>
            <SelectTrigger className="w-[180px] h-7 text-xs bg-white/[0.03] border-white/[0.08]">
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
            className="h-7 px-2 text-white/40 hover:text-white/70"
            onClick={() => graphRef.current?.focusActive()}
          >
            <Focus className="h-3.5 w-3.5" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2 text-white/40 hover:text-white/70"
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
          "row-span-1 border-r border-white/[0.06] bg-[#0d0f14] flex flex-col min-h-0 transition-all duration-200 overflow-hidden",
        )}>
          {/* Toggle */}
          <div className="flex items-center justify-between px-2 py-1.5 border-b border-white/[0.06] shrink-0">
            {!leftCollapsed && (
              <span className="text-[10px] font-semibold text-white/40 uppercase tracking-wider">
                Explorer
              </span>
            )}
            <Button
              variant="ghost"
              size="icon-xs"
              className="text-white/30 hover:text-white/60 ml-auto"
              onClick={() => setLeftCollapsed(!leftCollapsed)}
            >
              {leftCollapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
            </Button>
          </div>

          {!leftCollapsed && (
            <ScrollArea className="flex-1 min-h-0">
              <div className="flex flex-col">
                {/* Hierarchy tree */}
                <div className="border-b border-white/[0.06]">
                  <HierarchyTree
                    crews={crews}
                    agents={agents}
                    selectedCrewId={selectedCrewId}
                    selectedAgentSlug={selectedAgentSlug}
                    onCrewSelect={handleCrewSelect}
                    onAgentSelect={handleAgentSelect}
                  />
                </div>

                {/* Unified Inbox */}
                <div className="border-b border-white/[0.06] max-h-[280px]">
                  <UnifiedInbox
                    missions={missions}
                    onTaskSelect={handleInboxTaskSelect}
                  />
                </div>

                {/* Connection Map */}
                <div className="p-2">
                  <div className="text-[10px] font-semibold text-white/40 uppercase tracking-wider px-1 mb-1">
                    Connections
                  </div>
                  <ConnectionMap
                    crews={crews}
                    connections={connections}
                  />
                </div>
              </div>
            </ScrollArea>
          )}
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
                      "bg-[#0d0f14]/90 backdrop-blur-sm border border-white/[0.06]",
                      "transition-all cursor-default",
                      value > 0 && color === "blue" && "border-blue-500/30 text-blue-400",
                      value > 0 && color === "green" && "border-green-500/30 text-green-400",
                      value > 0 && color === "red" && "border-red-500/30 text-red-400",
                      value > 0 && color === "purple" && "border-purple-500/30 text-purple-400",
                      value === 0 && "text-white/30",
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
                    <span className="text-white/30">{label}</span>
                  </div>
                ))}
              </div>
            </>
          )}

          {activeTab === "timeline" && (
            <div className="p-4 h-full overflow-auto">
              <MissionTimeline missions={filteredMissions} />
            </div>
          )}

          {activeTab === "activity" && (
            <div className="p-4 h-full overflow-auto">
              <OrchestrationActivity missions={filteredMissions} />
            </div>
          )}

          {activeTab === "templates" && (
            <div className="p-4 h-full overflow-auto">
              <TemplateGallery workspaceId={workspaceId} />
            </div>
          )}

          {activeTab === "proposals" && (
            <div className="p-4 h-full overflow-auto">
              <ProposalReview workspaceId={workspaceId} />
            </div>
          )}

          {activeTab === "connections" && (
            <div className="p-4 h-full overflow-auto">
              <CrewConnections workspaceId={workspaceId} />
            </div>
          )}
        </div>

        {/* ---- Right panel ---- */}
        <div className={cn(
          "row-span-1 transition-all duration-200 overflow-hidden min-h-0",
          showRightPanel ? "w-[380px]" : "w-0",
        )}>
          {showRightPanel && (
            <ContextDetailPanel
              context={detailContext}
              onClose={handleDetailClose}
            />
          )}
        </div>

        {/* ---- Bottom drawer ---- */}
        <div
          className="col-span-3 border-t border-white/[0.06] bg-[#0d0f14] flex flex-col transition-all duration-200 overflow-hidden"
          style={{ height: drawerOpen ? 240 : 32 }}
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
                    ? "text-white/80 bg-white/[0.04]"
                    : "text-white/40 hover:text-white/60",
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
                className="text-white/30 hover:text-white/60"
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
          {drawerOpen && (
            <div className="flex-1 min-h-0 border-t border-white/[0.06]">
              {drawerTab === "messages" && (
                <A2AMessageStream
                  messages={[]}
                  crewFilter={a2aCrewFilter}
                  onFilterChange={setA2aCrewFilter}
                />
              )}

              {drawerTab === "exec" && (
                <div className="flex flex-col items-center justify-center h-full text-white/30">
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
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
