"use client"

import { useEffect, useRef } from "react"

import { useAttachmentScopePrefix } from "@/hooks/use-attachment-scope"
import { cancelJobsForKey, cancelJobsForScope } from "@/lib/chat-attachment-jobs"
import { useComposerStore } from "@/stores/composer-store"

/** Stop pending uploads of one chat and keep their chips as `cancelled`, so the
 *  same person can retry them. Finished chips are left alone. */
function cancelPending(key: string) {
  cancelJobsForKey(key)
  const { attachments, updateAttachment } = useComposerStore.getState()
  for (const a of attachments[key] ?? []) {
    if (a.status === "uploading" || a.status === "validating") {
      updateAttachment(key, a.id, { status: "cancelled", error: undefined })
    }
  }
}

/**
 * Owns the upload queue's lifetime for the chat page. Mounted ONCE, above every
 * layout branch (desktop, phone, Artifacts tab), so it survives the composer
 * remounting - which a breakpoint change or the phone's Artifacts / Work tab
 * does routinely and which must not cancel an upload.
 *
 * Uploads are cancelled when:
 *   - the selected chat changes (that chat's exact key),
 *   - the chat page unmounts (the person left /chat),
 *   - the human or workspace changes or signs out (handled by
 *     `useAttachmentScopePrefix`, which this component also runs so the
 *     invalidation happens with no composer mounted).
 */
export function AttachmentLifecycle({ sessionId }: { sessionId: string | null }) {
  const prefix = useAttachmentScopePrefix()
  const prefixRef = useRef(prefix)
  prefixRef.current = prefix
  const key = sessionId && prefix ? prefix + sessionId : null

  useEffect(() => {
    if (!key) return
    return () => cancelPending(key)
  }, [key])

  useEffect(
    () => () => {
      const p = prefixRef.current
      if (!p) return
      cancelJobsForScope(p)
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
