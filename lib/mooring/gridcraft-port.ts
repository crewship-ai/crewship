// GridCraft editor ↔ host, mooring protocol 2: the workbook
// file (.xlsx or .xlsm) is the source of truth. The host opens the stored file as it is
// and stores what the editor saves; there is no native JSON, no re-export after open and
// no snapshot proof. Every message has an exact key set; anything else closes the port.
// An editor error carries a machine code (chosen messages) and a text that may quote the
// workbook: the text never leaves this file.
import { sheetTouched, type SheetTouched } from "./gridcraft-replay"
import { verifyEditorHello, transferEditorPort, record, type CapabilityWindow, type WindowEnvelope } from "./channel"
import { engineBuildToken, savedRevisionToken, NATIVE_EXECUTE_COMMANDS, NATIVE_EXECUTE_MAX_BYTES, NATIVE_EXECUTE_MAX_COMMANDS, NATIVE_HELD_INPUT_DROPPED, type NativeEditAttempt, type NativeExecuteCommand, type NativeExecuteConflict, type NativeExecuteOptions, type NativeExecuteResult, NATIVE_EXECUTE_MAX_CONFLICTS, validExecuteLabel, type NativeHeldInputDropped, type NativeStructuredPort } from "./native-editor-port"

export const GRIDCRAFT_PROTOCOL = 2
/** The engine's own bound (upstream MAX_OPEN_BYTES); the server keeps a smaller upload limit. */
export const GRIDCRAFT_MAX_FILE_BYTES = 512 * 1024 * 1024
export const GRIDCRAFT_MAX_UNANSWERED_SAVES = 4
export const GRIDCRAFT_SAVE_FAILURES = ["conflict", "too_large", "forbidden", "failed"] as const
export type GridcraftSaveFailure = (typeof GRIDCRAFT_SAVE_FAILURES)[number]
export type GridcraftFormat = "xlsx" | "xlsm"
export interface GridcraftWarning { code: string; count: number }
export interface GridcraftOpened { document: string; revision: string; format: GridcraftFormat; calculated: boolean; warnings: GridcraftWarning[] }
export interface GridcraftSavedFile { requestId: string; document: string; revision: string; name: string; format: GridcraftFormat; bytes: Uint8Array; source: "host" | "editor"; /** The cells the person changed since the saved version, or unknown; absent when the editor does not say. */ touched?: SheetTouched }
/** The editor would not write the file: fidelity_loss names what it would drop, by part and count (no content). */
export class GridcraftSaveRefused extends Error {
  /** `bytes`: with fidelity_loss, the file as it would be written. Only for a local draft, never a save. */
  constructor(readonly code: string, readonly parts: GridcraftWarning[], readonly bytes?: Uint8Array) { super(code); this.name = "GridcraftSaveRefused" }
}
export interface GridcraftSaveRefusal { requestId: string; revision: string; code: string; parts: GridcraftWarning[]; bytes?: Uint8Array }
export interface GridcraftPortOptions {
  bundle: string
  frame: CapabilityWindow
  createChannel?(): { port1: NativeStructuredPort; port2: unknown }
  /** `calculated`: the editor has calculated every value itself since the open; absent from an older editor. */
  onChanged(change: { document: string; revision: string; dirty: boolean; calculated?: boolean }): void
  onDirty?(dirty: boolean): void
  /** A save the person started in the editor (Ctrl+S, menu): store it, then ack or saveFailed. */
  onSave(file: GridcraftSavedFile): void
  /** An editor-started save the editor then refused (fidelity_loss: it would drop parts of the workbook). */
  onSaveRefused?(refusal: GridcraftSaveRefusal): void
  /** PDF, CSV and other exports: offered for download, never stored as a version. */
  onExport?(file: { name: string; bytes: Uint8Array }): void
  onError(reason: string): void
  /** build: the engine build the editor runs; absent in a development build. */
  onReady?(version: string, build?: string): void
  onEditAttempt?(kind: NativeEditAttempt): void
  onHeldInputDropped?(reason: NativeHeldInputDropped): void
  /** Deadline of access and execute requests; open and save take their own. */
  timeoutMs?: number
}
export interface GridcraftPort {
  loaded(): void
  handleWindowMessage(event: WindowEnvelope): boolean
  open(file: { name: string; bytes: Uint8Array }, options: { calculate: boolean; autoFit: boolean }, signal: AbortSignal, timeoutMs: number): Promise<GridcraftOpened>
  requestSave(signal: AbortSignal, timeoutMs: number): Promise<GridcraftSavedFile>
  ack(requestId: string, version: string): boolean
  saveFailed(requestId: string, code: GridcraftSaveFailure): boolean
  setReadOnly(readOnly: boolean, signal: AbortSignal): Promise<boolean>
  execute(commands: NativeExecuteCommand[], options: NativeExecuteOptions, signal: AbortSignal): Promise<NativeExecuteResult>
  readonly document: string | undefined
  close(): void
}

const MAX_DEADLINE_MS = 10 * 60_000
const text = (value: unknown, limit = 256): value is string => typeof value === "string" && value.length > 0 && value.length <= limit && !value.includes("\0")
const requestID = (value: unknown): value is string => typeof value === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(value)
const code = (value: unknown): value is string => typeof value === "string" && /^[a-z][a-z0-9_]{0,63}$/.test(value)
const errorText = (value: unknown): value is string => typeof value === "string" && value.length <= 512
const u64 = (value: unknown): value is string => typeof value === "string" && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18446744073709551615n
const documentID = (value: unknown): value is string => text(value, 128) && !value.includes(":")
const format = (value: unknown): value is GridcraftFormat => value === "xlsx" || value === "xlsm"
const exact = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).length === keys.length && keys.every(key => key in value)
const failure = (value: Record<string, unknown>) => exact(value, ["type", "requestId", "code", "error"]) && code(value.code) && errorText(value.error)
// The cells the person kept: display text of each side, at most 256 characters (longer ones end in "…").
const cellText = (value: unknown): value is string => typeof value === "string" && value.length <= 257
function executeConflicts(value: unknown): NativeExecuteConflict[] | undefined {
  if (!Array.isArray(value) || value.length > NATIVE_EXECUTE_MAX_CONFLICTS) return undefined
  const out: NativeExecuteConflict[] = []
  for (const c of value) {
    if (!c || typeof c !== "object" || !exact(c as Record<string, unknown>, ["sheet", "cell", "agent", "kept"])) return undefined
    const { sheet, cell, agent, kept } = c as Record<string, unknown>
    if (!text(sheet, 255) || typeof cell !== "string" || !/^\$?[A-Z]{1,3}\$?[1-9][0-9]{0,6}$/.test(cell) || !cellText(agent) || !cellText(kept)) return undefined
    out.push({ sheet, cell, agent, kept })
  }
  return out
}
const MAX_REFUSED_PARTS = 32
/** An error reply to a save-request: any contract code, and for fidelity_loss the parts it would drop. */
function saveRefusal(value: Record<string, unknown>): { code: string; parts: GridcraftWarning[]; bytes?: Uint8Array } | null {
  if (failure(value)) return { code: value.code as string, parts: [] }
  const parts = value.parts, withFile = "bytes" in value
  // The refusal may carry the file as it would be written, for a local draft only.
  const bytes = withFile ? fileBytes(value.bytes) : undefined
  if (withFile && !bytes) return null
  if (!exact(value, ["type", "requestId", "code", "error", "parts", ...(withFile ? ["bytes"] : [])]) || value.code !== "fidelity_loss" || !errorText(value.error) || !Array.isArray(parts) || parts.length < 1 || parts.length > MAX_REFUSED_PARTS
    || !parts.every(p => record(p) && exact(p, ["code", "count"]) && code(p.code) && Number.isSafeInteger(p.count) && (p.count as number) > 0)) return null
  return { code: "fidelity_loss", parts: parts.map(p => ({ code: p.code as string, count: p.count as number })), ...(bytes ? { bytes } : {}) }
}
const fileBytes = (value: unknown): Uint8Array | null => value instanceof ArrayBuffer && value.byteLength > 0 && value.byteLength <= GRIDCRAFT_MAX_FILE_BYTES ? new Uint8Array(value) : null
const deadline = (ms: number) => Number.isFinite(ms) && ms >= 1 && ms <= MAX_DEADLINE_MS

interface Pending { kind: "open" | "save" | "access" | "execute"; readOnly?: boolean; resolve(value: unknown): void; reject(error: Error): void; finish(): void }

export function createGridcraftPort(options: GridcraftPortOptions): GridcraftPort {
  if (!options.frame || !/^[a-f0-9]{64}$/.test(options.bundle)) throw new Error("invalid_native_port")
  const timeoutMs = options.timeoutMs ?? 10_000
  if (!deadline(timeoutMs)) throw new Error("invalid_native_deadline")
  const pending = new Map<string, Pending>()
  // Saves the editor started (save-request seen, file not yet), and files waiting for ack or failure.
  const tickets = new Map<string, string>(), unanswered = new Map<string, GridcraftSavedFile>()
  let port: NativeStructuredPort | undefined, closed = false, loads = 0, ready = false, opening = false
  let bound: string | undefined, confirmedReadOnly = true, lastAcked: bigint | undefined, calculated = false
  const observe = (callback: () => void) => { try { callback() } catch { /* an observer never decides persistence */ } }
  const savesInFlight = () => [...pending.values()].filter(request => request.kind === "save").length + tickets.size + unanswered.size
  function close(reason?: string) {
    if (closed) return
    closed = true
    if (port) { port.onmessage = null; port.close() }
    for (const request of [...pending.values()]) { request.finish(); request.reject(new Error(reason ?? "native_port_closed")) }
    tickets.clear(); unanswered.clear()
    if (reason) observe(() => options.onError(reason))
  }
  function post(value: Record<string, unknown>, transfer?: Transferable[]): boolean {
    if (closed || !port) return false
    try { port.postMessage(value, transfer); return true } catch { close("native_port_send"); return false }
  }
  function rpc(kind: Pending["kind"], message: Record<string, unknown>, signal: AbortSignal, ms: number, transfer?: Transferable[], readOnly?: boolean): Promise<unknown> {
    if (closed || !port || signal.aborted || pending.size >= 8) return Promise.reject(new Error("native_port_unavailable"))
    const id = crypto.randomUUID()
    return new Promise((resolve, reject) => {
      const expire = () => {
        // A late import or access change cannot be undone: end the port rather than guess.
        if (kind === "open") { request.finish(); reject(new Error("native_request_timeout")); close("native_open_unconfirmed") }
        else if (kind === "access") close("native_access_unconfirmed")
        else { request.finish(); reject(new Error("native_request_timeout")) }
      }
      const abort = () => { if (kind === "open" || kind === "access") close(`native_${kind}_unconfirmed`); else { request.finish(); reject(new Error("native_request_aborted")) } }
      const timer = setTimeout(expire, ms)
      const request: Pending = { kind, readOnly, resolve, reject, finish() { clearTimeout(timer); signal.removeEventListener("abort", abort); pending.delete(id) } }
      pending.set(id, request); signal.addEventListener("abort", abort, { once: true })
      post({ ...message, requestId: id }, transfer)
    })
  }
  function answer(raw: Record<string, unknown>, kind: Pending["kind"]): Pending | null {
    if (!requestID(raw.requestId)) { close("native_port_protocol"); return null }
    const request = pending.get(raw.requestId)
    if (!request || request.kind !== kind) { close("native_port_protocol"); return null }
    if (failure(raw)) { request.finish(); request.reject(new Error(raw.code as string)); return null }
    return request
  }
  function receive(raw: unknown) {
    if (closed) return
    if (!record(raw) || typeof raw.type !== "string") { close("native_port_protocol"); return }
    switch (raw.type) {
      case "gridcraft:ready": {
        if (!exact(raw, ["type", "version", "accessProtocol", "readOnly", ...("build" in raw ? ["build"] : [])]) || !text(raw.version, 128) || raw.accessProtocol !== 1 || typeof raw.readOnly !== "boolean" || "build" in raw && !engineBuildToken(raw.build)) break
        if (ready || raw.readOnly !== true) { close("native_access_boot_writable"); return }
        const build = "build" in raw ? raw.build as string : undefined
        ready = true; observe(() => build === undefined ? options.onReady?.(raw.version as string) : options.onReady?.(raw.version as string, build)); return
      }
      case "gridcraft:opened": {
        const request = answer(raw, "open"); if (!request) return
        const warnings = raw.warnings
        if (!exact(raw, ["type", "requestId", "document", "revision", "format", "calculated", "warnings"]) || !documentID(raw.document) || !u64(raw.revision) || !format(raw.format) || typeof raw.calculated !== "boolean" || !Array.isArray(warnings) || warnings.length > 64
          || !warnings.every(w => record(w) && exact(w, ["code", "count"]) && code(w.code) && Number.isSafeInteger(w.count) && (w.count as number) > 0)) break
        bound = raw.document as string
        request.finish(); request.resolve({ document: bound, revision: raw.revision, format: raw.format, calculated: raw.calculated, warnings: warnings.map(w => ({ code: w.code, count: w.count })) } satisfies GridcraftOpened); return
      }
      case "gridcraft:changed": {
        const withCalculated = "calculated" in raw
        if (!exact(raw, ["type", "document", "revision", "dirty", ...(withCalculated ? ["calculated"] : [])]) || !documentID(raw.document) || !u64(raw.revision) || typeof raw.dirty !== "boolean") break
        if (withCalculated && (typeof raw.calculated !== "boolean" || (calculated && raw.calculated === false))) break // one way: false to true
        if (bound === undefined) return // the empty workbook the editor starts with
        if (raw.document !== bound) { close("native_document_changed"); return }
        if (raw.calculated === true) calculated = true
        observe(() => options.onChanged({ document: bound!, revision: raw.revision as string, dirty: raw.dirty as boolean, ...(withCalculated ? { calculated: raw.calculated as boolean } : {}) })); return
      }
      case "gridcraft:dirty": {
        if (!exact(raw, ["type", "dirty"]) || typeof raw.dirty !== "boolean") break
        if (bound !== undefined) observe(() => options.onDirty?.(raw.dirty as boolean)); return
      }
      case "gridcraft:closed": {
        if (!exact(raw, ["type"])) break
        if (bound !== undefined) close("native_document_changed"); return
      }
      case "gridcraft:failed": {
        if (!exact(raw, ["type", "code", "error"]) || !code(raw.code) || !errorText(raw.error)) break
        close("native_engine_failed"); return
      }
      case "gridcraft:access": {
        const request = answer(raw, "access"); if (!request) { if (!closed) close("native_access_unconfirmed"); return }
        if (!exact(raw, ["type", "requestId", "readOnly"]) || typeof raw.readOnly !== "boolean") break
        if (raw.readOnly !== request.readOnly) { close("native_access_mismatch"); return }
        confirmedReadOnly = raw.readOnly; request.finish(); request.resolve(raw.readOnly); return
      }
      case "gridcraft:executed": {
        const request = answer(raw, "execute"); if (!request) return
        const conflicts = "conflicts" in raw ? executeConflicts(raw.conflicts) : undefined
        if (!exact(raw, ["type", "requestId", "document", "revision", "applied", ...("savedRevision" in raw ? ["savedRevision"] : []), ...("conflicts" in raw ? ["conflicts"] : []), ...("conflictsTruncated" in raw ? ["conflictsTruncated"] : [])]) || "savedRevision" in raw && !savedRevisionToken(raw.savedRevision) || "conflicts" in raw && !conflicts || "conflictsTruncated" in raw && (raw.conflictsTruncated !== true || !conflicts) || !documentID(raw.document) || !u64(raw.revision) || !Number.isSafeInteger(raw.applied) || (raw.applied as number) < 1 || (raw.applied as number) > NATIVE_EXECUTE_MAX_COMMANDS) break
        if (raw.document !== bound) { close("native_document_changed"); return }
        request.finish(); request.resolve({ documentId: bound, revision: `${bound}:${raw.revision}`, ...("savedRevision" in raw ? { savedRevision: `${bound}:${raw.savedRevision}` } : {}), applied: raw.applied as number, ...(conflicts ? { conflicts, conflictsTruncated: raw.conflictsTruncated === true } : {}) } satisfies NativeExecuteResult); return
      }
      case "gridcraft:save-request": {
        if (!exact(raw, ["type", "requestId", "document", "revision"]) || !requestID(raw.requestId) || !documentID(raw.document) || !u64(raw.revision)) break
        if (bound === undefined) return
        if (raw.document !== bound) { close("native_document_changed"); return }
        if (tickets.has(raw.requestId) || unanswered.has(raw.requestId) || pending.has(raw.requestId)) break
        if (savesInFlight() >= GRIDCRAFT_MAX_UNANSWERED_SAVES) { close("native_save_backpressure"); return }
        tickets.set(raw.requestId, raw.revision); return
      }
      case "gridcraft:save": {
        if ("code" in raw) {
          const refusal = saveRefusal(raw); if (!refusal || !requestID(raw.requestId)) break
          const id = raw.requestId, asked = pending.get(id), ticket = tickets.get(id)
          if (asked?.kind === "save") { asked.finish(); asked.reject(new GridcraftSaveRefused(refusal.code, refusal.parts, refusal.bytes)); return }
          if (ticket === undefined) break
          tickets.delete(id)
          observe(() => options.onSaveRefused?.({ requestId: id, revision: ticket, code: refusal.code, parts: refusal.parts, ...(refusal.bytes ? { bytes: refusal.bytes } : {}) })); return
        }
        if (!exact(raw, ["type", "requestId", "document", "revision", "name", "format", "bytes", ...("touched" in raw ? ["touched"] : [])]) || !requestID(raw.requestId) || !documentID(raw.document) || !u64(raw.revision) || !text(raw.name) || !format(raw.format)) break
        const bytes = fileBytes(raw.bytes); if (!bytes) break
        const touched = "touched" in raw ? { touched: raw.touched === "unknown" ? "unknown" as const : sheetTouched(raw.touched) } : {}
        if (bound === undefined || raw.document !== bound) { close("native_document_changed"); return }
        const id = raw.requestId, asked = pending.get(id), ticket = tickets.get(id)
        if (asked?.kind === "save") {
          const file: GridcraftSavedFile = { requestId: id, document: bound, revision: raw.revision, name: raw.name, format: raw.format, bytes, source: "host", ...touched }
          unanswered.set(id, file); asked.finish(); asked.resolve(file); return
        }
        if (ticket === undefined || ticket !== raw.revision) break
        tickets.delete(id)
        const file: GridcraftSavedFile = { requestId: id, document: bound, revision: raw.revision, name: raw.name, format: raw.format, bytes, source: "editor", ...touched }
        unanswered.set(id, file); observe(() => options.onSave(file)); return
      }
      case "gridcraft:export": {
        if (!exact(raw, ["type", "name", "bytes"]) || !text(raw.name)) break
        const bytes = fileBytes(raw.bytes); if (!bytes) break
        observe(() => options.onExport?.({ name: raw.name as string, bytes })); return
      }
      case "gridcraft:edit-attempt": {
        if (!exact(raw, ["type", "document", "kind"]) || !documentID(raw.document) || raw.kind !== "pointer" && raw.kind !== "key") break
        if (bound === undefined || raw.document !== bound || !confirmedReadOnly) return
        observe(() => options.onEditAttempt?.(raw.kind as NativeEditAttempt)); return
      }
      case "gridcraft:held-input-dropped": {
        if (!exact(raw, ["type", "document", "reason"]) || !documentID(raw.document) || !(NATIVE_HELD_INPUT_DROPPED as readonly unknown[]).includes(raw.reason)) break
        observe(() => options.onHeldInputDropped?.(raw.reason as NativeHeldInputDropped)); return
      }
    }
    close("native_port_protocol")
  }
  return {
    get document() { return bound },
    loaded() { if (++loads > 1) close("native_editor_reloaded") },
    handleWindowMessage(event) {
      if (closed) return false
      const hello = verifyEditorHello(event, options.frame, { namespace: "gridcraft", bundle: options.bundle, waiting: !port, loads, protocol: GRIDCRAFT_PROTOCOL })
      if (hello === null) return false
      if (hello !== "accept") { close(`native_editor_${hello}`); return false }
      try {
        const channel = options.createChannel?.() ?? (() => { const native = new MessageChannel(); return { port1: native.port1 as unknown as NativeStructuredPort, port2: native.port2 } })()
        port = channel.port1
        transferEditorPort(options.frame, channel, "gridcraft", { bundle: options.bundle }, receive, GRIDCRAFT_PROTOCOL)
        return true
      } catch { close("native_port_connect"); return false }
    },
    async open(file, openOptions, signal, ms) {
      if (!deadline(ms)) throw new Error("invalid_native_deadline")
      if (bound !== undefined || opening) throw new Error("native_open_bound")
      if (closed || !port || signal.aborted) throw new Error("native_port_unavailable")
      if (!ready) throw new Error("native_access_not_ready")
      if (!text(file?.name) || !/\.(xlsx|xlsm)$/i.test(file.name)) throw new Error("open_unsupported")
      if (!(file.bytes instanceof Uint8Array) || file.bytes.byteLength < 1 || file.bytes.byteLength > GRIDCRAFT_MAX_FILE_BYTES) throw new Error("invalid_native_open")
      if (typeof openOptions?.calculate !== "boolean" || typeof openOptions.autoFit !== "boolean") throw new Error("invalid_native_open")
      const copy = file.bytes.slice()
      opening = true
      try { return await rpc("open", { type: "gridcraft:open", name: file.name, bytes: copy.buffer, calculate: openOptions.calculate, autoFit: openOptions.autoFit }, signal, ms, [copy.buffer]) as GridcraftOpened }
      finally { opening = false }
    },
    async requestSave(signal, ms) {
      if (!deadline(ms)) throw new Error("invalid_native_deadline")
      if (bound === undefined) throw new Error("native_open_required")
      if (savesInFlight() >= GRIDCRAFT_MAX_UNANSWERED_SAVES) throw new Error("native_save_backpressure")
      return await rpc("save", { type: "gridcraft:save-request" }, signal, ms) as GridcraftSavedFile
    },
    ack(requestId, version) {
      const file = unanswered.get(requestId)
      if (!file || !u64(version) || closed) return false
      // A newer revision is already the saved baseline: an older file is a conflict, never a baseline.
      if (lastAcked !== undefined && BigInt(file.revision) < lastAcked) return false
      if (!post({ type: "gridcraft:save-ack", requestId, document: file.document, revision: file.revision, version })) return false
      unanswered.delete(requestId); lastAcked = BigInt(file.revision); return true
    },
    saveFailed(requestId, failed) {
      if (!unanswered.has(requestId) || !(GRIDCRAFT_SAVE_FAILURES as readonly string[]).includes(failed)) return false
      if (!post({ type: "gridcraft:save-failed", requestId, code: failed, error: "" })) return false
      unanswered.delete(requestId); return true
    },
    async setReadOnly(readOnly, signal) {
      if (closed || !port || !ready || signal.aborted || typeof readOnly !== "boolean" || [...pending.values()].some(request => request.kind === "access")) throw new Error("native_access_unavailable")
      if (!readOnly && bound === undefined) throw new Error("native_open_required")
      return await rpc("access", { type: "gridcraft:set-access", readOnly }, signal, timeoutMs, undefined, readOnly) as boolean
    },
    async execute(commands, executeOptions, signal) {
      if (bound === undefined) throw new Error("native_open_required")
      if (!Array.isArray(commands) || commands.length < 1 || commands.length > NATIVE_EXECUTE_MAX_COMMANDS) throw new Error("native_execute_invalid")
      for (const entry of commands) {
        if (!entry || typeof entry !== "object" || !(NATIVE_EXECUTE_COMMANDS as readonly string[]).includes(entry.command) || entry.params !== undefined && (typeof entry.params !== "object" || entry.params === null || Array.isArray(entry.params))) throw new Error("native_execute_invalid")
      }
      const json = JSON.stringify(commands.map(entry => ({ command: entry.command, params: entry.params ?? {} })))
      if (new TextEncoder().encode(json).length > NATIVE_EXECUTE_MAX_BYTES) throw new Error("native_execute_invalid")
      let base: string | undefined
      if (executeOptions?.baseRevision !== undefined) {
        const cut = executeOptions.baseRevision.lastIndexOf(":")
        base = cut >= 0 ? executeOptions.baseRevision.slice(cut + 1) : executeOptions.baseRevision
        if (!savedRevisionToken(base)) throw new Error("native_execute_invalid")
      }
      const keep = executeOptions?.keep, label = executeOptions?.label
      // keep:"human" is always based on a saved version the editor confirmed.
      // A saved version's own revision (saved-<n>) is a base only for keep; the editor takes nothing else.
      if (keep === undefined && base !== undefined && !u64(base) || keep !== undefined && (keep !== "human" || !validExecuteLabel(label) || base === undefined) || keep === undefined && label !== undefined) throw new Error("native_execute_invalid")
      return await rpc("execute", { type: "gridcraft:execute", commands: json, saved: true, ...(base !== undefined ? { baseRevision: base } : {}), ...(keep ? { keep, label } : {}) }, signal, timeoutMs) as NativeExecuteResult
    },
    close() { close() },
  }
}
