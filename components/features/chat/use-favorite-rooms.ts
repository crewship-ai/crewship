"use client"

import { useQueries } from "@tanstack/react-query"
import { conversationRequest, type WorkspaceConversation } from "@/hooks/use-workspace-conversations"

/** Resolve pinned IDs through normal ACL-checked reads, even beyond the first list page. */
export function useFavoriteRooms(workspaceId: string | undefined, userId: string | undefined, favorites: string[], loaded: WorkspaceConversation[]) {
  const missing = favorites.filter((id) => id.startsWith("room:") && !loaded.some((room) => `room:${room.id}` === id)).map((id) => id.slice(5))
  const results = useQueries({ queries: missing.map((id) => ({
    queryKey: ["chat-favorite-room", workspaceId, userId, id],
    queryFn: ({ signal }: { signal: AbortSignal }) => conversationRequest<WorkspaceConversation>(workspaceId!, `conversations/${encodeURIComponent(id)}`, undefined, signal),
    enabled: !!workspaceId && !!userId,
    retry: false,
    refetchInterval: 120_000,
    refetchOnWindowFocus: true,
  })) })
  return results.flatMap((result, i) => !result.isError && result.data?.id === missing[i] ? [result.data] : [])
}
