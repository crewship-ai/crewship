"use client"

import { useCallback, useEffect, useState } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { FleetLayout } from "@/components/features/fleet/fleet-layout"

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

export default function FleetPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [crews, setCrews] = useState<CrewData[]>([])
  const [agents, setAgents] = useState<AgentData[]>([])
  const [missions, setMissions] = useState<MissionData[]>([])
  const [loading, setLoading] = useState(true)

  const fetchData = useCallback(async (silent = false) => {
    if (!workspaceId) {
      if (!silent) setLoading(false)
      return
    }
    if (!silent) setLoading(true)
    try {
      const [crewsRes, agentsRes, missionsRes] = await Promise.all([
        fetch(`/api/v1/crews?workspace_id=${workspaceId}`),
        fetch(`/api/v1/agents?workspace_id=${workspaceId}`),
        fetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=20&include_tasks=true`),
      ])
      if (crewsRes.ok) setCrews(await crewsRes.json())
      if (agentsRes.ok) setAgents(await agentsRes.json())
      if (missionsRes.ok) setMissions(await missionsRes.json())
    } catch {
      // ignore
    } finally {
      if (!silent) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => { fetchData() }, [fetchData])

  // Real-time updates
  useRealtimeEvent("agent.status", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("agent.created", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("agent.updated", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("agent.deleted", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("crew.created", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("crew.updated", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("crew.deleted", useCallback(() => { fetchData(true) }, [fetchData]))
  useRealtimeEvent("mission.updated", useCallback(() => { fetchData(true) }, [fetchData]))

  if (loading || wsLoading || !workspaceId) {
    return (
      <div className="h-full flex items-center justify-center">
        <Skeleton className="h-[600px] w-full m-6 rounded-xl" />
      </div>
    )
  }

  return (
    <FleetLayout
      crews={crews}
      agents={agents}
      missions={missions}
      workspaceId={workspaceId}
      onRefresh={() => fetchData()}
    />
  )
}
