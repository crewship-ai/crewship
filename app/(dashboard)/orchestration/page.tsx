"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Workflow, Clock, Activity, RefreshCw, Focus, LayoutTemplate, CheckCircle2, AlertTriangle, Settings2 } from "lucide-react"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { StatCard } from "@/components/layout/stat-card"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
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
import type { Mission, MissionTask } from "@/lib/types/mission"

export default function OrchestrationPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [missions, setMissions] = useState<Mission[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedMissionId, setSelectedMissionId] = useState<string>("all")
  const [selectedTask, setSelectedTask] = useState<MissionTask | null>(null)
  const [statusFilter, setStatusFilter] = useState<string | null>(null)
  const graphRef = useRef<WorkflowGraphRef>(null)

  const fetchMissions = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=50&include_tasks=true`)
      if (res.ok) {
        setMissions(await res.json())
      }
    } catch {
      // ignore
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    fetchMissions()
  }, [fetchMissions])

  // Auto-poll every 3s when any mission is active (MissionEngine updates DB directly)
  const hasActive = useMemo(
    () => missions.some((m) => m.status === "IN_PROGRESS" || m.status === "REVIEW"),
    [missions]
  )
  useEffect(() => {
    if (!hasActive) return
    const interval = setInterval(fetchMissions, 3000)
    return () => clearInterval(interval)
  }, [hasActive, fetchMissions])

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

  const handleMissionUpdate = useCallback(() => {
    fetchMissions()
  }, [fetchMissions])

  useRealtimeEvent("task.updated", handleTaskUpdate)
  useRealtimeEvent("mission.updated", handleMissionUpdate)

  const filteredMissions = useMemo(() => {
    let result = missions
    if (selectedMissionId !== "all") {
      result = result.filter((m) => m.id === selectedMissionId)
    }
    if (statusFilter) {
      result = result.filter((m) => m.status === statusFilter)
    }
    return result
  }, [missions, selectedMissionId, statusFilter])

  const selectedMission = useMemo(() => {
    if (selectedMissionId === "all") return null
    return missions.find((m) => m.id === selectedMissionId) || null
  }, [missions, selectedMissionId])

  // Find mission for selected task
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
      <div className="p-6 space-y-6">
        <Skeleton className="h-10 w-64" />
        <div className="grid grid-cols-4 gap-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-24" />
          ))}
        </div>
        <Skeleton className="h-[600px]" />
      </div>
    )
  }

  return (
    <div className="p-6 space-y-5">
      <PageHeader
        title="Orchestration"
        description="Real-time workflow visualization and mission coordination"
      >
        <div className="flex items-center gap-2">
          <Select value={selectedMissionId} onValueChange={(v) => { setSelectedMissionId(v); setStatusFilter(null) }}>
            <SelectTrigger className="w-[200px] h-8 text-xs">
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
          <Button variant="outline" size="sm" onClick={() => graphRef.current?.focusActive()}>
            <Focus className="h-4 w-4 mr-1" />
            Focus Active
          </Button>
          <Button variant="outline" size="sm" onClick={fetchMissions}>
            <RefreshCw className="h-4 w-4" />
          </Button>
          {workspaceId && (
            <CreateMissionWizard workspaceId={workspaceId} onCreated={fetchMissions} />
          )}
        </div>
      </PageHeader>

      {/* Stat cards — clickable to filter */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
        <button onClick={() => { setStatusFilter(statusFilter === "IN_PROGRESS" ? null : "IN_PROGRESS"); setSelectedMissionId("all") }} className="text-left">
          <StatCard
            title="Active"
            value={stats.active}
            subtitle="Currently running"
            icon={Workflow}
            iconClassName={cn("bg-blue-500/10 text-blue-500", statusFilter === "IN_PROGRESS" && "ring-2 ring-blue-500/40")}
            className={cn(statusFilter === "IN_PROGRESS" && "ring-1 ring-blue-500/30",
              stats.active > 0 && "border-blue-500/20")}
          />
        </button>
        <button onClick={() => { setStatusFilter(statusFilter === "PLANNING" ? null : "PLANNING"); setSelectedMissionId("all") }} className="text-left">
          <StatCard
            title="Planning"
            value={stats.planning}
            subtitle="Being planned"
            icon={Clock}
            iconClassName={cn("bg-purple-500/10 text-purple-500", statusFilter === "PLANNING" && "ring-2 ring-purple-500/40")}
            className={cn(statusFilter === "PLANNING" && "ring-1 ring-purple-500/30")}
          />
        </button>
        <button onClick={() => { setStatusFilter(statusFilter === "COMPLETED" ? null : "COMPLETED"); setSelectedMissionId("all") }} className="text-left">
          <StatCard
            title="Completed"
            value={stats.completed}
            subtitle="Successfully finished"
            icon={CheckCircle2}
            iconClassName={cn("bg-green-500/10 text-green-500", statusFilter === "COMPLETED" && "ring-2 ring-green-500/40")}
            className={cn(statusFilter === "COMPLETED" && "ring-1 ring-green-500/30")}
          />
        </button>
        <button onClick={() => { setStatusFilter(statusFilter === "FAILED" ? null : "FAILED"); setSelectedMissionId("all") }} className="text-left">
          <StatCard
            title="Failed"
            value={stats.failed}
            subtitle="Need attention"
            icon={AlertTriangle}
            iconClassName={cn("bg-red-500/10 text-red-500", statusFilter === "FAILED" && "ring-2 ring-red-500/40")}
            className={cn(statusFilter === "FAILED" && "ring-1 ring-red-500/30")}
          />
        </button>
      </div>

      {/* Mission control bar when a specific mission is selected */}
      {selectedMission && (
        <MissionControlBar mission={selectedMission} onMissionChanged={fetchMissions} />
      )}

      <Tabs defaultValue="graph" className="space-y-3">
        <TabsList>
          <TabsTrigger value="graph" className="gap-1.5">
            <Workflow className="h-3.5 w-3.5" />
            Graph
          </TabsTrigger>
          <TabsTrigger value="timeline" className="gap-1.5">
            <Clock className="h-3.5 w-3.5" />
            Timeline
          </TabsTrigger>
          <TabsTrigger value="activity" className="gap-1.5">
            <Activity className="h-3.5 w-3.5" />
            Activity
          </TabsTrigger>
          <TabsTrigger value="templates" className="gap-1.5">
            <LayoutTemplate className="h-3.5 w-3.5" />
            Templates
          </TabsTrigger>
          <TabsTrigger value="connections" className="gap-1.5">
            <Settings2 className="h-3.5 w-3.5" />
            Connections
          </TabsTrigger>
        </TabsList>

        <TabsContent value="graph" className="mt-0">
          <WorkflowGraph ref={graphRef} missions={filteredMissions} onTaskClick={handleNodeClick} />
        </TabsContent>

        <TabsContent value="timeline" className="mt-0">
          <MissionTimeline missions={filteredMissions} />
        </TabsContent>

        <TabsContent value="activity" className="mt-0">
          <OrchestrationActivity missions={filteredMissions} />
        </TabsContent>

        <TabsContent value="templates" className="mt-0">
          <TemplateGallery workspaceId={workspaceId!} />
        </TabsContent>

        <TabsContent value="connections" className="mt-0">
          <CrewConnections workspaceId={workspaceId!} />
        </TabsContent>
      </Tabs>

      {/* Task detail sheet */}
      <TaskDetailSheet
        task={selectedTask}
        mission={taskMission}
        allTasks={taskMission?.tasks || []}
        onClose={() => setSelectedTask(null)}
        onTaskChanged={fetchMissions}
      />
    </div>
  )
}
