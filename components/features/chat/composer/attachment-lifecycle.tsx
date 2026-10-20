"use client"

import { useEffect } from "react"

import { bumpEpoch, cancelJobsForScope, currentIdentity } from "@/lib/chat-attachment-jobs"
import { useComposerStore } from "@/stores/composer-store"

/**
 * Owns the upload queue's lifetime for the chat page. Mounted once, above both
 * layouts (desktop and phone), so the composer remounting - a breakpoint change
 * or the phone's Artifacts / Work tab - never touches an upload.
 *
 * Switching to another chat does NOT cancel: the queue is module-level, so the
 * upload finishes in the background and is done when the person comes back.
 *
 * Leaving the chat page does cancel: running requests abort, queued jobs never
 * start, the epoch moves (so nothing in flight can send later), and unfinished
 * chips stay as "cancelled" so the same person can retry them.
 *
 * Changes of person or workspace and sign-out are `AttachmentScopeSync`'s job.
 */
export function AttachmentLifecycle() {
  useEffect(
    () => () => {
      const p = currentIdentity().prefix
      if (!p) return
      cancelJobsForScope(p)
      bumpEpoch()
      const { attachments, updateAttachment } = useComposerStore.getState()
      for (const [k, list] of Object.entries(attachments)) {
        if (!k.startsWith(p)) continue
        for (const a of list) {
          if (a.status === "uploading" || a.status === "validating") {
            updateAttachment(k, a.id, { status: "cancelled", error: undefined })
          }
        }
      }
    },
    [],
  )

  return null
}
