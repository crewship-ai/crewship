"use client"

import { useCallback, useRef } from "react"
import { Camera } from "lucide-react"
import { toast } from "sonner"

import {
  Attachments,
  AttachmentDropZone,
  AttachmentTrigger,
} from "@/components/ai-elements/attachments"
import { Button } from "@/components/ui/button"
import { useComposerStore, type ComposerAttachment } from "@/stores/composer-store"
import { useWorkspace } from "@/hooks/use-workspace"
import { apiFetch } from "@/lib/api-fetch"

// 25 MB cap — best practice for chat attachments. Bigger than the
// previous 10 MB which was too small for screenshots / log dumps but
// well under the multipart parsing slowdown threshold.
const MAX_SIZE = 25 * 1024 * 1024

interface AttachmentZoneProps {
  agentId: string
  sessionId: string
  children: React.ReactNode
  /**
   * Draw the chip list for this session's attachments. Default true.
   *
   * There is ONE attachment list per session (the store), and more than one
   * zone can be mounted over it at a time: the composer always has one, and a
   * form's `file` / `photo` field mounts another while its sheet is open. Two
   * zones drawing the same list put the same file on screen twice, which reads
   * as "did I attach that twice?".
   *
   * So the list is never duplicated to fix that — the renderer is switched
   * off. The drop target stays live either way: dropping a file on the
   * composer while a sheet is open must still attach it, it just appears in
   * the sheet, next to the field that asked for it.
   */
  showChips?: boolean
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
  // other agent-scoped endpoint on the canvas. encodeURIComponent on
  // agentId / sessionId stops a malformed identifier from drifting into
  // a different endpoint (e.g. an id with a slash in it) — CodeQL flags
  // unescaped path interpolation as js/client-side-request-forgery.
  const url = `/api/v1/agents/${encodeURIComponent(agentId)}/chats/${encodeURIComponent(sessionId)}/attachments?workspace_id=${encodeURIComponent(workspaceId)}`
  const res = await apiFetch(url, {
    method: "POST",
    body: form,
    signal,
  })
  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: `HTTP ${res.status}` }))
    throw new Error(typeof body.error === "string" ? body.error : "upload failed")
  }
  return res.json()
}

/**
 * The one upload path for chat attachments.
 *
 * Drop, paperclip and camera all land here. This used to be two verbatim
 * copies of the same ~50 lines (one in AttachmentZone, one inlined in
 * AttachmentButton's onSelect); adding a third entry point for the camera is
 * exactly the moment to stop copying it, because the copies are what let the
 * abort registry, the size guard and the "user deleted the chip mid-upload"
 * check drift apart per entry point.
 */
export function useAttachmentUpload(agentId: string, sessionId: string) {
  const addAttachments = useComposerStore((s) => s.addAttachments)
  const removeAttachment = useComposerStore((s) => s.removeAttachment)
  const { workspaceId } = useWorkspace()

  return useCallback(
    async (files: File[]) => {
      if (!workspaceId) {
        toast.error("Workspace not loaded yet — try again in a moment")
        return
      }
      // Optimistically add chips with status: uploading; flip to ready
      // (with the server-side path) on success or to error on fail.
      // Pair each chip with its source File so a skipped (oversized)
      // file can't shift indices and mismatch chip ↔ file.
      const queued: Array<{ att: ComposerAttachment; file: File }> = []
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
          const { path, agent_path } = await uploadOne(agentId, sessionId, workspaceId, file, ac.signal)
          // User removal is authoritative — if the chip was deleted
          // while the upload was in flight, the success/error path
          // must not put it back. Re-read the latest store snapshot
          // and only promote the chip if it still exists.
          const stillThere = (useComposerStore.getState().attachments[sessionId] ?? [])
            .some((a) => a.id === att.id)
          if (!stillThere) continue
          removeAttachment(sessionId, att.id)
          // `path` (relative to the agent's working directory) is what the
          // outgoing message names — without it the file lands in the
          // container and nothing ever tells the agent it is there.
          addAttachments(sessionId, [{ ...att, status: "ready", url: agent_path, path }])
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
}

export function AttachmentZone({
  agentId,
  sessionId,
  children,
  showChips = true,
}: AttachmentZoneProps) {
  // Subscribe only to THIS session's attachment list — the whole-store
  // subscription re-rendered every mounted zone on any session's draft or
  // attachment write.
  const sessionAttachmentsRaw = useComposerStore((s) => s.attachments[sessionId])
  const removeAttachment = useComposerStore((s) => s.removeAttachment)
  const sessionAttachments = sessionAttachmentsRaw ?? []

  const handleFiles = useAttachmentUpload(agentId, sessionId)

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
      {showChips && sessionAttachments.length > 0 && (
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
  const handleFiles = useAttachmentUpload(agentId, sessionId)
  return (
    <AttachmentTrigger
      // No `accept` filter — chat attachments can be any file type
      // the agent might want to inspect (logs, screenshots, configs,
      // CSVs, archives). Server enforces size; type is informational.
      onSelect={handleFiles}
    />
  )
}

/**
 * Camera control. Mobile composer only.
 *
 * `capture="environment"` next to `accept="image/*"` is what makes a phone
 * open the rear camera instead of the document picker — without it the
 * composer's only route to a photo is camera app → gallery → file browser,
 * which is three screens for the most obvious thing anyone does from a phone.
 * The attribute is inert on a desktop browser (it falls back to a plain image
 * picker), but the control is still rendered only in the mobile composer:
 * a camera button on a desktop chat is noise, and the mobile composer is the
 * one rendered below the 768px breakpoint.
 *
 * Deliberately not built on AttachmentTrigger: that component
 * (components/ai-elements/attachments.tsx) forwards `accept` and `multiple`
 * but not `capture`, and the whole point of this control is `capture`. The
 * upload itself is NOT duplicated — it is the same useAttachmentUpload the
 * paperclip and the drop zone use, so a photo lands in the same per-session
 * attachment list, with the same size guard and the same abort-on-remove.
 */
export function CameraButton({ agentId, sessionId }: { agentId: string; sessionId: string }) {
  const handleFiles = useAttachmentUpload(agentId, sessionId)
  const inputRef = useRef<HTMLInputElement | null>(null)
  return (
    <>
      <input
        ref={inputRef}
        type="file"
        accept="image/*"
        capture="environment"
        multiple
        className="hidden"
        data-testid="camera-input"
        onChange={(e) => {
          const files = Array.from(e.target.files ?? [])
          if (files.length) void handleFiles(files)
          // Reset so photographing the same thing twice still fires change.
          e.target.value = ""
        }}
      />
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        className="h-7 w-7"
        onClick={() => inputRef.current?.click()}
      >
        <Camera className="h-3.5 w-3.5" />
        <span className="sr-only">Take a photo</span>
      </Button>
    </>
  )
}
