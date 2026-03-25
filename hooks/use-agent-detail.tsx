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

export function AgentDetailProvider({
  agentId,
  children,
}: {
  agentId: string
  children: ReactNode
}) {
  const { workspaceId } = useWorkspace()
  const [agent, setAgent] = useState<AgentDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [refreshKey, setRefreshKey] = useState(0)

  const refresh = useCallback(() => setRefreshKey((k) => k + 1), [])

  useEffect(() => {
    if (!workspaceId) return

    let cancelled = false
    setLoading(true)

    async function fetchAgent() {
      try {
        const res = await fetch(`/api/v1/agents/${agentId}?workspace_id=${workspaceId}`)
        if (!res.ok) {
          const data = await res.json().catch(() => ({ error: "Failed to load agent" }))
          if (!cancelled) setError(typeof data.error === "string" ? data.error : "Failed to load agent")
          return
        }
        const data: AgentDetail = await res.json()
        if (!cancelled) {
          setAgent(data)
          setError(null)
        }
      } catch {
        if (!cancelled) setError("Network error. Please try again.")
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    fetchAgent()
    return () => { cancelled = true }
  }, [agentId, workspaceId, refreshKey])

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

export function useAgentDetail(): AgentDetailContextValue {
  const ctx = useContext(AgentDetailContext)
  if (!ctx) {
    return { agent: null, loading: false, error: null, refresh: () => {}, setAgent: () => {} }
  }
  return ctx
}
