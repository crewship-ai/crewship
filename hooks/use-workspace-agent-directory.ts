"use client"

import { useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { readThrough } from "@/lib/stale-cache"

export interface WorkspaceAgentIdentity {
  id: string; slug: string; name: string
  avatar_url?: string | null; avatar_seed?: string | null; avatar_style?: string | null
  crew?: { avatar_style?: string | null } | null
}

/** One cached workspace list for identity chips, step avatars and reach cards. */
export function useWorkspaceAgentDirectory(workspaceId?: string) {
  const [state, setState] = useState<{ workspaceId?: string; agents: WorkspaceAgentIdentity[] | null; error: boolean }>({ agents: null, error: false })
  useEffect(() => {
    if (!workspaceId) return
    let live = true
    const result = readThrough<WorkspaceAgentIdentity[]>(`agents:${workspaceId}:list`, async () => {
      const res = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!res.ok) throw new Error("Agent directory unavailable")
      const data = await res.json()
      if (!Array.isArray(data)) throw new Error("Agent directory unavailable")
      return data
    })
    if (result.value) setState({ workspaceId, agents: result.value, error: false })
    result.fresh.then(agents => { if (live) setState({ workspaceId, agents, error: false }) }, () => { if (live) setState({ workspaceId, agents: result.value ?? null, error: true }) })
    return () => { live = false }
  }, [workspaceId])
  return state.workspaceId === workspaceId ? state : { agents: null, error: false }
}
