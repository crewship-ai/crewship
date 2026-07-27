"use client"

import { useCallback, useEffect, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"

/**
 * One agent that may post to a channel of its own accord.
 *
 * The server enriches the pairing with the agent's name, so this answers
 * "who can post here?" in the terms an admin thinks in rather than making
 * them resolve ids by hand.
 */
export interface ChannelAgent {
  id: string
  channel_id: string
  agent_id: string
  agent_name?: string
  agent_slug?: string
  granted_by?: string
  created_at?: string
}

/**
 * Which agents are allowed to post to one channel.
 *
 * Fetched per channel rather than for the whole list, because it is only ever
 * shown on a connection's detail — doing it eagerly for every row would be an
 * N+1 over a list the user is mostly scrolling past.
 */
export function useChannelAgents(
  workspaceId: string | null | undefined,
  channelId: string | null,
) {
  const [agents, setAgents] = useState<ChannelAgent[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    if (!workspaceId || !channelId) {
      setAgents([])
      return
    }
    setLoading(true)
    try {
      const res = await apiFetch(
        `/api/v1/notification-channels/${encodeURIComponent(channelId)}/agents` +
          `?workspace_id=${encodeURIComponent(workspaceId)}`,
      )
      if (!res.ok) throw new Error(`load channel agents: ${res.status}`)
      const body = await res.json()
      setAgents(Array.isArray(body?.agents) ? body.agents : [])
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : "failed to load")
      setAgents([])
    } finally {
      setLoading(false)
    }
  }, [workspaceId, channelId])

  useEffect(() => {
    void refresh()
  }, [refresh])

  return { agents, loading, error, refresh }
}
