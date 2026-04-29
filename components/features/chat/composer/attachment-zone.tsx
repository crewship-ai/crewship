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

// Module-level abort registry, keyed by `${sessionId}::${attachmentId}`.
// Both AttachmentZone (drop) and AttachmentButton (file picker) write
// here when they kick off an upload; the user-removal handler in
// AttachmentZone reads here to cancel in-flight requests so deleted
// files can't sneak through to the server side.
const abortRegistry = new Map<string, AbortController>()
const abortKey = (sessionId: string, id: string) => `${sessionId}::${id}`

function abortIfPending(sessionId: string, id: string) {
  const ac = abortRegistry.get(abortKey(sessionId, id))
  if (ac) {
    ac.abort()
    abortRegistry.delete(abortKey(sessionId, id))
  }
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError"
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
  signal?: AbortSignal,
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
    signal,
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
        const ac = new AbortController()
        abortRegistry.set(abortKey(sessionId, att.id), ac)
        try {
          const { agent_path } = await uploadOne(agentId, sessionId, workspaceId, file, ac.signal)
          // User removal is authoritative — if the chip was deleted
          // while the upload was in flight, the success/error path
          // must not put it back. Re-read the latest store snapshot
          // and only promote the chip if it still exists.
          const stillThere = (useComposerStore.getState().attachments[sessionId] ?? [])
            .some((a) => a.id === att.id)
          if (!stillThere) continue
          removeAttachment(sessionId, att.id)
          addAttachments(sessionId, [{ ...att, status: "ready", url: agent_path }])
        } catch (err) {
          if (isAbortError(err)) continue
          const stillThere = (useComposerStore.getState().attachments[sessionId] ?? [])
            .some((a) => a.id === att.id)
          if (!stillThere) continue
          removeAttachment(sessionId, att.id)
          addAttachments(sessionId, [{ ...att, status: "error" }])
          toast.error(`${att.name}: ${err instanceof Error ? err.message : String(err)}`)
        } finally {
          abortRegistry.delete(abortKey(sessionId, att.id))
        }
      }
    },
    [agentId, sessionId, workspaceId, addAttachments, removeAttachment],
  )

  // Wrap user removal so an in-flight upload is aborted before the
  // chip disappears from the store. Without this, a deleted file can
  // still finish uploading server-side.
  const handleRemove = useCallback(
    (id: string) => {
      abortIfPending(sessionId, id)
      removeAttachment(sessionId, id)
    },
    [sessionId, removeAttachment],
  )

  return (
    <div className="flex flex-col gap-2">
      <AttachmentDropZone onFiles={handleFiles} className="rounded-xl">
        {children}
      </AttachmentDropZone>
      {sessionAttachments.length > 0 && (
        <Attachments
          attachments={sessionAttachments}
          onRemove={handleRemove}
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
          const ac = new AbortController()
          abortRegistry.set(abortKey(sessionId, att.id), ac)
          try {
            const { agent_path } = await uploadOne(agentId, sessionId, workspaceId, file, ac.signal)
            const stillThere = (useComposerStore.getState().attachments[sessionId] ?? [])
              .some((a) => a.id === att.id)
            if (!stillThere) continue
            removeAttachment(sessionId, att.id)
            addAttachments(sessionId, [{ ...att, status: "ready", url: agent_path }])
          } catch (err) {
            if (isAbortError(err)) continue
            const stillThere = (useComposerStore.getState().attachments[sessionId] ?? [])
              .some((a) => a.id === att.id)
            if (!stillThere) continue
            removeAttachment(sessionId, att.id)
            addAttachments(sessionId, [{ ...att, status: "error" }])
            toast.error(`${att.name}: ${err instanceof Error ? err.message : String(err)}`)
          } finally {
            abortRegistry.delete(abortKey(sessionId, att.id))
          }
        }
      }}
    />
  )
}
