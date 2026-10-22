/**
 * Host side of the embedded workbook editor bridge (protocol 1).
 *
 * The editor runs in a frame on its own origin and is untrusted input, exactly like an HTTP
 * client. This module owns everything between that frame and the server:
 *
 * - Handshake: the editor document is served with CSP `sandbox allow-scripts` and framed with
 *   `EDITOR_FRAME_ATTRIBUTES`, so its origin is opaque and every message from it carries
 *   `origin === "null"`. A "null" origin alone proves nothing (any sandboxed document has it), so
 *   only `gridcraft:hello` whose `source` is our own frame window is considered, and a wrong
 *   protocol or bundle digest kills the session. The hello must follow the frame's first `load`
 *   (`frameLoaded()`); an earlier hello or any other load kills the session. The answer transfers a private `MessagePort`
 *   once; every later message travels over it as a JSON string, and any further window message
 *   from the frame kills the session.
 * - Every inbound port message is validated (size, protocol, exact keys, ops) before anything
 *   reaches the server. A malformed envelope kills the session; malformed ops are refused.
 * - The frame never learns the session id: the host turns the frame's monotonic `seq` into
 *   `mutation_id = <session_id>:<seq>` and keeps the request bytes, so a retry is byte-identical.
 * - The frame sees revisions strictly in order. A receipt or push beyond `confirmed + 1` first
 *   reads `commits?since=confirmed`; an `ack` waits until every earlier revision is delivered.
 * - "Saved" exists only for a receipt with `durability: "node"` (or a committed revision of our
 *   own in the commits feed, which is durable by construction and settles the pending write).
 *   A 200 without such a receipt is retried with the same mutation (after asking its status) a
 *   bounded number of times, then refused as `not_confirmed`; later mutations still go out.
 * - 409 reconciles (catch up, then refuse with the server's cells, or retry the same mutation
 *   when the head merely moved), 429 on the artifact retries the same mutation. A refusal
 *   (`editorRefusal`: lost access, an expired session, the editor turned off) destroys the
 *   session and the port; nothing retries it.
 * - The editor must say hello and receive its snapshot within `BRIDGE_HANDSHAKE_TIMEOUT_MS`, and
 *   a failed snapshot read at connect ends the session as `load`; the host shows the preview.
 *
 * The server is reached only through `WorkbookTransport`, so tests need no server.
 */

import type { SaveEvent } from "@/lib/editor-lifecycle/save-state"
import { createWriteQueue, type Pacing, type QueuedWrite, type SendFailure, type Timers } from "@/lib/editor-lifecycle/write-queue"
import { editorRefusal, type EditorRefusal } from "./editor-client"

export const BRIDGE_PROTOCOL = 1
/** Largest port message the host reads, in UTF-8 bytes. */
export const BRIDGE_MAX_MESSAGE_BYTES = 64 * 1024
/** Server limits for one human mutation (`cells.set`). */
export const BRIDGE_MAX_OPS = 64
export const BRIDGE_MAX_INPUT_BYTES = 8 * 1024
export const BRIDGE_MAX_SHEET_LENGTH = 128
/** Mutations the host holds before refusing new ones. */
export const BRIDGE_MAX_PENDING = 256
/** Page size of the commits feed. */
export const BRIDGE_COMMITS_LIMIT = 200
/** Retries of one mutation after a 409 that names no conflicting cell (the head moved). */
export const BRIDGE_MAX_REBASES = 3
/** Retry delay when a 429 carries no `retry_after_ms`. */
export const BRIDGE_DEFAULT_RETRY_MS = 150
/** Attempts of one mutation answered 200 without a durable receipt before it is refused as `not_confirmed`. */
export const BRIDGE_MAX_UNCONFIRMED = 5
/** From the bridge's creation until the editor said hello and holds its snapshot. */
export const BRIDGE_HANDSHAKE_TIMEOUT_MS = 15_000
/** Bridge pacing: a confirmed cell is sent at once, not on the document autosave cadence. */
export const BRIDGE_PACING: Partial<Pacing> = { minGapMs: 0, baseDelayMs: 250, maxDelayMs: 10_000 }

const CELL = /^[A-Z]{1,3}[1-9][0-9]{0,6}$/
const encoder = new TextEncoder()
const bytes = (text: string) => encoder.encode(text).length
const object = (value: unknown): value is Record<string, unknown> => Boolean(value) && typeof value === "object" && !Array.isArray(value)
const count = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0
const positive = (value: unknown): value is number => count(value) && value > 0
const text = (value: unknown, max = 256): value is string => typeof value === "string" && value.length > 0 && value.length <= max
/** Exactly these keys, with the optional ones allowed to be absent. */
function shaped(value: Record<string, unknown>, required: readonly string[], optional: readonly string[] = []): boolean {
  const keys = Object.keys(value)
  return required.every(key => key in value) && keys.every(key => required.includes(key) || optional.includes(key))
}

// ---------------------------------------------------------------------------------------------
// Wire shapes

export interface CellOp { op: "cell.set"; sheet: string; cell: string; input: string }
export interface ServerCellOp { kind: "cell.set"; sheet: string; cell: string; input: string }

/** `POST /api/v1/pages/{slug}/workbook/sessions`. */
export interface WorkbookSession {
  session_id: string
  actor_label: string
  rev: number
  generation: number
  protocol: number
  bundle_digest: string
  editor_url?: string
  expires_at?: string
}

/** Today's `Receipt` (`internal/appengine/types.go`). */
export interface WorkbookReceipt {
  operation_key: { artifact_id: string; authority_id: string; mutation_id: string }
  outcome: string
  rev: number
  durability: string
  [key: string]: unknown
}

export interface WorkbookConflict { cell: string; sheet?: string; input: string; rev: number; actor_label: string }

export type WorkbookCommit =
  | { rev: number; kind: "cells"; actor_label: string; own: boolean; session_id?: string; mutation_id?: string; ops: Omit<CellOp, "op">[] }
  | { rev: number; kind: "snapshot" }

export interface WorkbookCommits { head_rev: number; generation: number; commits: WorkbookCommit[] }

export interface WorkbookSnapshot { rev: number; generation: number; snapshot: unknown }

/** An HTTP answer as the bridge needs it; a network failure rejects instead. */
export interface TransportResponse { status: number; body: unknown }

/** The four server calls the bridge makes, bound to one page and one session. */
export interface WorkbookTransport {
  /** `GET …/workbook` */
  snapshot(): Promise<TransportResponse>
  /** `POST …/workbook/edits` with these exact bytes. */
  edit(mutationId: string, body: string): Promise<TransportResponse>
  /** `GET …/workbook/commits?since=&limit=` */
  commits(since: number, limit: number): Promise<TransportResponse>
  /** `GET …/workbook/operations/{mutation}` */
  operation(mutationId: string): Promise<TransportResponse>
  /** `DELETE …/workbook/sessions/{session}`, best effort. */
  closeSession(): Promise<void>
}

/** Host → editor. */
export type HostMessage =
  | { type: "snapshot"; protocol: 1; rev: number; generation: number; snapshot: unknown; discarded: number[] }
  | { type: "committed"; protocol: 1; rev: number; actor_label: string; ops: CellOp[] }
  | { type: "ack"; protocol: 1; seq: number; rev: number }
  | { type: "rejected"; protocol: 1; seq: number; code: RejectCode; current_rev: number; cells: WorkbookConflict[] }
  | { type: "online" | "offline"; protocol: 1 }

export type RejectCode = "precondition_failed" | "invalid_ops" | "conflict" | "history_quota_exceeded" | "too_many_pending" | "not_confirmed"

/** Editor → host, after validation. */
export interface FrameMutation { seq: number; base_rev: number; ops: CellOp[] }
export type FrameMessage =
  | { type: "mutation"; mutation: FrameMutation }
  | { type: "resume"; last_rev: number; pending: FrameMutation[] }

/**
 * How the host must create the editor frame: scripts only, no `allow-same-origin` (the origin
 * stays opaque, so the frame has no cookies, storage or same-origin reach into anything),
 * a credentialless load and no referrer.
 */
export const EDITOR_FRAME_ATTRIBUTES = Object.freeze({ sandbox: "allow-scripts", credentialless: true, referrerPolicy: "no-referrer" } as const)
/** `MessageEvent.origin` of a document with an opaque origin. */
export const OPAQUE_ORIGIN = "null"

/**
 * Why a session ended. `closed` is the host's own unmount and is never shown; every other reason
 * falls back to the read-only preview with a notice. `load`: the snapshot could not be read at
 * connect. `timeout`: no hello (or no snapshot) within `BRIDGE_HANDSHAKE_TIMEOUT_MS`.
 * `disabled`, `expired` and `access` are server refusals (`EditorRefusal`).
 */
export type KillReason = "handshake" | "protocol" | "generation" | "navigation" | "load" | "timeout" | EditorRefusal | "closed"

// ---------------------------------------------------------------------------------------------
// Validation

export function parseWorkbookSession(value: unknown): WorkbookSession | null {
  if (!object(value) || !text(value.session_id) || typeof value.actor_label !== "string" || !count(value.rev) || !positive(value.generation)) return null
  if (value.protocol !== BRIDGE_PROTOCOL || !text(value.bundle_digest)) return null
  return value as unknown as WorkbookSession
}

/** A durable receipt for exactly this mutation, or null. */
export function parseDurableReceipt(value: unknown, mutationId: string): WorkbookReceipt | null {
  if (!object(value) || !object(value.operation_key) || value.operation_key.mutation_id !== mutationId) return null
  if (value.outcome !== "applied" || value.durability !== "node" || !positive(value.rev)) return null
  return value as unknown as WorkbookReceipt
}

function parseCellOp(value: unknown, kind: "op" | "kind" | null): Omit<CellOp, "op"> | null {
  if (!object(value)) return null
  const extra = kind ? [kind] : []
  if (!shaped(value, ["sheet", "cell", "input", ...extra])) return null
  if (kind && value[kind] !== "cell.set") return null
  const { sheet, cell, input } = value
  if (!text(sheet, BRIDGE_MAX_SHEET_LENGTH) || typeof cell !== "string" || !CELL.test(cell)) return null
  if (typeof input !== "string" || bytes(input) > BRIDGE_MAX_INPUT_BYTES) return null
  return { sheet, cell, input }
}

function parseConflicts(value: unknown): WorkbookConflict[] | null {
  if (value === undefined) return []
  if (!Array.isArray(value)) return null
  const out: WorkbookConflict[] = []
  for (const item of value) {
    if (!object(item) || typeof item.cell !== "string" || !CELL.test(item.cell) || typeof item.input !== "string" || !count(item.rev) || typeof item.actor_label !== "string") return null
    if (item.sheet !== undefined && !text(item.sheet, BRIDGE_MAX_SHEET_LENGTH)) return null
    out.push({ cell: item.cell, ...(item.sheet === undefined ? {} : { sheet: item.sheet as string }), input: item.input, rev: item.rev, actor_label: item.actor_label })
  }
  return out
}

/** The commits feed after `since`: ascending, gap-free, each op valid. */
export function parseCommits(value: unknown, since: number): WorkbookCommits | null {
  if (!object(value) || !count(value.head_rev) || !positive(value.generation) || !Array.isArray(value.commits)) return null
  const commits: WorkbookCommit[] = []
  let previous = since
  for (const item of value.commits) {
    if (!object(item) || !positive(item.rev) || item.rev !== previous + 1 || item.rev > value.head_rev) return null
    previous = item.rev
    if (item.kind === "snapshot") { commits.push({ rev: item.rev, kind: "snapshot" }); continue }
    if (item.kind !== "cells" || typeof item.actor_label !== "string" || typeof item.own !== "boolean" || !Array.isArray(item.ops)) return null
    const ops = item.ops.map(op => parseCellOp(op, null))
    if (ops.some(op => !op)) return null
    if (item.session_id !== undefined && !text(item.session_id)) return null
    if (item.mutation_id !== undefined && !text(item.mutation_id)) return null
    commits.push({ rev: item.rev, kind: "cells", actor_label: item.actor_label, own: item.own, ops: ops as Omit<CellOp, "op">[], ...(typeof item.session_id === "string" ? { session_id: item.session_id } : {}), ...(typeof item.mutation_id === "string" ? { mutation_id: item.mutation_id } : {}) })
  }
  return { head_rev: value.head_rev, generation: value.generation, commits }
}

function parseSnapshot(value: unknown): WorkbookSnapshot | null {
  if (!object(value) || !count(value.rev) || !positive(value.generation) || !("snapshot" in value)) return null
  return { rev: value.rev, generation: value.generation, snapshot: value.snapshot }
}

type Parsed<T> = { ok: true; value: T } | { ok: false; reason: "protocol" | "invalid_ops"; seq?: number }

function parseMutation(value: unknown, withEnvelope: boolean): Parsed<FrameMutation> {
  if (!object(value)) return { ok: false, reason: "protocol" }
  const envelope = withEnvelope ? ["type", "protocol"] : []
  if (!shaped(value, ["seq", "base_rev", "ops", ...envelope]) || !positive(value.seq) || !positive(value.base_rev)) return { ok: false, reason: "protocol" }
  const seq = value.seq
  if (!Array.isArray(value.ops) || value.ops.length === 0 || value.ops.length > BRIDGE_MAX_OPS) return { ok: false, reason: "invalid_ops", seq }
  const ops: CellOp[] = [], cells = new Set<string>()
  for (const raw of value.ops) {
    const op = parseCellOp(raw, "op")
    const key = op && `${op.sheet}\u0000${op.cell}`
    if (!op || !key || cells.has(key)) return { ok: false, reason: "invalid_ops", seq }
    cells.add(key)
    ops.push({ op: "cell.set", ...op })
  }
  return { ok: true, value: { seq, base_rev: value.base_rev, ops } }
}

/** Validates one port message from the editor. Never throws. */
export function parseFrameMessage(data: unknown): Parsed<FrameMessage> {
  if (typeof data !== "string" || bytes(data) > BRIDGE_MAX_MESSAGE_BYTES) return { ok: false, reason: "protocol" }
  let value: unknown
  try { value = JSON.parse(data) } catch { return { ok: false, reason: "protocol" } }
  if (!object(value) || value.protocol !== BRIDGE_PROTOCOL) return { ok: false, reason: "protocol" }
  if (value.type === "mutation") {
    const parsed = parseMutation(value, true)
    return parsed.ok ? { ok: true, value: { type: "mutation", mutation: parsed.value } } : parsed
  }
  if (value.type === "resume") {
    if (!shaped(value, ["type", "protocol", "last_rev", "pending"]) || !count(value.last_rev) || !Array.isArray(value.pending) || value.pending.length > BRIDGE_MAX_PENDING) return { ok: false, reason: "protocol" }
    const pending: FrameMutation[] = []
    for (const item of value.pending) {
      const parsed = parseMutation(item, false)
      // A resume restates what was already sent; a bad entry there is not a fresh edit to refuse.
      if (!parsed.ok) return { ok: false, reason: "protocol" }
      pending.push(parsed.value)
    }
    return { ok: true, value: { type: "resume", last_rev: value.last_rev, pending } }
  }
  return { ok: false, reason: "protocol" }
}

// ---------------------------------------------------------------------------------------------
// Bridge

export interface BridgePort {
  postMessage(message: string): void
  close(): void
  onmessage: ((event: { data: unknown }) => void) | null
  start?(): void
}
export interface FrameWindow { postMessage(message: unknown, targetOrigin: string, transfer?: unknown[]): void }
export interface WindowMessage { origin: string; source: unknown; data: unknown }

export interface WorkbookBridgeOptions {
  /** The session the server opened for this frame. */
  session: WorkbookSession
  /** The frame's window (`iframe.contentWindow`); messages from any other source are ignored. */
  frame: () => FrameWindow | null | undefined
  transport: WorkbookTransport
  /** Display name of the page, sent in `connect`. */
  pageLabel: string
  /** Opaque display/correlation id for the frame; never the session id. */
  clientId: string
  /** Local correlation nonce for `connect`; not a credential. */
  frameNonce?: string
  createChannel?: () => { port1: BridgePort; port2: unknown }
  timers?: Timers
  pacing?: Partial<Pacing>
  random?: () => number
  onSaveEvent?: (event: SaveEvent) => void
  /** The session is over: destroy the frame. `unsaved` writes never got a receipt. */
  onKill?: (kill: { reason: KillReason; unsaved: number }) => void
  /** Tests only; defaults to `BRIDGE_HANDSHAKE_TIMEOUT_MS`. */
  handshakeTimeoutMs?: number
}

export type BridgePhase = "waiting" | "connecting" | "live" | "killed"

export interface WorkbookBridge {
  readonly phase: BridgePhase
  /** Last revision delivered to the editor, in order. */
  readonly confirmedRev: number
  /** Mutations without a receipt. */
  readonly pending: number
  /** Feed every `window` "message" event here. */
  handleWindowMessage(event: WindowMessage): void
  /**
   * Feed the iframe's every "load" event here. The hello is heard only after the first load (the
   * editor says hello from its own `load` event or later), so the port only ever goes to the
   * document whose load the host saw. Any other load means the frame now holds a different
   * document: a second load before the hello, or any load after it, ends the session, and the
   * port is never offered again: the host must create a new frame and a new session.
   */
  frameLoaded(): void
  /** A `workbook.committed` push for this page. Duplicates and stale revisions are no-ops. */
  notify(push: { rev: number; generation: number }): void
  /** The realtime connection came back: tell the editor, replay from the last revision, retry. */
  online(): void
  offline(): void
  /** Ends the session (a refusal the host learned of, `session_closed`, unmount). */
  close(reason?: KillReason): void
}

interface BridgeWrite extends QueuedWrite {
  seq: number
  /** The frame's request as received, to tell a restatement from a different mutation. */
  frameKey: string
  /** Exact request bytes; a retry sends them again. */
  body: string
  epoch: number
  /** An attempt may have reached the server: ask for its status before sending again. */
  uncertain: boolean
  rebases: number
  /** Attempts answered 200 without a durable receipt. */
  unconfirmed: number
}

type Outcome = { type: "ack"; rev: number } | { type: "rejected"; message: HostMessage }

type Handling =
  | { action: "retry" }
  | { action: "kill"; reason: KillReason }
  | { action: "reject"; code: RejectCode; cells?: WorkbookConflict[] }
  | { action: "reconcile"; headRev: number; cells: WorkbookConflict[] }
  | { action: "snapshot" }

/** A failed send: how the write queue counts it, and what the bridge does about it. */
class BridgeFailure extends Error {
  constructor(readonly failure: SendFailure, readonly handling: Handling) { super(failure.kind) }
}
const fail = (failure: SendFailure, handling: Handling): never => { throw new BridgeFailure(failure, handling) }

function retryAfter(body: unknown): number {
  return object(body) && count(body.retry_after_ms) ? body.retry_after_ms : BRIDGE_DEFAULT_RETRY_MS
}

/** Maps a non-200 edit answer onto queue accounting and bridge handling. */
function editFailure(response: TransportResponse): BridgeFailure {
  const { status, body } = response
  const error = object(body) && typeof body.error === "string" ? body.error : ""
  const refusal = editorRefusal(response)
  if (refusal) return new BridgeFailure({ kind: "denied" }, { action: "kill", reason: refusal })
  if (status === 400) return new BridgeFailure({ kind: "rejected" }, { action: "reject", code: "invalid_ops" })
  if (status === 410) return new BridgeFailure({ kind: "conflict" }, { action: "snapshot" })
  if (status === 409) {
    if (error === "stale_generation") return new BridgeFailure({ kind: "denied" }, { action: "kill", reason: "generation" })
    if (error === "in_progress") return new BridgeFailure({ kind: "transient", retryAfterMs: retryAfter(body) }, { action: "retry" })
    if (error === "precondition_failed") {
      const cells = object(body) ? parseConflicts(body.conflicts) : null
      const headRev = object(body) && count(body.head_rev) ? body.head_rev : 0
      if (cells) return new BridgeFailure({ kind: "conflict" }, { action: "reconcile", headRev, cells })
    }
    return new BridgeFailure({ kind: "rejected" }, { action: "reject", code: error === "precondition_failed" ? "precondition_failed" : "conflict" })
  }
  if (status === 429) {
    if (error === "history_quota_exceeded") return new BridgeFailure({ kind: "rejected" }, { action: "reject", code: "history_quota_exceeded" })
    const artifact = object(body) && body.scope === "artifact"
    // Busy artifact: another write is committing right now. The same mutation goes again shortly.
    return new BridgeFailure({ kind: artifact ? "rate-limited" : "transient", retryAfterMs: retryAfter(body) }, { action: "retry" })
  }
  return new BridgeFailure({ kind: "transient" }, { action: "retry" })
}

export function createWorkbookBridge(options: WorkbookBridgeOptions): WorkbookBridge {
  const { session, transport } = options
  const timers = options.timers ?? { now: () => Date.now(), setTimeout: (run, ms) => setTimeout(run, ms), clearTimeout: handle => clearTimeout(handle as ReturnType<typeof setTimeout>) }
  const emit = (event: SaveEvent) => { if (phase !== "killed" || event.type === "denied") options.onSaveEvent?.(event) }
  const mutationPrefix = `${session.session_id}:`

  let phase: BridgePhase = "waiting"
  /** Cleared once the editor holds its first snapshot; until then the session has a deadline. */
  let handshakeTimer: unknown = timers.setTimeout(() => { handshakeTimer = undefined; kill("timeout") }, options.handshakeTimeoutMs ?? BRIDGE_HANDSHAKE_TIMEOUT_MS)
  /** Load events of the frame so far; the hello must follow exactly one. */
  let loads = 0
  let port: BridgePort | undefined
  let confirmedRev = 0
  /** Lowest revision the editor can resume from: the last snapshot it loaded. */
  let baseRev = 0
  let epoch = 0
  let maxSeq = 0
  let wantRev = 0
  let catching: Promise<void> | undefined
  let catchUpTimer: unknown, catchUpFailures = 0
  let driving = false, driveTimer: unknown
  /** Receipt revisions waiting for earlier revisions to reach the editor: rev → seq. */
  const held = new Map<number, number>()
  /** Every seq this session accepted from the editor, by epoch. */
  const known = new Map<number, { epoch: number; frameKey: string }>()
  /** What the editor was told about a seq, to answer a restatement the same way. */
  const outcomes = new Map<number, Outcome>()

  const queue = createWriteQueue<BridgeWrite>({
    timers,
    pacing: { ...BRIDGE_PACING, ...options.pacing },
    random: options.random,
    classify: cause => cause instanceof BridgeFailure ? cause.failure : { kind: "offline" },
    onEvent: emit,
  })

  function post(message: HostMessage) {
    if (phase === "killed" || !port) return
    port.postMessage(JSON.stringify(message))
  }

  function kill(reason: KillReason) {
    if (phase === "killed") return
    const unsaved = queue.size
    phase = "killed"
    queue.stop()
    timers.clearTimeout(driveTimer); timers.clearTimeout(catchUpTimer); timers.clearTimeout(handshakeTimer)
    held.clear()
    for (const write of queue.writes()) queue.remove(write.id)
    if (port) { port.onmessage = null; port.close(); port = undefined }
    if (reason === "access") emit({ type: "denied" })
    if (reason !== "access") void transport.closeSession().catch(() => {})
    options.onKill?.({ reason, unsaved })
  }

  // ----- ordering -----------------------------------------------------------------------------

  function ownSeq(commit: Extract<WorkbookCommit, { kind: "cells" }>): number | undefined {
    if (commit.session_id !== session.session_id || !commit.mutation_id?.startsWith(mutationPrefix)) return undefined
    const seq = Number(commit.mutation_id.slice(mutationPrefix.length))
    return positive(seq) && String(seq) === commit.mutation_id.slice(mutationPrefix.length) ? seq : undefined
  }

  function ackFrame(seq: number, rev: number) {
    if (outcomes.has(seq)) return
    outcomes.set(seq, { type: "ack", rev })
    post({ type: "ack", protocol: 1, seq, rev })
  }

  function flushHeld() {
    for (let seq = held.get(confirmedRev + 1); seq !== undefined; seq = held.get(confirmedRev + 1)) {
      held.delete(confirmedRev + 1)
      confirmedRev++
      ackFrame(seq, confirmedRev)
    }
  }

  /** Our own revision in the feed is durable: the write it came from is done, receipt or not. */
  function settle(seq: number) {
    const write = queue.writes().find(item => item.seq === seq)
    if (!write) return
    const wasHead = queue.head() === write
    // The one in flight stays; its answer (or the next status check) finds it applied.
    if (!queue.remove(write.id)) return
    emit({ type: "ack", pending: queue.size })
    if (wasHead) queue.resume() // its backoff or halt belonged to it alone
    void drive()
  }

  function deliverCommit(commit: Extract<WorkbookCommit, { kind: "cells" }>) {
    // A held receipt names the revision as ours even when the feed carries no ids.
    const seq = held.get(commit.rev) ?? ownSeq(commit)
    // An edit refused as unconfirmed may still have committed: the editor then needs its value.
    if (seq !== undefined && known.get(seq)?.epoch === epoch && outcomes.get(seq)?.type !== "rejected") { ackFrame(seq, commit.rev); settle(seq) }
    else post({ type: "committed", protocol: 1, rev: commit.rev, actor_label: commit.actor_label, ops: commit.ops.map(op => ({ op: "cell.set", ...op })) })
    held.delete(commit.rev)
    confirmedRev = commit.rev
    flushHeld()
  }

  /** Loads the head snapshot: queued edits made against the old content are discarded. */
  async function reloadSnapshot(): Promise<boolean> {
    let response: TransportResponse
    try { response = await transport.snapshot() } catch { return false }
    if (phase === "killed") return false
    const refusal = editorRefusal(response)
    if (refusal) { kill(refusal); return false }
    const snapshot = response.status === 200 ? parseSnapshot(response.body) : null
    if (!snapshot) return false
    if (snapshot.generation !== session.generation) { kill("generation"); return false }
    epoch++
    const head = queue.head()
    const discarded: number[] = []
    for (const write of queue.writes()) if (queue.remove(write.id)) discarded.push(write.seq)
    // The write in flight belongs to the old content too; its outcome is no longer the editor's.
    if (head && queue.head() === head) discarded.unshift(head.seq)
    emit({ type: "recount", pending: queue.size })
    held.clear()
    confirmedRev = baseRev = snapshot.rev
    wantRev = Math.max(wantRev, snapshot.rev)
    post({ type: "snapshot", protocol: 1, rev: snapshot.rev, generation: snapshot.generation, snapshot: snapshot.snapshot, discarded })
    return true
  }

  function scheduleCatchUp() {
    timers.clearTimeout(catchUpTimer)
    catchUpFailures++
    const ms = Math.min(10_000, 250 * 2 ** Math.min(catchUpFailures - 1, 6))
    catchUpTimer = timers.setTimeout(() => { catchUpTimer = undefined; void catchUp(0) }, ms)
  }

  /** Reads the commits feed until the editor holds `rev` (and the head the feed reports). */
  function catchUp(rev: number): Promise<void> {
    wantRev = Math.max(wantRev, rev)
    if (phase !== "live") return Promise.resolve()
    catching ??= (async () => {
      try {
        while (phase === "live" && wantRev > confirmedRev) {
          const since = confirmedRev
          let response: TransportResponse
          try { response = await transport.commits(since, BRIDGE_COMMITS_LIMIT) } catch { scheduleCatchUp(); return }
          if (phase !== "live") return
          const refusal = editorRefusal(response)
          if (refusal) { kill(refusal); return }
          if (response.status === 410) { if (!(await reloadSnapshot())) scheduleCatchUp(); continue }
          const feed = response.status === 200 ? parseCommits(response.body, since) : null
          if (!feed) { scheduleCatchUp(); return }
          if (feed.generation !== session.generation) { kill("generation"); return }
          if (confirmedRev !== since) continue // a receipt moved the editor on meanwhile; read again
          wantRev = Math.max(wantRev, feed.head_rev)
          if (!feed.commits.length) {
            // The head is not there yet (a push ahead of its transaction's visibility): try later.
            if (wantRev > confirmedRev) scheduleCatchUp()
            return
          }
          for (const commit of feed.commits) {
            if (phase !== "live") return
            if (commit.rev <= confirmedRev) continue // a held ack already carried the editor past it
            if (commit.kind === "snapshot") {
              if (!(await reloadSnapshot())) { scheduleCatchUp(); return }
              break
            }
            deliverCommit(commit)
          }
        }
        catchUpFailures = 0
      } finally {
        catching = undefined
      }
    })()
    return catching
  }

  // ----- sending ------------------------------------------------------------------------------

  async function send(write: BridgeWrite): Promise<unknown> {
    if (write.uncertain) {
      // The previous attempt may have committed: ask before sending the same bytes again.
      let status: TransportResponse | undefined
      try { status = await transport.operation(write.id) } catch { /* fall through to the retry */ }
      if (status) {
        // 404 here only means the mutation never arrived.
        const refusal = status.status === 404 ? null : editorRefusal(status)
        if (refusal) fail({ kind: "denied" }, { action: "kill", reason: refusal })
        if (status.status === 200 && object(status.body)) {
          const receipt = parseDurableReceipt(status.body.receipt, write.id)
          if (receipt) return receipt
          if (status.body.state === "in_progress") fail({ kind: "transient", retryAfterMs: BRIDGE_DEFAULT_RETRY_MS }, { action: "retry" })
        }
      }
    }
    let response: TransportResponse
    try { response = await transport.edit(write.id, write.body) } catch { write.uncertain = true; return fail({ kind: "offline" }, { action: "retry" }) }
    if (response.status === 200) return response.body
    if (response.status >= 500) write.uncertain = true
    throw editFailure(response)
  }

  /**
   * Accepts only a durable receipt for this exact mutation. Anything else is retried with the
   * same bytes (asking its status first) and, after `BRIDGE_MAX_UNCONFIRMED` attempts, refused
   * as `not_confirmed` so the queue moves on to later mutations.
   */
  function verify(result: unknown, write: BridgeWrite): boolean {
    const receipt = parseDurableReceipt(result, write.id)
    if (!receipt) {
      // The commits feed already showed it committed while this attempt was in flight.
      if (outcomes.get(write.seq)?.type === "ack") return true
      write.uncertain = true
      if (++write.unconfirmed < BRIDGE_MAX_UNCONFIRMED) return fail({ kind: "transient" }, { action: "retry" })
      return fail({ kind: "unconfirmed" }, { action: "reject", code: "not_confirmed" })
    }
    if (write.epoch !== epoch || phase !== "live") return true
    const rev = receipt.rev
    if (rev <= confirmedRev) return true // the commits feed delivered it already
    if (rev === confirmedRev + 1) { confirmedRev = rev; ackFrame(write.seq, rev); flushHeld(); return true }
    held.set(rev, write.seq)
    void catchUp(rev)
    return true
  }

  function reject(write: BridgeWrite, code: RejectCode, cells: WorkbookConflict[] = []) {
    queue.remove(write.id)
    emit({ type: "recount", pending: queue.size })
    if (write.epoch !== epoch) return
    const message: HostMessage = { type: "rejected", protocol: 1, seq: write.seq, code, current_rev: confirmedRev, cells }
    outcomes.set(write.seq, { type: "rejected", message })
    post(message)
  }

  async function handle(cause: unknown, write: BridgeWrite | undefined) {
    if (!(cause instanceof BridgeFailure) || !write) return
    const handling = cause.handling
    if (write.epoch !== epoch && handling.action !== "kill") {
      // Sent against content a snapshot replaced; the editor already counts it as discarded.
      queue.remove(write.id)
      emit({ type: "recount", pending: queue.size })
      queue.resume()
      return
    }
    switch (handling.action) {
      case "retry": return
      case "kill": kill(handling.reason); return
      case "reject": reject(write, handling.code); queue.resume(); return
      case "snapshot": await reloadSnapshot(); queue.resume(); return
      case "reconcile":
        // The editor first sees what the server has, in order; then the verdict on its write.
        await catchUp(handling.headRev)
        if (phase !== "live") return
        if (handling.cells.length) reject(write, "precondition_failed", handling.cells)
        else if (write.rebases < BRIDGE_MAX_REBASES) write.rebases++ // the head moved: same mutation again
        else reject(write, "precondition_failed")
        queue.resume()
        return
    }
  }

  async function drive() {
    if (driving) return
    driving = true
    try {
      while (phase === "live" && queue.size > 0 && !queue.halted) {
        const ms = queue.wait()
        if (ms > 0) {
          timers.clearTimeout(driveTimer)
          if (ms !== Infinity) driveTimer = timers.setTimeout(() => { driveTimer = undefined; void drive() }, ms)
          return
        }
        const write = queue.head()
        try { await queue.deliver(send, verify) } catch (cause) { await handle(cause, write) }
      }
    } finally {
      driving = false
    }
  }

  // ----- editor messages ----------------------------------------------------------------------

  function replay(seq: number) {
    const outcome = outcomes.get(seq)
    if (!outcome) return
    if (outcome.type === "ack") post({ type: "ack", protocol: 1, seq, rev: outcome.rev })
    else post(outcome.message)
  }

  /** False when the editor broke the protocol. */
  function accept(mutation: FrameMutation): boolean {
    const frameKey = JSON.stringify([mutation.base_rev, mutation.ops])
    const seen = known.get(mutation.seq)
    if (seen) {
      // A restatement must be byte-for-byte the same mutation.
      if (seen.frameKey !== frameKey) return false
      replay(mutation.seq)
      return true
    }
    if (mutation.seq <= maxSeq || mutation.base_rev > confirmedRev) return false
    maxSeq = mutation.seq
    known.set(mutation.seq, { epoch, frameKey })
    const id = `${mutationPrefix}${mutation.seq}`
    const write: BridgeWrite = {
      id, seq: mutation.seq, frameKey, epoch, attempted: false, uncertain: false, rebases: 0, unconfirmed: 0,
      body: JSON.stringify({ session_id: session.session_id, mutation_id: id, base_rev: mutation.base_rev, generation: session.generation, ops: mutation.ops.map(({ sheet, cell, input }) => ({ kind: "cell.set", sheet, cell, input })) }),
    }
    if (queue.size >= BRIDGE_MAX_PENDING) { reject(write, "too_many_pending"); return true }
    queue.enqueue(write)
    emit({ type: "edit", pending: queue.size })
    return true
  }

  function refuseOps(seq: number) {
    if (seq <= maxSeq) return false
    maxSeq = seq
    known.set(seq, { epoch, frameKey: "invalid" })
    const message: HostMessage = { type: "rejected", protocol: 1, seq, code: "invalid_ops", current_rev: confirmedRev, cells: [] }
    outcomes.set(seq, { type: "rejected", message })
    post(message)
    return true
  }

  function onPortMessage(event: { data: unknown }) {
    if (phase !== "live") { if (phase === "connecting") kill("protocol"); return }
    const parsed = parseFrameMessage(event.data)
    if (!parsed.ok) {
      if (parsed.reason === "invalid_ops" && parsed.seq !== undefined && refuseOps(parsed.seq)) return
      kill("protocol")
      return
    }
    const message = parsed.value
    if (message.type === "mutation") {
      if (!accept(message.mutation)) kill("protocol")
      else void drive()
      return
    }
    // resume: the editor restates where it is and what it still waits for.
    if (message.last_rev > confirmedRev) { kill("protocol"); return }
    if (message.last_rev < confirmedRev) {
      if (message.last_rev < baseRev) { void reloadSnapshot().then(() => drive()); return }
      // Replay from where the editor is; acks it lost are sent again.
      const target = confirmedRev
      for (const [seq, outcome] of outcomes) if (outcome.type === "ack" && outcome.rev > message.last_rev) outcomes.delete(seq)
      held.clear()
      confirmedRev = message.last_rev
      void catchUp(target)
    }
    for (const mutation of message.pending) if (!accept(mutation)) { kill("protocol"); return }
    if (queue.halted === "unconfirmed") queue.resume()
    void drive()
  }

  // ----- handshake ----------------------------------------------------------------------------

  async function connect(frame: FrameWindow) {
    phase = "connecting"
    const channel = options.createChannel ? options.createChannel() : (new MessageChannel() as unknown as { port1: BridgePort; port2: unknown })
    port = channel.port1
    port.onmessage = onPortMessage
    port.start?.()
    // The target origin must be "*": an opaque origin cannot be named, and "null" matches nothing.
    // It is acceptable only here and only because:
    // - `frame` is the exact window object whose hello was just verified (our iframe's
    //   contentWindow), and postMessage delivers to that window alone, never to another frame;
    // - this call runs synchronously in the same task that verified the hello;
    // - the host page's CSP `frame-src` admits only the editor origin into that frame, so even a
    //   frame that navigated itself right after its hello holds an editor-origin document.
    // Nothing else is ever posted with "*"; all later traffic uses the transferred port.
    frame.postMessage(
      { type: "gridcraft:connect", protocol: BRIDGE_PROTOCOL, session: options.frameNonce ?? options.clientId, page_label: options.pageLabel, rev: session.rev, generation: session.generation, client_id: options.clientId },
      "*",
      [channel.port2],
    )
    phase = "live"
    if (!(await reloadSnapshot())) {
      // Not "closed" (the host's own unmount, never shown): the host must fall back visibly.
      if (phase === "live") kill("load")
      return
    }
    timers.clearTimeout(handshakeTimer); handshakeTimer = undefined
    emit({ type: "opened", canWrite: true })
    post({ type: "online", protocol: 1 })
    if (wantRev > confirmedRev) void catchUp(0)
  }

  return {
    get phase() { return phase },
    get confirmedRev() { return confirmedRev },
    get pending() { return queue.size },
    handleWindowMessage(event) {
      if (phase === "killed") return
      const frame = options.frame()
      // Only our own frame window is heard at all; every other window, whatever its origin, is ignored.
      if (!frame || event.source !== frame) return
      // Our frame must be sandboxed (opaque origin); a real origin means it is not.
      if (event.origin !== OPAQUE_ORIGIN) { kill("handshake"); return }
      // After connect the port is the only channel: a second hello (a reloaded frame that lost its
      // state) or any other window message from the frame ends the session.
      if (phase !== "waiting") { kill("handshake"); return }
      const data = event.data
      if (!object(data) || data.type !== "gridcraft:hello") return
      if (!shaped(data, ["type", "protocol", "bundle"]) || data.protocol !== BRIDGE_PROTOCOL || data.bundle !== session.bundle_digest) { kill("handshake"); return }
      // A hello before the frame's first load comes from a document the host has not seen load,
      // and that load would later be indistinguishable from a navigation: refuse it, port unsent.
      if (loads !== 1) { kill("handshake"); return }
      void connect(frame)
    },
    frameLoaded() {
      loads++
      // The first load is the editor document itself; any further one is a different document.
      if (phase === "connecting" || phase === "live" || loads > 1) kill("navigation")
    },
    notify(push) {
      if (phase === "killed" || !count(push?.rev) || !count(push?.generation)) return
      if (push.generation !== session.generation) { kill("generation"); return }
      if (push.rev <= confirmedRev && phase === "live") return
      void catchUp(push.rev)
    },
    online() {
      if (phase !== "live") return
      post({ type: "online", protocol: 1 })
      catchUpFailures = 0
      void catchUp(confirmedRev + 1)
      queue.resume()
      void drive()
    },
    offline() {
      if (phase !== "live") return
      post({ type: "offline", protocol: 1 })
      emit({ type: "offline" })
    },
    close(reason = "closed") { kill(reason) },
  }
}

// ---------------------------------------------------------------------------------------------
// HTTP transport

type Fetch = (url: string, init?: RequestInit & { cache?: RequestCache }) => Promise<Response>

/** `WorkbookTransport` over the workbook API of one page (`pageId`, not slug) and one session. */
export function createWorkbookHttpTransport(options: { workspaceId: string; pageId: string; sessionId: string; fetch: Fetch }): WorkbookTransport {
  const base = `/api/v1/pages/${encodeURIComponent("@" + options.pageId)}/workbook`
  const url = (path: string, query: Record<string, string> = {}) => `${base}${path}?${new URLSearchParams({ workspace_id: options.workspaceId, ...query })}`
  const call = async (target: string, init: RequestInit = {}): Promise<TransportResponse> => {
    const response = await options.fetch(target, { cache: "no-store", ...init })
    return { status: response.status, body: await response.json().catch(() => null) }
  }
  return {
    snapshot: () => call(url("")),
    edit: (_mutationId, body) => call(url("/edits"), { method: "POST", headers: { "content-type": "application/json" }, body }),
    commits: (since, limit) => call(url("/commits", { since: String(since), limit: String(limit) })),
    operation: mutationId => call(url(`/operations/${encodeURIComponent(mutationId)}`)),
    closeSession: async () => { await options.fetch(url(`/sessions/${encodeURIComponent(options.sessionId)}`), { method: "DELETE", cache: "no-store" }) },
  }
}
