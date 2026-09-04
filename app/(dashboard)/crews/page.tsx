"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Skeleton } from "@/components/ui/skeleton"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { usePagedList } from "@/hooks/use-paged-list"
import { CrewsLayout } from "@/components/features/crews/crews-layout"
import { apiFetch } from "@/lib/api-fetch"
import { visibleFleetAgents } from "@/lib/fleet-visibility"

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

/** One page is the whole fleet for every workspace this product has met;
 *  past it the explorer says how many more there are and loads them on ask,
 *  instead of silently stopping at the server's default of 100. */
const PAGE = 500

export default function CrewsPage() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [missions, setMissions] = useState<MissionData[]>([])
  // Bumped by realtime events and by saves; both lists and the missions
  // re-read on it. usePagedList owns the request race (a late answer from
  // a superseded fetch is dropped), so a workspace switch cannot repaint
  // the new workspace with the old one's rows.
  const [tick, setTick] = useState(0)

  const crewsList = usePagedList<CrewData>({
    url: workspaceId ? `/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}` : null,
    limit: PAGE,
    reloadKey: tick,
  })
  const agentsList = usePagedList<AgentData>({
    url: workspaceId ? `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}` : null,
    limit: PAGE,
    reloadKey: tick,
  })

  const crews = crewsList.items
  // The onboarding guide lives in a crew the crews list never returns; it
  // must not be the first row of the client's fleet (lib/fleet-visibility).
  const agents = useMemo(() => visibleFleetAgents(agentsList.items, crews), [agentsList.items, crews])

  const missionsAbort = useRef<AbortController | null>(null)
  useEffect(() => {
    missionsAbort.current?.abort()
    if (!workspaceId) {
      setMissions([])
      return
    }
    const controller = new AbortController()
    missionsAbort.current = controller
    apiFetch(`/api/v1/missions?workspace_id=${encodeURIComponent(workspaceId)}&limit=20&include_tasks=true`, { signal: controller.signal })
      .then((r) => (r.ok ? r.json() : []))
      .then((data) => { if (!controller.signal.aborted && Array.isArray(data)) setMissions(data) })
      .catch(() => { /* a transient failure keeps the previous missions */ })
    return () => controller.abort()
  }, [workspaceId, tick])

  const refresh = useCallback(() => setTick((t) => t + 1), [])

  // Real-time: debounced refetch (prevents a burst of concurrent fetches)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const debouncedRefetch = useCallback(() => {
    if (debounceRef.current !== null) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      refresh()
    }, 200)
  }, [refresh])
  useEffect(() => () => { if (debounceRef.current !== null) clearTimeout(debounceRef.current) }, [workspaceId])

  useRealtimeEvent("agent.status", debouncedRefetch)
  useRealtimeEvent("agent.created", debouncedRefetch)
  useRealtimeEvent("agent.updated", debouncedRefetch)
  useRealtimeEvent("agent.deleted", debouncedRefetch)
  useRealtimeEvent("crew.created", debouncedRefetch)
  useRealtimeEvent("crew.updated", debouncedRefetch)
  useRealtimeEvent("crew.deleted", debouncedRefetch)
  useRealtimeEvent("mission.updated", debouncedRefetch)

  // First paint only: a silent refetch after a save keeps the canvas up,
  // otherwise the list the open canvas resolved from would blank for a beat.
  // The lists report "not loading" for the one render before their first
  // effect fires, which painted an empty roster for a frame; until a load has
  // been seen, an empty workspace is "not fetched yet", not "empty".
  const loadSeen = useRef(false)
  if (crewsList.loading || agentsList.loading) loadSeen.current = true
  const beforeFirstResponse = !loadSeen.current && !crewsList.error && !agentsList.error
  const firstLoad = beforeFirstResponse || (crewsList.loading && crewsList.total === null) || (agentsList.loading && agentsList.total === null)
  if (wsLoading || (workspaceId && firstLoad && crews.length === 0 && agents.length === 0)) {
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

  const loadMore = () => {
    if (crewsList.hasMore) void crewsList.loadMore()
    if (agentsList.hasMore) void agentsList.loadMore()
  }

  return (
    <CrewsLayout
      crews={crews}
      agents={agents}
      missions={missions}
      workspaceId={workspaceId}
      // The stale-slug watcher needs a real "loaded" signal; array lengths
      // would mis-treat a legitimately empty workspace as still loading.
      loaded={!crewsList.loading && !agentsList.loading}
      crewsTotal={crewsList.total}
      agentsTotal={agentsList.total}
      hasMore={crewsList.hasMore || agentsList.hasMore}
      loadingMore={crewsList.loadingMore || agentsList.loadingMore}
      onLoadMore={loadMore}
      onRefresh={refresh}
    />
  )
}
