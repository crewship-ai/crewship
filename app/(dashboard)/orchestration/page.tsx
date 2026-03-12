"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Workflow, Clock, Activity, RefreshCw, Focus, LayoutTemplate } from "lucide-react"
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
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { WorkflowGraph, type WorkflowGraphRef } from "@/components/features/orchestration/workflow-graph"
import { MissionTimeline } from "@/components/features/orchestration/mission-timeline"
import { OrchestrationActivity } from "@/components/features/orchestration/orchestration-activity"
import { TemplateGallery } from "@/components/features/orchestration/template-gallery"
import type { Mission } from "@/lib/types/mission"

export default function OrchestrationPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [missions, setMissions] = useState<Mission[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedMissionId, setSelectedMissionId] = useState<string>("all")
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

  // Granular WS update: patch single task status in local state
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

  // Mission-level events: full refetch (rare, important)
  const handleMissionUpdate = useCallback(() => {
    fetchMissions()
  }, [fetchMissions])

  useRealtimeEvent("task.updated", handleTaskUpdate)
  useRealtimeEvent("mission.updated", handleMissionUpdate)

  const filteredMissions = useMemo(() => {
    if (selectedMissionId === "all") return missions
    return missions.filter((m) => m.id === selectedMissionId)
  }, [missions, selectedMissionId])

  const stats = useMemo(() => ({
    active: missions.filter((m) => m.status === "IN_PROGRESS").length,
    planning: missions.filter((m) => m.status === "PLANNING").length,
    completed: missions.filter((m) => m.status === "COMPLETED").length,
    failed: missions.filter((m) => m.status === "FAILED").length,
  }), [missions])

  if (loading || wsLoading) {
    return (
      <div className="p-6 space-y-6">
        <Skeleton className="h-10 w-64" />
        <div className="grid grid-cols-4 gap-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-24" />
          ))}
        </div>
        <Skeleton className="h-[500px]" />
      </div>
    )
  }

  return (
    <div className="p-6 space-y-6">
      <PageHeader
        title="Orchestration"
        description="Real-time workflow visualization and mission coordination"
      >
        <div className="flex items-center gap-2">
          <Select value={selectedMissionId} onValueChange={setSelectedMissionId}>
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
        </div>
      </PageHeader>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <StatCard title="Active" value={stats.active} subtitle="Currently running" icon={Workflow} />
        <StatCard title="Planning" value={stats.planning} subtitle="Being planned" icon={Clock} />
        <StatCard title="Completed" value={stats.completed} subtitle="Successfully finished" icon={Activity} />
        <StatCard title="Failed" value={stats.failed} subtitle="Need attention" icon={Activity} />
      </div>

      <Tabs defaultValue="graph" className="space-y-4">
        <TabsList>
          <TabsTrigger value="graph" className="gap-2">
            <Workflow className="h-4 w-4" />
            Graph
          </TabsTrigger>
          <TabsTrigger value="timeline" className="gap-2">
            <Clock className="h-4 w-4" />
            Timeline
          </TabsTrigger>
          <TabsTrigger value="activity" className="gap-2">
            <Activity className="h-4 w-4" />
            Activity
          </TabsTrigger>
          <TabsTrigger value="templates" className="gap-2">
            <LayoutTemplate className="h-4 w-4" />
            Templates
          </TabsTrigger>
        </TabsList>

        <TabsContent value="graph" className="mt-0">
          <WorkflowGraph ref={graphRef} missions={filteredMissions} />
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
      </Tabs>
    </div>
  )
}
