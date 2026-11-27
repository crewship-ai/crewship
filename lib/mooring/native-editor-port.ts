import { verifyEditorHello, transferEditorPort, record, type CapabilityWindow, type WindowEnvelope } from "./channel"
import { validNativeDraft, validNativeVersion, type NativeDraft } from "./native-draft"
import { createGridcraftNativePort } from "./gridcraft-native-port"
import type { NativeSaveReceipt } from "./single-editor-save"

export interface NativeStructuredPort {
  postMessage(value: unknown, transfer?: Transferable[]): void
  onmessage: ((event: { data: unknown }) => void) | null
  start?(): void
  close(): void
}
export interface NativeEditorPortOptions {
  applicationId: "wordcraft" | "gridcraft"
  enginePin: string
  workbookFormat?: "xlsx" | "xlsm"
  bundle: string
  frame: CapabilityWindow
  scope: { workspaceId: string; pageId: string; baseVersion: string; generation: string; sessionId: string }
  createChannel?(): { port1: NativeStructuredPort; port2: unknown }
  onChanged(editorRevision: string, documentId: string): void
  onSaveRequest(request: { requestId?: string; editorRevision: string; documentId: string; saveAs?: boolean }): void
  onError(reason: string): void
  /** build: the engine build the editor names in ready (GridCraft); absent otherwise. */
  onReady?(version: string, build?: string): void
  // New hosts require a qualified editor that boots with its native write guard.
  requireAccessProtocol?: boolean
  onExport?(file: { name: string; bytes: Uint8Array; saveAs?: boolean }): void
  /** A person tried to write while the host holds the Sheet read-only. Carries no content and no position. */
  onEditAttempt?(kind: NativeEditAttempt): void
  /** Keys typed while the write was being taken over were thrown away; the host tells the person why. */
  onHeldInputDropped?(reason: NativeHeldInputDropped): void
  timeoutMs?: number
}
export type NativeEditAttempt = "pointer" | "key"
export const NATIVE_HELD_INPUT_DROPPED = ["refused", "timeout", "limit", "pointer", "document", "draft"] as const
export type NativeHeldInputDropped = (typeof NATIVE_HELD_INPUT_DROPPED)[number]
export interface NativeEditorPort {
  loaded(): void
  handleWindowMessage(event: WindowEnvelope): boolean
  /** `timeoutMs` (1 ms to 120 s, default the port's) bounds each of the bootstrap's three requests: a large
   * document's import, recalculation and first export take longer than an edit. Later requests keep the port's. */
  bootstrap(bytes: Uint8Array, signal: AbortSignal, timeoutMs?: number): Promise<NativeEditorSnapshot>
  snapshot(signal: AbortSignal, manualRequestId?: string): Promise<NativeEditorSnapshot>
  validate(draft: NativeDraft, signal: AbortSignal): Promise<void>
  ack(receipt: NativeSaveReceipt): Promise<boolean>
  saveFailed(requestId: string, error: string): boolean
  setReadOnly(readOnly: boolean, signal: AbortSignal): Promise<boolean>
  /** Binds a clean, read-only editor to the lease generation the server just granted to this same session, so
   * its later saves are acknowledged without opening a new frame. It unlocks nothing: only `setReadOnly(false)`
   * does. False, with nothing changed, unless the editor is bound, idle, confirmed read-only and clean, the
   * session is the same, the generation is newer and the version is not older than the last acknowledged one. */
  rebind(scope: { sessionId: string; generation: string; baseVersion: string }): boolean
  /** Replays a fixed list of value commands on the open workbook (gridcraft only), as one undo step,
   * and makes the result the editor's saved baseline. Rejects without changing anything when the editor
   * is busy, the revision moved or the editor refuses; the caller then opens the whole document. */
  execute(commands: NativeExecuteCommand[], options: NativeExecuteOptions, signal: AbortSignal): Promise<NativeExecuteResult>
  close(): void
}
export function createNativeEditorPort(options: NativeEditorPortOptions): NativeEditorPort {
  if (options.applicationId === "gridcraft" && options.workbookFormat) {
    return createGridcraftNativePort({ ...options, fileName: "workbook." + options.workbookFormat })
  }
  return buildNativeEditorPort(options)
}

export interface NativeExecuteCommand { command: (typeof NATIVE_EXECUTE_COMMANDS)[number]; params?: Record<string, unknown> }
/**
 * keep "human": the commands are another author's saved log, replayed over the
 * person's unsaved state; a cell the person changed since the base keeps their value and is named in `conflicts`.
 * The whole replay is one undo step called `label`. Refused atomically (not_replayable, pin_mismatch, stale_base).
 */
export interface NativeExecuteOptions { baseRevision?: string; keep?: "human"; label?: string }
/** A cell both changed: shown in the chat card only (cell contents never go to the server's logs or audit). */
export interface NativeExecuteConflict { sheet: string; cell: string; agent: string; kept: string }
/** savedRevision: over unsaved work, the revision of the editor's saved version after the replay, apart
 *  from `revision` (the merged content); the next replay keeping the person's cells names it as its base. */
export interface NativeExecuteResult { documentId: string; revision: string; savedRevision?: string; applied: number; conflicts?: NativeExecuteConflict[]; conflictsTruncated?: boolean }
/** An engine build as the editor (ready.build) and the server (engineBuild) name it: "<version>+<commit12> patch <sha256>". */
export const engineBuildToken = (value: unknown): value is string => typeof value === "string" && value.length <= 160 && /^[0-9A-Za-z.\-]{1,64}\+[a-f0-9]{12} patch [a-f0-9]{64}$/.test(value)
/** A saved version's revision as the editor names it: a u64, or saved-<n> after a replay over unsaved work. */
export const savedRevisionToken = (value: unknown): value is string => typeof value === "string" && /^(?:[0-9]{1,20}|saved-[0-9]{1,20})$/.test(value)
export const NATIVE_EXECUTE_COMMANDS = ["cell.set", "range.setValues", "formulas.calculateNow", "edit.clearContents"] as const
export const NATIVE_EXECUTE_MAX_CONFLICTS = 200
export const validExecuteLabel = (label: unknown): label is string => typeof label === "string" && label.trim().length > 0 && label.length <= 80
export const NATIVE_EXECUTE_MAX_COMMANDS = 64
export const NATIVE_EXECUTE_MAX_BYTES = 64 * 1024
export interface NativeEditorSnapshot { bytes: Uint8Array; editorRevision: string; documentId: string; nativeState?: string; revision?: string; /** A workbook file's format, as the editor saved it. */ format?: "xlsx" | "xlsm"; /** The cells the person changed since the editor's saved version, as the application's adapter names them, or unknown. */ touched?: string[] | "unknown" }
interface SnapshotProof { requestId: string; revision: string; editorRevision: string; document: string; hash: Promise<string>; wordBytes?: Uint8Array }
interface PendingRPC { kind: "snapshot" | "validate" | "open" | "access" | "execute"; readOnly?: boolean; resolve(value: unknown): void; reject(error: Error): void; finish(): void }
// The server's limit for one native document (craftsession, 32 MiB): a larger export could not be saved.
const MAX_NATIVE_BYTES = 32 * 1024 * 1024
const MAX_BOOTSTRAP_DEADLINE_MS = 120_000
const text = (value: unknown, limit = 256): value is string => typeof value === "string" && value.length > 0 && value.length <= limit && !value.includes("\0")
const requestID = (value: unknown): value is string => text(value, 128) && new TextEncoder().encode(value).length <= 128
const exact = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).length === keys.length && keys.every(key => key in value)
// The editor refuses a native document over its 32 MiB limit with its own sentence
// (gridcraft-web native_document::validate_length); the host names it by code.
const editorError = (message: string) => /accepts at most 32 MiB\.?$/.test(message) ? "native_document_too_large" : message
function nativeBytes(value: unknown): Uint8Array | null {
  const bytes = value instanceof ArrayBuffer ? new Uint8Array(value) : value instanceof Uint8Array && value.buffer instanceof ArrayBuffer ? value : null
  return bytes && bytes.byteLength > 0 && bytes.byteLength <= MAX_NATIVE_BYTES && bytes.buffer.byteLength <= MAX_NATIVE_BYTES ? bytes.slice() : null
}
const gridRevision = (value: unknown): value is string => typeof value === "string" && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18446744073709551615n
const gridDocument = (value: unknown): value is string => text(value, 128) && !value.includes(":")

function buildNativeEditorPort(options: NativeEditorPortOptions): NativeEditorPort {
  options = { ...options, scope: { ...options.scope } }
  if (!options.frame || !text(options.scope.workspaceId) || !text(options.scope.pageId) || !/^[a-f0-9]{64}$/.test(options.bundle) || !["wordcraft", "gridcraft"].includes(options.applicationId) || !validNativeDraft({ applicationId: options.applicationId, enginePin: options.enginePin, ...options.scope, editorRevision: "initial", bytes: new Uint8Array() })) throw new Error("invalid_native_port")
  const timeoutMs = options.timeoutMs ?? 10_000
  if (!Number.isFinite(timeoutMs) || timeoutMs < 1 || timeoutMs > 60_000) throw new Error("invalid_native_deadline")
  const app = options.applicationId
  const wireHashes = new Map<string, string>()
  const pending = new Map<string, PendingRPC>(), proofs = new Map<string, SnapshotProof>(), tickets = new Map<string, string>(), finished = new Set<string>()
  let port: NativeStructuredPort | undefined, closed = false, loads = 0, latestWord: SnapshotProof | undefined
  let observedDocument: string | undefined, boundDocument: string | undefined, bootstrapTarget: string | undefined, bootstrapping = false, observedClosed = false
  let lastAcknowledged = BigInt(options.scope.baseVersion)
  let accessReady = false
  // A private-port editor boots read-only; afterwards only an applied access reply changes this.
  let confirmedReadOnly = true, observedDirty = false, acknowledging = 0
  const observe = (callback: () => void) => { try { callback() } catch { /* Observers cannot become persistence authority. */ } }
  function rememberFinished(id: string) { finished.add(id); if (finished.size > 64) finished.delete(finished.values().next().value!) }
  function close(reason?: string) {
    if (closed) return
    closed = true
    if (port) { port.onmessage = null; port.close() }
    for (const request of [...pending.values()]) { request.finish(); request.reject(new Error(reason ?? "native_port_closed")) }
    proofs.clear(); tickets.clear(); wireHashes.clear(); latestWord = undefined
    if (reason) observe(() => options.onError(reason))
  }
  function post(value: unknown, transfer?: Transferable[]): boolean {
    if (closed || !port) return false
    try { port.postMessage(value, transfer); return true } catch { close("native_port_send"); return false }
  }
  function rpc(kind: PendingRPC["kind"], signal: AbortSignal, id: string = crypto.randomUUID(), bytes?: Uint8Array, readOnly?: boolean, execute?: Record<string, unknown>, deadline = timeoutMs): Promise<unknown> {
    if (closed || !port || signal.aborted || !requestID(id) || pending.has(id) || finished.has(id) || pending.size >= 8 || app === "wordcraft" && [...pending.values()].some(value => value.kind === "snapshot")) return Promise.reject(new Error("native_port_unavailable"))
    return new Promise((resolve, reject) => {
      const abort = () => { if (kind === "access") close("native_access_unconfirmed"); else { request.finish(); reject(new Error("native_request_aborted")) } }
      const timer = setTimeout(() => { if (kind === "access") close("native_access_unconfirmed"); else { request.finish(); reject(new Error("native_request_timeout")) } }, deadline)
      const request: PendingRPC = { kind, readOnly, resolve, reject, finish() { clearTimeout(timer); signal.removeEventListener("abort", abort); pending.delete(id); rememberFinished(id) } }
      pending.set(id, request); signal.addEventListener("abort", abort, { once: true })
      if (signal.aborted) { abort(); return }
      if (kind === "execute") post({ type: "gridcraft:execute", requestId: id, ...execute })
      else if (kind === "access") post({ type: `${app}:set-access`, requestId: id, readOnly })
      else if (kind === "snapshot") post({ type: app === "wordcraft" ? "wordcraft:snapshot" : "gridcraft:export-native", requestId: id })
      else { const copy = bytes!.slice(); post(kind === "open" ? { type: `${app}:open`, requestId: id, name: "document.json", bytes: copy.buffer } : { type: "gridcraft:validate-native", requestId: id, bytes: copy.buffer }, [copy.buffer]) }
    })
  }
  async function snapshotResult(value: Record<string, unknown>, request: PendingRPC, id: string) {
    if ("error" in value) {
      const allowed = exact(value, ["type", "requestId", "error"]) || app === "gridcraft" && value.valid === false && exact(value, ["type", "requestId", "error", "valid"])
      if (!allowed || !text(value.error, 2048)) { close("native_port_protocol"); return }
      request.finish(); request.reject(new Error(editorError(value.error))); return
    }
    const keys = ["type", "requestId", "document", "revision", "bytes"]
    if (!exact(value, keys) || !gridDocument(value.document) || !(app === "wordcraft" ? text(value.revision) : gridRevision(value.revision))) { close("native_port_protocol"); return }
    if (boundDocument !== undefined && value.document !== boundDocument || bootstrapTarget !== undefined && value.document !== bootstrapTarget) { close("native_document_changed"); return }
    // A well-formed export past the document limit is the Sheet's size, not a broken editor: name it, keep the port.
    const raw = value.bytes instanceof ArrayBuffer || value.bytes instanceof Uint8Array ? value.bytes : undefined
    if (raw && raw.byteLength > MAX_NATIVE_BYTES) { request.finish(); request.reject(new Error("native_document_too_large")); return }
    const bytes = nativeBytes(value.bytes)
    if (!bytes) { close("native_port_protocol"); return }
    const revision = value.revision as string, document = value.document as string
    const wireIdentity = JSON.stringify([document, revision])
    const hex = (digest: ArrayBuffer) => Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")
    const [contentDigest, identityDigest] = await Promise.all([
      crypto.subtle.digest("SHA-256", new Uint8Array(bytes)),
      crypto.subtle.digest("SHA-256", new TextEncoder().encode(wireIdentity)),
    ])
    if (closed || pending.get(id) !== request) return
    const contentHash = `sha256:${hex(contentDigest)}`
    const previousHash = wireHashes.get(wireIdentity)
    if (previousHash !== undefined && previousHash !== contentHash) { close("unstable_editor_revision"); return }
    wireHashes.set(wireIdentity, contentHash)
    if (wireHashes.size > 256) wireHashes.delete(wireHashes.keys().next().value!)
    // Opaque server receipt identity binds one RPC, even when two manual saves
    // export identical bytes at the same engine revision. Never parse this token.
    const editorRevision = `snapshot:${id}:${hex(identityDigest)}`
    const hash = Promise.resolve(contentHash)
    const proof: SnapshotProof = { requestId: id, revision, editorRevision, document, hash }
    if (app === "wordcraft") { if (latestWord) latestWord.wordBytes = undefined; proof.wordBytes = bytes.slice(); latestWord = proof }
    proofs.set(id, proof)
    if (proofs.size > 256) {
      const expired = [...proofs.keys()].find(key => !tickets.has(key) && key !== id)
      if (expired !== undefined) proofs.delete(expired)
    }
    request.finish(); request.resolve({ bytes, editorRevision, documentId: document, revision: app === "wordcraft" ? revision : `${document}:${revision}`, ...(app === "wordcraft" ? { nativeState: hex(identityDigest) } : {}) })
  }
  function receive(raw: unknown) {
    if (closed) return
    if (!record(raw) || typeof raw.type !== "string") { close("native_port_protocol"); return }
    if (raw.type === `${app}:access`) {
      if (!requestID(raw.requestId)) { close("native_port_protocol"); return }
      const request = pending.get(raw.requestId)
      if (!request) { if (!finished.has(raw.requestId)) close("native_port_protocol"); return }
      if (request.kind !== "access") { close("native_port_protocol"); return }
      if (exact(raw, ["type", "requestId", "error"]) && text(raw.error, 2048)) { close("native_access_unconfirmed"); return }
      if (!exact(raw, ["type", "requestId", "readOnly"]) || typeof raw.readOnly !== "boolean") { close("native_port_protocol"); return }
      if (raw.readOnly !== request.readOnly) { close("native_access_mismatch"); return }
      request.finish(); request.resolve(raw.readOnly); return
    }
    if (raw.type === "gridcraft:executed" && app === "gridcraft") {
      if (!requestID(raw.requestId)) { close("native_port_protocol"); return }
      const request = pending.get(raw.requestId)
      if (!request) { if (!finished.has(raw.requestId)) close("native_port_protocol"); return }
      if (request.kind !== "execute") { close("native_port_protocol"); return }
      if (exact(raw, ["type", "requestId", "error"]) && text(raw.error, 2048)) { request.finish(); request.reject(new Error(raw.error)); return }
      if (!exact(raw, ["type", "requestId", "document", "revision", "applied"]) || !gridDocument(raw.document) || !gridRevision(raw.revision) || typeof raw.applied !== "number" || !Number.isInteger(raw.applied) || raw.applied < 1 || raw.applied > NATIVE_EXECUTE_MAX_COMMANDS) { close("native_port_protocol"); return }
      if (boundDocument !== undefined && raw.document !== boundDocument) { close("native_document_changed"); return }
      request.finish(); request.resolve({ documentId: raw.document, revision: `${raw.document}:${raw.revision}`, applied: raw.applied } satisfies NativeExecuteResult); return
    }
    if (raw.type === `${app}:opened`) {
      if (!requestID(raw.requestId)) { close("native_port_protocol"); return }
      const request = pending.get(raw.requestId)
      if (!request) { if (!finished.has(raw.requestId)) close("native_port_protocol"); return }
      if (request.kind !== "open") { close("native_port_protocol"); return }
      if (exact(raw, ["type", "requestId", "error"]) && text(raw.error, 2048)) { request.finish(); request.reject(new Error(editorError(raw.error))); return }
      if (!exact(raw, ["type", "requestId", "document", "revision"]) || !gridDocument(raw.document) || !(app === "wordcraft" ? text(raw.revision) : gridRevision(raw.revision))) { close("native_port_protocol"); return }
      bootstrapTarget = raw.document
      request.finish(); request.resolve(raw.document); return
    }
    const response = app === "wordcraft" ? raw.type === "wordcraft:snapshot-result" : raw.type === "gridcraft:native-exported"
    if (response || raw.type === "gridcraft:native-validated" && app === "gridcraft") {
      if (!requestID(raw.requestId)) { close("native_port_protocol"); return }
      const request = pending.get(raw.requestId)
      if (!request) { if (!finished.has(raw.requestId)) close("native_port_protocol"); return }
      if (response) { if (request.kind !== "snapshot") { close("native_port_protocol"); return }; void snapshotResult(raw, request, raw.requestId).catch(() => close("native_snapshot_hash")); return }
      if (request.kind !== "validate" || !(exact(raw, ["type", "requestId", "valid"]) || exact(raw, ["type", "requestId", "valid", "error"])) || typeof raw.valid !== "boolean" || "error" in raw && (typeof raw.error !== "string" || raw.error.length > 2048)) { close("native_port_protocol"); return }
      request.finish(); if (raw.valid && (!raw.error || raw.error === "")) request.resolve(undefined); else request.reject(new Error(typeof raw.error === "string" && raw.error || "native_validation_failed")); return
    }
    if (raw.type === "gridcraft:edit-attempt" && app === "gridcraft") {
      if (!exact(raw, ["type", "document", "kind"]) || !gridDocument(raw.document) || raw.kind !== "pointer" && raw.kind !== "key") { close("native_port_protocol"); return }
      // Only the bound, still read-only workbook asks for a write; a late message after an unlock is nothing.
      if (boundDocument === undefined || raw.document !== boundDocument || !confirmedReadOnly) return
      observe(() => options.onEditAttempt?.(raw.kind as NativeEditAttempt)); return
    }
    if (raw.type === "gridcraft:held-input-dropped" && app === "gridcraft") {
      if (!exact(raw, ["type", "document", "reason"]) || !gridDocument(raw.document) || !(NATIVE_HELD_INPUT_DROPPED as readonly unknown[]).includes(raw.reason)) { close("native_port_protocol"); return }
      observe(() => options.onHeldInputDropped?.(raw.reason as NativeHeldInputDropped)); return
    }
    if (raw.type === `${app}:changed`) {
      if (!(app === "wordcraft" ? exact(raw, ["type", "document", "revision"]) && gridDocument(raw.document) && text(raw.revision) : exact(raw, ["type", "document", "revision", "dirty"]) && gridDocument(raw.document) && gridRevision(raw.revision) && typeof raw.dirty === "boolean")) { close("native_port_protocol"); return }
      const document = raw.document as string
      if (boundDocument !== undefined && document !== boundDocument || bootstrapTarget !== undefined && document !== bootstrapTarget) { close("native_document_changed"); return }
      observedDocument = document; observedClosed = false
      // A Word change message carries no dirty flag: any change counts until `dirty` says otherwise.
      observedDirty = app === "gridcraft" ? raw.dirty as boolean : true
      if (boundDocument !== undefined) observe(() => options.onChanged(app === "wordcraft" ? raw.revision as string : `${document}:${raw.revision}`, document))
      return
    }
    if (raw.type === `${app}:save-request`) {
      if (app === "wordcraft") {
        if (!(exact(raw, ["type", "requestId", "document", "revision"]) || exact(raw, ["type", "requestId", "document", "revision", "saveAs"])) || !requestID(raw.requestId) || !gridDocument(raw.document) || !text(raw.revision) || "saveAs" in raw && typeof raw.saveAs !== "boolean") { close("native_port_protocol"); return }
        if (boundDocument !== undefined && raw.document !== boundDocument) { close("native_document_changed"); return }
        if (boundDocument === undefined) return
        const seen = tickets.get(raw.requestId)
        if (seen) { if (seen !== raw.revision) close("native_port_protocol"); return }
        if (tickets.size >= 8) { close("native_save_backpressure"); return }
        tickets.set(raw.requestId, raw.revision)
        observe(() => options.onSaveRequest({ requestId: raw.requestId as string, documentId: raw.document as string, editorRevision: raw.revision as string, ...(typeof raw.saveAs === "boolean" ? { saveAs: raw.saveAs } : {}) }))
      } else {
        if (!exact(raw, ["type", "document", "revision"]) || !gridDocument(raw.document) || !gridRevision(raw.revision)) { close("native_port_protocol"); return }
        if (boundDocument !== undefined && raw.document !== boundDocument) { close("native_document_changed"); return }
        if (boundDocument === undefined) return
        observe(() => options.onSaveRequest({ documentId: raw.document as string, editorRevision: `${raw.document}:${raw.revision}` }))
      }
      return
    }
    if (raw.type === `${app}:dirty` && exact(raw, ["type", "dirty"]) && typeof raw.dirty === "boolean") { observedDirty = raw.dirty; return }
    if (raw.type === `${app}:ready`) {
      if (!text(raw.version, 128)) { close("native_port_protocol"); return }
      if (exact(raw, ["type", "version", "accessProtocol", "readOnly"]) && raw.accessProtocol === 1 && typeof raw.readOnly === "boolean") {
        if (accessReady || raw.readOnly !== true) { close("native_access_boot_writable"); return }
        accessReady = true
      } else if (!exact(raw, ["type", "version"])) { close("native_port_protocol"); return }
      else if (options.requireAccessProtocol) { close("native_access_protocol_required"); return }
      observe(() => options.onReady?.(raw.version as string)); return
    }
    if (raw.type === "gridcraft:closed" && app === "gridcraft" && exact(raw, ["type"])) { if (boundDocument !== undefined || bootstrapTarget !== undefined) close("native_document_changed"); else { observedClosed = true; observedDocument = undefined }; return }
    if (raw.type === `${app}:failed` && exact(raw, ["type", "error"]) && text(raw.error, 2048)) { close("native_engine_failed"); return }
    if (raw.type === `${app}:save`) {
      if (!(exact(raw, ["type", "name", "bytes"]) || app === "wordcraft" && exact(raw, ["type", "name", "bytes", "saveAs"]) && typeof raw.saveAs === "boolean") || !text(raw.name)) { close("native_port_protocol"); return }
      const bytes = nativeBytes(raw.bytes); if (!bytes) { close("native_port_protocol"); return }
      observe(() => options.onExport?.({ name: raw.name as string, bytes, ...(typeof raw.saveAs === "boolean" ? { saveAs: raw.saveAs } : {}) })); return
    }
    close("native_port_protocol")
  }
  return {
    loaded() { if (++loads > 1) close("native_editor_reloaded") },
    handleWindowMessage(event) {
      if (closed) return false
      const hello = verifyEditorHello(event, options.frame, { namespace: app, bundle: options.bundle, waiting: !port, loads })
      if (hello === null) return false
      if (hello !== "accept") { close(`native_editor_${hello}`); return false }
      try {
        const channel = options.createChannel?.() ?? (() => { const native = new MessageChannel(); return { port1: native.port1 as unknown as NativeStructuredPort, port2: native.port2 } })()
        port = channel.port1
        transferEditorPort(options.frame, channel, app, { bundle: options.bundle }, receive)
        return true
      } catch { close("native_port_connect"); return false }
    },
    async bootstrap(bytes, signal, deadline = timeoutMs) {
      if (!Number.isFinite(deadline) || deadline < 1 || deadline > MAX_BOOTSTRAP_DEADLINE_MS) throw new Error("invalid_native_deadline")
      if (closed || !port || boundDocument !== undefined || bootstrapping || signal.aborted) throw new Error("native_bootstrap_unavailable")
      if (options.requireAccessProtocol && !accessReady) throw new Error("native_access_not_ready")
      const copy = nativeBytes(bytes); if (!copy) throw new Error("invalid_native_bootstrap")
      bootstrapping = true
      try {
        let before = observedDocument
        if (before === undefined && !observedClosed) before = (await rpc("snapshot", signal, undefined, undefined, undefined, undefined, deadline) as NativeEditorSnapshot).documentId
        const document = await rpc("open", signal, undefined, copy, undefined, undefined, deadline) as string
        if (document === before) { close("native_bootstrap_identity"); throw new Error("native_bootstrap_identity") }
        // A correlated opened result is emitted only after the async import.
        // Export FIFO or an unrelated File New's changed event is no barrier.
        const result = await rpc("snapshot", signal, undefined, undefined, undefined, undefined, deadline) as NativeEditorSnapshot
        if (closed || signal.aborted || result.documentId !== document) throw new Error("native_bootstrap_identity")
        boundDocument = document; observedDocument = document; bootstrapTarget = undefined
        for (const [id, proof] of proofs) if (proof.document !== document) proofs.delete(id)
        return result
      } finally { bootstrapping = false }
    },
    async snapshot(signal, manualRequestId) {
      if (boundDocument === undefined) throw new Error("native_bootstrap_required")
      if (manualRequestId !== undefined && (app !== "wordcraft" || !tickets.has(manualRequestId))) throw new Error("unknown_native_save_ticket")
      return await rpc("snapshot", signal, manualRequestId) as NativeEditorSnapshot
    },
    async validate(draft, signal) {
      if (closed || !port || boundDocument === undefined || signal.aborted || !validNativeDraft(draft) || draft.applicationId !== app || draft.enginePin !== options.enginePin) throw new Error("native_validation_unavailable")
      if (app === "gridcraft") { await rpc("validate", signal, undefined, draft.bytes); return }
      // Word's exact pinned snapshot command already parses its native output.
      // There is no separate Word validate RPC; prove this is that same snapshot.
      const checked = latestWord?.wordBytes
      if (!checked || latestWord?.editorRevision !== draft.editorRevision || checked.length !== draft.bytes.length || !checked.every((byte, index) => byte === draft.bytes[index])) throw new Error("unvalidated_word_snapshot")
    },
    async ack(receipt) {
      if (!receipt || typeof receipt !== "object") return false
      // Copy the verified candidate before awaiting its hash; host callbacks
      // cannot mutate the metadata midway through an acknowledgement.
      const saved = { ...receipt }
      // `unchanged` confirms this snapshot at a version the document already has,
      // so it may repeat the last acknowledged version but never go below it.
      const unchanged = saved.unchanged === true
      if ("unchanged" in saved && !unchanged) return false
      const superseded = () => unchanged ? BigInt(saved.version) < lastAcknowledged : BigInt(saved.version) <= lastAcknowledged
      if (closed || !port || boundDocument === undefined || saved.applicationId !== app || saved.enginePin !== options.enginePin || saved.workspaceId !== options.scope.workspaceId || saved.pageId !== options.scope.pageId || saved.sessionId !== options.scope.sessionId || saved.generation !== options.scope.generation || saved.issuedBy !== "crewship" || saved.durability !== "node" || saved.phase !== "committed" || saved.candidateHash !== saved.contentHash || !validNativeVersion(saved.baseVersion) || !validNativeVersion(saved.version) || BigInt(saved.version) !== BigInt(saved.baseVersion) + (unchanged ? 0n : 1n) || superseded()) return false
      const matching: SnapshotProof[] = []
      acknowledging++
      try {
        for (const proof of proofs.values()) {
          if (proof.editorRevision !== saved.editorRevision || proof.document !== boundDocument) continue
          try { if (await proof.hash === saved.contentHash) matching.push(proof) } catch { return false }
        }
      } finally { acknowledging-- }
      if (closed || !port || superseded() || matching.length !== 1) return false
      let sent = false
      for (const proof of matching) {
        const delivered = app === "wordcraft" ? post({ type: "wordcraft:saved", requestId: proof.requestId, document: proof.document, revision: proof.revision }) : post({ type: "gridcraft:save-ack", requestId: proof.requestId, document: proof.document, revision: proof.revision })
        if (delivered) { sent = true; proofs.delete(proof.requestId); tickets.delete(proof.requestId) }
      }
      if (sent) lastAcknowledged = BigInt(saved.version)
      return sent
    },
    saveFailed(id, error) {
      if (app !== "wordcraft" || !requestID(id) || !text(error, 512) || new TextEncoder().encode(error).length > 1024 || !tickets.has(id)) return false
      const sent = post({ type: "wordcraft:save-failed", requestId: id, error })
      if (sent) tickets.delete(id)
      return sent
    },
    async setReadOnly(readOnly, signal) {
      if (closed || !port || !accessReady || signal.aborted || typeof readOnly !== "boolean" || [...pending.values()].some(request => request.kind === "access")) throw new Error("native_access_unavailable")
      if (!readOnly && boundDocument === undefined) throw new Error("native_bootstrap_required")
      const applied = await rpc("access", signal, undefined, undefined, readOnly) as boolean
      confirmedReadOnly = applied
      return applied
    },
    rebind(scope) {
      if (closed || !port || boundDocument === undefined || bootstrapping || acknowledging > 0 || pending.size > 0 || !confirmedReadOnly || observedDirty) return false
      if (!scope || scope.sessionId !== options.scope.sessionId) return false
      if (!validNativeVersion(scope.generation) || scope.generation === "0" || BigInt(scope.generation) <= BigInt(options.scope.generation)) return false
      if (!validNativeVersion(scope.baseVersion) || BigInt(scope.baseVersion) < lastAcknowledged) return false
      options.scope.generation = scope.generation
      options.scope.baseVersion = scope.baseVersion
      lastAcknowledged = BigInt(scope.baseVersion)
      // Snapshots and save tickets of the earlier generation prove nothing under the new one.
      proofs.clear(); tickets.clear(); latestWord = undefined
      return true
    },
    async execute(commands, executeOptions, signal) {
      if (app !== "gridcraft" || boundDocument === undefined) throw new Error("native_bootstrap_required")
      if (!Array.isArray(commands) || commands.length < 1 || commands.length > NATIVE_EXECUTE_MAX_COMMANDS) throw new Error("native_execute_invalid")
      for (const entry of commands) {
        if (!entry || typeof entry !== "object" || !(NATIVE_EXECUTE_COMMANDS as readonly string[]).includes(entry.command) || entry.params !== undefined && (typeof entry.params !== "object" || entry.params === null || Array.isArray(entry.params))) throw new Error("native_execute_invalid")
      }
      const json = JSON.stringify(commands.map(entry => ({ command: entry.command, params: entry.params ?? {} })))
      if (new TextEncoder().encode(json).length > NATIVE_EXECUTE_MAX_BYTES) throw new Error("native_execute_invalid")
      const base = executeOptions?.baseRevision
      let revision: string | undefined
      if (base !== undefined) {
        const cut = base.lastIndexOf(":")
        revision = cut >= 0 ? base.slice(cut + 1) : base
        if (!gridRevision(revision)) throw new Error("native_execute_invalid")
      }
      return await rpc("execute", signal, undefined, undefined, undefined, { commands: json, saved: true, ...(revision !== undefined ? { baseRevision: revision } : {}) }) as NativeExecuteResult
    },
    close() { close() },
  }
}
