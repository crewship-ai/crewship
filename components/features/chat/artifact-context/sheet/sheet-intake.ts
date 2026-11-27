"use client"

import { createContext, useContext } from "react"
import { XLSX_IMPORT_LIMIT } from "@/lib/craft-xlsx-import"
import { PROVISIONAL_ATTACHMENT_LIMITS } from "@/lib/chat-attachment-queue"
import { isWorkbookName } from "./workbook-file"
import { abortIfPending } from "@/lib/chat-attachment-jobs"
import { useComposerStore } from "@/stores/composer-store"

export interface SheetOffer { file: File; /** False for a workbook too large to attach: it can only be a Sheet. */ attached: boolean }

/** Which of the added files can become a Sheet, and which are attached as usual. */
export function splitForSheets(files: File[], workbookLimit = XLSX_IMPORT_LIMIT): { offers: SheetOffer[]; attach: File[] } {
  const offers: SheetOffer[] = [], attach: File[] = []
  for (const file of files) {
    const workbook = isWorkbookName(file.name) && file.size > 0 && file.size <= workbookLimit
    const attached = file.size <= PROVISIONAL_ATTACHMENT_LIMITS.maxBytes
    if (workbook) offers.push({ file, attached })
    // Too large for both stays here too, so the ordinary refusal says so.
    if (!workbook || attached) attach.push(file)
  }
  return { offers, attach }
}

/** Set by a chat that can open Sheets; the upload path hands it the added files and attaches what comes back. */
const SheetIntakeContext = createContext<((files: File[]) => File[]) | null>(null)
export const SheetIntakeProvider = SheetIntakeContext.Provider
export const useSheetIntake = () => useContext(SheetIntakeContext)

/**
 * A workbook that opened as a Sheet is the Sheet now: its attachment chip goes, so the composer shows
 * it once and the message does not also hand the agent the file. The first attachment of that name and size.
 */
export function withdrawSheetAttachment(storeKey: string, file: File): boolean {
  const store = useComposerStore.getState()
  const match = (store.attachments[storeKey] ?? []).find(a => a.name === file.name && a.size === file.size)
  if (!match) return false
  abortIfPending(storeKey, match.id)
  store.removeAttachment(storeKey, match.id)
  return true
}

/**
 * The Sheet takes the attachment's place only when it was created in this chat: then the chat references it (the
 * next message carries it by default, the agent reads it with craft_sheet_read) and agent_files keeps it with the
 * agent for later chats. A Sheet created before the chat existed has neither, so the file stays attached.
 */
export function sheetReplacesAttachment(offer: { attached: boolean }, chatId: string | undefined): boolean {
  return offer.attached && Boolean(chatId)
}
