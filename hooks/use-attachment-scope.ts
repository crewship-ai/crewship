"use client"

import { useSessionSafe } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"

/**
 * The prefix that makes the composer's attachment list belong to ONE human in
 * ONE workspace. The store is keyed `<prefix><chatId>`, so another person who
 * opens the same group chat in the same browser reads an empty list and cannot
 * inherit names, pending Files or a queued upload from the previous person.
 *
 * No signed-in human means no prefix (the bare chat id): there is nobody to
 * scope by, and nothing can upload without a workspace anyway.
 */
export function attachmentScopePrefix(userId: string | null | undefined, workspaceId: string | null | undefined): string {
  return userId ? `${userId}::${workspaceId ?? ""}::` : ""
}

export function useAttachmentScopePrefix(): string {
  const session = useSessionSafe()
  const { workspaceId } = useWorkspace()
  return attachmentScopePrefix(session.data?.user?.id, workspaceId)
}

/** `{ prefix, key }`: `key` is what the composer store and the upload queue are
 *  indexed by for this chat. */
export function useAttachmentScope(chatId: string): { prefix: string; key: string } {
  const prefix = useAttachmentScopePrefix()
  return { prefix, key: prefix + chatId }
}
