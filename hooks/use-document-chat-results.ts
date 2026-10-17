"use client"
import { useCallback } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { useRealtimeEventSafe } from "@/hooks/use-realtime"
export interface DocumentChatResult { page_id: string; type: "document"; page_slug: string; title: string; revision: number; created_at: string; agent_id: string; agent_name: string; run_id: string; on_behalf_of_user_id: string }
interface DocumentChatResults { results: DocumentChatResult[] }
export class DocumentChatResultsError extends Error { constructor(message: string, public status: number) { super(message) } }
/** Only server-verified publication links. Never parse model text or infer a
 * result from an agent run finishing or an artifact's current head. */
export function useDocumentChatResults(workspaceId: string, chatId: string, currentUserId: string | null | undefined, streaming: boolean, enabled = false) {
  const client = useQueryClient()
  const available = enabled === true && Boolean(workspaceId && chatId && currentUserId)
  const key = ["document-chat-results", workspaceId, chatId, currentUserId ?? ""] as const
  const invalidate = useCallback(() => { void client.invalidateQueries({ queryKey: ["document-chat-results", workspaceId, chatId, currentUserId ?? ""] }) }, [client, workspaceId, chatId, currentUserId])
  useRealtimeEventSafe("page.updated", invalidate)
  useRealtimeEventSafe("conversation.updated", invalidate)
  useRealtimeEventSafe("run.completed", invalidate)
  useRealtimeEventSafe("realtime.reconnected", invalidate)
  const query = useQuery<DocumentChatResults>({ queryKey: key, enabled: available, retry: false, gcTime: 0, refetchInterval: streaming ? 5000 : 60000, refetchIntervalInBackground: false,
    queryFn: async ({ signal }) => {
      if (!available) return { results: [] }
      const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/artifact-results?${new URLSearchParams({ workspace_id: workspaceId })}`, { signal })
      const body = await response.json().catch(() => null)
      if (!response.ok) {
        if ([401, 403, 404].includes(response.status)) client.setQueryData<DocumentChatResults>(key, { results: [] })
        throw new DocumentChatResultsError(body?.error ?? "Could not refresh saved Documents.", response.status)
      }
      if (!Array.isArray(body?.results) || body.results.length > 20) throw new Error("Invalid saved Document response.")
      const results: DocumentChatResult[] = body.results.filter((item: DocumentChatResult) => item && typeof item.page_slug === "string" && item.page_slug && typeof item.title === "string" && Number.isSafeInteger(item.revision) && ((item.type === "document" && item.revision === 0 && typeof item.page_id === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(item.page_id))) && typeof item.agent_id === "string" && item.agent_id && typeof item.run_id === "string" && item.run_id && typeof item.on_behalf_of_user_id === "string" )
      return { results }
    },
  })
  return { ...query, data: available ? query.data : undefined }
}
