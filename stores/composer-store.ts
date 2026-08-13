"use client"

import { create } from "zustand"
import { persist, createJSONStorage } from "zustand/middleware"

import type { Attachment } from "@/components/ai-elements/attachments"

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
}

interface ComposerState {
  modelId: string | null
  drafts: Record<string, string>
  attachments: Record<string, ComposerAttachment[]>
  setModel: (id: string | null) => void
  setDraft: (sessionId: string, text: string) => void
  clearDraft: (sessionId: string) => void
  addAttachments: (sessionId: string, items: ComposerAttachment[]) => void
  removeAttachment: (sessionId: string, id: string) => void
  clearAttachments: (sessionId: string) => void
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
      removeAttachment: (sessionId, id) =>
        set((s) => ({
          attachments: {
            ...s.attachments,
            [sessionId]: (s.attachments[sessionId] ?? []).filter((a) => a.id !== id),
          },
        })),
      clearAttachments: (sessionId) =>
        set((s) => {
          const next = { ...s.attachments }
          delete next[sessionId]
          return { attachments: next }
        }),
    }),
    {
      name: "crewship-composer",
      storage: createJSONStorage(() => localStorage),
      partialize: (s) => ({ modelId: s.modelId, drafts: s.drafts }),
    },
  ),
)
