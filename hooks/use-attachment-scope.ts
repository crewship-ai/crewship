"use client"

import { useEffect } from "react"

import { useSessionSafe } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { cancelJobsForScope, enterScope } from "@/lib/chat-attachment-jobs"
import { useComposerStore } from "@/stores/composer-store"

/**
 * The prefix that makes the composer's attachment list belong to ONE human in
 * ONE workspace: `<userId>::<workspaceId>::`. The store, the upload queues and
 * the abort handles are keyed `<prefix><chatId>`, so another person who opens
 * the same group chat in the same browser reads an empty list and cannot
 * inherit names, pending Files or a queued upload.
 *
 * Empty while nobody is signed in or no workspace is loaded: uploads are
 * refused in that state (see `ready`), so no job is ever keyed under "".
 */
export function attachmentScopePrefix(userId: string | null | undefined, workspaceId: string | null | undefined): string {
  return userId && workspaceId ? `${userId}::${workspaceId}::` : ""
}

/** Prefix plus a module-level scope that outlives every component: when the
 *  scope changes, the old one's jobs are cancelled and its chips dropped no
 *  matter which component (or none) is mounted at that moment. */
export function useAttachmentScopePrefix(): string {
  const session = useSessionSafe()
  const { workspaceId } = useWorkspace()
  const prefix = attachmentScopePrefix(session.data?.user?.id, workspaceId)
  useEffect(() => {
    const { changed, previous } = enterScope(prefix)
    if (changed && previous) {
      cancelJobsForScope(previous)
      useComposerStore.getState().dropAttachmentsByPrefix(previous)
    }
  }, [prefix])
  return prefix
}

/** `{ prefix, key, ready }`: `key` indexes the composer store and the upload
 *  queue for this chat; `ready` is false until a human and workspace exist. */
export function useAttachmentScope(chatId: string): { prefix: string; key: string; ready: boolean } {
  const prefix = useAttachmentScopePrefix()
  return { prefix, key: prefix + chatId, ready: prefix !== "" }
}
