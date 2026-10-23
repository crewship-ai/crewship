/**
 * Shared save lifecycle of artifact editors (DOC-02, DX-03, DUR-01, SX-02).
 * Pure: no timers, no I/O. Callers report what happened; this decides what may
 * be claimed. Only a verified receipt makes work "saved" (DX-02).
 */

/** Where unsent work stands. Only "confirmed" means the server holds everything. */
export type SaveDurability =
  | "confirmed" // nothing unsent
  | "writing"   // unsent; the browser journal write has not resolved yet
  | "local"     // unsent; the journal write resolved, so it survives a reload
  | "unsaved"   // unsent and not retained by the browser either
  | "held"      // an earlier unsent draft is held in this browser, not restored

/** One word for the editor's save state; `trayed` work is "waiting". */
export type SaveWord = "opening" | "syncing" | "saving" | "saved" | "unsaved" | "waiting" | "readonly" | "busy"

export type SaveTrouble = "retrying" | "stuck" | "rate-limited" | "offline" | "conflict" | "rejected" | "unconfirmed" | "denied" | "storage"

export interface SaveState {
  opened: boolean
  canWrite: boolean
  /** False when the server has no save route: queued work can never be sent. */
  canSend: boolean
  /** Writes the server has not acknowledged, held drafts included. */
  pending: number
  /** Earlier drafts held in this browser and not restored. */
  held: number
  /** The browser journal is ready: unsent work survives a reload. */
  stored: boolean
  journalFailed: boolean
  /** Journal writes not yet resolved. */
  journalWrites: number
  sending: boolean
  /** A verified receipt covers everything sent; cleared by any new work. */
  receipt: boolean
  /** Another tab of this browser owns the journal. */
  busy: boolean
  trouble?: SaveTrouble
  retryAfterMs?: number
}

export const initialSaveState: SaveState = { opened: false, canWrite: false, canSend: true, pending: 0, held: 0, stored: false, journalFailed: false, journalWrites: 0, sending: false, receipt: false, busy: false }

export type SaveEvent =
  | { type: "opened"; canWrite: boolean; canSend?: boolean }
  /** A local change was queued; `pending` is the queue's size now. */
  | { type: "edit"; pending: number }
  /** The queue was recounted without a receipt (restored rows, a discard). */
  | { type: "recount"; pending: number }
  | { type: "journal"; writes: number }
  | { type: "stored" }
  /** Earlier drafts held, not restored (0: discarded); `pending` when the count changed with them. */
  | { type: "held"; drafts: number; pending?: number }
  | { type: "send" }
  /** A verified receipt; `pending` is what is still queued after it. */
  | { type: "ack"; pending: number }
  /** A retry answered with the original receipt: still a receipt for this write. */
  | { type: "duplicate-ack"; pending: number }
  /** Not acknowledged (503, network); `stuck` once the retry budget is spent. */
  | { type: "transient-fail"; retryAfterMs?: number; stuck?: boolean }
  | { type: "rate-limited"; retryAfterMs?: number }
  | { type: "offline" }
  /** 409: an old epoch, a changed generation or a moved base. */
  | { type: "conflict" }
  | { type: "rejected" }
  /** A 2xx that is not a verifiable receipt for this write. */
  | { type: "unconfirmed" }
  | { type: "denied" }
  /** The person took the server's value for a refused change: nothing of it is unsent any more. */
  | { type: "resolved" }
  /** `failed`: a journal write failed. `busy`: another tab owns the journal. */
  | { type: "storage-unavailable"; failed?: boolean; busy?: boolean }
  | { type: "reset" }

function next(state: SaveState, event: SaveEvent): SaveState {
  const failed = (trouble: SaveTrouble, retryAfterMs?: number): SaveState => ({ ...state, sending: false, receipt: false, trouble, retryAfterMs })
  switch (event.type) {
    case "opened": return { ...state, opened: true, canWrite: event.canWrite, canSend: event.canSend ?? true }
    case "edit": return { ...state, pending: Math.max(1, event.pending), receipt: false }
    case "recount": return { ...state, pending: Math.max(0, event.pending), receipt: state.receipt && event.pending <= 0 }
    case "journal": return { ...state, journalWrites: event.writes }
    case "stored": return { ...state, stored: true, busy: false }
    case "held": { const pending = event.pending ?? state.pending; return { ...state, held: event.drafts, pending, receipt: state.receipt && pending <= 0 } }
    case "send": return { ...state, sending: true, trouble: undefined, retryAfterMs: undefined }
    case "ack":
    case "duplicate-ack": return { ...state, sending: false, pending: event.pending, receipt: event.pending === 0, trouble: undefined, retryAfterMs: undefined }
    case "transient-fail": return failed(event.stuck ? "stuck" : "retrying", event.retryAfterMs)
    case "rate-limited": return failed("rate-limited", event.retryAfterMs)
    case "offline": return failed("offline")
    case "conflict": return failed("conflict")
    case "rejected": return failed("rejected")
    case "unconfirmed": return failed("unconfirmed")
    case "denied": return { ...failed("denied"), canWrite: false }
    // Only a refusal ends this way, and only once nothing else is unsent; never a receipt.
    case "resolved": return state.pending === 0 && (state.trouble === "conflict" || state.trouble === "rejected") ? { ...state, trouble: undefined } : state
    case "storage-unavailable": return { ...state, stored: false, journalFailed: state.journalFailed || Boolean(event.failed), busy: Boolean(event.busy), trouble: event.failed ? "storage" : state.trouble }
    case "reset": return initialSaveState
  }
}

/** Returns `state` itself when nothing changed, so a React reducer skips the render. */
export function saveReducer(state: SaveState, event: SaveEvent): SaveState {
  const value = next(state, event)
  const keys = new Set([...Object.keys(state), ...Object.keys(value)] as (keyof SaveState)[])
  return [...keys].every(key => value[key] === state[key]) ? state : value
}

export function durabilityOf(state: SaveState): SaveDurability {
  if (state.held > 0) return "held"
  if (state.pending === 0) return "confirmed"
  if (state.journalFailed || !state.stored) return state.journalWrites > 0 && !state.journalFailed ? "writing" : "unsaved"
  return state.journalWrites > 0 ? "writing" : "local"
}

export function saveWordOf(state: SaveState): SaveWord {
  if (!state.opened) return "opening"
  if (!state.canWrite || state.trouble === "denied") return "readonly"
  if (state.busy) return "busy"
  if (state.sending) return "syncing"
  // Nothing unsent but no receipt to point to (just opened): in step, not "saved".
  if (state.pending === 0) return state.receipt ? "saved" : "syncing"
  if (state.trouble === "retrying" || state.trouble === "rate-limited") return "waiting"
  if (state.trouble || !state.canSend) return "unsaved"
  return "saving"
}

/** Leaving now would lose work the server does not hold. */
export const losesWork = (state: SaveState) => state.pending > 0

/** Text for the one save status line. `saved`: a verified receipt covers the work. */
export function saveStateLabel(durability: SaveDurability, saved: boolean): string | undefined {
  switch (durability) {
    case "writing": return "Saving to this browser… · not yet confirmed by server"
    case "local": return "Saved locally in this browser · not yet confirmed by server"
    case "unsaved": return "Not stored in this browser or on the server · keep this tab open"
    case "held": return "An earlier unsent draft is held in this browser and was not restored"
    default: return saved ? "Confirmed by server" : undefined
  }
}
