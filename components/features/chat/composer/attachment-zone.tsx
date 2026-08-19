"use client"

import { createContext, useCallback, useContext, useRef } from "react"
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
import { emitChatEvent, mimeKind, uploadFailureReason } from "@/lib/telemetry"

/** Where the bytes came from. Four controls, one upload path. */
export type AttachmentSource = "picker" | "drop" | "paste" | "camera"

type UploadFailureReason =
  | "http_error"
  | "network"
  | "too_large"
  | "unsupported_type"
  | "rate_limited"
  | "unknown"

/**
 * A refused upload, carrying the CLASS of refusal alongside the sentence the
 * user is shown.
 *
 * The two are deliberately different things and only one of them is recorded:
 * `message` is the server's own words and can name a path or echo a driver
 * error, which is why the composer already keeps it out of the DOM. `reason`
 * is a closed set, and it is what telemetry gets.
 */
class UploadFailure extends Error {
  constructor(
    message: string,
    readonly reason: UploadFailureReason,
    readonly status?: number,
  ) {
    super(message)
    this.name = "UploadFailure"
  }
}

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
 * "Make sure this conversation exists, and tell me whether it does."
 *
 * A chat opened on an agent with no history is a DRAFT: the page mints the id
 * locally and nothing is written server-side until the conversation actually
 * starts (PRD Step 3 — arriving is not sending). The upload endpoint resolves
 * the chat row before it will take a byte (`SELECT agent_id FROM chats WHERE
 * id = ?`, internal/api/proxy_attachments.go) and answers 404 Chat not found
 * when there is none — so the first conversation with an agent accepted text
 * before it accepted a file, and "photograph the receipt, attach it, send" died
 * on the attach.
 *
 * Attaching a file IS an intent to converse, so it creates the row the same way
 * the first message does: ChatPanel's own `ensureSession`, which is already
 * handed to the composer for the send path, reaches the upload through here.
 * That keeps ONE creation path — it is idempotent per session (an in-flight map
 * collapses racing callers, and the POST is an `INSERT OR IGNORE` upsert), so
 * two files dropped at once still produce exactly one row.
 *
 * A CONTEXT rather than a prop because the zones are not all mounted by the
 * composer's own JSX: an ask form's `file` / `photo` field mounts another one
 * inside the sheet, over the same per-session list, and that upload needs the
 * row for exactly the same reason. Both live under the composer's provider.
 *
 * `null` (no provider) means "nobody can vouch for this session" and the upload
 * proceeds as it always did — that is the honest default for any future zone
 * mounted on a conversation that already exists.
 */
export type EnsureChatSession = () => Promise<boolean>

const EnsureChatSessionContext = createContext<EnsureChatSession | null>(null)

export function EnsureChatSessionProvider({
  ensureSession,
  children,
}: {
  ensureSession: EnsureChatSession
  children: React.ReactNode
}) {
  return (
    <EnsureChatSessionContext.Provider value={ensureSession}>
      {children}
    </EnsureChatSessionContext.Provider>
  )
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
    throw new UploadFailure(
      typeof body.error === "string" ? body.error : "upload failed",
      uploadFailureReason(res.status),
      res.status,
    )
  }
  return res.json()
}

function errorMessage(err: unknown): string {
  const raw = err instanceof Error ? err.message : String(err)
  return raw.trim() || "the upload failed"
}

/**
 * The toast for one refused file.
 *
 * Two rules, both learned from the dev2 report where two failed uploads
 * produced one visible message:
 *
 *   · The id is the ATTACHMENT's. Sonner replaces a toast that reuses an id,
 *     so a shared or absent id is how two files collapse into one statement —
 *     and per-attachment means a second failed retry updates that file's toast
 *     instead of stacking a third.
 *   · It says what to do. "invoice.pdf: permission denied" tells a user what
 *     broke and nothing about what is now true (the file is NOT attached) or
 *     what they can do about it (retry it, or send without it).
 *
 * The toast is still only the announcement. The durable statement is the chip,
 * which stays on screen saying "Upload failed" long after this has expired.
 */
function toastUploadFailure(sessionId: string, att: ComposerAttachment, reason: string) {
  toast.error(`${att.name} was not attached`, {
    id: `attachment-upload-failed:${sessionId}:${att.id}`,
    description: `${reason}. The file is not on the agent — press Retry on the chip, or remove it and send without it.`,
  })
}

/**
 * The one upload path for chat attachments.
 *
 * Drop, paperclip and camera all land here. This used to be two verbatim
 * copies of the same ~50 lines (one in AttachmentZone, one inlined in
 * AttachmentButton's onSelect); adding a third entry point for the camera is
 * exactly the moment to stop copying it, because the copies are what let the
 * abort registry, the size guard and the "user deleted the chip mid-upload"
 * check drift apart per entry point. Retry is a fourth entry point, and it
 * runs the same `runUpload` below rather than a fifth copy.
 *
 * Returns both halves: `upload` for new files, `retry` for a chip whose upload
 * was refused.
 */
export function useAttachmentUpload(agentId: string, sessionId: string) {
  const addAttachments = useComposerStore((s) => s.addAttachments)
  const updateAttachment = useComposerStore((s) => s.updateAttachment)
  const { workspaceId } = useWorkspace()
  const ensureChatSession = useContext(EnsureChatSessionContext)

  /** One file, start to finish. The chip already exists in the store and is
   *  patched IN PLACE — never removed and re-added, which would move it to the
   *  end of the list and reorder both the chips and the paths the message
   *  names. */
  const runUpload = useCallback(
    async (att: ComposerAttachment, file: File, wsId: string, source?: AttachmentSource) => {
      const key = abortKey(sessionId, att.id)
      const ac = new AbortController()
      abortRegistry.set(key, ac)
      const startedAt = Date.now()
      // User removal is authoritative — if the chip was deleted while the
      // upload was in flight, neither outcome may put it back. `updateAttachment`
      // is a no-op for an id that is gone, so this holds for both branches.
      try {
        // The row before the bytes. On a draft session this is the POST that
        // creates the conversation; on one that already exists it is a cached
        // `true` and costs nothing. Failing here throws into the same handler
        // a refused upload uses, so a conversation that could not be started
        // produces an error chip and a named toast rather than a chip that
        // reads as an attached file — and the endpoint is never called with a
        // chat id it would 404 on.
        if (ensureChatSession && !(await ensureChatSession())) {
          throw new UploadFailure("the conversation could not be started", "unknown")
        }
        const { path, agent_path } = await uploadOne(agentId, sessionId, wsId, file, ac.signal)
        // `path` (relative to the agent's working directory) is what the
        // outgoing message names — without it the file lands in the
        // container and nothing ever tells the agent it is there.
        updateAttachment(sessionId, att.id, {
          status: "ready",
          url: agent_path,
          path,
          error: undefined,
          // The bytes are no longer needed: nothing left to retry, and a
          // finished attachment must not pin a 25 MB File in memory.
          file: undefined,
        })
        emitChatEvent("attachment_uploaded", {
          session_id: sessionId,
          mime_kind: mimeKind(file.type),
          size_bytes: file.size,
          source,
          duration_ms: Date.now() - startedAt,
        })
      } catch (err) {
        if (isAbortError(err)) return
        const reason = errorMessage(err)
        // The classification, never `reason` — that string is the server's and
        // it is the thing this component works to keep off the screen.
        emitChatEvent("attachment_upload_failed", {
          session_id: sessionId,
          mime_kind: mimeKind(file.type),
          size_bytes: file.size,
          source,
          reason: err instanceof UploadFailure ? err.reason : "network",
          status: err instanceof UploadFailure ? err.status : undefined,
        })
        // No path and no url: a failed upload has no file behind it, so there
        // is nothing for `sendableAttachments` to name even by accident. The
        // File is KEPT — it is what Retry re-sends.
        updateAttachment(sessionId, att.id, {
          status: "error",
          path: undefined,
          url: undefined,
          error: reason,
          file,
        })
        // Only announce a failure for a chip the user can still see. A file
        // they removed mid-upload failing afterwards is not news.
        const stillThere = (useComposerStore.getState().attachments[sessionId] ?? []).some(
          (a) => a.id === att.id,
        )
        if (stillThere) toastUploadFailure(sessionId, att, reason)
      } finally {
        abortRegistry.delete(key)
      }
    },
    [agentId, sessionId, updateAttachment, ensureChatSession],
  )

  const upload = useCallback(
    async (files: File[], source?: AttachmentSource) => {
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
          // A refusal that never reached the network is still a failed
          // attachment to the person holding the phone, and it is the one the
          // server-side logs can never show.
          emitChatEvent("attachment_upload_failed", {
            session_id: sessionId,
            mime_kind: mimeKind(f.type),
            size_bytes: f.size,
            source,
            reason: "too_large",
          })
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
            file: f,
          },
        })
      }
      if (queued.length === 0) return
      addAttachments(sessionId, queued.map(({ att }) => att))
      for (const { att, file } of queued) {
        await runUpload(att, file, workspaceId, source)
      }
    },
    [sessionId, workspaceId, addAttachments, runUpload],
  )

  /** Re-send a chip whose upload was refused, keeping its id and its place in
   *  the list. Same guards, same endpoint, same failure handling — a retry
   *  that fails is a failure again, not a silent no-op. */
  const retry = useCallback(
    async (id: string) => {
      if (!workspaceId) {
        toast.error("Workspace not loaded yet — try again in a moment")
        return
      }
      const att = (useComposerStore.getState().attachments[sessionId] ?? []).find(
        (a) => a.id === id,
      )
      // Already running — the Retry control is only rendered on an error chip,
      // but two calls for one chip would mean two POSTs of the same file and
      // two abort controllers under one key.
      if (!att || att.status === "uploading") return
      // No File means the page was reloaded out from under the chip. Say so
      // rather than spinning: the user has to pick the file again.
      if (!att.file) {
        toast.error("Attach the file again — the browser no longer has it to retry.")
        return
      }
      updateAttachment(sessionId, id, { status: "uploading", error: undefined })
      await runUpload(att, att.file, workspaceId)
    },
    [sessionId, workspaceId, updateAttachment, runUpload],
  )

  return { upload, retry }
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

  const { upload, retry } = useAttachmentUpload(agentId, sessionId)

  const handleFiles = useCallback((files: File[]) => void upload(files, "drop"), [upload])
  const handleRetry = useCallback((id: string) => void retry(id), [retry])

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
          onRetry={handleRetry}
          className="px-2"
        />
      )}
    </div>
  )
}

export function AttachmentButton({ agentId, sessionId }: { agentId: string; sessionId: string }) {
  const { upload } = useAttachmentUpload(agentId, sessionId)
  return (
    <AttachmentTrigger
      // No `accept` filter — chat attachments can be any file type
      // the agent might want to inspect (logs, screenshots, configs,
      // CSVs, archives). Server enforces size; type is informational.
      onSelect={(files: File[]) => upload(files, "picker")}
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
  const { upload } = useAttachmentUpload(agentId, sessionId)
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
          if (files.length) void upload(files, "camera")
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
