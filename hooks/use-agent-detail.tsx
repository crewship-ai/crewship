"use client"

import {
  createContext,
  useContext,
  useState,
  useEffect,
  useCallback,
  type ReactNode,
} from "react"
import { useWorkspace } from "@/hooks/use-workspace"
import { useRealtimeEvent } from "@/hooks/use-realtime"
import { useAgentFetch } from "@/hooks/use-agent-fetch"
import { apiFetch } from "@/lib/api-fetch"

interface AgentCrew {
  name: string
  slug: string
  color: string | null
  avatar_style: string | null
}

interface AgentCounts {
  skills: number
  credentials: number
  chats: number
}

/** Full agent record including crew association, LLM configuration, and resource counts. */
export interface AgentDetail {
  id: string
  workspace_id: string
  crew_id: string | null
  name: string
  slug: string
  description: string | null
  role_title: string | null
  agent_role: string
  lead_mode: string | null
  status: string
  cli_adapter: string
  llm_provider: string | null
  llm_model: string | null
  system_prompt: string | null
  avatar_seed: string | null
  avatar_style: string | null
  timeout_seconds: number
  tool_profile: string
  memory_enabled: boolean
  created_at: string
  updated_at: string
  crew: AgentCrew | null
  _count: AgentCounts
}

interface AgentDetailContextValue {
  agent: AgentDetail | null
  loading: boolean
  error: string | null
  refresh: () => void
  setAgent: React.Dispatch<React.SetStateAction<AgentDetail | null>>
}

const AgentDetailContext = createContext<AgentDetailContextValue | null>(null)

/**
 * Context provider that fetches and caches agent detail, auto-refreshing on real-time status/run events.
 */
export function AgentDetailProvider({
  agentId,
  children,
}: {
  agentId: string
  children: ReactNode
}) {
  const { workspaceId } = useWorkspace()
  const [agent, setAgent] = useState<AgentDetail | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)

  const refresh = useCallback(() => setRefreshKey((k) => k + 1), [])

  const { data: fetched, loading, error: fetchError } = useAgentFetch<AgentDetail>(
    async (signal) => {
      // Network-layer failures (DNS, refused connection, offline) collapse
      // to a single canonical user message; only HTTP non-2xx surfaces the
      // server's own error string.
      let res: Response
      try {
        res = await apiFetch(
          `/api/v1/agents/${agentId}?workspace_id=${workspaceId}`,
          { signal },
        )
      } catch (e) {
        if (e instanceof DOMException && e.name === "AbortError") throw e
        throw new Error("Network error. Please try again.")
      }
      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to load agent" }))
        throw new Error(typeof data.error === "string" ? data.error : "Failed to load agent")
      }
      return res.json() as Promise<AgentDetail>
    },
    [agentId, workspaceId, refreshKey],
    { enabled: !!workspaceId, logLabel: "useAgentDetail" },
  )

  // Bridge useAgentFetch's read-only data into the locally mutable agent
  // state — context consumers expose setAgent for optimistic updates.
  useEffect(() => {
    if (fetched) setAgent(fetched)
  }, [fetched])

  const error = fetchError instanceof Error ? fetchError.message : null

  // Real-time: auto-refresh agent detail when status or runs change (filtered by agentId)
  useRealtimeEvent("agent.status", useCallback((event) => {
    if (event.payload.agent_id === agentId) refresh()
  }, [refresh, agentId]))
  useRealtimeEvent("run.completed", useCallback((event) => {
    if (event.payload.agent_id === agentId) refresh()
  }, [refresh, agentId]))
  useRealtimeEvent("run.failed", useCallback((event) => {
    if (event.payload.agent_id === agentId) refresh()
  }, [refresh, agentId]))

  return (
    <AgentDetailContext.Provider value={{ agent, loading, error, refresh, setAgent }}>
      {children}
    </AgentDetailContext.Provider>
  )
}

/** Access the current agent's detail data. Returns safe defaults when used outside AgentDetailProvider. */
export function useAgentDetail(): AgentDetailContextValue {
  const ctx = useContext(AgentDetailContext)
  if (!ctx) {
    return { agent: null, loading: false, error: null, refresh: () => {}, setAgent: () => {} }
  }
  return ctx
}
