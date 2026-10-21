"use client"
import { useCallback, useMemo, useRef } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
import { fetchLibrary, fetchLinks, fetchWorkCards, type Fetched, type LibraryRow, type WorkCardEntry } from "./client"
import type { ConversationLink } from "./linked-viewer-targets"
import { useArtifactContextEnabled } from "./use-artifact-context-enabled"

interface Scope { workspaceId: string; chatId: string; currentUserId?: string | null }

/** Each surface reads through this: with the flag off the query is disabled, so no request leaves the page. */
function useChatContextQuery<T>(name: string, scope: Scope, fetcher: (signal: AbortSignal) => Promise<Fetched<T>>, refetchInterval: (data: Fetched<T> | undefined) => number | false) {
  const enabled = useArtifactContextEnabled() && Boolean(scope.workspaceId && scope.chatId && scope.currentUserId)
  const client = useQueryClient()
  const key = useMemo(() => ["chat-artifact-context", name, scope.workspaceId, scope.chatId, scope.currentUserId ?? ""] as const, [name, scope.workspaceId, scope.chatId, scope.currentUserId])
  const invalidate = useCallback(() => { if (enabled) void client.invalidateQueries({ queryKey: key }) }, [client, enabled, key])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("run.completed", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const query = useQuery<Fetched<T>>({
    queryKey: key, enabled, retry: false, gcTime: 0,
    refetchInterval: query => refetchInterval(query.state.data), refetchIntervalInBackground: false,
    queryFn: ({ signal }) => fetcher(signal),
  })
  // A failed refresh keeps the last good data of this same scope and says so; a denial or an unserved route never does.
  const lastGood = useRef<{ key: string; value: Fetched<T> } | null>(null)
  const scopeKey = JSON.stringify(key)
  if (enabled && query.data?.status === "ok") lastGood.current = { key: scopeKey, value: query.data }
  if (!enabled || (query.data && query.data.status !== "error" && query.data.status !== "ok")) lastGood.current = null
  const stale = enabled && query.data?.status === "error" && lastGood.current?.key === scopeKey
  const result = !enabled ? undefined : stale ? lastGood.current!.value : query.data
  return { enabled, refetch: query.refetch, loading: enabled && query.isPending, result, stale }
}

const ACTIVE = new Set(["queued", "running", "waiting"])

export function useWorkCards(scope: Scope) {
  const query = useChatContextQuery<WorkCardEntry[]>("work-cards", scope, signal => fetchWorkCards(scope.workspaceId, scope.chatId, scope.currentUserId, signal),
    data => data?.status === "ok" && data.data.some(entry => ACTIVE.has(entry.model.runState)) ? 8000 : false)
  return { ...query, cards: query.result?.status === "ok" ? query.result.data : [] }
}

export function useConversationLinks(scope: Scope) {
  const query = useChatContextQuery<ConversationLink[]>("links", scope, signal => fetchLinks(scope.workspaceId, scope.chatId, signal), () => false)
  return { ...query, links: query.result?.status === "ok" ? query.result.data : [] }
}

export function useConversationLibrary(scope: Scope & { open: boolean }) {
  const query = useChatContextQuery<LibraryRow[]>("library", { ...scope, currentUserId: scope.open ? scope.currentUserId : null }, signal => fetchLibrary(scope.workspaceId, scope.chatId, signal), () => false)
  return { ...query, rows: query.result?.status === "ok" ? query.result.data : [] }
}
