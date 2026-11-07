import { type NativeDraft, type NativeDraftJournal, validNativeDraft, validNativeVersion } from "./native-draft"

/** `attemptId` is a fresh nonce per transmitted save; reconcile sends the same
 * one so the server can retire an attempt that did not commit (#4899). */
export interface NativeSaveRequest extends NativeDraft { workspaceId: string; pageId: string; contentHash: string; attemptId?: string }
export interface NativeSaveReceipt extends Omit<NativeSaveRequest, "bytes" | "attemptId"> {
  version: string
  durability: "node"
  issuedBy: "crewship"
  phase: "committed"
  candidateHash: string
  /** No new version: `version` is `baseVersion`, which already holds this capture (#5075). */
  unchanged?: true
}
/** `absent` (reconcile only): the server retired the attempt; it did not commit and never will. */
export type NativeSaveFailure = "conflict" | "quota" | "write_revoked" | "read_revoked" | "absent"
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
  /** The bytes the server confirmed as `baseVersion`, exactly as loaded. Seeds
   * the unchanged-content baseline for a freshly opened editor. Never a local
   * draft. Word's per-RPC nativeState is unknown at load, so it is not compared.
   * `mountedState` is the engine's nativeState right after it was bootstrapped
   * from exactly these bytes. An export captured before the first edit whose
   * nativeState still equals it is the engine's rendition of this version
   * (Word normalizes it) and is never saved as a new version, so accepting a
   * proposal or handing the editor over stays on `baseVersion` (#4699). The
   * engine's own revision is compared, so a `changed` event that arrives late
   * cannot hide an edit. Without it a differing export is saved, as for a
   * restored draft; an engine without nativeState (Sheet) is unaffected. */
  loaded?: { bytes: Uint8Array; mountedState?: string }
  generation: string
  sessionId: string
  journal: NativeDraftJournal
  engine: {
    snapshot(signal: AbortSignal, requestId?: string): Promise<{ bytes: Uint8Array; editorRevision: string; nativeState?: string; revision?: string }>
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
  /** Waits before asking the server about an unconfirmed save; the last one repeats. */
  reconcileBackoffMs?: number[]
}
interface Prepared { draft: NativeSaveRequest; epoch: number; sequence: number; nativeState?: string }

const RECONCILE_BACKOFF_MS = [2_000, 10_000, 30_000, 60_000]

/** One immutable editor/lease lifecycle. A new generation needs a new editor
 * loaded from its confirmed version, never an automatic replay of recovery. */
export function createSingleEditorSave(options: SingleEditorSaveOptions) {
  options = { ...options }
  if (![options.workspaceId, options.pageId].every(value => typeof value === "string" && value.length > 0 && value.length <= 256 && !value.includes("\0")) || !validNativeDraft({ ...options, editorRevision: "initial", bytes: new Uint8Array() })) throw new Error("invalid_native_session")
  const idleMs = options.idleMs ?? 300, timeoutMs = options.timeoutMs ?? 10_000
  if (!Number.isFinite(idleMs) || idleMs < 1 || idleMs > 5000 || !Number.isFinite(timeoutMs) || timeoutMs < 1 || timeoutMs > 60_000) throw new Error("invalid_save_deadline")
  let active = true, writable = true, dirty = false, edits = 0, baseVersion = options.baseVersion
  // While access is being confirmed, input cannot be seen by whoever types it, so it
  // is not work to keep yet. A hold asks for one capture of what came before, sends
  // nothing, and a snapshot reaches the row only if it is proven older than held input.
  let held = false, heldAt = 0, preReturn: Promise<unknown> = Promise.resolve()
  // The engine's own order, as arrival does not show it: a Word answer overtakes nothing,
  // a Sheet export is answered before its change notice and can raise the revision itself.
  let lastRevision: string | undefined, heldRevision: string | undefined, firstHeldRevision: string | undefined
  // Every edit up to this count is in a durable row, or was clean at a hold.
  let keptEdits = 0
  let blocked: string | undefined, latest: Prepared | undefined, unconfirmed: Prepared | undefined
  let confirmed: { nativeState: string | undefined; contentHash: string; version: string; seeded?: boolean; mountedState?: string } | undefined
  let pending: { attempt: Prepared; abort: AbortController; done: Promise<void> } | undefined
  let timer: ReturnType<typeof setTimeout> | undefined
  let reconcileTimer: ReturnType<typeof setTimeout> | undefined, reconcileAsks = 0
  let preparations = Promise.resolve(), draftWrites = Promise.resolve()
  let lastPersistedSequence = 0, manualCount = 0
  // Sequences whose journal write has not settled, including ones past the deadline.
  const unsettledWrites = new Set<number>()
  let preparingCount = 0
  let nextPreparation = 0, lastFailedPreparation = 0, nextAutosave = 0
  let writeRetirement: Promise<boolean> | undefined
  // A validated receipt whose local row retirement failed. It is delivered to
  // the receipt observer (the native ACK) once that row is retired, or
  // superseded by a newer receipt; it is never sent again (#4852).
  let owedReceipt: NativeSaveReceipt | undefined
  // After a retired attempt the confirmed baseline is no longer proof of the
  // server head (another holder may have committed meanwhile). Until a new
  // receipt, every capture is sent with the base-version fence (#4900 review).
  let baselineStale = false
  // One automatic retry per failed row retirement (#4852); reset by any
  // retirement that succeeds. A retry that fails again stays visible.
  let retirementRetried = false
  const lifetime = new AbortController()
  let lastState: NativeSaveState = { phase: "idle", dirty: false }
  const stateWaiters = new Set<() => void>()
  const notifyStateWaiters = () => { for (const check of [...stateWaiters]) check() }
  const emit = (phase: NativeSaveState["phase"], reason?: string) => {
    if (!active && phase !== "stopped") return
    lastState = { phase, dirty, ...(reason ? { reason } : {}) }
    try { options.onState({ ...lastState }) } catch { /* UI observers cannot alter persistence or privacy fences. */ }
    notifyStateWaiters()
  }
  const stop = () => {
    if (!active) return
    active = false; clearTimeout(timer); clearTimeout(reconcileTimer); lifetime.abort(); pending?.abort.abort()
    latest = undefined; unconfirmed = undefined; confirmed = undefined; owedReceipt = undefined
    emit("stopped")
  }
  const unregister = options.journal.onRetire(stop)
  // Seed the baseline from the confirmed server bytes (same SHA-256 as a save).
  // A later receipt replaces it; a normalized or restored-draft capture hashes
  // differently, so it simply never matches and is saved.
  const seedVersion = options.baseVersion
  const seeding: Promise<void> = options.loaded
    ? crypto.subtle.digest("SHA-256", new Uint8Array(options.loaded.bytes)).then(digest => {
      if (active && !confirmed && !baselineStale) confirmed = { nativeState: undefined, seeded: true, mountedState: options.loaded?.mountedState, version: seedVersion, contentHash: `sha256:${Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, "0")).join("")}` }
    }, () => {})
    : Promise.resolve()
  emit("idle")
  const heldInput = () => held && edits > heldAt
  const sheetOrder = (revision: string) => BigInt(revision.slice(revision.lastIndexOf(":") + 1))
  // Holds everything before this hold, not merely nothing of what came during it.
  const hasWorkBeforeHold = (revision: string | undefined) => revision !== undefined && heldRevision !== undefined && (revision === heldRevision
    || options.applicationId === "gridcraft" && sheetOrder(revision) >= sheetOrder(heldRevision))
  const beforeHeldInput = (revision: string | undefined) => !heldInput() || revision !== undefined && (revision === heldRevision
    || options.applicationId === "gridcraft" && firstHeldRevision !== undefined && sheetOrder(revision) < sheetOrder(firstHeldRevision))
  function schedule() {
    clearTimeout(timer)
    const token = ++nextAutosave
    if (active && !held) timer = setTimeout(() => { if (token === nextAutosave) void save(undefined, token) }, idleMs)
  }
  // A local storage fence is retried: the next capture either persists (which
  // lifts it) or fails visibly again. Server and authority fences never are.
  function scheduleIfNeeded() {
    if (active && writable && dirty && (!blocked || blocked === "storage") && !unconfirmed && !pending && preparingCount === 0 && manualCount === 0) schedule()
  }
  // Only a save may answer `unchanged`: reconcile proves the exact successor.
  function validReceipt(raw: unknown, attempt: Prepared, unchangedAllowed = false): raw is NativeSaveReceipt {
    if (!raw || typeof raw !== "object") return false
    const r = raw as Record<string, unknown>, d = attempt.draft
    const unchanged = unchangedAllowed && r.unchanged === true
    if ("unchanged" in r && !unchanged) return false
    return validNativeVersion(r.version) && BigInt(r.version) === BigInt(d.baseVersion) + (unchanged ? 0n : 1n)
      && r.durability === "node" && r.issuedBy === "crewship" && r.phase === "committed" && r.candidateHash === d.contentHash
      && r.workspaceId === d.workspaceId && r.pageId === d.pageId && r.applicationId === d.applicationId && r.enginePin === d.enginePin && r.baseVersion === d.baseVersion
      && r.sessionId === d.sessionId && r.generation === d.generation && r.editorRevision === d.editorRevision && r.contentHash === d.contentHash
  }
  async function settle(raw: unknown, attempt: Prepared, unchangedAllowed: boolean) {
    if (!active || !writable) return
    const receipt: unknown = raw && typeof raw === "object" ? { ...raw } : raw
    if (!validReceipt(receipt, attempt, unchangedAllowed)) { unconfirmed = attempt; emit("uncertain", "invalid_receipt"); return }
    if (!active || !writable) return
    baseVersion = receipt.version
    unconfirmed = undefined
    try { await confirmRow(attempt.draft) } catch {
      // The server committed exactly these bytes; only the local recovery row
      // could not be retired (e.g. an IDB deadline that fired late after a
      // main-thread stall, or a journal that never answered). Keep the row,
      // report it, and record the committed baseline so the retry retires the
      // row instead of sending a duplicate version. The fence lifts on the
      // next durable capture (#4852). The transport's own `finally` schedules
      // that one retry.
      if (!active || !writable) return
      confirmed = { nativeState: attempt.nativeState, contentHash: attempt.draft.contentHash, version: receipt.version }; baselineStale = false
      owedReceipt = receipt
      retirementRetried = true
      unretired(attempt); return
    }
    if (!active || !writable) return
    confirmed = { nativeState: attempt.nativeState, contentHash: attempt.draft.contentHash, version: receipt.version }; baselineStale = false
    owedReceipt = undefined; retirementRetried = false
    deliver(receipt)
    if (!active || !writable) return
    dirty = newerThan(attempt) || latest?.draft.editorRevision !== attempt.draft.editorRevision
    emit(dirty ? "dirty" : "saved")
  }
  // The request's identity without what only a transmission carries.
  function unsent({ bytes: _bytes, attemptId: _attemptId, ...identity }: NativeSaveRequest) { return identity }
  function attemptNonce(): string {
    if (typeof crypto.randomUUID === "function") return crypto.randomUUID()
    return Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("")
  }
  function deliver(receipt: NativeSaveReceipt) {
    try { void Promise.resolve(options.onReceipt?.(receipt)).catch(() => {}) } catch { /* Receipt observers cannot change persistence. */ }
  }
  // A journal step that never answers must not hold the transport (`pending`),
  // a manual ticket, a flush or a write retirement forever (#4852). Past the
  // deadline it counts as failed storage. The step itself keeps running in the
  // journal's own queue: a late persist is not claimed as durable here, and a
  // late confirm only ever retires this mount's row for exactly that draft.
  function journalStep(step: () => Promise<void>): Promise<void> {
    return new Promise<void>((resolve, reject) => {
      const deadline = setTimeout(() => reject(new Error("journal_timeout")), timeoutMs)
      Promise.resolve().then(step).then(resolve, reject).finally(() => clearTimeout(deadline))
    })
  }
  const confirmRow = (draft: NativeDraft) => journalStep(() => options.journal.confirm(draft))
  // Whether nothing newer than `attempt` was captured or edited since it was prepared.
  const newerThan = (attempt: Prepared) => preparingCount > 0 || lastFailedPreparation > (latest?.sequence ?? 0) || edits !== attempt.epoch || latest?.draft.contentHash !== attempt.draft.contentHash
  // The server holds these bytes but their local recovery row could not be
  // retired. Say so instead of "unsaved" when nothing newer exists; newer
  // edits keep the plain storage reason. Either way the editor stays dirty
  // (leave guard, no native ACK yet) and one retry retires the row (#4870).
  function unretired(attempt: Prepared) {
    blocked = "storage"
    emit("draft", newerThan(attempt) ? "storage" : "unretired")
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
  // A page frozen past the deadline loses a save it may well have made, and a
  // query cannot hurt: ask until the outcome is known. Nothing is resent here.
  function scheduleReconcile() {
    if (!unconfirmed) { clearTimeout(reconcileTimer); reconcileTimer = undefined; reconcileAsks = 0; return }
    if (reconcileTimer !== undefined || !active || !writable || !options.transport.reconcile) return
    const backoff = options.reconcileBackoffMs ?? RECONCILE_BACKOFF_MS
    reconcileTimer = setTimeout(() => {
      reconcileTimer = undefined
      reconcileAsks++
      if (active && writable && unconfirmed && !pending) void transmit(unconfirmed, true)
      else scheduleReconcile()
    }, backoff[Math.min(reconcileAsks, backoff.length - 1)])
  }
  async function transmit(attempt: Prepared, reconcile: boolean) {
    if (!active || !writable || pending) return
    if (!reconcile) attempt = { ...attempt, draft: { ...attempt.draft, attemptId: attemptNonce() } }
    let complete!: () => void
    const operation = { attempt, abort: new AbortController(), done: new Promise<void>(resolve => { complete = resolve }) }; pending = operation
    emit("saving")
    try {
      const result = await bounded(signal => reconcile ? options.transport.reconcile!(attempt.draft, signal) : options.transport.save(attempt.draft, signal), operation.abort)
      if (!active || !writable || pending !== operation) return
      await settle(result, attempt, !reconcile)
    } catch (error) {
      if (!active || !writable || pending !== operation) return
      if (reconcile && error instanceof NativeSaveError && error.reason === "absent" && unconfirmed === attempt) {
        // The server retired this exact attempt under its writer fence: it did
        // not commit and never will, even if its request is still pending (e.g.
        // the browser went offline before it left). That ends the uncertainty
        // without sending it again. The pipeline resumes with a fresh capture
        // at the same base, never judged "unchanged" against the old baseline,
        // so the server's base fence decides: saved, or a visible conflict
        // with the draft kept (#4899). A plain 409 stays a conflict.
        unconfirmed = undefined; confirmed = undefined; baselineStale = true; dirty = true
        emit("dirty", "not_committed")
      } else if (reconcile && error instanceof NativeSaveError && error.reason === "conflict") {
        // Reconcile proved nothing and recorded nothing (lease lost, retirement
        // limit, or no attempt id): the outcome stays unknown. Keep the attempt
        // unconfirmed, send nothing, and let a later check ask again.
        unconfirmed = attempt; emit("uncertain", "unconfirmed")
      } else if (error instanceof NativeSaveError) {
        blocked = error.reason
        if (error.reason === "read_revoked") { await revokeRead(); return }
        if (error.reason === "write_revoked") { revokeWrite(); return }
        emit("draft", error.reason)
      } else { unconfirmed = attempt; emit("uncertain", "unconfirmed") }
    } finally {
      if (pending === operation) pending = undefined
      complete()
      scheduleReconcile()
      scheduleIfNeeded()
      notifyStateWaiters()
    }
  }
  // Preserve the newest durable recovery row when an older queued manual
  // snapshot rebases. Test the sequence inside this queue, after prior writes.
  // A write that missed the controller deadline still runs in the journal and
  // may land later: when it does, it advances the fence like any durable
  // write, and while it is unsettled no older snapshot may be written behind
  // it (the journal is serial, so that older write would replace the newer
  // row and a later confirm would retire it). Such an older write fails as
  // storage instead; it never overwrites a newer draft (#4887 review).
  function persistDraft(draft: NativeDraft, sequence: number): Promise<void> {
    const writing = draftWrites.then(async () => {
      if (!active || sequence < lastPersistedSequence) return
      if ([...unsettledWrites].some(newer => newer > sequence)) throw new Error("newer_draft_unsettled")
      const write = Promise.resolve().then(() => options.journal.persist(draft))
      unsettledWrites.add(sequence)
      void write.then(() => {
        if (sequence > lastPersistedSequence) lastPersistedSequence = sequence
        // Storage answered durably again: an earlier failed row retirement must
        // not stop every later save while the lease is held (#4852).
        if (blocked === "storage") blocked = undefined
      }, () => {}).finally(() => { unsettledWrites.delete(sequence) })
      await journalStep(() => write)
      if (sequence > lastPersistedSequence) lastPersistedSequence = sequence
      if (blocked === "storage") blocked = undefined
    })
    draftWrites = writing.catch(() => {})
    return writing
  }
  function save(requestId?: string, autosaveToken?: number, hostFlush = false, localOnly = false): Promise<void> {
    if (held) return Promise.reject(new Error("native_save_held"))
    const manual = requestId !== undefined || hostFlush
    // A press of Ctrl+S: Word's carries a ticket, Sheet's has none. Revocation's
    // own capture also arrives here, but it can no longer reach the transport.
    const explicit = autosaveToken === undefined && !hostFlush
    if (requestId !== undefined && (typeof requestId !== "string" || !requestId || requestId.length > 128 || requestId.includes("\0"))) return Promise.reject(new Error("invalid_native_save_ticket"))
    if (manual && manualCount >= 4) return Promise.reject(new Error("native_save_backpressure"))
    if (manual) manualCount++
    clearTimeout(timer)
    preparingCount++
    const sequence = ++nextPreparation
    const preparing = preparations.then(async (): Promise<Prepared | undefined> => {
      // A newer idle request supersedes queued autosaves, but never an active
      // snapshot or an explicit manual ticket. Capture locally even while the
      // transport is pending/uncertain; those fences apply only to sending.
      if (!active || heldInput() || autosaveToken !== undefined && autosaveToken !== nextAutosave) return
      const epoch = edits
      dirty = true
      let stage = "engine"
      try {
        const snapshot = await options.engine.snapshot(lifetime.signal, requestId)
        if (!active || !beforeHeldInput(snapshot.revision)) return
        const proven = heldInput() && hasWorkBeforeHold(snapshot.revision) ? heldAt : epoch
        const draft: NativeDraft = { applicationId: options.applicationId, enginePin: options.enginePin, sessionId: options.sessionId, generation: options.generation, baseVersion, editorRevision: snapshot.editorRevision, bytes: snapshot.bytes.slice() }
        if (!validNativeDraft(draft)) throw new Error("invalid_native_snapshot")
        stage = "storage"
        await persistDraft(draft, sequence)
        keptEdits = Math.max(keptEdits, proven)
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
      if (localOnly) { scheduleIfNeeded(); return }
      if (!attempt || !active) { if (manual) throw new Error(active ? "native_snapshot_failed" : "native_session_retired"); return }
      // A manual ticket owns this exact prepared snapshot. Wait for the bounded
      // transport, but never replay a queued ticket after an unknown ACK.
      const unavailable = () => !active ? "native_session_retired" : !writable ? "write_revoked" : held ? "native_save_held" : blocked ?? (unconfirmed ? "unconfirmed" : pending ? "pending" : undefined)
      let lookedUp = false
      for (;;) {
        if (manual) while (pending && active && writable && !blocked && !unconfirmed) await pending.done
        // Ctrl+S during an unknown outcome first asks about the original attempt,
        // once, and resends nothing. A proven commit becomes the baseline judged
        // below; a retired attempt makes this capture a fresh save under the base
        // fence; anything else stays unconfirmed and fails below, unsent (#5075).
        if (explicit && !lookedUp && active && writable && !held && !blocked && unconfirmed && options.transport.reconcile) {
          lookedUp = true
          if (pending) await pending.done
          else await transmit(unconfirmed, true)
          continue
        }
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
      // A timer, a reconnect, a host release/transfer flush or Ctrl+S can capture
      // the exact bytes already committed at the current base (any application,
      // not only Word). Never create a version from them: retire the local
      // recovery row. An idle capture stops there, as the receipt and native ACK
      // belong to the original request. Ctrl+S gets its own ACK for this capture,
      // which also needs the native state of the baseline: otherwise the server
      // decides (#5075). An untouched export of an engine mounted from the seeded
      // bytes is that same version.
      await seeding
      const baseline = confirmed
      const unchanged = baseline && (baseline.contentHash === attempt.draft.contentHash || untouched(attempt))
      const sameState = baseline && (baseline.seeded
        ? !explicit || baseline.mountedState === undefined || baseline.mountedState === attempt.nativeState
        : baseline.nativeState === attempt.nativeState)
      if (baseline && unchanged && sameState && baseline.version === baseVersion) {
        const confirmation: NativeSaveReceipt | undefined = explicit ? { ...unsent(attempt.draft), baseVersion: baseline.version, version: baseline.version, durability: "node", issuedBy: "crewship", phase: "committed", candidateHash: attempt.draft.contentHash, unchanged: true } : undefined
        try { await confirmRow(attempt.draft) } catch {
          // The bytes are committed; only their row stayed. Retry once by
          // itself, as after a failed retirement in settle, instead of waiting
          // for the next edit, Ctrl+S or flush (#4852). A press is owed its ACK
          // from that retry and fails until then.
          if (!unavailable() && confirmed === baseline && baseVersion === baseline.version) {
            if (confirmation) owedReceipt = confirmation
            unretired(attempt)
            if (!retirementRetried) { retirementRetried = true; scheduleIfNeeded() }
          }
          if (requestId !== undefined) throw new Error("storage")
          return
        }
        retirementRetried = false
        // Another save can finish, fail, or retire authority during storage.
        // Its state and receipt baseline always supersede this older capture.
        if (unavailable() || confirmed !== baseline || baseVersion !== baseline.version) {
          if (requestId !== undefined) throw new Error(unavailable() ?? "superseded")
          return
        }
        // Storage recovered after a committed save whose row could not be
        // retired: its own validated receipt now completes the native ACK.
        const owed = owedReceipt
        if (owed && owed.version === baseline.version && (owed.unchanged || owed.contentHash === baseline.contentHash)) { owedReceipt = undefined; deliver(owed) }
        if (confirmation) deliver(confirmation)
        dirty = newerThan(attempt) || latest?.nativeState !== attempt.nativeState
        emit(dirty ? "dirty" : "saved")
        scheduleIfNeeded()
        return
      }
      await transmit(attempt, false)
      if (manual && unavailable()) throw new Error(unavailable())
    })
    return manual ? sending.finally(() => { manualCount--; scheduleIfNeeded(); notifyStateWaiters() }) : sending
  }
  function waitForCleanSaved(): Promise<void> {
    return new Promise((resolve, reject) => {
      let finished = false
      const finish = (error?: Error) => {
        if (finished) return
        finished = true; clearTimeout(deadline); stateWaiters.delete(check)
        lifetime.signal.removeEventListener("abort", check)
        if (error) reject(error); else resolve()
      }
      const check = () => {
        if (!active || lifetime.signal.aborted) { finish(new Error("native_session_retired")); return }
        if (!writable || blocked || unconfirmed || lastState.phase === "uncertain" || lastState.phase === "read-only" || lastState.phase === "stopped" || lastState.phase === "draft" && lastState.reason) {
          finish(new Error(!writable ? "write_revoked" : blocked ?? (unconfirmed ? "unconfirmed" : lastState.reason ?? "native_save_unconfirmed"))); return
        }
        if (lastState.phase === "saved" && !lastState.dirty && !dirty && !pending && preparingCount === 0 && manualCount === 0) finish()
      }
      const deadline = setTimeout(() => finish(new Error("native_flush_timeout")), timeoutMs)
      stateWaiters.add(check); lifetime.signal.addEventListener("abort", check, { once: true })
      check()
    })
  }
  // Still the seeded server version as the engine rendered it on mount: no
  // receipt since, no edit before the capture and the engine's own state
  // unchanged since bootstrap.
  function untouched(attempt: Prepared): boolean {
    // The trusted mount identity is available before its background hash.
    // Write retirement must not leave an untouched recovery row merely because
    // that hash has not settled. A receipt or proven-absent baseline excludes it.
    return options.loaded?.mountedState !== undefined && !baselineStale && baseVersion === seedVersion
      && attempt.epoch === 0 && edits === 0 && attempt.nativeState === options.loaded.mountedState
      && attempt.draft.baseVersion === seedVersion
  }
  function revokeWrite(): Promise<boolean> {
    if (!active) return Promise.resolve(false)
    if (writeRetirement) return writeRetirement
    // Revoke sending synchronously. The host must first suspend input, then
    // await this fresh durable capture before closing the editor or journal.
    // A failed capture retains the native frame; it never grants a handoff.
    // A save already sent may have committed; its reply is abandoned here, so
    // keep it as the unconfirmed attempt for proveLost to reconcile.
    if (pending && !unconfirmed) unconfirmed = pending.attempt
    writable = false; blocked = "write_revoked"; clearTimeout(timer); clearTimeout(reconcileTimer); pending?.abort.abort()
    const sequence = nextPreparation + 1, heldEdits = held ? heldAt : undefined
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
      // Lost during a hold, the capture from before it is the draft; the buffer is not.
      void (heldEdits !== undefined ? preReturn : save()).then(async () => {
        const captured = active && (heldEdits !== undefined ? keptEdits >= heldEdits : lastPersistedSequence >= sequence)
        if (captured && !finished) { clearTimeout(deadline); await proveLost(); await retireUntouched(); await retireOwed() }
        finish(captured)
      }, () => finish(false))
    })
    emit("read-only", "write_revoked")
    return writeRetirement
  }
  // A reply lost while the lease expired is still provable for its exact
  // successor. Confirm only that attempt and an identical fresh capture; any
  // other capture stays a draft to compare. Never sends the snapshot again.
  async function proveLost() {
    const attempt = unconfirmed
    if (!attempt || !options.transport.reconcile) return
    try {
      const raw = await bounded(signal => options.transport.reconcile!(attempt.draft, signal), new AbortController())
      const receipt: unknown = raw && typeof raw === "object" ? { ...raw } : raw
      if (!active || unconfirmed !== attempt || !validReceipt(receipt, attempt)) return
      unconfirmed = undefined
      await confirmRow(attempt.draft)
      const capture = latest?.draft
      if (active && capture && capture !== attempt.draft && capture.contentHash === attempt.draft.contentHash) await confirmRow(capture)
    } catch (error) {
      // Read loss purges like a save that reports it; otherwise the draft remains for comparison.
      if (error instanceof NativeSaveError && error.reason === "read_revoked") await revokeRead().catch(() => {})
    }
  }
  // Exactly the bytes of the version the server confirmed to this session (#5089).
  function isConfirmedCopy(attempt: Prepared): boolean {
    return confirmed !== undefined && !confirmed.seeded && !baselineStale && confirmed.version === baseVersion
      && attempt.draft.baseVersion === baseVersion && attempt.draft.contentHash === confirmed.contentHash
  }
  // A final capture that is the server version, untouched since mount or equal
  // to the last confirmed save, must not stay as a "local draft" offered on every
  // later open or hold the leave guard. Unproven, it stays for compare.
  async function retireUntouched() {
    const capture = latest
    if (!active || !capture || unconfirmed || !untouched(capture) && !isConfirmedCopy(capture)) return
    try {
      await confirmRow(capture.draft)
      // Confirmation may race another capture, an edit or privacy retirement.
      // Only this exact, still unchanged final capture can release the guard.
      if (active && latest === capture && !unconfirmed && !newerThan(capture) && (untouched(capture) || isConfirmedCopy(capture))) {
        dirty = false
        emit("read-only", "write_revoked")
      }
    } catch { /* It remains a draft to compare. */ }
  }
  // A final capture with exactly the bytes of a committed save whose row could
  // not be retired is that server version; retire it instead of offering it.
  async function retireOwed() {
    const owed = owedReceipt, capture = latest
    if (!active || !owed || !capture || capture.draft.contentHash !== owed.contentHash) return
    try { await confirmRow(capture.draft); owedReceipt = undefined } catch { /* It remains a draft to compare. */ }
  }
  function revokeRead() { stop(); unregister(); return options.journal.revokeRead() }
  return {
    edited(revision?: string) {
      if (!active) return
      if (held) firstHeldRevision ??= revision
      lastRevision = revision
      edits++; dirty = true; emit(writable ? "dirty" : "read-only"); schedule()
    },
    // A hidden tab takes no keys, so this copy is clean. It is no barrier: a tab can
    // freeze before it finishes, and retirement still asks what reached the journal.
    captureLocal() { return active && writable && dirty && !held ? save(undefined, undefined, false, true).catch(() => {}) : Promise.resolve() },
    hold() {
      if (!writable || held) return
      clearTimeout(timer)
      if (!dirty) keptEdits = edits
      // In this turn, ahead of any key: save() refuses once the hold is on.
      preReturn = keptEdits < edits ? save(undefined, ++nextAutosave).catch(() => {}) : Promise.resolve()
      held = true; heldAt = edits; heldRevision = lastRevision; firstHeldRevision = undefined
    },
    release() {
      const typed = heldInput()
      held = false
      // Local, whatever the transport is doing: the send fences stay where they are.
      if (typed && writable) void save(undefined, ++nextAutosave).catch(() => {})
      else scheduleIfNeeded()
    },
    save: (requestId?: string) => save(requestId),
    // Host transfer flush waits for pending transport without claiming a native Ctrl+S ticket.
    async flush() { await save(undefined, undefined, true); await waitForCleanSaved() },
    // The server version the editor's clean, saved content is confirmed at: the
    // last receipt, or the loaded version when nothing has changed since opening.
    confirmedVersion(): string | undefined {
      if (!active || !writable || blocked || unconfirmed || pending || dirty || lastState.phase !== "saved") return undefined
      return confirmed?.version === baseVersion ? confirmed.version : undefined
    },
    async reconcile() { if (active && writable && unconfirmed && !pending && options.transport.reconcile) await transmit(unconfirmed, true) },
    revokeWrite,
    revokeRead,
    close() { stop(); unregister(); return options.journal.close() },
  }
}
