"use client"

import { useCallback, useEffect, useState } from "react"
import { Workflow, Clock, Activity, RefreshCw } from "lucide-react"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/layout/page-header"
import { StatCard } from "@/components/layout/stat-card"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { WorkflowGraph } from "@/components/features/orchestration/workflow-graph"
import { MissionTimeline } from "@/components/features/orchestration/mission-timeline"
import { OrchestrationActivity } from "@/components/features/orchestration/orchestration-activity"
import type { Mission } from "@/lib/types/mission"

export default function OrchestrationPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [missions, setMissions] = useState<Mission[]>([])
  const [loading, setLoading] = useState(true)

  const fetchMissions = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await fetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=50`)
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

  useRealtimeEvent("mission.updated", fetchMissions)
  useRealtimeEvent("task.updated", fetchMissions)

  const stats = {
    active: missions.filter((m) => m.status === "IN_PROGRESS").length,
    planning: missions.filter((m) => m.status === "PLANNING").length,
    completed: missions.filter((m) => m.status === "COMPLETED").length,
    failed: missions.filter((m) => m.status === "FAILED").length,
  }

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
        <Button variant="outline" size="sm" onClick={fetchMissions}>
          <RefreshCw className="h-4 w-4 mr-2" />
          Refresh
        </Button>
      </PageHeader>

      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <StatCard title="Active Missions" value={stats.active} subtitle="Currently running" icon={Workflow} />
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
        </TabsList>

        <TabsContent value="graph" className="mt-0">
          <WorkflowGraph missions={missions} />
        </TabsContent>

        <TabsContent value="timeline" className="mt-0">
          <MissionTimeline missions={missions} />
        </TabsContent>

        <TabsContent value="activity" className="mt-0">
          <OrchestrationActivity missions={missions} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
