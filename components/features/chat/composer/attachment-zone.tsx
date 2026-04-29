"use client"

import { useCallback } from "react"
import { toast } from "sonner"

import {
  Attachments,
  AttachmentDropZone,
  AttachmentTrigger,
  type Attachment,
} from "@/components/ai-elements/attachments"
import { useComposerStore } from "@/stores/composer-store"

const MAX_SIZE = 10 * 1024 * 1024

interface AttachmentZoneProps {
  sessionId: string
  children: React.ReactNode
}

export function AttachmentZone({ sessionId, children }: AttachmentZoneProps) {
  const { attachments, addAttachments, removeAttachment } = useComposerStore()
  const sessionAttachments = attachments[sessionId] ?? []

  const handleFiles = useCallback(
    (files: File[]) => {
      const accepted: Attachment[] = []
      for (const f of files) {
        if (f.size > MAX_SIZE) {
          toast.error(`${f.name} exceeds 10 MB`)
          continue
        }
        accepted.push({
          id: crypto.randomUUID(),
          name: f.name,
          size: f.size,
          type: f.type || "application/octet-stream",
          status: "ready",
        })
      }
      if (accepted.length) addAttachments(sessionId, accepted)
    },
    [sessionId, addAttachments],
  )

  return (
    <div className="flex flex-col gap-2">
      <AttachmentDropZone onFiles={handleFiles} className="rounded-xl">
        {children}
      </AttachmentDropZone>
      {sessionAttachments.length > 0 && (
        <Attachments
          attachments={sessionAttachments}
          onRemove={(id) => removeAttachment(sessionId, id)}
          className="px-2"
        />
      )}
    </div>
  )
}

export function AttachmentButton({ sessionId }: { sessionId: string }) {
  const { addAttachments } = useComposerStore()
  return (
    <AttachmentTrigger
      onSelect={(files) => {
        const accepted: Attachment[] = files
          .filter((f) => f.size <= MAX_SIZE || (toast.error(`${f.name} exceeds 10 MB`), false))
          .map((f) => ({
            id: crypto.randomUUID(),
            name: f.name,
            size: f.size,
            type: f.type || "application/octet-stream",
            status: "ready",
          }))
        if (accepted.length) addAttachments(sessionId, accepted)
      }}
    />
  )
}
