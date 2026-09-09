"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { invalidate, readThrough } from "@/lib/stale-cache"
import type { AgentBinding } from "@/components/features/integrations/composio/types"

/** A notification channel this agent may post to. Destination deliberately absent. */
export interface AgentChannel {
  id: string
  type: string
  provider?: string
  enabled: boolean
}

/**
 * Everything this agent can reach outside itself: the app toolkits it may call
 * and the notification channels it may post to.
 *
 * One hook for both because they answer one question — "what can this agent
 * touch?" — that a reviewer asks in one go. Two calls behind it, cached, so
 * opening several agents in a row does not re-fetch what was just shown.
 *
 * Failed requests are reported separately from an empty set of permissions.
 */
export function useAgentReach(workspaceId: string | null | undefined, agentId: string | null) {
  const [toolkits, setToolkits] = useState<AgentBinding[]>([])
  const [channels, setChannels] = useState<AgentChannel[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const generation = useRef(0)
  const cancelPending = useCallback(() => { generation.current++ }, [])

  const load = useCallback(async () => {
    const request = ++generation.current
    setError(null)
    setLoading(true)
    setToolkits([])
    setChannels([])
    if (!workspaceId || !agentId) {
      setToolkits([])
      setChannels([])
      setLoading(false)
      return
    }
    const ws = encodeURIComponent(workspaceId)
    const id = encodeURIComponent(agentId)

    const bind = readThrough(`composio:${workspaceId}:bind:${agentId}`, async () => {
      const r = await apiFetch(`/api/v1/integrations/composio/agents/${id}/bind?workspace_id=${ws}`)
      if (!r.ok) throw new Error(String(r.status))
      const b = (await r.json()) as { bindings?: AgentBinding[] }
      return b.bindings ?? []
    })
    const chans = readThrough(`notify:${workspaceId}:agent-channels:${agentId}`, async () => {
      const r = await apiFetch(`/api/v1/agents/${id}/notification-channels?workspace_id=${ws}`)
      if (!r.ok) throw new Error(String(r.status))
      const b = (await r.json()) as { channels?: AgentChannel[] }
      return b.channels ?? []
    })

    if (bind.value) setToolkits(bind.value)
    if (chans.value) setChannels(chans.value)
    if (bind.value && chans.value) setLoading(false)

    const [t, c] = await Promise.allSettled([bind.fresh, chans.fresh])
    if (generation.current !== request) return
    if (t.status === "rejected" || c.status === "rejected") setError("Some tools or channels could not be loaded.")
    if (t.status === "fulfilled") setToolkits(t.value)
    else if (!bind.value) setToolkits([])
    if (c.status === "fulfilled") setChannels(c.value)
    else if (!chans.value) setChannels([])
    setLoading(false)
  }, [workspaceId, agentId])

  const refresh = useCallback(() => {
    invalidate(`composio:${workspaceId}:bind:${agentId}`)
    invalidate(`notify:${workspaceId}:agent-channels:${agentId}`)
    return load()
  }, [workspaceId, agentId, load])
  useEffect(() => {
    void load()
    return cancelPending
  }, [load, cancelPending])

  return { toolkits, channels, loading, error, refresh }
}
