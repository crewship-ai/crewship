import { openProtectedDocumentDrafts as openDocumentDrafts } from "../protected-document-drafts"
import { sheetRanges, SHEET_MERGE_RANGE_LIMIT, type SheetRange } from "./sheet-realtime"
import { GRIDCRAFT_DRAFT_FILE, GRIDCRAFT_MAX_FILE_BYTES } from "./gridcraft-draft-file"
import { DocumentStorageError, purgeDocumentDrafts, registerDocumentDraftWriter, removeDocumentDraftRow } from "../document-queue"

export interface NativeDraft {
  applicationId: string
  enginePin: string
  baseVersion: string
  generation: string
  sessionId: string
  editorRevision: string
  bytes: Uint8Array
  /** A workbook Sheet keeps its xlsx or xlsm file itself; absent for a native document. */
  format?: NativeFileFormat
  /** The file of a save the editor refused for fidelity_loss, lacking these parts. Offered only for
   * download, never compared, copied into an editor or saved). */
  lossy?: Array<{ code: string; count: number }>
}
export type NativeFileFormat = "xlsx" | "xlsm"
export interface NativeRecovery { draft: NativeDraft; actions: readonly ["compare", "copy"] | readonly ["download"]; /** Journal row this recovery was read from; only `discardRecovered` uses it. */ source?: { id: string; snapshot: string }; overlap?: SheetRange[] }
export interface NativeDraftJournal {
  /** `final`: the capture of a retiring editor. It may repeat what is stored. */
  persist(draft: NativeDraft, final?: boolean): Promise<void>
  confirm: (draft: NativeDraft) => Promise<void>
  recovery(): Promise<NativeRecovery[]>
  archiveConflict?(draft: NativeDraft, overlap: SheetRange[]): Promise<NativeRecovery>
  /** Removes the exact stored row a recovery was read from (an earlier mount's
   * draft). `confirm` only retires this mount's own row. */
  discardRecovered(recovery: NativeRecovery): Promise<void>
  onRetire(listener: () => void): () => void
  close(): Promise<void>
  revokeRead(): Promise<void>
  /** Erases only this mount's own persisted row, also after close (#4348).
   * Unlike `revokeRead`, it never touches another mount's rows, so a closed
   * handle may use it on a read revocation. */
  eraseOwnRow?(): Promise<void>
}
// Raw native payload matches the server limit; base64 and journal metadata use a larger encoded budget.
export const MAX_NATIVE_BYTES = 32 * 1024 * 1024
/** A workbook Sheet's file, compressed as the editor wrote it: the server's limit. */
export const MAX_WORKBOOK_BYTES = GRIDCRAFT_MAX_FILE_BYTES
/**
 * How an application keeps a draft as a file: its formats, their limit and the signature the bytes
 * start with. An application without one keeps only its native document.
 */
export interface DraftFileAdapter { formats: readonly string[]; maxBytes: number; signature(bytes: Uint8Array): boolean }
const DRAFT_FILES: Record<string, DraftFileAdapter> = { gridcraft: GRIDCRAFT_DRAFT_FILE }
export const draftFileAdapter = (applicationId: string): DraftFileAdapter | undefined => Object.prototype.hasOwnProperty.call(DRAFT_FILES, applicationId) ? DRAFT_FILES[applicationId] : undefined
// A journal row: a 64 MiB workbook as base64 (about 85.4 MiB) with its metadata. The protected
// store seals it within the draft store's 128 MiB (MAX_DRAFT_STORE_BYTES).
const MAX_ENCODED_BYTES = 96 * 1024 * 1024
export const validWorkbookFormat = (format: unknown): format is NativeFileFormat => format === "xlsx" || format === "xlsm"
const fail = () => new DocumentStorageError("Private native draft storage is unavailable; keep a recovery copy.")
const identifier = (s: unknown): s is string => typeof s === "string" && s.length > 0 && s.length <= 256 && !s.includes("\0")
export function validNativeVersion(value: unknown): value is string {
  return typeof value === "string" && /^(0|[1-9][0-9]{0,18})$/.test(value) && BigInt(value) <= 9223372036854775807n
}
export function validNativeDraft(d: NativeDraft): boolean {
  return identifier(d.applicationId) && identifier(d.enginePin) && identifier(d.sessionId)
    && validNativeVersion(d.baseVersion)
    && validNativeVersion(d.generation) && d.generation !== "0"
    && identifier(d.editorRevision)
    && d.bytes instanceof Uint8Array
    && (d.format === undefined ? d.bytes.byteLength <= MAX_NATIVE_BYTES : validDraftFile(d))
    && (d.lossy === undefined || validLossy(d))
}
// A file draft as its application's adapter describes it. The empty probe of a session passes.
function validDraftFile(d: NativeDraft): boolean {
  const file = draftFileAdapter(d.applicationId)
  return file !== undefined && file.formats.includes(d.format as string) && d.bytes.byteLength <= file.maxBytes && (d.bytes.byteLength === 0 || file.signature(d.bytes))
}
const MAX_LOSSY_PARTS = 32
function validLossy(d: NativeDraft): boolean {
  const parts = d.lossy
  return d.format !== undefined && d.bytes.byteLength > 0 && Array.isArray(parts) && parts.length >= 1 && parts.length <= MAX_LOSSY_PARTS
    && parts.every(p => p !== null && typeof p === "object" && Object.keys(p).length === 2 && typeof p.code === "string" && /^[a-z][a-z0-9_]{0,63}$/.test(p.code) && Number.isSafeInteger(p.count) && p.count > 0)
}
function encode(draft: NativeDraft, overlap?: SheetRange[]): string {
  if (!validNativeDraft(draft)) throw fail()
  let binary = ""
  for (let i = 0; i < draft.bytes.length; i += 32768) binary += String.fromCharCode(...draft.bytes.subarray(i, i + 32768))
  return JSON.stringify({ kind: "native-whole-document-v1", applicationId: draft.applicationId, enginePin: draft.enginePin, baseVersion: draft.baseVersion, generation: draft.generation, sessionId: draft.sessionId, editorRevision: draft.editorRevision, ...(draft.format ? { format: draft.format } : {}), ...(draft.lossy ? { lossy: draft.lossy.map(p => ({ code: p.code, count: p.count })) } : {}), ...(overlap ? { overlap } : {}), nativeBase64: btoa(binary) })
}
function decode(snapshot: string): Pick<NativeRecovery, "draft" | "overlap"> | null {
  try {
    if (snapshot.length > MAX_ENCODED_BYTES) return null
    const value = JSON.parse(snapshot)
    if (value?.kind !== "native-whole-document-v1" || typeof value.nativeBase64 !== "string") return null
    const binary = atob(value.nativeBase64)
    const draft: NativeDraft = { applicationId: value.applicationId, enginePin: value.enginePin, sessionId: value.sessionId, baseVersion: value.baseVersion, generation: value.generation, editorRevision: value.editorRevision, ...(value.format !== undefined ? { format: value.format } : {}), ...(value.lossy !== undefined ? { lossy: value.lossy } : {}), bytes: Uint8Array.from(binary, c => c.charCodeAt(0)) }
    const overlap = value.overlap === undefined ? undefined : sheetRanges(value.overlap, SHEET_MERGE_RANGE_LIMIT)
    if (!validNativeDraft(draft) || (value.overlap !== undefined && (!overlap?.length || draft.applicationId !== "gridcraft" || !draft.format))) return null
    return { draft, ...(overlap ? { overlap } : {}) }
  } catch { return null }
}

/** Existing private IDB journal, with a separate row per mounted tab. Recovery
 * is data for compare/copy only; it never creates a write or adopts a lease. */
export async function openNativeDraftJournal(scope: { human: string; workspace: string; page: string; applicationId: string }, options?: { signal?: AbortSignal }): Promise<NativeDraftJournal> {
  scope = { ...scope }
  if (![scope.human, scope.workspace, scope.page, scope.applicationId].every(identifier)) throw fail()
  const id = `native:${crypto.randomUUID()}`
  let active = true, closed = false, closing = false, revoking = false
  let current: string | undefined
  const archives = new Set<string>()
  let store: Awaited<ReturnType<typeof openDocumentDrafts>> | undefined
  let opening: Promise<Awaited<ReturnType<typeof openDocumentDrafts>>> | undefined
  let tail = Promise.resolve()
  let retired: Promise<void> | undefined, purged: Promise<void> | undefined
  let unregister = () => {}
  const listeners = new Set<() => void>()
  const closeStore = () => { if (!closed && store) { closed = true; store.close() } }
  const retire = () => {
    if (retired) return retired
    active = false
    unregister()
    retired = Promise.resolve(opening).catch(() => {}).then(() => tail)
    for (const listener of listeners) { try { listener() } catch { /* Observer failures cannot block privacy retirement. */ } }
    return retired
  }
  const close = () => {
    if (revoking) return purged!
    closing = true
    return retire().then(closeStore)
  }
  unregister = registerDocumentDraftWriter(scope.human, close, { storage: true, hasDraft: () => current !== undefined || archives.size > 0 })
  try {
    if (!active) throw fail()
    opening = openDocumentDrafts(scope.human, scope.workspace, scope.page, { maxDraftBytes: MAX_ENCODED_BYTES, requireExclusive: true, signal: options?.signal })
    store = await opening
    if (!active || !store.exclusive) { closeStore(); throw fail() }
  } catch (error) { active = false; unregister(); closeStore(); throw error }
  const journal = store
  function enqueue<T>(fn: () => Promise<T>): Promise<T> {
    if (!active) return Promise.reject(fail())
    const work = tail.then(async () => { if (!active) throw fail(); return fn() })
    tail = work.then(() => {}, () => {})
    return work.then(value => { if (!active) throw fail(); return value })
  }
  return {
    persist(draft, final) {
      if (!active || draft.applicationId !== scope.applicationId) return Promise.reject(fail())
      const snapshot = encode(draft)
      return enqueue(async () => {
        await journal.put({ id, schema: 1, epoch: "native-v1", update: "", attempted: true, snapshot }, { final })
        current = snapshot
      })
    },
    confirm: (draft) => {
      if (!active) return Promise.reject(fail())
      const snapshot = encode(draft)
      return enqueue(async () => { if (current === snapshot) { await journal.remove(id); current = undefined } })
    },
    archiveConflict(draft, ranges) {
      const overlap = sheetRanges(ranges, SHEET_MERGE_RANGE_LIMIT)
      if (!active || draft.applicationId !== scope.applicationId || draft.applicationId !== "gridcraft" || !draft.format || !overlap?.length) return Promise.reject(fail())
      const archiveId = `native:conflict:${crypto.randomUUID()}`
      const snapshot = encode(draft, overlap)
      return enqueue(async () => {
        await journal.put({ id: archiveId, schema: 1, epoch: "native-v1", update: "", attempted: true, snapshot })
        archives.add(archiveId)
        return { draft, overlap, actions: ["compare", "copy"], source: { id: archiveId, snapshot } }
      })
    },
    discardRecovered(recovery) {
      const source = recovery.source
      if (!source || source.id === id) return Promise.resolve()
      return enqueue(async () => {
        if (!journal.exclusive) return
        const row = (await journal.load()).find(candidate => candidate.id === source.id)
        if (row && row.snapshot === source.snapshot) { await journal.remove(row.id); archives.delete(row.id) }
      })
    },
    recovery() {
      return enqueue(async () => {
        if (!journal.exclusive) return []
        const recovered: NativeRecovery[] = []
        for (const row of await journal.load()) {
          if (row.id === id || row.schema !== 1 || row.epoch !== "native-v1") continue
          const decoded = decode(row.snapshot)
          if (decoded?.draft.applicationId === scope.applicationId) recovered.push({ ...decoded, actions: decoded.draft.lossy ? ["download"] : ["compare", "copy"], source: { id: row.id, snapshot: row.snapshot } })
        }
        return recovered
      })
    },
    onRetire(listener) {
      if (!active) listener()
      else listeners.add(listener)
      return () => { listeners.delete(listener) }
    },
    close,
    eraseOwnRow() {
      // Writes already queued finish first; a retired handle accepts no more.
      return retire().then(async () => {
        await Promise.all([id, ...archives].map(rowId => removeDocumentDraftRow(scope.human, scope.workspace, scope.page, { schema: 1, epoch: "native-v1", id: rowId })))
        current = undefined; archives.clear()
      }).finally(closeStore)
    },
    revokeRead() {
      if (purged) return purged
      // An old closed mount has no live admission to erase a fresh mount.
      if (closing || !active) return Promise.reject(fail())
      revoking = true
      let complete!: () => void, reject!: (cause: unknown) => void
      purged = new Promise<void>((resolve, fail) => { complete = resolve; reject = fail })
      // Install the promise before observers run, so a reentrant close joins
      // this purge and cannot release the exclusive Page lock prematurely.
      void retire().then(() => purgeDocumentDrafts(scope.human, scope.workspace, scope.page)).then(
        () => { closeStore(); complete() },
        error => { closeStore(); reject(error) },
      )
      return purged
    },
  }
}
