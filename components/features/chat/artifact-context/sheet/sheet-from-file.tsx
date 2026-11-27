"use client"

import { useEffect, useRef, useState } from "react"
import { FileSpreadsheet, X } from "lucide-react"
import { Button } from "@/components/ui/button"
import { XLSX_IMPORT_LIMIT, XlsxImportError } from "@/lib/craft-xlsx-import"
import { uploadWorkbook, WORKBOOK_REFUSAL } from "./workbook-upload"
import { PROVISIONAL_ATTACHMENT_LIMITS } from "@/lib/chat-attachment-queue"
import { useWordDocumentCreation } from "@/components/features/pages/use-word-document-creation"
import { artifactsItemHref } from "../artifacts-href"
import { clearUnfinishedImport, noteUnfinishedImport } from "./unfinished-imports"
import { isWorkbookName, workbookBaseName } from "./workbook-file"

const mb = (bytes: number) => `${Math.round(bytes / (1024 * 1024))} MB`
/** Unique without listing Pages, so the address says nothing about Pages this person cannot see. */
const freeSlug = (name: string) => {
  const base = name.toLowerCase().normalize("NFKD").replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "").slice(0, 48).replace(/-$/, "") || "sheet"
  return `${base}-${crypto.randomUUID().slice(0, 8)}`
}

/** What the person can do after a failed import, for each reason the server names. */
const NEXT: Record<string, string> = {
  too_large: "Remove sheets or rows you do not need in Excel, save a copy and try again with it.",
  not_xlsx: "Save it again in Excel as .xlsx, without a password, and try again.",
  truncated: "Nothing was imported, so nothing in the Sheet is incomplete. Try a copy with fewer sheets.",
  busy: "Try again in a moment.",
  sheet_off: "Ask whoever runs Crewship here to turn Sheet on. The workbook stays attached to your message.",
  unpacked_too_large: "Remove sheets or rows you do not need in Excel, save a copy and try again with it.",
  too_many_parts: "Remove sheets or rows you do not need in Excel, save a copy and try again with it.",
  not_workbook: "Open it in Excel, save it as .xlsx or .xlsm, and try again.",
  encrypted: "Save a copy in Excel without a password and try again.",
  unsupported: "Save it as .xlsx in Excel (or .xlsm, to keep its macros) and try again.",
  quota: "Ask whoever runs Crewship here for more space, or delete Sheets you no longer need.",
  chat_not_found: "Open the workbook as a Sheet from your own chat, or from Artifacts.",
}
/** The import client speaks of pages and XML elements; in the chat the same refusals say Sheet and cells. */
const OWN_WORDS: Record<string, string> = {
  not_empty: "This Sheet already holds a workbook, and an import never replaces one.",
  forbidden: "Only the person who owns this empty Sheet can import a workbook into it.",
  sheet_off: "Sheet is not available on this server.",
  // The workbook file itself is checked before anything is stored.
  ...WORKBOOK_REFUSAL,
}

/** The server's sentence when there is one; never an internal code such as page_create_failed. */
function readable(error: unknown, fallback: string): string {
  if (error instanceof XlsxImportError) return OWN_WORDS[error.code] ?? error.message
  const message = error instanceof Error ? error.message.trim() : ""
  return /\s/.test(message) && /[.!?]$/.test(message) ? message : fallback
}

function nextStep(error: unknown): string {
  const code = error instanceof XlsxImportError ? error.code : ""
  return NEXT[code] ?? "Try again. If it fails again, the workbook may hold something the Sheet cannot read yet."
}

type Phase = { at: "offer" } | { at: "working"; state?: "uploading" } | { at: "failed"; message: string; next: string; final?: boolean } | { at: "done"; pageId: string }

/**
 * One workbook added to a message, offered as a Sheet. The attachment itself is
 * not touched: whatever happens here, the message and the file stay. The server
 * decides who may create the Sheet; this only says beforehand what it knows.
 */
export function SheetFromFileRow({ workspaceId, userId, chatId, ensureChat, onWorking, onFailed, onSendPlain, file, attached, sideBySide, sheetName, onOpen, onDismiss }: {
  workspaceId: string
  /** True once the Sheet could not be made, false when it is tried again or made: the message waits for the person's choice. */
  onFailed?: (failed: boolean) => void
  /** The person chose to send the attached workbook as a plain file after all. */
  onSendPlain?: () => void
  /** The new Sheet's name, when not the file's own (an output file: beside a Sheet of the same name, never over it). */
  sheetName?: string
  /** The chat the workbook was added to: the Sheet belongs to it from version 1. */
  chatId?: string
  /** A chat not on the server yet is started first and named here, so the Sheet is tied to it; undefined when it could not be. */
  ensureChat?: () => Promise<string | undefined>
  /** True while the Sheet is being made, false once that stopped: Send waits for it. */
  onWorking?: (working: boolean) => void
  /** Whose unfinished upload is noted when the row goes away mid-upload. */
  userId?: string
  file: File
  attached: boolean
  /** The Sheet opens beside the chat; otherwise (a phone) a link opens it. */
  sideBySide: boolean
  /** chat: the chat the Sheet was tied to, if any. */
  onOpen: (pageId: string, chat?: string) => void
  onDismiss: () => void
}) {
  const creation = useWordDocumentCreation(workspaceId)
  const [phase, setPhase] = useState<Phase>({ at: "offer" })
  const abort = useRef<AbortController | null>(null)
  const mounted = useRef(true)
  // The keyboard follows the row while it works, and goes to Try again after a failure, unless the person went elsewhere meanwhile.
  const rowRef = useRef<HTMLDivElement>(null), progressRef = useRef<HTMLDivElement>(null), retryRef = useRef<HTMLButtonElement>(null)
  const keyboardHere = () => { const active = document.activeElement; return !active || active === document.body || Boolean(rowRef.current?.contains(active)) }
  // Left mid-upload: the empty Sheet stays, and is noted so it can be offered for deletion. Never deleted here.
  const leftBehind = useRef<() => void>(() => {})
  leftBehind.current = () => { if (phase.at === "working" && creation.emptyPage && userId) noteUnfinishedImport({ userId, workspaceId }, creation.emptyPage.id) }
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; leftBehind.current(); abort.current?.abort() } }, [])
  const available = creation.availability.gridcraftNative === true
  const name = sheetName ?? workbookBaseName(file.name)
  // Whether the attachment itself arrived is its own chip's to say.
  const kept = " Your original file did not change, and neither did your message or its files."
  const tooLarge = attached ? "" : ` Too large to attach (limit ${mb(PROVISIONAL_ATTACHMENT_LIMITS.maxBytes)}).`

  const start = async () => {
    if (phase.at === "working") return
    const controller = abort.current = new AbortController()
    setPhase({ at: "working" })
    onFailed?.(false)
    onWorking?.(true)
    try {
      // The chat comes first: a Sheet tied to it is carried by the next message, and stays with the agent.
      const chat = chatId ?? (ensureChat ? await ensureChat() : undefined)
      if (!mounted.current) return
      if (ensureChat && !chat) { setPhase({ at: "failed", message: "The chat could not be started, so no Sheet was created.", next: "Try again." }); onFailed?.(true); return }
      // The workbook itself becomes version 1, as it is: nothing is converted.
      const made = await creation.create.mutateAsync({ name, slug: freeSlug(name), format: "sheet", prepareImport: async pageId => async () => {
        if (mounted.current) setPhase({ at: "working", state: "uploading" })
        return await uploadWorkbook({ workspaceId, pageId, file, signal: controller.signal, ...(chat ? { chatId: chat } : {}) })
      } })
      if (!mounted.current) return
      if (!made.nativePageId) throw new Error("The Sheet was created but could not be opened. Open it from Artifacts.")
      if (userId) clearUnfinishedImport({ userId, workspaceId }, made.nativePageId)
      setPhase({ at: "done", pageId: made.nativePageId })
      onOpen(made.nativePageId, chat)
    } catch (error) {
      if (!mounted.current) return
      const cancelled = error instanceof Error && error.name === "AbortError"
      setPhase({ at: "failed", message: cancelled ? "The upload was cancelled." : readable(error, "The Sheet could not be created."), next: cancelled ? "Open it as a Sheet again whenever you want." : nextStep(error), final: error instanceof XlsxImportError && error.code === "sheet_off" })
      onFailed?.(true)
    } finally { abort.current = null; onWorking?.(false) }
  }
  const discard = async () => {
    try { await creation.discardEmptyPage(); if (mounted.current) setPhase({ at: "offer" }) }
    catch (error) { if (mounted.current) setPhase({ at: "failed", message: readable(error, "The empty Sheet could not be deleted."), next: "Try again, or delete it later in Artifacts." }) }
  }

  // On a workbook server an .xlsx or .xlsm opens as a Sheet at once; the server still decides.
  const autoOpen = creation.availability.gridcraftWorkbook === true && available && creation.allowed && isWorkbookName(file.name) && file.size <= XLSX_IMPORT_LIMIT
  const autoStarted = useRef(false)
  useEffect(() => {
    if (!autoOpen || autoStarted.current || creation.availability.isPending) return
    autoStarted.current = true
    void start()
  }, [autoOpen, creation.availability.isPending]) // eslint-disable-line react-hooks/exhaustive-deps -- once per file

  useEffect(() => {
    if (!keyboardHere()) return
    if (phase.at === "working") progressRef.current?.focus()
    else if (phase.at === "failed") retryRef.current?.focus()
  }, [phase.at])

  if (creation.availability.isPending) return null
  // Open beside the chat: the Sheet's own chip above the composer is the one row for this file.
  if (phase.at === "done" && sideBySide) return null
  const reason = !available ? "Sheet is not available on this server." : !creation.allowed ? "You may not create Sheets in this workspace." : null
  return <div ref={rowRef} role="group" aria-label={`${file.name} as a Sheet`} data-testid="sheet-from-file" data-phase={phase.at}
    className="mx-auto flex w-full max-w-3xl flex-wrap items-center gap-x-3 gap-y-1 px-4 py-1 text-xs">
    <span className="flex min-w-0 flex-1 basis-40 items-center gap-2">
      <FileSpreadsheet className="size-4 shrink-0 text-sheet" aria-hidden />
      <span className="truncate">{file.name}</span>
    </span>
    {!reason && ((phase.at === "offer" && !autoOpen) || (phase.at === "failed" && !phase.final)) && <Button ref={retryRef} type="button" size="xs" className="coarse:min-h-[44px]" aria-label={`Open ${file.name} as a Sheet`} onClick={() => void start()}>{phase.at === "failed" ? "Try again" : "Open as a Sheet"}</Button>}
    {phase.at === "working" && phase.state && <Button type="button" size="xs" variant="outline" className="coarse:min-h-[44px]" onClick={() => abort.current?.abort()}>Cancel</Button>}
    {phase.at === "done" && !sideBySide && <Button asChild size="xs" className="coarse:min-h-[44px]"><a href={artifactsItemHref(workspaceId, phase.pageId)}>Open</a></Button>}
    {phase.at !== "working" && <Button type="button" variant="ghost" size="icon" className="size-7 coarse:size-[44px]" aria-label={`Dismiss ${file.name}`} onClick={onDismiss}><X className="size-4" aria-hidden /></Button>}
    <div ref={progressRef} tabIndex={-1} aria-live="polite" className="basis-full text-muted-foreground outline-none empty:hidden">
      {phase.at === "working" && <p>{phase.state === "uploading" ? "Uploading the workbook…" : "Creating the Sheet…"}</p>}
      {phase.at === "done" && <p>{sideBySide ? "Opened beside the chat." : "The Sheet is ready."}</p>}
    </div>
    {phase.at === "offer" && <p className="basis-full text-muted-foreground">{reason ? `${reason}${tooLarge}`
      : attached ? `Edit it together with the agent. Files up to ${mb(PROVISIONAL_ATTACHMENT_LIMITS.maxBytes)} can be attached, workbooks up to ${mb(XLSX_IMPORT_LIMIT)} opened as a Sheet.`
      : `Too large to attach (limit ${mb(PROVISIONAL_ATTACHMENT_LIMITS.maxBytes)}). It can still be opened as a Sheet (limit ${mb(XLSX_IMPORT_LIMIT)}).`}</p>}
    {phase.at === "failed" && <div role="alert" className="flex basis-full flex-wrap items-center gap-2 text-destructive">
      <p>{phase.message} {phase.next}{kept}{attached && onSendPlain ? " The message waits until you try again or send the workbook as a plain file." : ""}</p>
      {attached && onSendPlain && <Button type="button" size="xs" variant="outline" className="coarse:min-h-[44px]" onClick={onSendPlain}>Send as a plain file</Button>}
      {creation.emptyPage && <Button type="button" size="xs" variant="outline" className="coarse:min-h-[44px]" onClick={() => void discard()}>Delete the empty Sheet</Button>}
    </div>}
  </div>
}
