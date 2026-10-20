import { createUploadQueue } from "@/lib/chat-attachment-queue"

/**
 * Upload jobs for the chat composer, held outside React so they outlive the
 * composer's own mounts. The composer remounts on a breakpoint change and on
 * the phone's Artifacts / Work tab; an upload must not notice.
 *
 * A "key" is `<userId>::<workspaceId>::<chatId>`. A "scope" prefix is
 * `<userId>::<workspaceId>::` and always ends with the delimiter, so one chat
 * id (or user id) that merely starts with another never matches it.
 */

const queues = new Map<string, ReturnType<typeof createUploadQueue>>()

/** One serial queue per key. Picker, paste, drop, camera and retry share it. */
export function queueFor(key: string) {
  let q = queues.get(key)
  if (!q) queues.set(key, (q = createUploadQueue(() => queues.delete(key))))
  return q
}

const aborts = new Map<string, { key: string; ac: AbortController }>()
const abortId = (key: string, id: string) => `${key}\u0000${id}`

export function registerAbort(key: string, id: string, ac: AbortController) {
  aborts.set(abortId(key, id), { key, ac })
}
export function clearAbort(key: string, id: string) {
  aborts.delete(abortId(key, id))
}
export function abortIfPending(key: string, id: string) {
  const e = aborts.get(abortId(key, id))
  if (e) {
    e.ac.abort()
    aborts.delete(abortId(key, id))
  }
}

/** Cancel everything for ONE chat key: queued tasks never start, running
 *  requests abort. Exact match only. */
export function cancelJobsForKey(key: string) {
  queues.get(key)?.cancelAll()
  for (const [id, e] of aborts) {
    if (e.key === key) {
      e.ac.abort()
      aborts.delete(id)
    }
  }
}

/** Cancel everything under a human+workspace scope (identity change, logout,
 *  leaving the chat page). */
export function cancelJobsForScope(prefix: string) {
  if (!prefix || !prefix.endsWith("::")) {
    throw new Error("a scope prefix must be non-empty and end with '::'")
  }
  for (const [k, q] of queues) if (k.startsWith(prefix)) q.cancelAll()
  for (const [id, e] of aborts) {
    if (e.key.startsWith(prefix)) {
      e.ac.abort()
      aborts.delete(id)
    }
  }
}

// The scope the app is currently in, and a counter that moves every time it
// changes. A job remembers the epoch it was queued under and refuses to touch
// the network if it has moved on - including A -> B -> A, where the prefix
// alone would look current again.
let scopePrefix: string | null = null
let scopeUserId: string | null = null
let epoch = 0

export function currentScopeEpoch(): number {
  return epoch
}

// Is the current identity CONFIRMED (a person and a workspace are known right
// now)? While it is not, nothing may start sending: the Files are kept, but
// the network waits until the same identity is confirmed again. If a
// different identity is confirmed instead, the epoch moves and the waiting
// jobs are released as stale.
let confirmed = false
const waiters: Array<{ epoch: number; resolve: (ok: boolean) => void }> = []

function flushWaiters() {
  for (let i = waiters.length - 1; i >= 0; i--) {
    const w = waiters[i]
    if (w.epoch !== epoch) {
      waiters.splice(i, 1)
      w.resolve(false)
    } else if (confirmed) {
      waiters.splice(i, 1)
      w.resolve(true)
    }
  }
}

/** Synchronous read of the committed confirmation. */
export function identityConfirmed(): boolean {
  return confirmed
}

export function setIdentityConfirmed(value: boolean) {
  confirmed = value
  flushWaiters()
}

/** Resolves true when it is safe to send for a job queued under `queuedEpoch`
 *  (identity confirmed, same epoch), false when that epoch has been left. */
export function untilConfirmed(queuedEpoch: number): Promise<boolean> {
  if (epoch !== queuedEpoch) return Promise.resolve(false)
  if (confirmed) return Promise.resolve(true)
  return new Promise((resolve) => {
    waiters.push({ epoch: queuedEpoch, resolve })
  })
}

/** The identity the module last committed. Consumers derive from it (see
 *  hooks/use-attachment-scope.ts) so a transient gap does not read as a change. */
export function currentIdentity(): { userId: string | null; prefix: string } {
  return { userId: scopeUserId, prefix: scopePrefix ?? "" }
}

/** Move the epoch without touching jobs, for callers that cancel on their own
 *  terms (leaving the chat page): cancel and epoch must move together. */
export function bumpEpoch() {
  epoch++
  flushWaiters()
}

export function enterScope(
  prefix: string,
  userId: string | null = null,
): { changed: boolean; previous: string | null } {
  if (prefix === scopePrefix && userId === scopeUserId) return { changed: false, previous: scopePrefix }
  const previous = scopePrefix
  scopePrefix = prefix
  scopeUserId = userId
  epoch++
  flushWaiters()
  return { changed: true, previous }
}

/** Test isolation: forget queues, abort handles and the scope. */
export function __resetForTests() {
  for (const q of queues.values()) q.cancelAll()
  queues.clear()
  for (const e of aborts.values()) e.ac.abort()
  aborts.clear()
  scopePrefix = null
  scopeUserId = null
  epoch = 0
  confirmed = false
  for (const w of waiters.splice(0)) w.resolve(false)
}
