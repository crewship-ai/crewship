"use client"

import { useCallback } from "react"
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"

export interface WorkspaceConversation {
  id: string
  title: string
  created_by: string
  kind: "group" | "channel"
  is_direct: boolean
  access_scope: "participants" | "workspace"
  last_sequence: number
  unread_count: number
  direct_user_name?: string
  direct_user_id?: string
  direct_avatar_url?: string | null
  muted?: boolean
}
export interface WorkspaceMessage {
  id: string
  client_id: string
  sequence: number
  author_agent_id?: string
  source_kind?: string
  kind?: string
  mentioned_agent_ids?: string[]
  author_user_id: string
  author_avatar_url?: string | null
  author_avatar_style?: string | null
  author_avatar_seed?: string
  author_slug?: string
  author_name: string
  content: string
  created_at: string
}
export interface ConversationParticipant { user_id: string; name: string; role: string; avatar_url?: string | null }
export interface ConversationPerson { id: string; full_name: string | null; email: string; avatar_url?: string | null }

export class ConversationRequestError extends Error {
  constructor(public status: number, message: string) { super(message) }
}

export async function conversationRequest<T>(workspaceId: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await apiFetch(`/api/v1/${path}${path.includes("?") ? "&" : "?"}workspace_id=${encodeURIComponent(workspaceId)}`, {
    signal,
    ...(body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }),
  })
  if (!response.ok) throw new ConversationRequestError(response.status, response.status === 403 || response.status === 404 ? "This conversation is unavailable or you no longer have access." : `Unable to complete request (${response.status}). Please retry.`)
  if (response.status === 204) return undefined as T
  return response.json()
}

export function useWorkspaceConversations(workspaceId: string, userId: string) {
  const client = useQueryClient()
  const key = ["workspace-conversations", workspaceId, userId]
  const refresh = useCallback(() => {
    void client.invalidateQueries({ queryKey: ["workspace-conversations", workspaceId, userId] })
  }, [client, workspaceId, userId])
  useRealtimeEventSafe("conversation.updated", refresh)
  useRealtimeEventSafe("assignment.updated", refresh)
  useRealtimeEventSafe("realtime.reconnected", refresh)
  const list = useInfiniteQuery({
    queryKey: [...key, "list"],
    initialPageParam: 0,
    queryFn: ({ signal, pageParam }) => conversationRequest<{ conversations: WorkspaceConversation[]; next_offset: number | null }>(workspaceId, `conversations?offset=${pageParam}&limit=100`, undefined, signal),
    getNextPageParam: (page) => page.next_offset ?? undefined,
    enabled: !!workspaceId && !!userId,
    refetchInterval: 120_000,
    refetchIntervalInBackground: false,
  })
  return { list, refresh, key }
}

export function useConversationMessages(workspaceId: string, userId: string, conversationId: string) {
  return useInfiniteQuery({
    queryKey: ["workspace-conversations", workspaceId, userId, conversationId, "messages"],
    initialPageParam: 0,
    queryFn: ({ signal, pageParam }) => conversationRequest<{ messages: WorkspaceMessage[]; has_more: boolean }>(workspaceId, `conversations/${encodeURIComponent(conversationId)}/messages?limit=100${pageParam ? `&before_sequence=${pageParam}` : ""}`, undefined, signal),
    getNextPageParam: (page) => page.has_more ? page.messages[0]?.sequence : undefined,
    enabled: !!workspaceId && !!userId,
    refetchInterval: 120_000,
    refetchIntervalInBackground: false,
  })
}
