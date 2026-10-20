"use client"

import { useEffect, useLayoutEffect } from "react"

import { useSessionSafe } from "@/hooks/use-auth"
import { useWorkspace } from "@/hooks/use-workspace"
import { cancelJobsForScope, currentIdentity, enterScope, setIdentityConfirmed } from "@/lib/chat-attachment-jobs"
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

export interface IdentityInput {
  userId: string | null | undefined
  status: string
  workspaceId: string | null | undefined
}
interface Identity {
  userId: string | null
  prefix: string
}

/**
 * What the scope becomes, given what it is and what auth/workspace say now.
 * Pure, so a render can use it without side effects.
 *
 * Only a change from one real identity to another, or an explicit sign-out,
 * moves the scope. A momentary gap (session being revalidated, workspace
 * briefly unknown) keeps the current one - cancelling an upload because a
 * request was in flight would be worse than the problem it guards.
 */
export function nextIdentity(cur: Identity, input: IdentityInput): Identity {
  if (input.status === "unauthenticated") return { userId: null, prefix: "" }
  if (!input.userId) return cur // session unread: not a sign-out
  if (input.workspaceId) {
    return { userId: input.userId, prefix: attachmentScopePrefix(input.userId, input.workspaceId) }
  }
  // Known person, workspace momentarily unknown. The same person: keep going.
  // A different person must not inherit the previous scope.
  return input.userId === cur.userId ? cur : { userId: input.userId, prefix: "" }
}

/** Commit the identity: advance the epoch and, when a real scope is left,
 *  cancel its jobs and drop its chips. Idempotent. */
export function applyIdentity(input: IdentityInput) {
  const next = nextIdentity(currentIdentity(), input)
  const { changed, previous } = enterScope(next.prefix, next.userId)
  if (changed && previous) {
    cancelJobsForScope(previous)
    useComposerStore.getState().dropAttachmentsByPrefix(previous)
  }
  // Unknown (session unread, workspace unknown) is not confirmed: queued jobs
  // pause until the same identity is back; a different one releases them stale.
  setIdentityConfirmed(isConfirmed(input))
}

/** A person AND a workspace are known right now. */
export function isConfirmed(input: IdentityInput): boolean {
  return !!input.userId && !!input.workspaceId
}

// Layout effects do not run on the server; fall back so SSR does not warn.
const useCommitEffect = typeof window === "undefined" ? useEffect : useLayoutEffect

function useIdentityInput(workspaceId: string | null | undefined): IdentityInput {
  const session = useSessionSafe()
  return { userId: session.data?.user?.id, status: session.status, workspaceId }
}

/** Keep the module's scope in step with auth and workspace. Safe to call from
 *  any number of places; `AttachmentScopeSync` calls it from the app shell so it
 *  does not depend on a composer being mounted. */
export function useAttachmentScopeSync(workspaceId: string | null | undefined) {
  const input = useIdentityInput(workspaceId)
  // At commit (before paint and before passive effects), so the confirmation
  // flag a paused job reads is as fresh as the render that saw the change.
  useCommitEffect(() => {
    applyIdentity(input)
  }, [input.userId, input.status, input.workspaceId])
}

/** The prefix for this render, derived from the committed identity plus what
 *  auth says now (no one-render window where a new person reads the old key),
 *  and kept in sync as a side effect for consumers mounted without the shell. */
export function useAttachmentScopePrefix(): string {
  const { workspaceId } = useWorkspace()
  const input = useIdentityInput(workspaceId)
  useAttachmentScopeSync(workspaceId)
  // While the identity is unknown the previous person's chips must not show:
  // the empty prefix reads nothing (the Files are kept under their own key).
  return isConfirmed(input) ? nextIdentity(currentIdentity(), input).prefix : ""
}

/** `{ prefix, key, ready }`: `key` indexes the composer store and the upload
 *  queue for this chat; `ready` is false until a human and workspace exist. */
export function useAttachmentScope(chatId: string): {
  prefix: string
  key: string
  ready: boolean
  /** Identity is unknown right now but a person is on record: files are held
   *  (masked), nothing may send, and the composer says it is waiting. */
  waitingForSignIn: boolean
} {
  const prefix = useAttachmentScopePrefix()
  const { workspaceId } = useWorkspace()
  const input = useIdentityInput(workspaceId)
  const waitingForSignIn = !isConfirmed(input) && input.status !== "unauthenticated" && currentIdentity().prefix !== ""
  return { prefix, key: prefix + chatId, ready: prefix !== "", waitingForSignIn }
}
