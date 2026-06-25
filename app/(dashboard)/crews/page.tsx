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
  // PR-D F5 ephemeral lifecycle (server returns these; absent on permanent agents).
  ephemeral?: boolean
  expires_at?: string | null
  expired_at?: string | null
  parent_lead_id?: string | null
  hire_reason?: string | null
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

  // Cancel in-flight fetch when workspace changes so a late response from
  // workspace A can't repopulate crews/agents/missions after the user has
  // switched to workspace B.
  const abortRef = useRef<AbortController | null>(null)

  const fetchData = useCallback(async (silent = false) => {
    if (!workspaceId) {
      setCrews([])
      setAgents([])
      setMissions([])
      if (!silent) setLoading(false)
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller
    if (!silent) {
      // On a user-visible reload (workspace switch / manual refresh) we
      // must drop the previous workspace's data before firing the new
      // requests — otherwise a failing fetch would render the old
      // workspace's crews/agents/missions under the new header.
      setCrews([])
      setAgents([])
      setMissions([])
      setLoading(true)
    }
    try {
      const [crewsRes, agentsRes, missionsRes] = await Promise.all([
        fetch(`/api/v1/crews?workspace_id=${workspaceId}`, { signal: controller.signal }),
        fetch(`/api/v1/agents?workspace_id=${workspaceId}`, { signal: controller.signal }),
        fetch(`/api/v1/missions?workspace_id=${workspaceId}&limit=20&include_tasks=true`, { signal: controller.signal }),
      ])
      if (controller.signal.aborted) return
      if (crewsRes.ok) setCrews(await crewsRes.json())
      if (controller.signal.aborted) return
      if (agentsRes.ok) setAgents(await agentsRes.json())
      if (controller.signal.aborted) return
      if (missionsRes.ok) setMissions(await missionsRes.json())
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") return
      // other errors: leave state as-is (previous data survives a transient network blip)
    } finally {
      // Only the currently-owned controller is allowed to flip loading
      // back off. Without this, a silent refetch that aborted the
      // original visible request would never clear the skeleton.
      if (abortRef.current === controller && !controller.signal.aborted) {
        setLoading(false)
      }
    }
  }, [workspaceId])

  useEffect(() => {
    fetchData()
    return () => {
      abortRef.current?.abort()
    }
  }, [fetchData])

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

  if (loading || wsLoading) {
    return (
      <div className="h-full flex items-center justify-center">
        <Skeleton className="h-[600px] w-full m-6 rounded-xl" />
      </div>
    )
  }

  if (!workspaceId) {
    return (
      <div className="h-full flex flex-col items-center justify-center gap-2 p-6 text-center">
        <p className="text-sm font-medium text-foreground/80">No workspace selected</p>
        <p className="text-[12px] text-muted-foreground max-w-sm">
          Pick a workspace from the toolbar to see its crews, agents and missions.
        </p>
      </div>
    )
  }

  // CrewsLayout only mounts once the initial fetch has resolved (guarded by
  // the `loading || wsLoading` skeleton above), so we can promise it the
  // data is loaded. This is what drives its stale-slug watcher — using the
  // array lengths as a loaded proxy would mis-treat legitimately empty
  // workspaces as "still loading" and silently pin invalid ?agent= slugs.
  return (
    <CrewsLayout
      crews={crews}
      agents={agents}
      missions={missions}
      workspaceId={workspaceId}
      loaded
      onRefresh={() => fetchData()}
    />
  )
}
