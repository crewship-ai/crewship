export type WordCraftConnectionPhase = "connecting" | "connected" | "disconnected"
export type WordCraftServerSaveState = "saved" | "saving" | "unknown"
export interface WordCraftConnectionState {
  phase: WordCraftConnectionPhase
  serverSaveState: WordCraftServerSaveState
  revision: string | null
  hasUnconfirmedDraft: boolean
}

/** Input must already be validated against the current document/session by the
 * host. confirmedDraftVersion comes from server reconciliation of op_id, never
 * from an optimistic edit, a heartbeat or a transport-open callback. */
export type WordCraftServerObservation =
  | { kind: "heartbeat" }
  | { kind: "snapshot" | "receipt"; revision: string; saveState: WordCraftServerSaveState; confirmedDraftVersion?: number }

const counter = (value: string) => typeof value === "string" && /^(0|[1-9][0-9]{0,19})$/.test(value) && BigInt(value) <= 18_446_744_073_709_551_615n

/** Presentation-only monitor. It has no transport, replay queue or editor data.
 * The owning editor stays mounted and retains its draft and IME composition. */
export class WordCraftConnectionMonitor {
  private state: WordCraftConnectionState = { phase: "connecting", serverSaveState: "unknown", revision: null, hasUnconfirmedDraft: false }
  private readonly listeners = new Set<() => void>()
  private timer: ReturnType<typeof setTimeout> | undefined
  private connection = 0
  private draftVersion = 0
  private confirmedDraftVersion = 0
  private disposed = false

  constructor(private readonly silenceMs: number) {
    if (!Number.isFinite(silenceMs) || silenceMs <= 0 || silenceMs > 2_147_483_647) throw new Error("invalid_server_silence_limit")
  }

  getSnapshot = (): Readonly<WordCraftConnectionState> => this.state
  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener)
    return () => { this.listeners.delete(listener) }
  }

  /** Call for a new transport, but never to restore a saved label. Retain the
   * returned epoch on every callback so the old socket cannot revive this one. */
  beginConnection(): number {
    if (this.disposed) throw new Error("connection_monitor_disposed")
    this.connection++
    this.update({ phase: "connecting", serverSaveState: "unknown" })
    this.armDeadline()
    return this.connection
  }

  /** Record local input without reading, replacing or resending any of it.
   * Associate this version with the original op_id in the existing host. */
  retainDraft(): number {
    if (this.disposed) throw new Error("connection_monitor_disposed")
    if (this.draftVersion === Number.MAX_SAFE_INTEGER) throw new Error("draft_version_exhausted")
    this.draftVersion++
    this.update({ hasUnconfirmedDraft: true })
    return this.draftVersion
  }

  /** A close/error is immediate; silence uses the same state without closing
   * the editor or asking the owner to resend pending operations. */
  disconnected(connection: number): void {
    if (!this.current(connection)) return
    this.clearDeadline()
    this.update({ phase: "disconnected", serverSaveState: "unknown" })
  }

  observeServer(connection: number, observation: WordCraftServerObservation): boolean {
    if (!this.current(connection)) return false
    if (observation.kind === "heartbeat") {
      // Liveness is not persistence. A recovered connection still needs the
      // current authoritative snapshot/receipt before it can say saved.
      if (this.state.phase === "disconnected") this.update({ phase: "connecting", serverSaveState: "unknown" })
      this.armDeadline()
      return true
    }
    if (!counter(observation.revision) || !["saved", "saving", "unknown"].includes(observation.saveState)) return false
    const confirmed = observation.confirmedDraftVersion ?? this.confirmedDraftVersion
    if (!Number.isSafeInteger(confirmed) || confirmed < 0 || confirmed > this.draftVersion) return false
    // Valid duplicate/out-of-order traffic proves liveness, but never persistence.
    this.armDeadline()
    const previous = this.state.revision
    if (previous !== null) {
      if (BigInt(observation.revision) < BigInt(previous)) return false
      if (observation.revision === previous) {
        const recoverySnapshot = observation.kind === "snapshot" && this.state.phase !== "connected"
        // Saving/unknown and durable Saved may describe the same revision.
        // Allow that one-way upgrade only after authoritative state on this
        // connection. An exact duplicate can never clear later local input.
        const durableUpgrade = this.state.phase === "connected" && this.state.serverSaveState !== "saved" && observation.saveState === "saved"
        if (!recoverySnapshot && !durableUpgrade) return false
      }
    }
    if (observation.saveState === "saved") this.confirmedDraftVersion = Math.max(this.confirmedDraftVersion, confirmed)
    this.update({ phase: "connected", serverSaveState: observation.saveState, revision: observation.revision, hasUnconfirmedDraft: this.draftVersion > this.confirmedDraftVersion })
    this.armDeadline()
    return true
  }

  dispose(): void {
    this.disposed = true
    this.clearDeadline()
    this.listeners.clear()
  }

  private current(connection: number): boolean { return !this.disposed && connection > 0 && connection === this.connection }
  private clearDeadline(): void { if (this.timer !== undefined) clearTimeout(this.timer); this.timer = undefined }
  private armDeadline(): void {
    this.clearDeadline()
    const connection = this.connection
    this.timer = setTimeout(() => this.disconnected(connection), this.silenceMs)
  }
  private update(value: Partial<WordCraftConnectionState>): void {
    if (Object.entries(value).every(([key, entry]) => this.state[key as keyof WordCraftConnectionState] === entry)) return
    this.state = { ...this.state, ...value }
    this.listeners.forEach(listener => listener())
  }
}
