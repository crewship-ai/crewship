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
    snapshot(signal: AbortSignal, requestId?: string): Promise<{ bytes: Uint8Array; editorRevision: string; nativeState?: string }>
    validate(draft: NativeDraft, signal: AbortSignal): Promise<void>
  }
  transport: {
    save(request: NativeSaveRequest, signal: AbortSignal): Promise<unknown>
    reconcile?(request: NativeSaveRequest, signal: AbortSignal): Promise<unknown>
  }
  onState(state: NativeSaveState): void
  onReceipt?(receipt: NativeSaveReceipt): void | Promise<unknown>
  idleMs?: number
  timeoutMs?: number
}
interface Prepared { draft: NativeSaveRequest; epoch: number; sequence: number; nativeState?: string }

/** One immutable editor/lease lifecycle. A new generation needs a new editor
 * loaded from its confirmed version, never an automatic replay of recovery. */
export function createSingleEditorSave(options: SingleEditorSaveOptions) {
  options = { ...options }
  if (![options.workspaceId, options.pageId].every(value => typeof value === "string" && value.length > 0 && value.length <= 256 && !value.includes("\0")) || !validNativeDraft({ ...options, editorRevision: "initial", bytes: new Uint8Array() })) throw new Error("invalid_native_session")
  const idleMs = options.idleMs ?? 300, timeoutMs = options.timeoutMs ?? 10_000
  if (!Number.isFinite(idleMs) || idleMs < 1 || idleMs > 5000 || !Number.isFinite(timeoutMs) || timeoutMs < 1 || timeoutMs > 60_000) throw new Error("invalid_save_deadline")
  let active = true, writable = true, dirty = false, edits = 0, baseVersion = options.baseVersion
  let blocked: string | undefined, latest: Prepared | undefined, unconfirmed: Prepared | undefined
  let confirmed: { nativeState: string; contentHash: string; version: string } | undefined
  let pending: { abort: AbortController; done: Promise<void> } | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let preparations = Promise.resolve(), draftWrites = Promise.resolve()
  let lastPersistedSequence = 0, manualCount = 0
  let preparingCount = 0
  let nextPreparation = 0, lastFailedPreparation = 0, nextAutosave = 0
  let writeRetirement: Promise<boolean> | undefined
  const lifetime = new AbortController()
  const emit = (phase: NativeSaveState["phase"], reason?: string) => {
    if (!active && phase !== "stopped") return
    try { options.onState({ phase, dirty, ...(reason ? { reason } : {}) }) } catch { /* UI observers cannot alter persistence or privacy fences. */ }
  }
  const stop = () => {
    if (!active) return
    active = false; clearTimeout(timer); lifetime.abort(); pending?.abort.abort()
    latest = undefined; unconfirmed = undefined; confirmed = undefined
    emit("stopped")
  }
  const unregister = options.journal.onRetire(stop)
  emit("idle")
  function schedule() {
    clearTimeout(timer)
    const token = ++nextAutosave
    if (active) timer = setTimeout(() => { if (token === nextAutosave) void save(undefined, token) }, idleMs)
  }
  function scheduleIfNeeded() {
    if (active && writable && dirty && !blocked && !unconfirmed && !pending && preparingCount === 0 && manualCount === 0) schedule()
  }
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
    const receipt: unknown = raw && typeof raw === "object" ? { ...raw } : raw
    if (!validReceipt(receipt, attempt)) { unconfirmed = attempt; emit("uncertain", "invalid_receipt"); return }
    if (!active || !writable) return
    baseVersion = receipt.version
    unconfirmed = undefined
    try { await options.journal.confirm(attempt.draft) } catch { blocked = "storage"; emit("draft", "storage"); return }
    if (!active || !writable) return
    confirmed = attempt.nativeState === undefined ? undefined : { nativeState: attempt.nativeState, contentHash: attempt.draft.contentHash, version: receipt.version }
    try { void Promise.resolve(options.onReceipt?.(receipt)).catch(() => {}) } catch { /* Receipt observers cannot change persistence. */ }
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
    let complete!: () => void
    const operation = { abort: new AbortController(), done: new Promise<void>(resolve => { complete = resolve }) }; pending = operation
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
      complete()
      scheduleIfNeeded()
    }
  }
  // Preserve the newest durable recovery row when an older queued manual
  // snapshot rebases. Test the sequence inside this queue, after prior writes.
  function persistDraft(draft: NativeDraft, sequence: number): Promise<void> {
    const writing = draftWrites.then(async () => {
      if (!active || sequence < lastPersistedSequence) return
      await options.journal.persist(draft)
      lastPersistedSequence = sequence
    })
    draftWrites = writing.catch(() => {})
    return writing
  }
  function save(requestId?: string, autosaveToken?: number): Promise<void> {
    const manual = requestId !== undefined
    if (manual && (typeof requestId !== "string" || !requestId || requestId.length > 128 || requestId.includes("\0"))) return Promise.reject(new Error("invalid_native_save_ticket"))
    if (manual && manualCount >= 4) return Promise.reject(new Error("native_save_backpressure"))
    if (manual) manualCount++
    clearTimeout(timer)
    preparingCount++
    const sequence = ++nextPreparation
    const preparing = preparations.then(async (): Promise<Prepared | undefined> => {
      // A newer idle request supersedes queued autosaves, but never an active
      // snapshot or an explicit manual ticket. Capture locally even while the
      // transport is pending/uncertain; those fences apply only to sending.
      if (!active || autosaveToken !== undefined && autosaveToken !== nextAutosave) return
      const epoch = edits
      dirty = true
      let stage = "engine"
      try {
        const snapshot = await options.engine.snapshot(lifetime.signal, requestId)
        if (!active) return
        const draft: NativeDraft = { applicationId: options.applicationId, enginePin: options.enginePin, sessionId: options.sessionId, generation: options.generation, baseVersion, editorRevision: snapshot.editorRevision, bytes: snapshot.bytes.slice() }
        if (!validNativeDraft(draft)) throw new Error("invalid_native_snapshot")
        stage = "storage"
        await persistDraft(draft, sequence)
        if (!active) return
        stage = "engine"
        await options.engine.validate({ ...draft, bytes: draft.bytes.slice() }, lifetime.signal)
        if (!active) return
        const digest = await crypto.subtle.digest("SHA-256", new Uint8Array(draft.bytes))
        if (!active) return
        const contentHash = `sha256:${Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")}`
        if (latest && draft.editorRevision === latest.draft.editorRevision && contentHash !== latest.draft.contentHash) throw new Error("unstable_editor_revision")
        const nativeState = options.applicationId === "wordcraft" && typeof snapshot.nativeState === "string" && snapshot.nativeState.length > 0 && snapshot.nativeState.length <= 256 && !snapshot.nativeState.includes("\0") ? snapshot.nativeState : undefined
        latest = { nativeState, draft: { ...draft, workspaceId: options.workspaceId, pageId: options.pageId, contentHash }, epoch, sequence }
        return latest
      } catch {
        lastFailedPreparation = sequence
        if (active) emit(writable ? "draft" : "read-only", stage)
      }
    })
    const ready = preparing.finally(() => { preparingCount-- })
    preparations = ready.then(() => {}, () => {})
    const sending = ready.then(async attempt => {
      if (!attempt || !active) { if (manual) throw new Error(active ? "native_snapshot_failed" : "native_session_retired"); return }
      // A manual ticket owns this exact prepared snapshot. Wait for the bounded
      // transport, but never replay a queued ticket after an unknown ACK.
      const unavailable = () => !active ? "native_session_retired" : !writable ? "write_revoked" : blocked ?? (unconfirmed ? "unconfirmed" : pending ? "pending" : undefined)
      for (;;) {
        if (manual) while (pending && active && writable && !blocked && !unconfirmed) await pending.done
        if (unavailable()) {
          emit(!writable ? "read-only" : unconfirmed ? "uncertain" : "draft", blocked)
          if (manual) throw new Error(unavailable())
          return
        }
        if (attempt.draft.baseVersion === baseVersion) break
        attempt = { ...attempt, draft: { ...attempt.draft, baseVersion } }
        try { await persistDraft(attempt.draft, attempt.sequence) } catch { emit("draft", "storage"); if (manual) throw new Error("storage"); return }
        if (latest?.sequence === attempt.sequence) latest = attempt
        // Another manual operation may have started while this journal write
        // awaited storage. Repeat both the transport fence and base sampling.
      }
      // A timer can capture a second RPC for the exact state already committed
      // by the server. Retire only its local recovery row; the RPC-bound receipt
      // and native ACK remain exclusive to the original request. Explicit saves
      // (including transfer flushes without a native ticket) always transmit.
      if (autosaveToken !== undefined && attempt.nativeState !== undefined && confirmed?.nativeState === attempt.nativeState && confirmed.contentHash === attempt.draft.contentHash && confirmed.version === baseVersion) {
        const baseline = confirmed
        try { await options.journal.confirm(attempt.draft) } catch {
          if (!unavailable() && confirmed === baseline && baseVersion === baseline.version) { blocked = "storage"; emit("draft", "storage") }
          return
        }
        // Another save can finish, fail, or retire authority during storage.
        // Its state and receipt baseline always supersede this older idle RPC.
        if (unavailable() || confirmed !== baseline || baseVersion !== baseline.version) return
        dirty = preparingCount > 0 || lastFailedPreparation > (latest?.sequence ?? 0) || edits !== attempt.epoch || latest?.draft.contentHash !== attempt.draft.contentHash || latest?.nativeState !== attempt.nativeState
        emit(dirty ? "dirty" : "saved")
        scheduleIfNeeded()
        return
      }
      await transmit(attempt, false)
      if (manual && unavailable()) throw new Error(unavailable())
    })
    return manual ? sending.finally(() => { manualCount--; scheduleIfNeeded() }) : sending
  }
  function revokeWrite(): Promise<boolean> {
    if (!active) return Promise.resolve(false)
    if (writeRetirement) return writeRetirement
    // Revoke sending synchronously. The host must first suspend input, then
    // await this fresh durable capture before closing the editor or journal.
    // A failed capture retains the native frame; it never grants a handoff.
    writable = false; blocked = "write_revoked"; clearTimeout(timer); pending?.abort.abort()
    const sequence = nextPreparation + 1
    writeRetirement = new Promise<boolean>(resolve => {
      let finished = false
      const finish = (captured: boolean) => {
        if (finished) return
        finished = true; clearTimeout(deadline); lifetime.signal.removeEventListener("abort", abort)
        resolve(captured)
      }
      const abort = () => finish(false)
      const deadline = setTimeout(() => finish(false), timeoutMs)
      lifetime.signal.addEventListener("abort", abort, { once: true })
      if (lifetime.signal.aborted) { abort(); return }
      void save().then(() => finish(active && lastPersistedSequence >= sequence), () => finish(false))
    })
    emit("read-only", "write_revoked")
    return writeRetirement
  }
  function revokeRead() { stop(); unregister(); return options.journal.revokeRead() }
  return {
    edited() { if (!active) return; edits++; dirty = true; emit(writable ? "dirty" : "read-only"); schedule() },
    save: (requestId?: string) => save(requestId),
    async reconcile() { if (active && writable && unconfirmed && !pending && options.transport.reconcile) await transmit(unconfirmed, true) },
    revokeWrite,
    revokeRead,
    close() { stop(); unregister(); return options.journal.close() },
  }
}
