/** Limits and the single upload queue behind every attachment entry point
 *  (picker, paste, drop, camera, retry). CH-18. */

export interface AttachmentLimits {
  maxCount: number
  maxBytes: number
}

/** Defaults used until the server's capabilities for the context say otherwise
 *  (CH-17). The size cap matches the upload endpoint's existing 25 MB guard. */
export const DEFAULT_ATTACHMENT_LIMITS: AttachmentLimits = {
  maxCount: 10,
  maxBytes: 25 * 1024 * 1024,
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
  limits: AttachmentLimits = DEFAULT_ATTACHMENT_LIMITS,
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
  limits: AttachmentLimits = DEFAULT_ATTACHMENT_LIMITS,
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
 *  whichever control produced them. A task whose id was cancelled before it
 *  started never runs. */
export function createUploadQueue() {
  let tail: Promise<void> = Promise.resolve()
  const cancelled = new Set<string>()
  return {
    enqueue(id: string, run: () => Promise<void>): Promise<void> {
      cancelled.delete(id)
      const next = tail.then(async () => {
        if (cancelled.has(id)) {
          cancelled.delete(id)
          return
        }
        try {
          await run()
        } catch {
          // A task reports its own failure; the queue must keep draining.
        }
      })
      tail = next
      return next
    },
    /** Mark an id so it is skipped if it has not started yet. */
    cancel(id: string) {
      cancelled.add(id)
    },
  }
}
