"use client"
import { useCallback } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
export interface ArtifactChatResult { page_slug: string; title: string; revision: number; created_at: string; agent_id: string; agent_name: string; run_id: string; on_behalf_of_user_id: string }
interface ArtifactChatResults { results: ArtifactChatResult[] }
export class ArtifactChatResultsError extends Error { constructor(message: string, public status: number) { super(message) } }
/** Only server-verified publication links. Never parse model text or infer a
 * result from an agent run finishing or an artifact's current head. */
export function useArtifactChatResults(workspaceId: string, chatId: string, streaming: boolean) {
  const client = useQueryClient()
  const key = ["artifact-chat-results", workspaceId, chatId] as const
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ["artifact-chat-results", workspaceId, chatId] }) }, [client, workspaceId, chatId])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("conversation.updated", invalidate)
  useRealtimeEventSafe("run.completed", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  return useQuery<ArtifactChatResults>({ queryKey: key, enabled: Boolean(workspaceId && chatId), retry: false, gcTime: 0, refetchInterval: streaming ? 5000 : 60000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/artifact-results?${new URLSearchParams({ workspace_id: workspaceId })}`, { signal })
      const body = await response.json().catch(() => null)
      if (!response.ok) {
        if ([401, 403, 404].includes(response.status)) client.setQueryData<ArtifactChatResults>(key, { results: [] })
        throw new ArtifactChatResultsError(body?.error ?? "Could not refresh published artifacts.", response.status)
      }
      if (!Array.isArray(body?.results) || body.results.length > 20) throw new Error("Invalid published artifact response.")
      const results: ArtifactChatResult[] = body.results.filter((item: ArtifactChatResult) => item && typeof item.page_slug === "string" && item.page_slug && typeof item.title === "string" && Number.isSafeInteger(item.revision) && item.revision > 0 && typeof item.agent_id === "string" && item.agent_id && typeof item.run_id === "string" && item.run_id && typeof item.on_behalf_of_user_id === "string" && item.on_behalf_of_user_id)
      return { results }
    },
  })
}
