"use client"

import { useCallback, useMemo, useRef } from "react"
import { Camera, Paperclip } from "lucide-react"

import { Attachments, AttachmentDropZone } from "@/components/ai-elements/attachments"
import { Button } from "@/components/ui/button"
import {
  useComposerStore,
  attachmentsForOwner,
  type AttachmentOwner,
  type ComposerAttachment,
} from "@/stores/composer-store"

import { useAttachmentUpload } from "../composer/attachment-zone"
import type { AskFormField } from "./types"

/**
 * The upload control inside one `file` / `photo` field, and the chips that are
 * that field's answer.
 *
 * The rule this exists for: **an upload belongs to the field it was dropped
 * into.** Before, every upload field of a form mounted the composer's own
 * `AttachmentZone` over the one per-session list, so all of them saw all of
 * it — one file satisfied every required upload field, the message named it
 * under each of them, and the sheet coped by drawing chips for only the first
 * field, which left the second one looking like it had never been answered.
 *
 * Two things are deliberately NOT duplicated here:
 *
 *   · **The upload.** `useAttachmentUpload` is the composer's one path — same
 *     endpoint, same 25 MB guard, same abort registry, same per-file failure
 *     toast, same Retry. A field is a fourth entry point to it, not a second
 *     implementation.
 *   · **The chip.** `Attachments` is the same renderer the composer uses, so a
 *     failed upload says "Upload failed — not attached" in the same words
 *     wherever it is drawn (PRD §5.6).
 *
 * What is local is only WHICH attachments this control is about, and that is
 * the whole fix.
 *
 * The controls are hand-rolled rather than reusing `AttachmentButton` /
 * `CameraButton` for exactly one reason: those mint their uploads through
 * `useAttachmentUpload(agentId, sessionId)` with no owner, and there is no seam
 * to pass one. When that hook takes an optional owner (see the note on
 * `claimAttachmentsForFiles`), these two buttons become those two components
 * again and `claim` below goes away.
 */

const NO_ATTACHMENTS: ComposerAttachment[] = []

export interface AskFieldAttachmentsProps {
  agentId: string
  sessionId: string
  /** The open form's id — half of the ownership key, so a file attached to
   *  `document` in one form is not read as the answer to a `document` field in
   *  another. */
  formId: string
  field: AskFormField
}

export function AskFieldAttachments({
  agentId,
  sessionId,
  formId,
  field,
}: AskFieldAttachmentsProps) {
  const owner = useMemo<AttachmentOwner>(
    () => ({ formId, field: field.name }),
    [formId, field.name],
  )
  const all = useComposerStore((s) => s.attachments[sessionId]) ?? NO_ATTACHMENTS
  const mine = useMemo(() => attachmentsForOwner(all, owner), [all, owner])
  const claim = useComposerStore((s) => s.claimAttachmentsForFiles)

  const { upload } = useAttachmentUpload(agentId, sessionId)

  /**
   * Start the upload, then claim the chips it just minted for this field.
   *
   * `upload` adds every chip to the store before it awaits its first request,
   * so by the time this returns the records exist and are matched back to the
   * `File` objects they were made from. A file that somehow was not claimed
   * stays the message's own, which is the safe failure: it is named by the
   * attachment block and satisfies no required field, rather than silently
   * answering a question it was never dropped into.
   */
  const handleFiles = useCallback(
    (files: File[]) => {
      const done = upload(files)
      claim(sessionId, owner, files)
      void done.catch(() => {})
    },
    [upload, claim, sessionId, owner],
  )

  const isPhoto = field.type === "photo"
  const camera = (
    <FilePicker
      testId={`ask-camera-${field.name}`}
      label="Take a photo"
      icon={<Camera className="h-3.5 w-3.5" />}
      onFiles={handleFiles}
      // `capture="environment"` is what makes a phone open the rear camera
      // instead of the document picker — the one control a receipt-shaped
      // question actually needs on the device people photograph receipts with
      // (PRD §5.4). Inert on a desktop, where it degrades to an image picker.
      accept="image/*"
      capture="environment"
    />
  )
  const paperclip = (
    <FilePicker
      testId={`ask-upload-${field.name}`}
      label="Attach files"
      icon={<Paperclip className="h-3.5 w-3.5" />}
      onFiles={handleFiles}
    />
  )

  return (
    <div className="flex flex-col gap-2">
      <AttachmentDropZone onFiles={handleFiles} className="rounded-xl">
        <div className="flex items-center gap-2 rounded-lg border border-dashed px-3 py-2.5">
          {/* A `photo` field leads with the camera and a `file` field with the
              paperclip. Both offer both: a receipt is photographed on a phone
              and dragged in from a folder on a desktop, so the type is a hint
              about which is likelier, not a restriction. */}
          {isPhoto ? camera : paperclip}
          {isPhoto ? paperclip : camera}
          <span className="text-xs text-muted-foreground">
            {isPhoto ? "Take a photo, or drop a file here" : "Drop a file here, or pick one"}
          </span>
        </div>
      </AttachmentDropZone>
      <AskAttachmentChips agentId={agentId} sessionId={sessionId} attachments={mine} />
    </div>
  )
}

/**
 * The chips for attachments that answer no question — the composer's own.
 *
 * They are drawn here because the composer hides its whole chip list while a
 * sheet with an upload control is open (one list, one visible renderer), and a
 * file that is on screen nowhere is the failure mode §5.6 is about: it will be
 * named in the outgoing message, so the user has to be able to see it and take
 * it out. It says whose it is, because "attached to the message" and "this is
 * the contract you asked for" are different claims.
 */
export function AskMessageAttachments({
  agentId,
  sessionId,
  attachments,
}: {
  agentId: string
  sessionId: string
  attachments: ComposerAttachment[]
}) {
  if (attachments.length === 0) return null
  return (
    <div className="space-y-1" data-testid="ask-message-attachments">
      <p className="text-xs text-muted-foreground">Attached to this message</p>
      <AskAttachmentChips agentId={agentId} sessionId={sessionId} attachments={attachments} />
    </div>
  )
}

function AskAttachmentChips({
  agentId,
  sessionId,
  attachments,
}: {
  agentId: string
  sessionId: string
  attachments: ComposerAttachment[]
}) {
  const removeAttachment = useComposerStore((s) => s.removeAttachment)
  const { retry } = useAttachmentUpload(agentId, sessionId)

  // Removal is per attachment id, so it can only ever affect the one chip the
  // user pressed — the other field's answer is a different id in the same list.
  const handleRemove = useCallback(
    (id: string) => removeAttachment(sessionId, id),
    [removeAttachment, sessionId],
  )
  const handleRetry = useCallback((id: string) => void retry(id), [retry])

  if (attachments.length === 0) return null
  return (
    <Attachments
      attachments={attachments}
      onRemove={handleRemove}
      onRetry={handleRetry}
      className="px-1"
    />
  )
}

/** A hidden `<input type="file">` and the button that opens it. */
function FilePicker({
  testId,
  label,
  icon,
  onFiles,
  accept,
  capture,
}: {
  testId: string
  label: string
  icon: React.ReactNode
  onFiles: (files: File[]) => void
  accept?: string
  capture?: "environment" | "user"
}) {
  const inputRef = useRef<HTMLInputElement | null>(null)
  return (
    <>
      <input
        ref={inputRef}
        type="file"
        multiple
        accept={accept}
        capture={capture}
        className="hidden"
        data-testid={testId}
        onChange={(e) => {
          const files = Array.from(e.target.files ?? [])
          if (files.length) onFiles(files)
          // Reset, so picking the same file twice still fires a change.
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
        {icon}
        <span className="sr-only">{label}</span>
      </Button>
    </>
  )
}
