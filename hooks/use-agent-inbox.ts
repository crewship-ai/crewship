"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useWorkspace } from "@/hooks/use-workspace"

export interface PeerMessage {
  id: string
  from_agent_name: string
  from_agent_slug: string
  to_agent_name?: string | null
  question: string
  status: string
  created_at: string
  direction: "incoming" | "outgoing"
}

export interface AgentInbox {
  approvals_pending: number
  assignments_open: number
  escalations_open: number
  peer_messages: PeerMessage[]
  cost_usd_this_month: number
  llm_calls_this_month: number
  tokens_used_this_month: number
}

export interface AgentInboxResult {
  inbox: AgentInbox | null
  loading: boolean
  error: string | null
  refresh: () => void
}

const EMPTY_INBOX: AgentInbox = {
  approvals_pending: 0,
  assignments_open: 0,
  escalations_open: 0,
  peer_messages: [],
  cost_usd_this_month: 0,
  llm_calls_this_month: 0,
  tokens_used_this_month: 0,
}

/**
 * Fetches the consolidated "waiting on this agent" payload from
 * GET /api/v1/agents/{agentId}/inbox. Aborts on agent switch so rapid
 * explorer clicks don't pile up orphan fetches.
 */
export function useAgentInbox(agentId: string | null | undefined): AgentInboxResult {
  const { workspaceId } = useWorkspace()
  const [inbox, setInbox] = useState<AgentInbox | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [nonce, setNonce] = useState(0)
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!agentId || !workspaceId) {
      setInbox(null)
      setLoading(false)
      setError(null)
      return
    }
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    setLoading(true)
    setError(null)
    fetch(`/api/v1/agents/${agentId}/inbox?workspace_id=${workspaceId}`, {
      signal: controller.signal,
    })
      .then(async (res) => {
        if (!res.ok) {
          if (res.status === 404) {
            setInbox(EMPTY_INBOX)
            return
          }
          throw new Error(`inbox fetch ${res.status}`)
        }
        const data = (await res.json()) as AgentInbox
        setInbox(data)
      })
      .catch((err: Error) => {
        if (err.name === "AbortError") return
        setError(err.message)
        setInbox(null)
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })

    return () => controller.abort()
  }, [agentId, workspaceId, nonce])

  const refresh = useCallback(() => setNonce((n) => n + 1), [])

  return { inbox, loading, error, refresh }
}
