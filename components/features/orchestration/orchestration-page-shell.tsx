"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent, type RealtimeEvent } from "@/hooks/use-realtime"
import { OrchestrationLayout, type OrchestrationMode } from "@/components/features/orchestration/orchestration-layout"
import type { Mission } from "@/lib/types/mission"
import type { CrewSummary, AgentSummary, CrewConnection } from "@/lib/types/orchestration"

// Shared data-fetching shell used by /issues, /activity, and the legacy
// /orchestration redirect. The IA refactor split the old single
// "Orchestration" page into three focused surfaces; the data each one
// needs (missions / crews / agents / connections) is identical, so the
// fetch + realtime wiring lives here and only the `mode` differs.
//
// /orchestration/page.tsx still delegates to this shell so deep links
// keep working until the alias is removed.
export function OrchestrationPageShell({ mode }: { mode: OrchestrationMode }) {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [missions, setMissions] = useState<Mission[]>([])
  const [crews, setCrews] = useState<CrewSummary[]>([])
  const [agents, setAgents] = useState<AgentSummary[]>([])
  const [connections, setConnections] = useState<CrewConnection[]>([])
  const [loading, setLoading] = useState(true)
  const [selectedMissionId, setSelectedMissionId] = useState<string>("all")

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
      // ignore — empty data is the loading-state fallback
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

  if (loading || wsLoading) {
    return (
      <div className="h-full flex items-center justify-center">
        <Skeleton className="h-[600px] w-full m-6 rounded-xl" />
      </div>
    )
  }

  return (
    <OrchestrationLayout
      missions={missions}
      crews={crews}
      agents={agents}
      connections={connections}
      workspaceId={workspaceId!}
      selectedMissionId={selectedMissionId}
      onMissionChange={setSelectedMissionId}
      onRefresh={fetchData}
      onMissionCreated={fetchData}
      mode={mode}
    />
  )
}
