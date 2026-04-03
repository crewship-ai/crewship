"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  Workflow, Clock, Activity, RefreshCw, Focus, LayoutTemplate,
  Settings2, FileText, ChevronsUpDown, Check,
} from "lucide-react"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { WorkflowGraph, type WorkflowGraphRef } from "@/components/features/orchestration/workflow-graph"
import { MissionTimeline } from "@/components/features/orchestration/mission-timeline"
import { OrchestrationActivity } from "@/components/features/orchestration/orchestration-activity"
import { TemplateGallery } from "@/components/features/orchestration/template-gallery"
import { MissionControlBar } from "@/components/features/orchestration/mission-control-bar"
import { TaskDetailSheet } from "@/components/features/orchestration/task-detail-sheet"
import { CreateMissionWizard } from "@/components/features/orchestration/create-mission-wizard"
import { CrewConnections } from "@/components/features/orchestration/crew-connections"
import { ProposalReview } from "@/components/features/orchestration/proposal-review"
import { GraphLegend } from "@/components/features/orchestration/graph-legend"
import type { Mission, MissionTask } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"

export default function OrchestrationPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [missions, setMissions] = useState<Mission[]>([])
  const [crews, setCrews] = useState<CrewSummary[]>([])
  const [agents, setAgents] = useState<AgentSummary[]>([])
  const [connections, setConnections] = useState<CrewConnection[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedMissionId, setSelectedMissionId] = useState<string>("all")
  const [selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const [activeTab, setActiveTab] = useState("graph")
  const [missionPickerOpen, setMissionPickerOpen] = useState(false)
  const graphRef = useRef<WorkflowGraphRef>(null)

  const fetchData = useCallback(async () => {
    if (!workspaceId) return
    try {
      const [missionsRes, crewsRes, agentsRes, connsRes] = await Promise.all([
        fetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=50&include_tasks=true`),
        fetch(`/api/v1/crews?workspace_id=${workspaceId}`),
        fetch(`/api/v1/agents?workspace_id=${workspaceId}`),
        fetch(`/api/v1/crew-connections?workspace_id=${workspaceId}`),
      ])
      if (missionsRes.ok) setMissions(await missionsRes.json())
      if (crewsRes.ok) setCrews(await crewsRes.json())
      if (agentsRes.ok) setAgents(await agentsRes.json())
      if (connsRes.ok) setConnections(await connsRes.json())
    } catch {
      // ignore
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { fetchData() }, [fetchData])

  const hasActive = useMemo(
    () => missions.some((m) => m.status === "IN_PROGRESS" || m.status === "REVIEW"),
    [missions]
  )
  // Only poll missions during active execution (crews/agents/connections are stable)
  const fetchMissionsOnly = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=50&include_tasks=true`)
      if (res.ok) setMissions(await res.json())
    } catch { /* ignore */ }
  }, [workspaceId])

  useEffect(() => {
    if (!hasActive) return
    const interval = setInterval(fetchMissionsOnly, 3000)
    return () => clearInterval(interval)
  }, [hasActive, fetchMissionsOnly])

  const handleTaskUpdate = useCallback((event: RealtimeEvent) => {
    const { id, status, mission_id } = event.payload
    if (!id || !status) return
    setMissions((prev) =>
      prev.map((m) => {
        if (mission_id && m.id !== mission_id) return m
        const taskIdx = m.tasks?.findIndex((t) => t.id === id) ?? -1
        if (taskIdx === -1) return m
        const tasks = [...(m.tasks || [])]
        tasks[taskIdx] = { ...tasks[taskIdx], status: status as never }
        return { ...m, tasks }
      })
    )
  }, [])

  useRealtimeEvent("task.updated", handleTaskUpdate)
  useRealtimeEvent("mission.updated", useCallback(() => fetchData(), [fetchData]))

  // Keyboard shortcuts
  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      // Skip modified keys (Ctrl+R, Cmd+R, etc.) and auto-repeat
      if (e.repeat || e.ctrlKey || e.metaKey || e.altKey) return
      // Skip if user is typing in an input/textarea
      const tag = (e.target as HTMLElement)?.tagName
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return

      switch (e.key.toLowerCase()) {
        case "f":
          e.preventDefault()
          graphRef.current?.focusActive()
          break
        case "r":
          e.preventDefault()
          fetchData()
          break
        case "escape":
          setSelectedTask(null)
          break
        case "1": setActiveTab("graph"); break
        case "2": setActiveTab("timeline"); break
        case "3": setActiveTab("activity"); break
        case "4": setActiveTab("templates"); break
        case "5": setActiveTab("proposals"); break
        case "6": setActiveTab("connections"); break
      }
    }
    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [fetchData])

  // Group missions by status for the picker
  const missionGroups = useMemo(() => {
    const active = missions.filter((m) => m.status === "IN_PROGRESS" || m.status === "PLANNING" || m.status === "REVIEW")
    const completed = missions.filter((m) => m.status === "COMPLETED")
    const failed = missions.filter((m) => m.status === "FAILED" || m.status === "CANCELLED")
    return { active, completed, failed }
  }, [missions])

  const selectedMissionLabel = useMemo(() => {
    if (selectedMissionId === "all") return "All missions"
    const found = missions.find((m) => m.id === selectedMissionId)
    if (!found) {
      // Mission was deleted or workspace switched — reset
      setSelectedMissionId("all")
      return "All missions"
    }
    return found.title
  }, [missions, selectedMissionId])

  const filteredMissions = useMemo(() => {
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [missions, selectedMissionId])

  const selectedMission = useMemo(() => {
    if (selectedMissionId === "all") return null
    return missions.find((m) => m.id === selectedMissionId) || null
  }, [missions, selectedMissionId])

  const taskMission = useMemo(() => {
    if (!selectedTask) return null
    return missions.find((m) => m.tasks?.some((t) => t.id === selectedTask.id)) || null
  }, [missions, selectedTask])

  const stats = useMemo(() => ({
    active: missions.filter((m) => m.status === "IN_PROGRESS").length,
    planning: missions.filter((m) => m.status === "PLANNING").length,
    completed: missions.filter((m) => m.status === "COMPLETED").length,
    failed: missions.filter((m) => m.status === "FAILED").length,
  }), [missions])

  const handleNodeClick = useCallback((task: MissionTask) => {
    setSelectedTask(task)
  }, [])

  if (loading || wsLoading) {
    return (
      <div className="h-full flex items-center justify-center">
        <Skeleton className="h-[600px] w-full m-6 rounded-xl" />
      </div>
    )
  }

  return (
    <div className="flex flex-col h-[calc(100vh-48px)]">
      {/* Compact toolbar — n8n style */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-white/[0.06] bg-[#0a0c10]/80 backdrop-blur-sm shrink-0">
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
          <Popover open={missionPickerOpen} onOpenChange={setMissionPickerOpen}>
            <PopoverTrigger asChild>
              <Button
                variant="outline"
                role="combobox"
                aria-expanded={missionPickerOpen}
                className="w-[200px] h-7 text-xs bg-white/[0.03] border-white/[0.08] justify-between font-normal"
              >
                <span className="truncate">{selectedMissionLabel}</span>
                <ChevronsUpDown className="h-3 w-3 shrink-0 opacity-50" />
              </Button>
            </PopoverTrigger>
            <PopoverContent className="w-[260px] p-0" align="end">
              <Command>
                <CommandInput placeholder="Search missions..." className="h-8 text-xs" />
                <CommandList>
                  <CommandEmpty>No missions found.</CommandEmpty>
                  <CommandGroup>
                    <CommandItem
                      value="all"
                      onSelect={() => { setSelectedMissionId("all"); setMissionPickerOpen(false) }}
                    >
                      <Check className={cn("mr-2 h-3 w-3", selectedMissionId === "all" ? "opacity-100" : "opacity-0")} />
                      All missions
                    </CommandItem>
                  </CommandGroup>
                  {missionGroups.active.length > 0 && (
                    <CommandGroup heading="Active">
                      {missionGroups.active.map((m) => (
                        <CommandItem
                          key={m.id}
                          value={m.title}
                          onSelect={() => { setSelectedMissionId(m.id); setMissionPickerOpen(false) }}
                        >
                          <Check className={cn("mr-2 h-3 w-3", selectedMissionId === m.id ? "opacity-100" : "opacity-0")} />
                          <span className="truncate">{m.title}</span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  )}
                  {missionGroups.completed.length > 0 && (
                    <CommandGroup heading="Completed">
                      {missionGroups.completed.map((m) => (
                        <CommandItem
                          key={m.id}
                          value={m.title}
                          onSelect={() => { setSelectedMissionId(m.id); setMissionPickerOpen(false) }}
                        >
                          <Check className={cn("mr-2 h-3 w-3", selectedMissionId === m.id ? "opacity-100" : "opacity-0")} />
                          <span className="truncate">{m.title}</span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  )}
                  {missionGroups.failed.length > 0 && (
                    <CommandGroup heading="Failed">
                      {missionGroups.failed.map((m) => (
                        <CommandItem
                          key={m.id}
                          value={m.title}
                          onSelect={() => { setSelectedMissionId(m.id); setMissionPickerOpen(false) }}
                        >
                          <Check className={cn("mr-2 h-3 w-3", selectedMissionId === m.id ? "opacity-100" : "opacity-0")} />
                          <span className="truncate">{m.title}</span>
                        </CommandItem>
                      ))}
                    </CommandGroup>
                  )}
                </CommandList>
              </Command>
            </PopoverContent>
          </Popover>
          <Button variant="ghost" size="sm" aria-label="Focus active task" title="Focus active task (F)" className="h-7 px-2 text-white/40 hover:text-white/70" onClick={() => graphRef.current?.focusActive()}>
            <Focus className="h-3.5 w-3.5" />
          </Button>
          <Button variant="ghost" size="sm" aria-label="Refresh data" title="Refresh data (R)" className="h-7 px-2 text-white/40 hover:text-white/70" onClick={fetchData}>
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
          {workspaceId && (
            <CreateMissionWizard workspaceId={workspaceId} onCreated={fetchData} />
          )}
        </div>
      </div>

      {/* Mission control bar when selected */}
      {selectedMission && (
        <div className="shrink-0">
          <MissionControlBar mission={selectedMission} workspaceId={workspaceId!} onMissionChanged={fetchData} />
        </div>
      )}

      {/* Main content area — full height */}
      <div className="flex-1 relative overflow-hidden">
        {activeTab === "graph" && (
          <>
            {/* Graph — fills entire area */}
            <WorkflowGraph
              ref={graphRef}
              missions={filteredMissions}
              crews={crews}
              agents={agents}
              connections={connections}
              onTaskClick={handleNodeClick}
            />

            {/* Floating legend — bottom-left of graph */}
            <GraphLegend />

            {/* Floating stats overlay — top-left of graph */}
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
            <TemplateGallery workspaceId={workspaceId!} />
          </div>
        )}

        {activeTab === "proposals" && (
          <div className="p-4 h-full overflow-auto">
            <ProposalReview workspaceId={workspaceId!} />
          </div>
        )}

        {activeTab === "connections" && (
          <div className="p-4 h-full overflow-auto">
            <CrewConnections workspaceId={workspaceId!} />
          </div>
        )}
      </div>

      <TaskDetailSheet
        task={selectedTask}
        mission={taskMission}
        allTasks={taskMission?.tasks || []}
        workspaceId={workspaceId!}
        onClose={() => setSelectedTask(null)}
        onTaskChanged={fetchData}
      />
    </div>
  )
}
