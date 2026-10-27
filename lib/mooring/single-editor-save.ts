import { type NativeDraft, type NativeDraftJournal, validNativeDraft, validNativeVersion } from "./native-draft"

export interface NativeSaveRequest extends NativeDraft { workspaceId: string; pageId: string; contentHash: string }
export interface NativeSaveReceipt extends Omit<NativeSaveRequest, "bytes"> {
  version: string
  durability: "node"
  issuedBy: "crewship"
  phase: "committed"
  candidateHash: string
}
export type NativeSaveFailure = "conflict" | "quota" | "write_revoked" | "read_revoked"
export class NativeSaveError extends Error {
  constructor(readonly reason: NativeSaveFailure) { super(reason) }
}
export interface NativeSaveState {
  phase: "idle" | "dirty" | "saving" | "saved" | "draft" | "uncertain" | "read-only" | "stopped"
  dirty: boolean
  reason?: string
}
export interface SingleEditorSaveOptions {
  workspaceId: string
  pageId: string
  applicationId: string
  enginePin: string
  baseVersion: string
  generation: string
  sessionId: string
  journal: NativeDraftJournal
  engine: {
    snapshot(signal: AbortSignal): Promise<{ bytes: Uint8Array; editorRevision: string }>
    validate(draft: NativeDraft, signal: AbortSignal): Promise<void>
  }
  transport: {
    save(request: NativeSaveRequest, signal: AbortSignal): Promise<unknown>
    reconcile?(request: NativeSaveRequest, signal: AbortSignal): Promise<unknown>
  }
  onState(state: NativeSaveState): void
  idleMs?: number
  timeoutMs?: number
}
interface Prepared { draft: NativeSaveRequest; epoch: number; sequence: number }

/** One immutable editor/lease lifecycle. A new generation needs a new editor
 * loaded from its confirmed version, never an automatic replay of recovery. */
export function createSingleEditorSave(options: SingleEditorSaveOptions) {
  if (![options.workspaceId, options.pageId].every(value => typeof value === "string" && value.length > 0 && value.length <= 256 && !value.includes("\0")) || !validNativeDraft({ ...options, editorRevision: "initial", bytes: new Uint8Array() })) throw new Error("invalid_native_session")
  const idleMs = options.idleMs ?? 300, timeoutMs = options.timeoutMs ?? 10_000
  if (!Number.isFinite(idleMs) || idleMs < 1 || idleMs > 5000 || !Number.isFinite(timeoutMs) || timeoutMs < 1 || timeoutMs > 60_000) throw new Error("invalid_save_deadline")
  let active = true, writable = true, dirty = false, edits = 0, baseVersion = options.baseVersion
  let blocked: string | undefined, latest: Prepared | undefined, unconfirmed: Prepared | undefined
  let pending: { abort: AbortController } | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let preparations = Promise.resolve()
  let preparingCount = 0
  let nextPreparation = 0, lastFailedPreparation = 0
  const lifetime = new AbortController()
  const emit = (phase: NativeSaveState["phase"], reason?: string) => {
    if (!active && phase !== "stopped") return
    try { options.onState({ phase, dirty, ...(reason ? { reason } : {}) }) } catch { /* UI observers cannot alter persistence or privacy fences. */ }
  }
  const stop = () => {
    if (!active) return
    active = false; clearTimeout(timer); lifetime.abort(); pending?.abort.abort()
    latest = undefined; unconfirmed = undefined
    emit("stopped")
  }
  const unregister = options.journal.onRetire(stop)
  emit("idle")
  function schedule() { clearTimeout(timer); if (active) timer = setTimeout(() => { void save() }, idleMs) }
  function validReceipt(raw: unknown, attempt: Prepared): raw is NativeSaveReceipt {
    if (!raw || typeof raw !== "object") return false
    const r = raw as Record<string, unknown>, d = attempt.draft
    return validNativeVersion(r.version) && BigInt(r.version) === BigInt(d.baseVersion) + 1n
      && r.durability === "node" && r.issuedBy === "crewship" && r.phase === "committed" && r.candidateHash === d.contentHash
      && r.workspaceId === d.workspaceId && r.pageId === d.pageId && r.applicationId === d.applicationId && r.enginePin === d.enginePin && r.baseVersion === d.baseVersion
      && r.sessionId === d.sessionId && r.generation === d.generation && r.editorRevision === d.editorRevision && r.contentHash === d.contentHash
  }
  async function settle(raw: unknown, attempt: Prepared) {
    if (!active || !writable) return
    if (!validReceipt(raw, attempt)) { unconfirmed = attempt; emit("uncertain", "invalid_receipt"); return }
    baseVersion = raw.version
    unconfirmed = undefined
    try { await options.journal.confirm(attempt.draft) } catch { blocked = "storage"; emit("draft", "storage"); return }
    if (!active || !writable) return
    dirty = preparingCount > 0 || lastFailedPreparation > (latest?.sequence ?? 0) || edits !== attempt.epoch || latest?.draft.contentHash !== attempt.draft.contentHash || latest?.draft.editorRevision !== attempt.draft.editorRevision
    emit(dirty ? "dirty" : "saved")
  }
  async function bounded(callback: (signal: AbortSignal) => Promise<unknown>, abort: AbortController): Promise<unknown> {
    let onAbort!: () => void
    const aborted = new Promise<never>((_, reject) => {
      onAbort = () => reject(new Error("save_unconfirmed"))
      abort.signal.addEventListener("abort", onAbort, { once: true })
      if (abort.signal.aborted) onAbort()
    })
    const deadline = setTimeout(() => abort.abort(), timeoutMs)
    try {
      return await Promise.race([Promise.resolve().then(() => { if (abort.signal.aborted) throw new Error("save_retired"); return callback(abort.signal) }), aborted])
    } finally { clearTimeout(deadline); abort.signal.removeEventListener("abort", onAbort) }
  }
  async function transmit(attempt: Prepared, reconcile: boolean) {
    if (!active || !writable || pending) return
    const operation = { abort: new AbortController() }; pending = operation
    emit("saving")
    try {
      const result = await bounded(signal => reconcile ? options.transport.reconcile!(attempt.draft, signal) : options.transport.save(attempt.draft, signal), operation.abort)
      if (!active || !writable || pending !== operation) return
      await settle(result, attempt)
    } catch (error) {
      if (!active || !writable || pending !== operation) return
      if (error instanceof NativeSaveError) {
        blocked = error.reason
        if (error.reason === "read_revoked") { await revokeRead(); return }
        if (error.reason === "write_revoked") { revokeWrite(); return }
        emit("draft", error.reason)
      } else { unconfirmed = attempt; emit("uncertain", "unconfirmed") }
    } finally {
      if (pending === operation) pending = undefined
      if (active && writable && dirty && !blocked && !unconfirmed) schedule()
    }
  }
  function save(): Promise<void> {
    clearTimeout(timer)
    preparingCount++
    const sequence = ++nextPreparation
    const preparing = preparations.then(async (): Promise<Prepared | undefined> => {
      if (!active) return
      const epoch = edits
      dirty = true
      let stage = "engine"
      try {
        const snapshot = await options.engine.snapshot(lifetime.signal)
        if (!active) return
        const draft: NativeDraft = { applicationId: options.applicationId, enginePin: options.enginePin, sessionId: options.sessionId, generation: options.generation, baseVersion, editorRevision: snapshot.editorRevision, bytes: snapshot.bytes.slice() }
        if (!validNativeDraft(draft)) throw new Error("invalid_native_snapshot")
        stage = "storage"
        await options.journal.persist(draft)
        if (!active) return
        stage = "engine"
        await options.engine.validate({ ...draft, bytes: draft.bytes.slice() }, lifetime.signal)
        if (!active) return
        const digest = await crypto.subtle.digest("SHA-256", new Uint8Array(draft.bytes))
        if (!active) return
        const contentHash = `sha256:${Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")}`
        if (latest && draft.editorRevision === latest.draft.editorRevision && contentHash !== latest.draft.contentHash) throw new Error("unstable_editor_revision")
        latest = { draft: { ...draft, workspaceId: options.workspaceId, pageId: options.pageId, contentHash }, epoch, sequence }
        return latest
      } catch {
        lastFailedPreparation = sequence
        if (active) emit(writable ? "draft" : "read-only", stage)
      }
    })
    const ready = preparing.finally(() => { preparingCount-- })
    preparations = ready.then(() => {}, () => {})
    return ready.then(async attempt => {
      if (!attempt || !active) return
      if (!writable || blocked || unconfirmed || pending) { emit(!writable ? "read-only" : unconfirmed ? "uncertain" : "draft", blocked); return }
      if (attempt.draft.baseVersion !== baseVersion) {
        attempt = { ...attempt, draft: { ...attempt.draft, baseVersion } }
        try { await options.journal.persist(attempt.draft) } catch { emit("draft", "storage"); return }
        if (!active || !writable || blocked || unconfirmed || pending) return
        latest = attempt
      }
      await transmit(attempt, false)
    })
  }
  function revokeWrite() {
    if (!active) return
    writable = false; blocked = "write_revoked"; clearTimeout(timer); pending?.abort.abort()
    emit("read-only", "write_revoked")
    if (dirty) void save()
  }
  function revokeRead() { stop(); unregister(); return options.journal.revokeRead() }
  return {
    edited() { if (!active) return; edits++; dirty = true; emit(writable ? "dirty" : "read-only"); schedule() },
    save,
    async reconcile() { if (active && writable && unconfirmed && !pending && options.transport.reconcile) await transmit(unconfirmed, true) },
    revokeWrite,
    revokeRead,
    close() { stop(); unregister(); return options.journal.close() },
  }
}
