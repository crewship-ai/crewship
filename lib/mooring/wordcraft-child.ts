/** Isolated WordApp child. No fetch, credential, WebSocket or host authority. */
import { bundleDigest, decodeMessage, record } from "./channel"

const FRAME_BYTES = 262144
const BOOT_BYTES = 2097152
const MAX_ORIGINALS = 256
const bytes = (raw: string) => new TextEncoder().encode(raw).length
interface Selection { free?(): void }
interface Proposal { operation_id(): string; human_payload_json(checkpoint: Uint8Array): string }
interface Draft {
  native_json(): string; stable_ids_json(): string; optimistic_checkpoint(): Uint8Array
  observe_native_selection(before: string, target: string, start: string, end: string): Selection
  validate_native_transition_selected(before: string, after: string, selection: Selection): void
  native_transition_selected(id: string, before: string, after: string, selection: Selection): Proposal
  undo(id: string): Proposal; redo(id: string): Proposal
  apply_verified_document_update(data: string): void
  apply_verified_own_document_update(data: string, operation: string): void
}
interface Pending {
  check_host_receipt(raw: string): string
  original_request_json(): string
  own_operation_for_host_peer_cut(expectedFrom: string, frame: string): string | undefined
}
export interface WordCraftChildSDK {
  validate_host_bootstrap(joined: string, checkpoint: string): Uint8Array
  /** Host-delivery DATA only; not an acknowledgement of our pending membership. */
  validate_host_peer_document_update(joined: string, expectedFrom: string, frame: string): string
  WordCraftModel: { restore(checkpoint: Uint8Array): { is_read_only(): boolean; free?(): void } }
  WordCraftLocalDraft: new (checkpoint: Uint8Array, origin: string, access: string) => Draft
  WordCraftPendingCommit: new (joined: string, checkpoint: string, original: string) => Pending
  bind_agent_change(scope: string, original: string): string
}
interface NativeHandle { project_native(raw: string): void; stop(): void }
export interface WordCraftNativeWeb {
  start_collaborative(native: string, ids: string, callbacks: {
    can_edit(): boolean
    before(before: string, target: string, start: string, end: string, command: string): void
    validate(before: string, after: string): boolean
    authored(before: string, after: string): boolean
    history(before: string, command: string): string
    /** Local failure diagnostic only; never a receipt or authority. */
    failed(): void
  }): NativeHandle
}
export interface ChildWindow {
  parent: { postMessage(value: unknown, origin: string): void }
  addEventListener(type: "message", listener: (event: MessageEvent) => void): void
  removeEventListener(type: "message", listener: (event: MessageEvent) => void): void
}
function keys(value: Record<string, unknown>, expected: string[]): boolean {
  return Object.keys(value).length === expected.length && expected.every(key => key in value)
}
function parsed(raw: string): Record<string, unknown> {
  const value: unknown = JSON.parse(raw)
  if (!record(value)) throw new Error("invalid_data")
  return value
}
const id = () => `human_${crypto.randomUUID()}`
const emptyHash = `sha256:${"0".repeat(64)}`

/** Loader supplies already pinned SDK and native WASM modules from THIS bundle.
 * waitForNativeReady observes actual WebRunner initialization (loading element
 * removed), never a document mutation or a Saved condition. */
export function startWordCraftChild(
  sdk: WordCraftChildSDK, native: WordCraftNativeWeb, bundle: string,
  waitForNativeReady: () => Promise<void>, hostWindow: ChildWindow = window,
): { close(): void } {
  if (!bundleDigest(bundle)) throw new Error("invalid_bundle")
  let port: MessagePort | undefined
  let connected = false
  let booted = false
  let stopped = false
  let access = "reader"
  let writableModel = false
  let draft: Draft | undefined
  let handle: NativeHandle | undefined
  let joinedRaw = ""
  let checkpointRaw = ""
  let joined: Record<string, unknown>
  let observed = "0"
  let selection: Selection | undefined
  let operationId = ""
  let operationBase = "0"
  let lastOwnId: string | undefined
  let lastPeerRaw: string | undefined
  const originals = new Map<string, { raw: string; pending: Pending }>()
  const originalChanges = new Map<string, { pending: Pending; operation: string }>()
  let originalBytes = 0
  const send = (type: string, fields: Record<string, string> = {}) => {
    const raw = JSON.stringify({ protocol: 1, type, ...fields })
    if (!port || bytes(raw) > FRAME_BYTES) throw new Error("quota_exceeded")
    port.postMessage(raw)
  }
  const status = (state: string, reason: string) => send("wordcraft:status", { state, reason })
  const stop = (reason: string) => {
    if (stopped) return
    stopped = true
    handle?.stop()
    // Keep draft, every original raw packet, native view and undo history.
    try { status("unsaved-draft", reason) } catch { /* retain local originals */ }
    if (port) { port.onmessage = null; port.close() }
  }
  const emit = (proposal: Proposal) => {
    if (!draft || stopped || access !== "writer") throw new Error("forbidden")
    // Capture the exact immutable authored cut SYNCHRONOUSLY before any await,
    // peer delivery or next local command. Rust computes the existing digest.
    const authoredCut = draft.optimistic_checkpoint()
    const payload = parsed(proposal.human_payload_json(authoredCut))
    const opId = proposal.operation_id()
    if (opId !== operationId || originals.size >= MAX_ORIGINALS) throw new Error("quota_exceeded")
    const changeId = `change_${opId}`
    const requestId = `request_${opId}`
    const change = parsed(sdk.bind_agent_change(JSON.stringify(joined.scope), JSON.stringify({
      change_id: changeId, membership_hash: emptyHash,
      operations: [{ op_id: opId, fingerprint: emptyHash, participant_ref: joined.participant_ref,
        base_revision: operationBase, dependencies: lastOwnId ? [lastOwnId] : [], read_preconditions: [], payload }],
    })))
    const raw = JSON.stringify({ type: "submit_changes", request_id: requestId,
      scope: joined.scope, session_id: joined.session_id, versions: joined.versions, changes: [change] })
    const rawBytes = bytes(raw)
    if (rawBytes > FRAME_BYTES - originalBytes || originals.has(requestId)) throw new Error("quota_exceeded")
    const pending = new sdk.WordCraftPendingCommit(joinedRaw, checkpointRaw, raw)
    if (pending.original_request_json() !== raw) throw new Error("invalid_original")
    originals.set(requestId, { raw, pending })
    originalChanges.set(changeId, { pending, operation: opId })
    originalBytes += rawBytes
    lastOwnId = opId
    // MUST precede submit. Parent snapshots its local draft epoch with EXACT raw.
    status("draft", "local-original")
    send("wordcraft:submit", { raw })
    status("pending", "awaiting-host")
  }
  const begin = () => { operationId = id(); operationBase = observed }
  const callbacks = {
    failed: () => stop("native-projection-failed"),
    can_edit: () => !stopped && writableModel && access === "writer" && !!draft,
    before: (before: string, target: string, start: string, end: string, _command: string) => {
      if (!draft || stopped || access !== "writer") throw "forbidden"
      if (draft.native_json() !== before) throw "mismatched_native"
      selection?.free?.()
      selection = draft.observe_native_selection(before, target, start, end)
      begin()
    },
    validate: (before: string, after: string) => {
      if (!draft || stopped || !selection) return false
      draft.validate_native_transition_selected(before, after, selection)
      return true
    },
    authored: (before: string, after: string) => {
      try {
        if (!draft || !selection || stopped) return false
        emit(draft.native_transition_selected(operationId, before, after, selection))
        return true
      } catch { stop("capture-failed"); return false }
    },
    history: (before: string, command: string) => {
      if (!draft || stopped || access !== "writer") throw "forbidden"
      if (draft.native_json() !== before || !["edit.undo", "edit.redo"].includes(command)) throw "mismatched_native"
      begin()
      // SDK refusal happens BEFORE history mutation/pop. Ordinary deletion is
      // allowed; only unsupported delete/overlap UNDO/REDO is refused.
      const proposal = command === "edit.undo" ? draft.undo(operationId) : draft.redo(operationId)
      try { emit(proposal); return draft.native_json() }
      catch { stop("capture-failed"); throw "capture_failed" }
    },
  }
  const hostFrame = (raw: string) => {
    if (!draft || !handle || stopped || bytes(raw) > FRAME_BYTES) throw new Error("invalid_frame")
    // JS dispatch is not validation; the retained Rust boundaries below qualify
    // own receipts and peer DATA through their distinct APIs.
    const value = parsed(raw)
    if (value.type === "commit_result") {
      const original = typeof value.request_id === "string" ? originals.get(value.request_id) : undefined
      if (!original) throw new Error("unknown_receipt")
      const checked = parsed(original.pending.check_host_receipt(raw))
      const changes = checked.changes
      if (!Array.isArray(changes) || changes.some(change => !record(change) || change.status !== "saved")) stop("host-rejected")
      // NEVER move observed/global cut to receipt.outerrevision or overwrite B.
      return
    }
    if (value.type === "document_update") {
      if (lastPeerRaw === raw) return // exact immediate host retry, no second apply
      const data = sdk.validate_host_peer_document_update(joinedRaw, observed, raw)
      // JS only dispatches to a retained ORIGINAL. Rust checks the selected
      // entire membership and cut; this classification never acknowledges it.
      const cuts = record(value.receipt) && Array.isArray(value.receipt.changes) ? value.receipt.changes : []
      const selected = cuts.find(cut => record(cut) && cut.status === "saved" && cut.last_revision === value.to_revision)
      const own = record(selected) && typeof selected.change_id === "string" ? originalChanges.get(selected.change_id) : undefined
      if (own) {
        const operation = own.pending.own_operation_for_host_peer_cut(observed, raw)
        if (operation !== own.operation) throw new Error("invalid_original_cut")
        draft.apply_verified_own_document_update(data, operation)
      } else draft.apply_verified_document_update(data)
      handle.project_native(draft.native_json())
      if (typeof value.to_revision !== "string") throw new Error("invalid_cut")
      observed = value.to_revision
      lastPeerRaw = raw
      return
    }
    throw new Error("unsupported_host_frame")
  }
  const receive = (raw: unknown) => {
    if (stopped || typeof raw !== "string") return
    const maxBytes = booted ? FRAME_BYTES : BOOT_BYTES
    // Cheap UTF16 length preflight before decodeMessage allocates UTF8 bytes.
    if (raw.length > maxBytes) { stop("invalid-port-message"); return }
    const value = decodeMessage(raw, maxBytes)
    if (!value || JSON.stringify(value) !== raw) { stop("invalid-port-message"); return }
    try {
      if (value.type === "wordcraft:bootstrap") {
        if (booted || !keys(value, ["protocol", "type", "joined_json", "checkpoint_json", "assigned_origin", "access"])
          || typeof value.joined_json !== "string" || typeof value.checkpoint_json !== "string"
          || typeof value.assigned_origin !== "string" || !["reader", "writer"].includes(String(value.access))) throw new Error("invalid_bootstrap")
        const state = sdk.validate_host_bootstrap(value.joined_json, value.checkpoint_json)
        joinedRaw = value.joined_json; checkpointRaw = value.checkpoint_json
        joined = parsed(joinedRaw)
        const checkpoint = parsed(checkpointRaw)
        if (typeof checkpoint.revision !== "string") throw new Error("invalid_cut")
        observed = checkpoint.revision
        access = String(value.access)
        const model = sdk.WordCraftModel.restore(state)
        writableModel = !model.is_read_only()
        model.free?.()
        draft = new sdk.WordCraftLocalDraft(state, value.assigned_origin, access)
        handle = native.start_collaborative(draft.native_json(), draft.stable_ids_json(), callbacks)
        booted = true
        void waitForNativeReady().then(() => { if (!stopped) send("wordcraft:ready") }).catch(() => stop("native-start-failed"))
      } else if (value.type === "wordcraft:host-frame" && booted && keys(value, ["protocol", "type", "raw"]) && typeof value.raw === "string") {
        hostFrame(value.raw)
      } else if (value.type === "wordcraft:stop" && keys(value, ["protocol", "type", "reason"]) && typeof value.reason === "string") {
        stop("host-closed")
      } else throw new Error("invalid_port_message")
    } catch { stop("invalid-host-data") }
  }
  const connect = (event: MessageEvent) => {
    if (event.source !== hostWindow.parent || connected || !record(event.data)
      || !keys(event.data, ["type", "protocol", "bundle"]) || event.data.type !== "wordcraft:connect"
      || event.data.protocol !== 1 || event.data.bundle !== bundle || event.ports.length !== 1) return
    connected = true
    port = event.ports[0]
    port.onmessage = event => receive(event.data)
    port.start()
    hostWindow.removeEventListener("message", connect)
  }
  hostWindow.addEventListener("message", connect)
  hostWindow.parent.postMessage({ type: "wordcraft:hello", protocol: 1, bundle }, "*")
  return { close() { stop("unmounted"); hostWindow.removeEventListener("message", connect); port?.close() } }
}
