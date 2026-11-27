import { apiFetch } from "@/lib/api-fetch"

/** The server converts a workbook of at most this many bytes. */
export const XLSX_IMPORT_LIMIT = 64 * 1024 * 1024

/** Plain words for why the server did not import, by its code. */
const REFUSAL: Record<string, string> = {
  not_xlsx: "This file is not a readable .xlsx workbook. An encrypted or damaged file cannot be imported.",
  too_large: "This workbook is larger than the import limits (a sheet may hold at most 1,000,000 XML elements, roughly 330,000 cells with formulas; a workbook at most 16 million cells and 256 sheets).",
  truncated: "The editor would read only part of this workbook, so nothing was imported.",
  failed: "The conversion failed.",
}

export class XlsxImportError extends Error {
  /** retryAfterSeconds: the server's Retry-After of a "busy" refusal. */
  constructor(readonly code: string, message: string, readonly retryAfterSeconds?: number) { super(message); this.name = "XlsxImportError" }
}

export type XlsxImportState = "uploading" | "converting"

export interface XlsxImportResult {
  /** The finished conversion; the server initializes the page from it (see initializeFromImport). */
  importId: string
  /** What the file held that did not come across (macros, links to other workbooks...). */
  notes: string[]
  cells?: number
}

const POLL_MS = 1000
/** The server keeps a conversion for 15 minutes; stop waiting a little earlier. */
const GIVE_UP_MS = 14 * 60 * 1000

const sleep = (ms: number, signal?: AbortSignal) => new Promise<void>((resolve, reject) => {
  if (signal?.aborted) return reject(new DOMException("Aborted", "AbortError"))
  const timer = setTimeout(() => { signal?.removeEventListener("abort", onAbort); resolve() }, ms)
  const onAbort = () => { clearTimeout(timer); reject(new DOMException("Aborted", "AbortError")) }
  signal?.addEventListener("abort", onAbort, { once: true })
})

async function readJson(response: Response): Promise<Record<string, unknown> | null> {
  const value: unknown = await response.json().catch(() => null)
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null
}

function refused(status: number, body: Record<string, unknown> | null, retryAfter?: string | null): XlsxImportError {
  if (status === 413) return new XlsxImportError("too_large", REFUSAL.too_large)
  if (status === 409) return new XlsxImportError("not_empty", "This page already holds a Sheet; an import never replaces one.")
  if (status === 403) return new XlsxImportError("forbidden", "Only the owner of an empty page can import a workbook into it.")
  if (status === 503) {
    // The server says which: "sheet_off" (trying again will not help) or "busy"; anything else is busy.
    if (body?.code === "sheet_off") return new XlsxImportError("sheet_off", "Sheet is not available on this server.")
    const seconds = Number.parseInt(retryAfter ?? "", 10)
    return new XlsxImportError("busy", "The server is converting other workbooks. Try again in a moment.", Number.isFinite(seconds) && seconds > 0 ? seconds : undefined)
  }
  const message = typeof body?.error === "string" ? body.error : "The import could not be started."
  return new XlsxImportError("failed", message)
}

/**
 * Converts an .xlsx workbook on the server. The page must be an empty one the
 * person owns; nothing is written to it here: the caller then has the server
 * initialize the page from the finished conversion (initializeFromImport),
 * so the converted document never travels to the browser and back.
 * `onState` says what is happening; `signal` cancels the wait (the server
 * discards the conversion by itself after 15 minutes).
 */
export async function importXlsx(args: {
  workspaceId: string
  /** The page's stable id, or its slug. */
  page: string
  file: Blob
  signal?: AbortSignal
  onState?: (state: XlsxImportState) => void
}): Promise<XlsxImportResult> {
  const { workspaceId, page, file, signal, onState } = args
  if (file.size === 0 || file.size > XLSX_IMPORT_LIMIT) {
    throw new XlsxImportError("too_large", "The workbook must be between 1 byte and 64 MiB.")
  }
  const base = `/api/v1/pages/${encodeURIComponent(page)}/craft/import`
  const query = `?${new URLSearchParams({ workspace_id: workspaceId })}`
  onState?.("uploading")
  const start = await apiFetch(`${base}${query}`, { method: "POST", headers: { "Content-Type": "application/octet-stream" }, body: file, signal, redirect: "error", skipRefresh: true })
  const started = await readJson(start)
  if (start.status !== 202 || typeof started?.id !== "string" || !started.id) throw refused(start.status, started, start.headers.get("Retry-After"))
  const id = started.id
  onState?.("converting")
  const deadline = Date.now() + GIVE_UP_MS
  for (;;) {
    const poll = await apiFetch(`${base}/${encodeURIComponent(id)}${query}`, { signal, cache: "no-store", redirect: "error" })
    const body = await readJson(poll)
    if (!poll.ok || !body) throw new XlsxImportError("failed", "The import status could not be read.")
    if (body.state === "ready") {
      const archive = body.archive && typeof body.archive === "object" ? body.archive as Record<string, unknown> : null
      const notes = Array.isArray(archive?.notes) ? (archive!.notes as unknown[]).filter((n): n is string => typeof n === "string") : []
      return { importId: id, notes, cells: typeof body.cells === "number" ? body.cells : undefined }
    }
    if (body.state === "failed") {
      const code = typeof body.code === "string" && body.code in REFUSAL ? body.code : "failed"
      throw new XlsxImportError(code, REFUSAL[code])
    }
    if (body.state !== "converting") throw new XlsxImportError("failed", "The import status is not understood.")
    if (Date.now() > deadline) throw new XlsxImportError("failed", "The conversion is taking too long; it was stopped. Try a smaller workbook.")
    await sleep(POLL_MS, signal)
  }
}

/**
 * Has the server initialize the page from a finished conversion. The answer is
 * the ordinary initialize answer (session token, generation, version): the
 * caller checks it the way it checks any initialization. 409 means the
 * conversion is not finished or failed; 404 an unknown or foreign one.
 */
export function initializeFromImport(args: { workspaceId: string; page: string; importId: string; signal?: AbortSignal }): Promise<Response> {
  const { workspaceId, page, importId, signal } = args
  return apiFetch(`/api/v1/pages/${encodeURIComponent(page)}/craft/import/${encodeURIComponent(importId)}/initialize?${new URLSearchParams({ workspace_id: workspaceId })}`,
    { method: "POST", redirect: "error", skipRefresh: true, signal })
}

/** Base64 of bytes without building one huge argument list. */
export function bytesToBase64(bytes: Uint8Array): string {
  let binary = ""
  for (let i = 0; i < bytes.length; i += 32768) binary += String.fromCharCode(...bytes.subarray(i, i + 32768))
  return btoa(binary)
}
