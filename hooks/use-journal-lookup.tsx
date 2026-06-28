"use client"

// useJournalLookup — workspace-scoped lookup table that the journal UI
// consumes to enrich entry cards with human-readable names, crew icons
// and palette colors. The data is small (a few hundred rows max per
// workspace) and changes rarely — fetch once per workspace + invalidate
// on the matching realtime events.
//
// Provider pattern: a single fetch lives at the layout level (or in
// /journal/page) via JournalLookupProvider; descendants read with
// useJournalLookup() and get an empty default when the provider isn't
// mounted (degrades gracefully — id-only rendering rather than crash).
//
// Backed by GET /api/v1/journal/lookup (Phase G of unified-journal).

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { apiFetch } from "@/lib/api-fetch"

export interface CrewLookup {
  id: string
  name: string
  slug: string
  icon: string | null
  color: string | null
}

export interface AgentLookup {
  id: string
  name: string
  slug: string
  crew_id: string | null
  avatar_seed: string | null
  avatar_style: string | null
}

export interface MissionLookup {
  id: string
  title: string
  status: string
}

export interface JournalLookupValue {
  crews: Map<string, CrewLookup>
  agents: Map<string, AgentLookup>
  missions: Map<string, MissionLookup>
  loading: boolean
  refresh: () => void
}

const EMPTY_VALUE: JournalLookupValue = {
  crews: new Map(),
  agents: new Map(),
  missions: new Map(),
  loading: false,
  refresh: () => {},
}

const JournalLookupContext = createContext<JournalLookupValue>(EMPTY_VALUE)

interface ProviderProps {
  workspaceId: string | null
  children: ReactNode
}

/**
 * JournalLookupProvider fetches the workspace lookup table once on
 * mount and refetches when crew/agent/mission realtime events arrive.
 * Place this around any subtree that renders journal entries (most
 * naturally at the /journal page level).
 */
export function JournalLookupProvider({ workspaceId, children }: ProviderProps) {
  const [crews, setCrews] = useState<Map<string, CrewLookup>>(new Map())
  const [agents, setAgents] = useState<Map<string, AgentLookup>>(new Map())
  const [missions, setMissions] = useState<Map<string, MissionLookup>>(new Map())
  const [loading, setLoading] = useState(false)

  // requestSeq guards against out-of-order responses: a slow fetch for
  // workspace A can land after a fresh fetch for workspace B has
  // already populated the maps. We bump on every fetch and only commit
  // results when the response's seq still matches the latest.
  const requestSeq = useRef(0)
  // Track the workspaceId active when the request was started so a
  // late response from the previous workspace can't overwrite the
  // current one's data even if seq counters happen to align.
  const activeWorkspaceRef = useRef<string | null>(workspaceId)
  useEffect(() => {
    activeWorkspaceRef.current = workspaceId
  }, [workspaceId])

  const fetchLookup = useCallback(async () => {
    if (!workspaceId) return
    const mySeq = ++requestSeq.current
    const myWorkspace = workspaceId
    setLoading(true)
    try {
      const res = await apiFetch(`/api/v1/journal/lookup?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!res.ok) return
      const data = (await res.json()) as {
        crews: CrewLookup[]
        agents: AgentLookup[]
        missions: MissionLookup[]
      }
      // Stale-response guard: discard if a newer fetch has been kicked
      // off, or if the active workspace has changed since this request
      // started.
      if (mySeq !== requestSeq.current) return
      if (myWorkspace !== activeWorkspaceRef.current) return
      setCrews(new Map(data.crews.map((c) => [c.id, c])))
      setAgents(new Map(data.agents.map((a) => [a.id, a])))
      setMissions(new Map(data.missions.map((m) => [m.id, m])))
    } catch {
      // Network errors leave the previous map intact — fail-open over
      // throwing so the journal still renders with whatever cached
      // names existed before the hiccup.
    } finally {
      // Only the latest in-flight request flips loading off so
      // sequential fetches don't briefly show "loaded" between them.
      if (mySeq === requestSeq.current) setLoading(false)
    }
  }, [workspaceId])

  // Clear maps the moment workspace switches so we don't render the
  // previous workspace's chips while the new fetch is in flight.
  useEffect(() => {
    setCrews(new Map())
    setAgents(new Map())
    setMissions(new Map())
    fetchLookup()
  }, [fetchLookup])

  // Invalidate on entity-shape changes. Each handler simply re-fetches
  // — refresh cost is one small JSON call, not worth surgical updates.
  // (mission.created isn't in the realtime union today; mission.updated
  // covers the common rename case, and a stale missing entry just
  // renders id-only until the next page load.)
  useRealtimeEvent("crew.created", fetchLookup)
  useRealtimeEvent("crew.updated", fetchLookup)
  useRealtimeEvent("crew.deleted", fetchLookup)
  useRealtimeEvent("agent.created", fetchLookup)
  useRealtimeEvent("agent.updated", fetchLookup)
  useRealtimeEvent("agent.deleted", fetchLookup)
  useRealtimeEvent("mission.updated", fetchLookup)

  const value = useMemo<JournalLookupValue>(
    () => ({ crews, agents, missions, loading, refresh: fetchLookup }),
    [crews, agents, missions, loading, fetchLookup],
  )

  return <JournalLookupContext.Provider value={value}>{children}</JournalLookupContext.Provider>
}

/**
 * useJournalLookup reads the cached lookup tables. Returns empty maps
 * when no provider is mounted — components that need enrichment should
 * still render gracefully in that case.
 */
export function useJournalLookup(): JournalLookupValue {
  return useContext(JournalLookupContext)
}
