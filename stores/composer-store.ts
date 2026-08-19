"use client"

import { create } from "zustand"
import { persist, createJSONStorage } from "zustand/middleware"

import type { Attachment } from "@/components/ai-elements/attachments"
import type { AttachmentOwner } from "@/lib/attachment-message"

export type { AttachmentOwner }

/**
 * A composer attachment: the presentational `Attachment` (what the chip
 * renders) plus the server-assigned path (what the MESSAGE has to name).
 *
 * The upload response carries two paths — `agent_path`, the absolute
 * `/output/<slug>/attachments/<chatId>/<file>`, kept in `url`; and `path`,
 * the `attachments/<chatId>/<file>` form relative to the agent's working
 * directory, kept here. The message names the relative one
 * (lib/attachment-message.ts). Both are kept because they answer different
 * questions and deriving either from the other means parsing a filename.
 */
export type ComposerAttachment = Attachment & {
  /** `attachments/<chatId>/<filename>` — set once the upload succeeds. */
  path?: string
  /**
   * The source File, kept only while the upload has not succeeded.
   *
   * It is what makes Retry possible on a failed chip: the bytes are still in
   * the page, so a refused upload does not cost the user a second trip through
   * the file picker (three screens on a phone, and the camera roll on the way
   * back). Dropped the moment the upload lands, so a finished attachment is
   * not holding 25 MB alive, and never persisted — `partialize` below writes
   * only `modelId` and `drafts`, so nothing here is ever serialised.
   */
  file?: File
  /**
   * Which ask-form field this upload answers, if any.
   *
   * The list is keyed by SESSION and that is deliberately unchanged: one
   * session, one place a file can be, so the abort registry, the retry path
   * and the "did the user delete this chip mid-upload" check keep working on
   * one list. What was missing is which question a file answers — every
   * `file` / `photo` field of an open form read the whole list, so one upload
   * satisfied every required upload field and the message named it under each
   * of them.
   *
   * Ownership therefore rides on the ATTACHMENT rather than becoming a second
   * keying of the store. Absent is the plain composer's case — the file
   * belongs to the message, not to a question — and every path that predates
   * ask forms keeps working by writing nothing here.
   */
  owner?: AttachmentOwner
}

export function sameOwner(a: AttachmentOwner | undefined, b: AttachmentOwner): boolean {
  return !!a && a.formId === b.formId && a.field === b.field
}

/** The uploads that answer one form field. */
export function attachmentsForOwner(
  list: ComposerAttachment[],
  owner: AttachmentOwner,
): ComposerAttachment[] {
  return list.filter((a) => sameOwner(a.owner, owner))
}

/** The uploads that answer no question: the message's own attachments, which
 *  is everything the composer's paperclip, camera and drop zone produce. */
export function messageOwnAttachments(list: ComposerAttachment[]): ComposerAttachment[] {
  return list.filter((a) => !a.owner)
}

interface ComposerState {
  modelId: string | null
  drafts: Record<string, string>
  attachments: Record<string, ComposerAttachment[]>
  setModel: (id: string | null) => void
  setDraft: (sessionId: string, text: string) => void
  clearDraft: (sessionId: string) => void
  addAttachments: (sessionId: string, items: ComposerAttachment[]) => void
  /** Patch one attachment in place — an upload finishing, failing, or being
   *  retried. In place because remove+add moves the chip to the end of the
   *  list: the message names paths in composer order, so a retried file would
   *  quietly reorder what the user sees and what the agent is told. */
  updateAttachment: (
    sessionId: string,
    id: string,
    patch: Partial<ComposerAttachment>,
  ) => void
  removeAttachment: (sessionId: string, id: string) => void
  /**
   * Assign `owner` to the attachments that were created for `files` and do not
   * belong to a field yet.
   *
   * Matched on File IDENTITY (the `file` each pending chip holds for Retry),
   * not on position or on "everything new", so a file dropped on the composer
   * at the same moment cannot be swept into a form field by accident.
   *
   * It is a separate step because the record is minted inside
   * `useAttachmentUpload` (composer/attachment-zone.tsx), which does not take
   * an owner yet — see the seam noted there. Until it does, the field's own
   * upload handler claims what it just started, synchronously, before the
   * first request is awaited. If that ordering ever changed the file would
   * stay unowned, which is the safe direction: an unowned upload satisfies no
   * required field and is named by the message block instead of vanishing.
   */
  claimAttachmentsForFiles: (
    sessionId: string,
    owner: AttachmentOwner,
    files: File[],
  ) => void
  /**
   * Drop the message's own attachments — what a send consumes.
   *
   * Field-owned uploads are deliberately kept: they are an open form's
   * answers, and an unrelated message going out from the same composer is not
   * a reason to silently discard the receipt somebody just photographed into a
   * question above it. The sheet clears its own when it closes.
   */
  clearAttachments: (sessionId: string) => void
  /** Drop every upload that answers a field of `formId` — the sheet closing,
   *  sent or abandoned. Its answers do not outlive it. */
  clearFormAttachments: (sessionId: string, formId: string) => void
}

/** Write a session's list back, dropping the key when nothing is left so an
 *  empty session is indistinguishable from one that never had attachments. */
function withList(
  attachments: Record<string, ComposerAttachment[]>,
  sessionId: string,
  list: ComposerAttachment[],
): Record<string, ComposerAttachment[]> {
  const next = { ...attachments }
  if (list.length === 0) delete next[sessionId]
  else next[sessionId] = list
  return next
}

export const useComposerStore = create<ComposerState>()(
  persist(
    (set) => ({
      modelId: null,
      drafts: {},
      attachments: {},
      setModel: (modelId) => set({ modelId }),
      setDraft: (sessionId, text) =>
        set((s) => ({ drafts: { ...s.drafts, [sessionId]: text } })),
      clearDraft: (sessionId) =>
        set((s) => {
          const next = { ...s.drafts }
          delete next[sessionId]
          return { drafts: next }
        }),
      addAttachments: (sessionId, items) =>
        set((s) => ({
          attachments: {
            ...s.attachments,
            [sessionId]: [...(s.attachments[sessionId] ?? []), ...items],
          },
        })),
      updateAttachment: (sessionId, id, patch) =>
        set((s) => {
          const list = s.attachments[sessionId]
          if (!list?.some((a) => a.id === id)) return s
          return {
            attachments: {
              ...s.attachments,
              [sessionId]: list.map((a) => (a.id === id ? { ...a, ...patch } : a)),
            },
          }
        }),
      removeAttachment: (sessionId, id) =>
        set((s) => ({
          attachments: {
            ...s.attachments,
            [sessionId]: (s.attachments[sessionId] ?? []).filter((a) => a.id !== id),
          },
        })),
      claimAttachmentsForFiles: (sessionId, owner, files) =>
        set((s) => {
          const list = s.attachments[sessionId]
          if (!list) return s
          let claimed = false
          const next = list.map((a) => {
            if (a.owner || !a.file || !files.includes(a.file)) return a
            claimed = true
            return { ...a, owner }
          })
          return claimed ? { attachments: { ...s.attachments, [sessionId]: next } } : s
        }),
      clearAttachments: (sessionId) =>
        set((s) => {
          const list = s.attachments[sessionId]
          if (!list) return s
          return { attachments: withList(s.attachments, sessionId, list.filter((a) => !!a.owner)) }
        }),
      clearFormAttachments: (sessionId, formId) =>
        set((s) => {
          const list = s.attachments[sessionId]
          if (!list) return s
          const kept = list.filter((a) => a.owner?.formId !== formId)
          if (kept.length === list.length) return s
          return { attachments: withList(s.attachments, sessionId, kept) }
        }),
    }),
    {
      name: "crewship-composer",
      storage: createJSONStorage(() => localStorage),
      partialize: (s) => ({ modelId: s.modelId, drafts: s.drafts }),
    },
  ),
)
