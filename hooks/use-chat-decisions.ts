"use client"

import { useCallback, useEffect, useRef } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { E2CardPageSchema, type DecisionRef } from "@/lib/chat-e2-cards"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export const chatDecisionKeys = { refs: (workspace: string | null, user: string | undefined, chat: string) => ["chat-decisions", workspace, user, chat] as const }
class DecisionReadError extends Error { constructor(readonly denied: boolean) { super(denied ? "Unavailable" : "Could not verify current access") } }

export function useChatDecisions({ workspaceId, userId, chatId, enabled }: { workspaceId: string | null; userId?: string; chatId: string; enabled: boolean }) {
  const client = useQueryClient()
  const key = chatDecisionKeys.refs(workspaceId, userId, chatId)
  const scope = JSON.stringify(key)
  const active = enabled && !!workspaceId && !!userId && !!chatId
  const denied = useRef(false)
  const previous = useRef(scope)
  if (previous.current !== scope) { previous.current = scope; denied.current = false }
  const query = useQuery({
    queryKey: key, enabled: active, gcTime: 0, staleTime: 0, retry: false,
    refetchInterval: 30_000, refetchOnWindowFocus: "always", refetchOnReconnect: "always",
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/decisions?${new URLSearchParams({ workspace_id: workspaceId! })}`, { signal })
      if (!response.ok) {
        const unavailable = [401, 403, 404].includes(response.status)
        if (unavailable) { denied.current = true; client.setQueryData(key, []) }
        throw new DecisionReadError(unavailable)
      }
      const page = E2CardPageSchema.parse(await response.json())
      if (!page.reset || !page.snapshot_complete || page.has_more) throw new DecisionReadError(false)
      denied.current = false
      return page.refs.filter((reference): reference is DecisionRef => reference.kind === "decision")
    },
  })
  const reconcile = useCallback(() => { if (active) void client.invalidateQueries({ queryKey: chatDecisionKeys.refs(workspaceId, userId, chatId) }) }, [active, client, workspaceId, userId, chatId])
  useRealtimeEventSafe("escalation.resolved", reconcile)
  useRealtimeEventSafe("escalation.created", reconcile)
  useRealtimeEventSafe("workspace.updated", reconcile)
  useEffect(() => {
    if (!active) client.removeQueries({ queryKey: key })
    // The viewer-scoped key is the lifetime of this projection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, scope, client])
  const unavailable = !active || denied.current || query.error instanceof DecisionReadError && query.error.denied
  return { references: unavailable ? [] : query.data ?? [], rechecking: active && (query.isFetching || query.isPending || !!query.error), unavailable, reconcile }
}
