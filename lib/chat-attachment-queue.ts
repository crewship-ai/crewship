/** Limits and the single upload queue behind every attachment entry point
 *  (picker, paste, drop, camera, retry). CH-18. */

export interface AttachmentLimits {
  maxCount: number
  maxBytes: number
}

/** PROVISIONAL client-side limits. The server has not approved any attachment
 *  limit yet: 25 MB is the existing upload guard, and the count of 10 is a
 *  placeholder, not a product or server limit. This is the only place either
 *  number is written; the composer, the refusal text and the tests all read
 *  it, so swapping in server capabilities (CH-17) means replacing this one
 *  constant (or passing `limits` through). */
export const PROVISIONAL_ATTACHMENT_LIMITS: AttachmentLimits = {
  maxCount: 10,
  maxBytes: 25 * 1024 * 1024,
}

/** Clipboard text, if any (text/plain). */
export function textFromClipboard(data: Pick<DataTransfer, "getData"> | null | undefined): string {
  try {
    return data?.getData?.("text/plain") ?? ""
  } catch {
    return ""
  }
}

export type AttachmentRefusal = "too_large" | "empty" | "too_many"

export interface RejectedFile {
  file: File
  reason: AttachmentRefusal
}

export interface BatchValidation {
  accepted: File[]
  rejected: RejectedFile[]
}

/** Validate a batch against the limits. `existingCount` is the number of
 *  attachments already in the composer that still count (everything except
 *  cancelled ones). Files are taken in order until the count limit is hit. */
export function validateAttachmentBatch(
  existingCount: number,
  files: File[],
  limits: AttachmentLimits = PROVISIONAL_ATTACHMENT_LIMITS,
): BatchValidation {
  const accepted: File[] = []
  const rejected: RejectedFile[] = []
  for (const file of files) {
    if (file.size === 0) rejected.push({ file, reason: "empty" })
    else if (file.size > limits.maxBytes) rejected.push({ file, reason: "too_large" })
    else if (existingCount + accepted.length >= limits.maxCount) rejected.push({ file, reason: "too_many" })
    else accepted.push(file)
  }
  return { accepted, rejected }
}

export function refusalMessage(
  name: string,
  reason: AttachmentRefusal,
  limits: AttachmentLimits = PROVISIONAL_ATTACHMENT_LIMITS,
): string {
  switch (reason) {
    case "too_large":
      return `${name} exceeds ${Math.round(limits.maxBytes / 1024 / 1024)} MB`
    case "empty":
      return `${name} is empty`
    case "too_many":
      return `${name} was not attached — at most ${limits.maxCount} files per message`
  }
}

/** Files from a clipboard payload, deduplicated by identity. Text-only pastes
 *  return an empty list so the caller leaves normal paste alone. */
export function filesFromClipboard(data: Pick<DataTransfer, "files" | "items"> | null | undefined): File[] {
  if (!data) return []
  const out: File[] = []
  for (const f of Array.from(data.files ?? [])) out.push(f)
  if (out.length === 0) {
    for (const item of Array.from(data.items ?? [])) {
      if (item.kind === "file") {
        const f = item.getAsFile()
        if (f) out.push(f)
      }
    }
  }
  return out
}

/** One serial queue: uploads run one at a time in the order they were queued,
 *  whichever control produced them. Each enqueue gets a token; a task whose id
 *  was cancelled or re-enqueued before it started is stale and never runs, so
 *  cancel-then-retry uploads exactly once. `onIdle` fires when the last task
 *  finishes so the owner can drop the queue. */
export function createUploadQueue(onIdle?: () => void) {
  let tail: Promise<void> = Promise.resolve()
  let seq = 0
  let pending = 0
  const current = new Map<string, number>()
  return {
    enqueue(id: string, run: () => Promise<void>): Promise<void> {
      const token = ++seq
      current.set(id, token)
      pending++
      const next = tail.then(async () => {
        try {
          if (current.get(id) !== token) return
          try {
            await run()
          } catch {
            // A task reports its own failure; the queue must keep draining.
          }
        } finally {
          if (current.get(id) === token) current.delete(id)
          if (--pending === 0) onIdle?.()
        }
      })
      tail = next
      return next
    },
    /** Invalidate a queued id so it is skipped if it has not started. */
    cancel(id: string) {
      current.delete(id)
    },
    size: () => current.size,
  }
}
