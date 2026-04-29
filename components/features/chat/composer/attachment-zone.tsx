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
import { useWorkspace } from "@/hooks/use-workspace"

// 25 MB cap — best practice for chat attachments. Bigger than the
// previous 10 MB which was too small for screenshots / log dumps but
// well under the multipart parsing slowdown threshold.
const MAX_SIZE = 25 * 1024 * 1024

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
  workspaceId: string,
  file: File,
): Promise<{ path: string; agent_path: string }> {
  const form = new FormData()
  form.append("file", file)
  // workspace_id is required by the wsCtx middleware — without it the
  // request 400s before reaching the handler. Same pattern as every
  // other agent-scoped endpoint on the canvas.
  const url = `/api/v1/agents/${agentId}/chats/${sessionId}/attachments?workspace_id=${encodeURIComponent(workspaceId)}`
  const res = await fetch(url, {
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
  const { workspaceId } = useWorkspace()
  const sessionAttachments = attachments[sessionId] ?? []

  const handleFiles = useCallback(
    async (files: File[]) => {
      if (!workspaceId) {
        toast.error("Workspace not loaded yet — try again in a moment")
        return
      }
      // Optimistically add chips with status: uploading; flip to ready
      // (with the server-side path) on success or to error on fail.
      // Pair each chip with its source File so a skipped (oversized)
      // file can't shift indices and mismatch chip ↔ file.
      const queued: Array<{ att: Attachment; file: File }> = []
      for (const f of files) {
        if (f.size > MAX_SIZE) {
          toast.error(`${f.name} exceeds ${Math.round(MAX_SIZE / 1024 / 1024)} MB`)
          continue
        }
        queued.push({
          file: f,
          att: {
            id: crypto.randomUUID(),
            name: f.name,
            size: f.size,
            type: f.type || "application/octet-stream",
            status: "uploading",
          },
        })
      }
      if (queued.length === 0) return
      addAttachments(sessionId, queued.map(({ att }) => att))
      for (const { att, file } of queued) {
        try {
          const { agent_path } = await uploadOne(agentId, sessionId, workspaceId, file)
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
    [agentId, sessionId, workspaceId, addAttachments, removeAttachment],
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
  const { workspaceId } = useWorkspace()
  return (
    <AttachmentTrigger
      // No `accept` filter — chat attachments can be any file type
      // the agent might want to inspect (logs, screenshots, configs,
      // CSVs, archives). Server enforces size; type is informational.
      onSelect={async (files) => {
        if (!workspaceId) {
          toast.error("Workspace not loaded yet — try again in a moment")
          return
        }
        const queued: Array<{ att: Attachment; file: File }> = []
        for (const f of files) {
          if (f.size > MAX_SIZE) {
            toast.error(`${f.name} exceeds ${Math.round(MAX_SIZE / 1024 / 1024)} MB`)
            continue
          }
          queued.push({
            file: f,
            att: {
              id: crypto.randomUUID(),
              name: f.name,
              size: f.size,
              type: f.type || "application/octet-stream",
              status: "uploading",
            },
          })
        }
        if (queued.length === 0) return
        addAttachments(sessionId, queued.map(({ att }) => att))
        for (const { att, file } of queued) {
          try {
            const { agent_path } = await uploadOne(agentId, sessionId, workspaceId, file)
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
