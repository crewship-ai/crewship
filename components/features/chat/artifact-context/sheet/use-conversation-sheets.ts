"use client"

import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"

/** At most this many pages of references are read; a conversation with more lists the oldest ones. */
const MAX_PAGES = 5

export const conversationRefsKey = (workspaceId: string | null | undefined, chatId: string | null | undefined) => ["chat-artifact-refs", workspaceId, chatId] as const

/**
 * Sheet ids from the server's pages of references, oldest first and each once,
 * then the ones opened in this browser that the server does not list yet. A
 * reference to another kind of artifact is left out; the link itself still asks
 * the server whether the reader sees a Sheet there.
 */
export function sheetIdsFromRefs(pages: unknown[], openedHere: string[]): string[] {
  const ids: string[] = []
  for (const page of pages) {
    const refs = page && typeof page === "object" && Array.isArray((page as { refs?: unknown }).refs) ? (page as { refs: unknown[] }).refs : []
    for (const ref of refs) {
      if (!ref || typeof ref !== "object") continue
      const { page_id: id, application } = ref as { page_id?: unknown; application?: unknown }
      if (typeof id !== "string" || !id || (application !== undefined && application !== "gridcraft")) continue
      if (!ids.includes(id)) ids.push(id)
    }
  }
  for (const id of openedHere) if (!ids.includes(id)) ids.push(id)
  return ids
}

/** Every page of this conversation's references the server lists for this reader (at most MAX_PAGES). */
async function fetchConversationRefs(workspaceId: string, chatId: string, signal: AbortSignal): Promise<unknown[]> {
  const pages: unknown[] = []
  let cursor: string | undefined
  for (let i = 0; i < MAX_PAGES; i++) {
    const params = new URLSearchParams({ workspace_id: workspaceId, limit: "100" })
    if (cursor) params.set("cursor", cursor)
    const response = await apiFetch(`/api/v1/chats/${encodeURIComponent(chatId)}/artifact-refs?${params}`, { signal })
    if (!response.ok) throw Error("artifact_refs_unavailable")
    const page: unknown = await response.json()
    pages.push(page)
    const next = page && typeof page === "object" ? (page as { next_cursor?: unknown }).next_cursor : undefined
    if (typeof next !== "string" || !next) break
    cursor = next
  }
  return pages
}

function useConversationRefs(workspaceId: string | null | undefined, chatId: string | null | undefined) {
  const enabled = Boolean(workspaceId && chatId)
  const query = useQuery({
    queryKey: conversationRefsKey(workspaceId, chatId),
    enabled,
    retry: false,
    staleTime: 0,
    queryFn: ({ signal }) => fetchConversationRefs(workspaceId!, chatId!, signal),
  })
  return enabled && query.data ? query.data : []
}

/** The Sheets messages in this conversation carried, as the server lists them for this reader, plus those opened here. */
export function useConversationSheets(workspaceId: string | null | undefined, chatId: string | null | undefined, openedHere: string[]): string[] {
  return sheetIdsFromRefs(useConversationRefs(workspaceId, chatId), openedHere)
}

/**
 * The Sheets one message went with: what it carried when it was sent here, then
 * what the server recorded for that message id (a reloaded history keeps no
 * artifact_refs on the message itself). Each id once.
 */
export function messageSheetIds(sentRefs: unknown, pages: unknown[], messageId: string | undefined): string[] {
  const ids: string[] = []
  for (const ref of Array.isArray(sentRefs) ? sentRefs : []) {
    const id = ref && typeof ref === "object" ? (ref as { page_id?: unknown }).page_id : undefined
    if (typeof id === "string" && id && !ids.includes(id)) ids.push(id)
  }
  if (!messageId) return ids
  for (const page of pages) {
    const refs = page && typeof page === "object" && Array.isArray((page as { refs?: unknown }).refs) ? (page as { refs: unknown[] }).refs : []
    for (const ref of refs) {
      if (!ref || typeof ref !== "object") continue
      const { page_id: id, message_id: owner, application } = ref as { page_id?: unknown; message_id?: unknown; application?: unknown }
      if (owner !== messageId || typeof id !== "string" || !id || (application !== undefined && application !== "gridcraft")) continue
      if (!ids.includes(id)) ids.push(id)
    }
  }
  return ids
}

/** The Sheets this message went with; see messageSheetIds. */
export function useMessageSheets(workspaceId: string | null | undefined, chatId: string | null | undefined, messageId: string | undefined, sentRefs: unknown): string[] {
  return messageSheetIds(sentRefs, useConversationRefs(workspaceId, messageId ? chatId : null), messageId)
}
