/** Durable message admission, client side (CH-06).
 *
 *  A send is an INTENT with a stable key. The draft is cleared only after the
 *  server confirms durable acceptance. A timeout is "unknown", not "failed":
 *  retrying re-sends the same key and the same body, so the server can return
 *  the original result instead of creating a second message. If the user edits
 *  the body while an outcome is unknown, that is a different message and the
 *  composer must ask before dropping the original.
 *
 *  The server endpoint does not exist yet (see coord entry FE-REQ-F1-3); this
 *  module is written against the typed `MessageAdmissionClient` below and is
 *  exercised with fixtures. Nothing imports it from the live send path. */

export interface AdmissionBody {
  conversationId: string
  text: string
  /** Server paths of attachments that are ready, in composer order. */
  attachmentPaths: string[]
  model?: string
  effort?: string
}

export type AdmissionResult =
  | { status: "accepted"; messageId: string }
  /** Same key, different body — the server refuses to guess. */
  | { status: "conflict" }
  /** Definitively not accepted (validation, revoked, over budget…). */
  | { status: "rejected"; reason: string }

export interface MessageAdmissionClient {
  admit(request: { intentKey: string; body: AdmissionBody }, signal: AbortSignal): Promise<AdmissionResult>
}

export type SendIntentState =
  | { phase: "idle" }
  | { phase: "sending"; key: string }
  /** Outcome not known (timeout / dropped connection). Draft is kept. */
  | { phase: "unknown"; key: string }
  | { phase: "accepted"; key: string; messageId: string }
  | { phase: "conflict"; key: string }
  | { phase: "rejected"; reason: string }

/** Order-stable canonical form: what "the same body" means. */
export function canonicalBody(b: AdmissionBody): string {
  return JSON.stringify([b.conversationId, b.text, b.attachmentPaths, b.model ?? null, b.effort ?? null])
}

export interface SubmitOutcome {
  state: SendIntentState
  /** True only when the draft may be cleared. */
  clearDraft: boolean
}

export interface SendIntentOptions {
  client: MessageAdmissionClient
  timeoutMs?: number
  newKey?: () => string
}

/** Tracks the single in-flight or unknown intent of one composer. */
export function createSendIntent({ client, timeoutMs = 10_000, newKey = () => crypto.randomUUID() }: SendIntentOptions) {
  let state: SendIntentState = { phase: "idle" }
  let key: string | null = null
  let bodyKey: string | null = null

  async function submit(body: AdmissionBody): Promise<SubmitOutcome> {
    const canon = canonicalBody(body)
    if (state.phase === "sending") return { state, clearDraft: false }
    if (key !== null && bodyKey !== canon) {
      // The previous attempt is unresolved and the body changed: do not reuse
      // its key (that would be a guaranteed conflict) and do not silently
      // mint a new one (that could duplicate it). Ask.
      state = { phase: "conflict", key }
      return { state, clearDraft: false }
    }
    if (key === null) {
      key = newKey()
      bodyKey = canon
    }
    const intentKey = key
    state = { phase: "sending", key: intentKey }
    const ac = new AbortController()
    const timer = setTimeout(() => ac.abort(), timeoutMs)
    try {
      const result = await client.admit({ intentKey, body }, ac.signal)
      if (result.status === "accepted") {
        key = null
        bodyKey = null
        state = { phase: "accepted", key: intentKey, messageId: result.messageId }
        return { state, clearDraft: true }
      }
      if (result.status === "conflict") {
        state = { phase: "conflict", key: intentKey }
        return { state, clearDraft: false }
      }
      key = null
      bodyKey = null
      state = { phase: "rejected", reason: result.reason }
      return { state, clearDraft: false }
    } catch {
      // Timeout, abort or network loss: the server may have accepted it.
      state = { phase: "unknown", key: intentKey }
      return { state, clearDraft: false }
    } finally {
      clearTimeout(timer)
    }
  }

  return {
    submit,
    get state() {
      return state
    },
    /** The user chose to send the edited body as a NEW message. The earlier
     *  unresolved intent is abandoned on the client; the server may still hold
     *  it, which is why this is an explicit choice. */
    startNew() {
      key = null
      bodyKey = null
      state = { phase: "idle" }
    },
  }
}
