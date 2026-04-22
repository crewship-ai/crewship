"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { CrewsLayout } from "@/components/features/crews/crews-layout"

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

export default function CrewsPage() {
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

  // Real-time: debounced refetch (prevents burst of 8×3 concurrent fetches)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const debouncedRefetch = useCallback(() => {
    if (debounceRef.current !== null) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      void fetchData(true)
    }, 200)
  }, [fetchData])

  // Clear any pending timer on unmount or when workspace changes,
  // otherwise a late-firing timeout can overwrite state with data from
  // the previous workspace.
  useEffect(() => {
    return () => {
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [workspaceId])

  useRealtimeEvent("agent.status", debouncedRefetch)
  useRealtimeEvent("agent.created", debouncedRefetch)
  useRealtimeEvent("agent.updated", debouncedRefetch)
  useRealtimeEvent("agent.deleted", debouncedRefetch)
  useRealtimeEvent("crew.created", debouncedRefetch)
  useRealtimeEvent("crew.updated", debouncedRefetch)
  useRealtimeEvent("crew.deleted", debouncedRefetch)
  useRealtimeEvent("mission.updated", debouncedRefetch)

  if (loading || wsLoading || !workspaceId) {
    return (
      <div className="h-full flex items-center justify-center">
        <Skeleton className="h-[600px] w-full m-6 rounded-xl" />
      </div>
    )
  }

  return (
    <CrewsLayout
      crews={crews}
      agents={agents}
      missions={missions}
      workspaceId={workspaceId}
      onRefresh={() => fetchData()}
    />
  )
}
