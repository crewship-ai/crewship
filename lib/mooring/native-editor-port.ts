import { verifyEditorHello, transferEditorPort, record, type CapabilityWindow, type WindowEnvelope } from "./channel"
import { validNativeDraft, validNativeVersion, type NativeDraft } from "./native-draft"
import type { NativeSaveReceipt } from "./single-editor-save"

export interface NativeStructuredPort {
  postMessage(value: unknown, transfer?: Transferable[]): void
  onmessage: ((event: { data: unknown }) => void) | null
  start?(): void
  close(): void
}
export interface NativeEditorPortOptions {
  applicationId: "wordcraft" | "gridcraft" | "deckcraft"
  enginePin: string
  bundle: string
  frame: CapabilityWindow
  scope: { workspaceId: string; pageId: string; baseVersion: string; generation: string; sessionId: string }
  createChannel?(): { port1: NativeStructuredPort; port2: unknown }
  onChanged(editorRevision: string, documentId: string): void
  onSaveRequest(request: { requestId?: string; editorRevision: string; documentId: string; saveAs?: boolean }): void
  onError(reason: string): void
  onReady?(version: string): void
  // New hosts require a qualified editor that boots with its native write guard.
  requireAccessProtocol?: boolean
  onExport?(file: { name: string; bytes: Uint8Array; saveAs?: boolean }): void
  timeoutMs?: number
}
export interface NativeEditorPort {
  loaded(): void
  handleWindowMessage(event: WindowEnvelope): boolean
  bootstrap(bytes: Uint8Array, signal: AbortSignal): Promise<NativeEditorSnapshot>
  snapshot(signal: AbortSignal, manualRequestId?: string): Promise<NativeEditorSnapshot>
  validate(draft: NativeDraft, signal: AbortSignal): Promise<void>
  ack(receipt: NativeSaveReceipt): Promise<boolean>
  saveFailed(requestId: string, error: string): boolean
  setReadOnly(readOnly: boolean, signal: AbortSignal): Promise<boolean>
  close(): void
}
export function createNativeEditorPort(options: NativeEditorPortOptions): NativeEditorPort { return buildNativeEditorPort(options) }

export interface NativeEditorSnapshot { bytes: Uint8Array; editorRevision: string; documentId: string; nativeState?: string }
interface SnapshotProof { requestId: string; revision: string; editorRevision: string; document: string; hash: Promise<string>; wordBytes?: Uint8Array }
interface PendingRPC { kind: "snapshot" | "validate" | "open" | "access"; readOnly?: boolean; resolve(value: unknown): void; reject(error: Error): void; finish(): void }
const MAX_NATIVE_BYTES = 32 * 1024 * 1024
const text = (value: unknown, limit = 256): value is string => typeof value === "string" && value.length > 0 && value.length <= limit && !value.includes("\0")
const requestID = (value: unknown): value is string => text(value, 128) && new TextEncoder().encode(value).length <= 128
const exact = (value: Record<string, unknown>, keys: string[]) => Object.keys(value).length === keys.length && keys.every(key => key in value)
function nativeBytes(value: unknown): Uint8Array | null {
  const bytes = value instanceof ArrayBuffer ? new Uint8Array(value) : value instanceof Uint8Array && value.buffer instanceof ArrayBuffer ? value : null
  return bytes && bytes.byteLength > 0 && bytes.byteLength <= MAX_NATIVE_BYTES && bytes.buffer.byteLength <= MAX_NATIVE_BYTES ? bytes.slice() : null
}
const gridRevision = (value: unknown): value is string => typeof value === "string" && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18446744073709551615n
const gridDocument = (value: unknown): value is string => text(value, 128) && !value.includes(":")

function buildNativeEditorPort(options: NativeEditorPortOptions): NativeEditorPort {
  options = { ...options, scope: { ...options.scope } }
  if (!options.frame || !text(options.scope.workspaceId) || !text(options.scope.pageId) || !/^[a-f0-9]{64}$/.test(options.bundle) || !["wordcraft", "gridcraft", "deckcraft"].includes(options.applicationId) || !validNativeDraft({ applicationId: options.applicationId, enginePin: options.enginePin, ...options.scope, editorRevision: "initial", bytes: new Uint8Array() })) throw new Error("invalid_native_port")
  const timeoutMs = options.timeoutMs ?? 10_000
  if (!Number.isFinite(timeoutMs) || timeoutMs < 1 || timeoutMs > 60_000) throw new Error("invalid_native_deadline")
  const app = options.applicationId
  const validDocument = (value: unknown): value is string => app === "deckcraft" ? gridRevision(value) && value !== "0" : gridDocument(value)
  const validRevision = (value: unknown): value is string => app === "wordcraft" ? text(value) : gridRevision(value) && (app !== "deckcraft" || value !== "0")
  const wireHashes = new Map<string, string>()
  const pending = new Map<string, PendingRPC>(), proofs = new Map<string, SnapshotProof>(), tickets = new Map<string, string>(), finished = new Set<string>()
  let port: NativeStructuredPort | undefined, closed = false, loads = 0, latestWord: SnapshotProof | undefined
  let observedDocument: string | undefined, boundDocument: string | undefined, bootstrapTarget: string | undefined, bootstrapping = false, observedClosed = false
  let lastAcknowledged = BigInt(options.scope.baseVersion)
  let accessReady = false
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
  function rpc(kind: PendingRPC["kind"], signal: AbortSignal, id: string = crypto.randomUUID(), bytes?: Uint8Array, readOnly?: boolean): Promise<unknown> {
    if (closed || !port || signal.aborted || !requestID(id) || pending.has(id) || finished.has(id) || pending.size >= 8 || app === "wordcraft" && [...pending.values()].some(value => value.kind === "snapshot")) return Promise.reject(new Error("native_port_unavailable"))
    return new Promise((resolve, reject) => {
      const abort = () => { if (kind === "access") close("native_access_unconfirmed"); else { request.finish(); reject(new Error("native_request_aborted")) } }
      const timer = setTimeout(() => { if (kind === "access") close("native_access_unconfirmed"); else { request.finish(); reject(new Error("native_request_timeout")) } }, timeoutMs)
      const request: PendingRPC = { kind, readOnly, resolve, reject, finish() { clearTimeout(timer); signal.removeEventListener("abort", abort); pending.delete(id); rememberFinished(id) } }
      pending.set(id, request); signal.addEventListener("abort", abort, { once: true })
      if (signal.aborted) { abort(); return }
      if (kind === "access") post({ type: `${app}:set-access`, requestId: id, readOnly })
      else if (kind === "snapshot") post({ type: app === "wordcraft" ? "wordcraft:snapshot" : `${app}:export-native`, requestId: id })
      else { const copy = bytes!.slice(); post(kind === "open" ? { type: `${app}:open`, requestId: id, name: app === "deckcraft" ? "document.deckcraft" : "document.json", bytes: copy.buffer } : { type: `${app}:validate-native`, requestId: id, bytes: copy.buffer }, [copy.buffer]) }
    })
  }
  async function snapshotResult(value: Record<string, unknown>, request: PendingRPC, id: string) {
    if ("error" in value) {
      const allowed = exact(value, ["type", "requestId", "error"]) || app !== "wordcraft" && value.valid === false && exact(value, ["type", "requestId", "error", "valid"])
      if (!allowed || !text(value.error, 2048)) { close("native_port_protocol"); return }
      request.finish(); request.reject(new Error(value.error)); return
    }
    const keys = ["type", "requestId", "document", "revision", "bytes"]
    if (!exact(value, keys) || !validDocument(value.document) || !validRevision(value.revision)) { close("native_port_protocol"); return }
    if (boundDocument !== undefined && value.document !== boundDocument || bootstrapTarget !== undefined && value.document !== bootstrapTarget) { close("native_document_changed"); return }
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
    request.finish(); request.resolve({ bytes, editorRevision, documentId: document, ...(app === "wordcraft" ? { nativeState: hex(identityDigest) } : {}) })
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
    if (raw.type === `${app}:opened`) {
      if (!requestID(raw.requestId)) { close("native_port_protocol"); return }
      const request = pending.get(raw.requestId)
      if (!request) { if (!finished.has(raw.requestId)) close("native_port_protocol"); return }
      if (request.kind !== "open") { close("native_port_protocol"); return }
      if (exact(raw, ["type", "requestId", "error"]) && text(raw.error, 2048)) { request.finish(); request.reject(new Error(raw.error)); return }
      if (!exact(raw, ["type", "requestId", "document", "revision"]) || !validDocument(raw.document) || !validRevision(raw.revision)) { close("native_port_protocol"); return }
      bootstrapTarget = raw.document
      request.finish(); request.resolve(raw.document); return
    }
    const response = app === "wordcraft" ? raw.type === "wordcraft:snapshot-result" : raw.type === `${app}:native-exported`
    if (response || raw.type === `${app}:native-validated` && app !== "wordcraft") {
      if (!requestID(raw.requestId)) { close("native_port_protocol"); return }
      const request = pending.get(raw.requestId)
      if (!request) { if (!finished.has(raw.requestId)) close("native_port_protocol"); return }
      if (response) { if (request.kind !== "snapshot") { close("native_port_protocol"); return }; void snapshotResult(raw, request, raw.requestId).catch(() => close("native_snapshot_hash")); return }
      if (request.kind !== "validate" || !(exact(raw, ["type", "requestId", "valid"]) || exact(raw, ["type", "requestId", "valid", "error"])) || typeof raw.valid !== "boolean" || "error" in raw && (typeof raw.error !== "string" || raw.error.length > 2048)) { close("native_port_protocol"); return }
      request.finish(); if (raw.valid && (!raw.error || raw.error === "")) request.resolve(undefined); else request.reject(new Error(typeof raw.error === "string" && raw.error || "native_validation_failed")); return
    }
    if (raw.type === `${app}:changed`) {
      if (!(app === "wordcraft" ? exact(raw, ["type", "document", "revision"]) && validDocument(raw.document) && text(raw.revision) : exact(raw, ["type", "document", "revision", "dirty"]) && validDocument(raw.document) && validRevision(raw.revision) && typeof raw.dirty === "boolean")) { close("native_port_protocol"); return }
      const document = raw.document as string
      if (boundDocument !== undefined && document !== boundDocument || bootstrapTarget !== undefined && document !== bootstrapTarget) { close("native_document_changed"); return }
      observedDocument = document; observedClosed = false
      if (boundDocument !== undefined) observe(() => options.onChanged(app === "wordcraft" ? raw.revision as string : `${document}:${raw.revision}`, document))
      return
    }
    if (raw.type === `${app}:save-request`) {
      if (app === "wordcraft") {
        if (!(exact(raw, ["type", "requestId", "document", "revision"]) || exact(raw, ["type", "requestId", "document", "revision", "saveAs"])) || !requestID(raw.requestId) || !validDocument(raw.document) || !text(raw.revision) || "saveAs" in raw && typeof raw.saveAs !== "boolean") { close("native_port_protocol"); return }
        if (boundDocument !== undefined && raw.document !== boundDocument) { close("native_document_changed"); return }
        if (boundDocument === undefined) return
        const seen = tickets.get(raw.requestId)
        if (seen) { if (seen !== raw.revision) close("native_port_protocol"); return }
        if (tickets.size >= 8) { close("native_save_backpressure"); return }
        tickets.set(raw.requestId, raw.revision)
        observe(() => options.onSaveRequest({ requestId: raw.requestId as string, documentId: raw.document as string, editorRevision: raw.revision as string, ...(typeof raw.saveAs === "boolean" ? { saveAs: raw.saveAs } : {}) }))
      } else {
        if (!exact(raw, ["type", "document", "revision"]) || !validDocument(raw.document) || !validRevision(raw.revision)) { close("native_port_protocol"); return }
        if (boundDocument !== undefined && raw.document !== boundDocument) { close("native_document_changed"); return }
        if (boundDocument === undefined) return
        observe(() => options.onSaveRequest({ documentId: raw.document as string, editorRevision: `${raw.document}:${raw.revision}` }))
      }
      return
    }
    if (raw.type === `${app}:dirty` && exact(raw, ["type", "dirty"]) && typeof raw.dirty === "boolean") return
    if (raw.type === `${app}:ready`) {
      if (!text(raw.version, 128)) { close("native_port_protocol"); return }
      if (exact(raw, ["type", "version", "accessProtocol", "readOnly"]) && raw.accessProtocol === 1 && typeof raw.readOnly === "boolean") {
        if (accessReady || raw.readOnly !== true) { close("native_access_boot_writable"); return }
        accessReady = true
      } else if (!exact(raw, ["type", "version"])) { close("native_port_protocol"); return }
      else if (options.requireAccessProtocol) { close("native_access_protocol_required"); return }
      observe(() => options.onReady?.(raw.version as string)); return
    }
    if (raw.type === `${app}:closed` && app !== "wordcraft" && exact(raw, ["type"])) { if (boundDocument !== undefined || bootstrapTarget !== undefined) close("native_document_changed"); else { observedClosed = true; observedDocument = undefined }; return }
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
    async bootstrap(bytes, signal) {
      if (closed || !port || boundDocument !== undefined || bootstrapping || signal.aborted) throw new Error("native_bootstrap_unavailable")
      if (options.requireAccessProtocol && !accessReady) throw new Error("native_access_not_ready")
      const copy = nativeBytes(bytes); if (!copy) throw new Error("invalid_native_bootstrap")
      bootstrapping = true
      try {
        let before = observedDocument
        if (before === undefined && !observedClosed) before = (await rpc("snapshot", signal) as NativeEditorSnapshot).documentId
        const document = await rpc("open", signal, undefined, copy) as string
        if (document === before) { close("native_bootstrap_identity"); throw new Error("native_bootstrap_identity") }
        // A correlated opened result is emitted only after the async import.
        // Export FIFO or an unrelated File New's changed event is no barrier.
        const result = await rpc("snapshot", signal) as NativeEditorSnapshot
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
      if (app !== "wordcraft") { await rpc("validate", signal, undefined, draft.bytes); return }
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
      if (closed || !port || boundDocument === undefined || saved.applicationId !== app || saved.enginePin !== options.enginePin || saved.workspaceId !== options.scope.workspaceId || saved.pageId !== options.scope.pageId || saved.sessionId !== options.scope.sessionId || saved.generation !== options.scope.generation || saved.issuedBy !== "crewship" || saved.durability !== "node" || saved.phase !== "committed" || saved.candidateHash !== saved.contentHash || !validNativeVersion(saved.baseVersion) || !validNativeVersion(saved.version) || BigInt(saved.version) !== BigInt(saved.baseVersion) + 1n || BigInt(saved.version) <= lastAcknowledged) return false
      const matching: SnapshotProof[] = []
      for (const proof of proofs.values()) {
        if (proof.editorRevision !== saved.editorRevision || proof.document !== boundDocument) continue
        try { if (await proof.hash === saved.contentHash) matching.push(proof) } catch { return false }
      }
      if (closed || !port || BigInt(saved.version) <= lastAcknowledged || matching.length !== 1) return false
      let sent = false
      for (const proof of matching) {
        const delivered = app === "wordcraft" ? post({ type: "wordcraft:saved", requestId: proof.requestId, document: proof.document, revision: proof.revision }) : post({ type: `${app}:save-ack`, requestId: proof.requestId, document: proof.document, revision: proof.revision })
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
      return await rpc("access", signal, undefined, undefined, readOnly) as boolean
    },
    close() { close() },
  }
}
