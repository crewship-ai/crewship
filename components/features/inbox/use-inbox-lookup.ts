"use client"

import { useEffect, useMemo, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import type { InboxAgentRef, InboxCrewRef, InboxLookup } from "@/components/features/inbox-v2/inbox-v2-types"

/**
 * The crews and agents this workspace has, keyed the way inbox rows name
 * them: crews by id, agents by slug and by id.
 *
 * Rows carry a crew id and an agent slug (the approvals queue an agent id);
 * a page fetches each list once so every row and the reading pane can say
 * "Ops · Riley" with a colour and a face. Both lists are the same endpoints
 * the crews page reads; the setup crew's agent is excluded by the server.
 * Shared by /inbox-v2 and the older /inbox, which render the same pane.
 *
 * Plain fetches rather than react-query: the older /inbox mounts without a
 * QueryClient, and a lookup that takes the pane down when its provider is
 * missing would be worse than the ids it replaces.
 */
export function useInboxLookup(workspaceId: string | null | undefined): InboxLookup {
  const [crews, setCrews] = useState<InboxCrewRef[] | null>(null)
  const [agents, setAgents] = useState<InboxAgentRef[] | null>(null)

  useEffect(() => {
    if (!workspaceId) return
    let cancelled = false
    const ws = encodeURIComponent(workspaceId)
    apiFetch(`/api/v1/crews?workspace_id=${ws}&limit=500`)
      .then((r) => (r.ok ? r.json() : []))
      .then((rows: { id: string; name: string; slug: string; color?: string | null }[]) => {
        if (cancelled) return
        setCrews((Array.isArray(rows) ? rows : []).map((c) => ({ id: c.id, name: c.name, slug: c.slug, color: c.color ?? null })))
      })
      .catch(() => {
        if (!cancelled) setCrews([])
      })
    apiFetch(`/api/v1/agents?workspace_id=${ws}&limit=500`)
      .then((r) => (r.ok ? r.json() : []))
      .then((rows: InboxAgentRef[]) => {
        if (cancelled) return
        setAgents(Array.isArray(rows) ? rows : [])
      })
      .catch(() => {
        if (!cancelled) setAgents([])
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId])

  return useMemo(() => ({
    crewById: new Map((crews ?? []).map((c) => [c.id, c])),
    agentBySlug: new Map((agents ?? []).map((a) => [a.slug, a])),
    agentById: new Map((agents ?? []).map((a) => [a.id, a])),
    ready: crews !== null && agents !== null,
  }), [crews, agents])
}
