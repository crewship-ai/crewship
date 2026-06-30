"use client"

import { useMemo } from "react"
import { useAgentFetch } from "@/hooks/use-agent-fetch"
import { apiFetch } from "@/lib/api-fetch"

// useWorkspaceAgents — lightweight slug→id map for the workspace's
// agents. Used by the trace side panel to resolve a step's
// `agent_slug` (all the run/DSL carries) into the agent id the files
// endpoints (`/api/v1/agents/{id}/files…`) require.
//
// Fetches `/api/v1/agents?workspace_id=…` once per workspace (same
// endpoint the command palette uses) and derives a Map. Cheap enough to
// live in the page without its own cache layer.

export interface WorkspaceAgentLite {
  id: string
  slug: string
  name: string
}

export function useWorkspaceAgents(workspaceId: string | null | undefined): {
  agents: WorkspaceAgentLite[]
  bySlug: Map<string, string>
  loading: boolean
} {
  const { data, loading } = useAgentFetch<WorkspaceAgentLite[]>(
    async (signal) => {
      const res = await apiFetch(
        `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId as string)}`,
        { signal },
      )
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const json = await res.json()
      return Array.isArray(json) ? (json as WorkspaceAgentLite[]) : []
    },
    [workspaceId],
    { enabled: Boolean(workspaceId), logLabel: "useWorkspaceAgents" },
  )

  const agents = useMemo(() => data ?? [], [data])
  const bySlug = useMemo(() => {
    const m = new Map<string, string>()
    for (const a of agents) {
      if (a?.slug && a?.id) m.set(a.slug, a.id)
    }
    return m
  }, [agents])

  return { agents, bySlug, loading }
}
