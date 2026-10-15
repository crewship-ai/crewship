"use client"

import { useCallback, useEffect, useRef } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import type { EntityRef, EntityRefPage } from "@/lib/chat-entity-cards"
import { useRealtimeChannelSafe, useRealtimeEventSafe, useRealtimeStatusSafe } from "@/hooks/use-realtime"

export const chatEntityKeys = {
  refs: (workspace: string | null, chat: string, user: string | undefined) => ["chat-entity-refs", workspace, chat, user] as const,
  detail: (workspace: string | null, chat: string, user: string | undefined) => ["chat-issue-detail", workspace, chat, user] as const,
}
const RECHECK_MS = 60_000
class RefError extends Error {
  constructor(readonly unavailable: boolean) { super(unavailable ? "Unavailable" : "Could not verify current access") }
}

export function useChatEntityRefs({ workspaceId, chatId, userId, enabled }: {
  workspaceId: string | null; chatId: string; userId?: string; enabled: boolean
}) {
  const client = useQueryClient()
  const key = chatEntityKeys.refs(workspaceId, chatId, userId)
  const cursor = useRef<string | undefined>(undefined)
  const scope = JSON.stringify(key)
  const priorScope = useRef(scope)
  if (priorScope.current !== scope) { cursor.current = undefined; priorScope.current = scope }
  const active = enabled && !!workspaceId && !!chatId && !!userId
  const status = useRealtimeStatusSafe()
  useRealtimeChannelSafe(active ? `session:${chatId}` : null)
  const query = useQuery({
    queryKey: key, enabled: active, retry: false, gcTime: 0, staleTime: 0,
    refetchInterval: RECHECK_MS, refetchIntervalInBackground: false,
    refetchOnWindowFocus: "always", refetchOnReconnect: "always",
    queryFn: async ({ signal }) => {
      // A replacement is an authorization boundary. The renderer also hides
      // data while fetching, including in the gap before page one arrives.
      client.removeQueries({ queryKey: chatEntityKeys.detail(workspaceId, chatId, userId) })
      let next = cursor.current
      let snapshot: string | undefined
      const refs = new Map<string, EntityRef>()
      let restarted = false
      for (let pageCount = 0; pageCount < 1000; pageCount++) {
        const params = new URLSearchParams({ workspace_id: workspaceId!, limit: "100" })
        if (next) params.set("cursor", next)
        const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/entity-refs?${params}`, { signal })
        if (response.status === 400 && !restarted) {
          const error = await response.json().catch(() => null)
          if (error?.error === "cursor_invalid") { next = undefined; cursor.current = undefined; snapshot = undefined; refs.clear(); restarted = true; continue }
        }
        if (!response.ok) { cursor.current = undefined; throw new RefError([401, 403, 404].includes(response.status)) }
        const page = await response.json() as EntityRefPage
        if (page.contract_version != null && page.contract_version !== 1) throw new RefError(false)
        if (page.reset) { refs.clear(); snapshot = page.snapshot_id }
        if (!snapshot || snapshot !== page.snapshot_id || !Array.isArray(page.refs)) throw new RefError(false)
        for (const ref of page.refs) refs.set(ref.event_id, ref)
        if (page.snapshot_complete && !page.has_more) {
          cursor.current = page.next_cursor
          return { refs: [...refs.values()], checkedAt: Date.now() }
        }
        if (!page.has_more || !page.next_cursor || page.next_cursor === next) throw new RefError(false)
        next = page.next_cursor
      }
      throw new RefError(false)
    },
  })
  const reconcile = useCallback(() => {
    if (!active) return
    void client.invalidateQueries({ queryKey: chatEntityKeys.refs(workspaceId, chatId, userId) })
  }, [active, client, workspaceId, chatId, userId])
  useRealtimeEventSafe("chat.entity_refs_changed", (event) => { if (event.payload.contract_version === 1) reconcile() })
  useRealtimeEventSafe("issue.revision_changed", (event) => { if (event.payload.contract_version === 1) reconcile() })
  // Existing access hints are intentionally broad and data-free to this reader.
  useRealtimeEventSafe("workspace.updated", reconcile)
  useEffect(() => { if (status === "connected") reconcile() }, [status, reconcile])
  useEffect(() => {
    const visible = () => { if (document.visibilityState === "visible") reconcile() }
    document.addEventListener("visibilitychange", visible)
    return () => document.removeEventListener("visibilitychange", visible)
  }, [reconcile])
  useEffect(() => {
    if (!active) {
      cursor.current = undefined
      client.removeQueries({ queryKey: key })
      client.removeQueries({ queryKey: chatEntityKeys.detail(workspaceId, chatId, userId) })
    }
    // The serialized key is the lifetime of viewer-scoped authorization.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, scope, client])
  const unavailable = !active || query.error instanceof RefError && query.error.unavailable
  const rechecking = active && (query.isFetching || query.isPending || !!query.error)
  return {
    refs: unavailable || rechecking ? [] : query.data?.refs ?? [],
    slots: unavailable ? [] : query.data?.refs ?? [],
    state: unavailable ? "unavailable" as const : rechecking ? "rechecking" as const : "ready" as const,
    checkedAt: query.data?.checkedAt,
    live: active && !rechecking && !query.error && status === "connected" && Date.now() - (query.data?.checkedAt ?? 0) < RECHECK_MS,
    reconcile,
  }
}
