"use client"

import { useCallback, useEffect, useRef } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { purgeChatDecisionRecovery, purgeChatDecisionSourceRecovery } from "@/lib/chat-decision-recovery"
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
      const ensureCurrent = () => { if (signal.aborted || previous.current !== scope) throw new DOMException("Superseded decision scope", "AbortError") }
      ensureCurrent()
      let cursor = ""
      let snapshot = ""
      let restarted = false
      let references: DecisionRef[] = []
      for (let pageCount = 0; pageCount < 1000; pageCount++) {
        const params = new URLSearchParams({ workspace_id: workspaceId!, limit: "100" })
        if (cursor) params.set("cursor", cursor)
        const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/decisions?${params}`, { signal })
        ensureCurrent()
        if (response.status === 400 && cursor && !restarted) {
          const body = await response.json().catch(() => null)
          ensureCurrent()
          if (body?.error === "cursor_invalid") { cursor = ""; snapshot = ""; references = []; restarted = true; continue }
        }
        if (!response.ok) {
          const unavailable = [401, 403, 404].includes(response.status)
          if (unavailable) { denied.current = true; client.setQueryData(key, []); purgeChatDecisionRecovery(workspaceId!,userId!,chatId) }
          throw new DecisionReadError(unavailable)
        }
        const raw = await response.json()
        ensureCurrent()
        const page = E2CardPageSchema.parse(raw)
        if (page.refs.some(ref => ref.kind !== "decision" || ref.entity.workspace_id !== workspaceId)) throw new DecisionReadError(false)
        if (page.reset) { references = []; snapshot = page.snapshot_id }
        if (!snapshot || page.snapshot_id !== snapshot) throw new DecisionReadError(false)
        references.push(...page.refs.filter((reference): reference is DecisionRef => reference.kind === "decision"))
        if (page.snapshot_complete && !page.has_more) { denied.current = false; purgeChatDecisionRecovery(workspaceId!,userId!,chatId,new Set(references.filter(ref=>ref.decision.status==="pending").map(ref=>JSON.stringify([ref.decision.source.type,ref.decision.source.id])))); return references }
        if (!page.has_more || !page.next_cursor || page.next_cursor === cursor) throw new DecisionReadError(false)
        cursor = page.next_cursor
      }
      throw new DecisionReadError(false)
    },
  })
  const evictSource = useCallback((type: DecisionRef["decision"]["source"]["type"], id: string) => {
    client.setQueryData<DecisionRef[]>(chatDecisionKeys.refs(workspaceId,userId,chatId), current => current?.filter(ref => ref.decision.source.type !== type || ref.decision.source.id !== id))
    if (workspaceId && userId) purgeChatDecisionSourceRecovery(workspaceId,userId,chatId,type,id)
  }, [client,workspaceId,userId,chatId])
  const reconcile = useCallback(() => { if (active) void client.invalidateQueries({ queryKey: chatDecisionKeys.refs(workspaceId, userId, chatId) }) }, [active, client, workspaceId, userId, chatId])
  useRealtimeEventSafe("chat.entity_refs_changed", event => {
    if (event.channel === `chat-hints:${chatId}` && event.payload.contract_version === 1) reconcile()
  })
  useRealtimeEventSafe("escalation.resolved", reconcile)
  useRealtimeEventSafe("escalation.created", reconcile)
  useRealtimeEventSafe("workspace.updated", reconcile)
  useEffect(() => {
    if (!active) client.removeQueries({ queryKey: key })
    // The viewer-scoped key is the lifetime of this projection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, scope, client])
  const unavailable = !active || denied.current || query.error instanceof DecisionReadError && query.error.denied
  return { references: unavailable ? [] : query.data ?? [], rechecking: active && (query.isFetching || query.isPending || !!query.error), unavailable, reconcile, evictSource }
}
