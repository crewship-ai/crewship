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
  agentId: string
  sessionId: string
  children: React.ReactNode
}

/**
 * Upload a single file to the session's attachment store. The endpoint
 * lives at POST /api/v1/agents/{agentId}/chats/{chatId}/attachments —
 * file lands at /output/<slug>/attachments/<chatId>/<filename> on the
 * agent side. Returns the server-assigned path so the chip can later
 * be referenced in the prompt.
 */
async function uploadOne(
  agentId: string,
  sessionId: string,
  file: File,
): Promise<{ path: string; agent_path: string }> {
  const form = new FormData()
  form.append("file", file)
  const res = await fetch(`/api/v1/agents/${agentId}/chats/${sessionId}/attachments`, {
    method: "POST",
    credentials: "include",
    body: form,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(typeof body.error === "string" ? body.error : "upload failed")
  }
  return res.json()
}

export function AttachmentZone({ agentId, sessionId, children }: AttachmentZoneProps) {
  const { attachments, addAttachments, removeAttachment } = useComposerStore()
  const sessionAttachments = attachments[sessionId] ?? []

  const handleFiles = useCallback(
    async (files: File[]) => {
      // Optimistically add chips with status: uploading; flip to ready
      // (with the server-side path) on success or to error on fail.
      const queued: Attachment[] = []
      for (const f of files) {
        if (f.size > MAX_SIZE) {
          toast.error(`${f.name} exceeds 10 MB`)
          continue
        }
        queued.push({
          id: crypto.randomUUID(),
          name: f.name,
          size: f.size,
          type: f.type || "application/octet-stream",
          status: "uploading",
        })
      }
      if (queued.length === 0) return
      addAttachments(sessionId, queued)
      for (let i = 0; i < queued.length; i++) {
        const att = queued[i]
        const file = files[i]
        try {
          const { agent_path } = await uploadOne(agentId, sessionId, file)
          // Update chip to ready + remember server path on URL field.
          // Re-using the existing addAttachments to overwrite by id.
          removeAttachment(sessionId, att.id)
          addAttachments(sessionId, [{ ...att, status: "ready", url: agent_path }])
        } catch (err) {
          removeAttachment(sessionId, att.id)
          addAttachments(sessionId, [{ ...att, status: "error" }])
          toast.error(`${att.name}: ${err instanceof Error ? err.message : String(err)}`)
        }
      }
    },
    [agentId, sessionId, addAttachments, removeAttachment],
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

export function AttachmentButton({ agentId, sessionId }: { agentId: string; sessionId: string }) {
  const { addAttachments, removeAttachment } = useComposerStore()
  return (
    <AttachmentTrigger
      onSelect={async (files) => {
        const queued: Attachment[] = files
          .filter((f) => {
            if (f.size > MAX_SIZE) {
              toast.error(`${f.name} exceeds 10 MB`)
              return false
            }
            return true
          })
          .map((f) => ({
            id: crypto.randomUUID(),
            name: f.name,
            size: f.size,
            type: f.type || "application/octet-stream",
            status: "uploading" as const,
          }))
        if (queued.length === 0) return
        addAttachments(sessionId, queued)
        for (let i = 0; i < queued.length; i++) {
          const att = queued[i]
          const file = files[i]
          try {
            const { agent_path } = await uploadOne(agentId, sessionId, file)
            removeAttachment(sessionId, att.id)
            addAttachments(sessionId, [{ ...att, status: "ready", url: agent_path }])
          } catch (err) {
            removeAttachment(sessionId, att.id)
            addAttachments(sessionId, [{ ...att, status: "error" }])
            toast.error(`${att.name}: ${err instanceof Error ? err.message : String(err)}`)
          }
        }
      }}
    />
  )
}
