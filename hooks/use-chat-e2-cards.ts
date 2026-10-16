"use client"

import { useCallback, useEffect, useRef } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { E2CardPageSchema, type E2CardRef } from "@/lib/chat-e2-cards"
import { purgeE2SourceRecovery } from "@/lib/chat-e2-recovery"
import { useRealtimeChannelSafe, useRealtimeEventSafe, useRealtimeStatusSafe } from "@/hooks/use-realtime"

class CardReadError extends Error {
  constructor(readonly denied: boolean) { super(denied ? "Unavailable" : "Could not verify current access") }
}

/** Replacement snapshots are validated in full before replacing any mounted card. */
export function useChatE2Cards({ kind, workspaceId, chatId, userId, enabled }: {
  kind: "proposal" | "decision"; workspaceId: string | null; chatId: string; userId?: string; enabled: boolean
}) {
  const client = useQueryClient()
  const key = ["chat-e2-cards", kind, workspaceId, chatId, userId] as const
  const active = enabled && !!workspaceId && !!chatId && !!userId
  const realtimeStatus = useRealtimeStatusSafe()
  const scope = JSON.stringify(key)
  const liveScope = useRef<string | null>(null)
  const deniedScope = useRef<string | null>(null)
  const identity = useRef<string | null>(null)
  identity.current = active ? scope : null
  const evict = useCallback((refs: E2CardRef[]) => {
    if (!workspaceId || !userId) return
    for (const ref of refs) purgeE2SourceRecovery(workspaceId, userId, ref.entity.id)
  }, [workspaceId, userId])
  const query = useQuery({
    queryKey: key, enabled: active, gcTime: 0, staleTime: 0, retry: false,
    refetchInterval: 60_000, refetchOnWindowFocus: "always", refetchOnReconnect: "always",
    queryFn: async ({ signal }) => {
      let cursor: string | undefined, snapshot: string | undefined
      let restarted = false
      const refs = new Map<string, E2CardRef>()
      for (let count = 0; count < 1000; count++) {
        const params = new URLSearchParams({ workspace_id: workspaceId! })
        if (cursor) params.set("cursor", cursor)
        const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/${kind}s?${params}`, { signal, cache: "no-store" })
        if (signal.aborted || identity.current !== scope) throw new DOMException("Aborted", "AbortError")
        if (response.status === 400 && cursor && !restarted) {
          cursor = undefined; snapshot = undefined; refs.clear(); restarted = true; continue
        }
        if (!response.ok) {
          const denied = [401, 403, 404].includes(response.status)
          if (denied) {
            liveScope.current = null
            deniedScope.current = scope
            evict(client.getQueryData<{ refs: E2CardRef[] }>(key)?.refs ?? [])
            client.setQueryData(key, { refs: [] as E2CardRef[], checkedAt: undefined })
          }
          throw new CardReadError(denied)
        }
        const page = E2CardPageSchema.parse(await response.json())
        if (signal.aborted || identity.current !== scope) throw new DOMException("Aborted", "AbortError")
        if (page.reset) { refs.clear(); snapshot = page.snapshot_id }
        if (!snapshot || snapshot !== page.snapshot_id) throw new CardReadError(false)
        for (const ref of page.refs) {
          if (ref.kind !== kind || ref.entity.workspace_id !== workspaceId) throw new CardReadError(false)
          refs.set(ref.event_id, ref)
        }
        if (page.snapshot_complete && !page.has_more) {
          const present = new Set([...refs.values()].map(ref => ref.entity.id))
          evict((client.getQueryData<{ refs: E2CardRef[] }>(key)?.refs ?? []).filter(ref => !present.has(ref.entity.id)))
          if (deniedScope.current === scope) deniedScope.current = null
          return { refs: [...refs.values()], checkedAt: Date.now() }
        }
        if (!page.has_more || !page.next_cursor || page.next_cursor === cursor) throw new CardReadError(false)
        cursor = page.next_cursor
      }
      throw new CardReadError(false)
    },
  })
  const reconcile = useCallback(() => {
    if (active) void client.invalidateQueries({ queryKey: ["chat-e2-cards", kind, workspaceId, chatId, userId] })
  }, [active, client, kind, workspaceId, chatId, userId])
  useRealtimeChannelSafe(active ? `chat-hints:${chatId}` : null)
  useRealtimeEventSafe("chat.entity_refs_changed", event => {
    if (event.channel === `chat-hints:${chatId}` && event.payload.contract_version === 1) reconcile()
  })
  useRealtimeEventSafe("workspace.updated", reconcile)
  const previousRealtimeStatus = useRef(realtimeStatus)
  useEffect(() => {
    const previous = previousRealtimeStatus.current
    previousRealtimeStatus.current = realtimeStatus
    if (realtimeStatus === "connected" && previous !== "connected") reconcile()
  }, [realtimeStatus, reconcile])
  useEffect(() => {
    const visible = () => { if (document.visibilityState === "visible") reconcile() }
    document.addEventListener("visibilitychange", visible)
    return () => document.removeEventListener("visibilitychange", visible)
  }, [reconcile])
  useEffect(() => {
    if (!active) client.removeQueries({ queryKey: ["chat-e2-cards", kind, workspaceId, chatId, userId] })
  }, [active, client, kind, workspaceId, chatId, userId])
  const unavailable = !active || deniedScope.current === scope || query.error instanceof CardReadError && query.error.denied
  liveScope.current = unavailable ? null : scope
  useEffect(() => () => {
    if (liveScope.current === scope) liveScope.current = null
    if (identity.current === scope) identity.current = null
  }, [scope])
  const applySnapshot = async (ref: E2CardRef) => {
    if (liveScope.current !== scope || !active || ref.kind !== kind || ref.entity.workspace_id !== workspaceId) return
    await client.cancelQueries({ queryKey: key })
    if (liveScope.current !== scope) return
    client.setQueryData<{ refs: E2CardRef[]; checkedAt?: number }>(key, prior => ({
      refs: [...(prior?.refs ?? []).filter(value => value.entity.id !== ref.entity.id), ref],
      checkedAt: undefined,
    }))
    reconcile()
  }
  const evictSource = (id: string) => {
    if (!workspaceId || !userId) return
    purgeE2SourceRecovery(workspaceId, userId, id)
    client.setQueryData<{ refs: E2CardRef[]; checkedAt?: number }>(key, prior => prior && ({
      refs: prior.refs.filter(ref => ref.entity.id !== id), checkedAt: undefined,
    }))
    reconcile()
  }
  return {
    refs: unavailable ? [] : query.data?.refs ?? [],
    state: unavailable ? "unavailable" as const : query.isFetching || query.isPending || query.error ? "rechecking" as const : "ready" as const,
    checkedAt: query.data?.checkedAt, reconcile, applySnapshot, evictSource,
  }
}
