/**
 * Host side of the embedded workbook editor bridge (protocol 1).
 *
 * The editor runs in a sandboxed frame (served from a dedicated editor origin, or by default under
 * `/_editor/b/` of the Studio origin, always in an opaque origin) and is untrusted input, exactly like an HTTP
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
 *   when the head merely moved; `snapshot_required` is never retried), 429 on the artifact
 *   retries the same mutation, `403 effect_out_of_mandate` refuses the one edit. A refusal
 *   (`editorRefusal`: lost access, the editor turned off) destroys the session and the port;
 *   nothing retries it.
 * - `409 editor_session_expired` on an edit (the session expired, was closed, belongs to an older
 *   generation or was opened with another sign-in; servers before that code answered
 *   `401 session_expired`, read the same way) opens a new session through `reopenSession` and
 *   sends the pending edits again under it: same frame `seq`, base and ops, the new session's
 *   `mutation_id` (the server binds the id to its session). An edit that may have reached the old session is first looked up by
 *   its old id, and its receipt is taken instead of sending it again. Each expired session is
 *   replaced once; a replacement that expires again before the server accepted anything from it
 *   (within `BRIDGE_REOPEN_SETTLE_MS`) ends the editor as `expired`.
 * - The commits feed answering `410 snapshot_required` (history pruned) rereads the snapshot.
 * - Bundle pin: the plain lowercase hex SHA-256 of the bundle's manifest bytes, the same in the
 *   session's `bundle_digest`, the `/b/<digest>/` path of `editor_url` and the frame's hello. An
 *   empty or differently written pin (for example `sha256:` prefixed) never matches: fail closed.
 *   A well-formed hello naming another bundle (or none) ends the session as `bundle`, not as a
 *   broken handshake, so the host can say that the bundle pin is the cause.
 * - Open drafts: the editor may report which cells have an open, uncommitted cell editor
 *   (`draft`, addresses only, never content). A revision of another writer that changes such a
 *   cell is reported to the host at once (`onConflict` `changed`); the draft stays the editor's.
 * - Open refusals: every refused edit (a same-cell conflict, `snapshot_required`, an effect out of
 *   mandate, invalid ops, a full history, an unconfirmed write…) stays open in the bridge with all
 *   of its cells, the person's values and, for the cells another writer changed, theirs
 *   (`onRefusals`). It stays open until the person decides: `keepMine` writes every cell of it
 *   against the head as one host edit (mutation `<session_id>:h<n>`, reaching the editor as a
 *   committed revision); `takeTheirs` drops the conflicting cells and writes the others the same
 *   way (or drops the edit when no cell conflicted). A host edit that a snapshot reload overtakes
 *   comes back as an open refusal (`reloaded`) instead of being dropped. Only when no refusal is
 *   open and nothing is unsent does the save state leave "refused", and the editor reloads the
 *   head (the only message on which it drops its own refusal notices).
 * - Overflow: past `BRIDGE_MAX_REFUSALS` open refusals a new edit is refused at once
 *   (`too_many_pending`) and only the editor keeps its values. The bridge counts such edits (a
 *   number, never their values): they count as refused in the save state, as unsaved on kill, and
 *   hold back the head reload, until the person discards them (`discardOverflow`).
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
/** Refused edits the host holds open for the person's decision (the same bound). */
export const BRIDGE_MAX_REFUSALS = BRIDGE_MAX_PENDING
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
/** A replacement session must live this long (or have an edit accepted) before it may be replaced again. */
export const BRIDGE_REOPEN_SETTLE_MS = 60_000
/** Bridge pacing: a confirmed cell is sent at once, not on the document autosave cadence. */
export const BRIDGE_PACING: Partial<Pacing> = { minGapMs: 0, baseDelayMs: 250, maxDelayMs: 10_000 }

const CELL = /^[A-Z]{1,3}[1-9][0-9]{0,6}$/
const BUNDLE_DIGEST = /^[0-9a-f]{64}$/
/** A bundle pin as the server and the editor origin write it: plain lowercase hex SHA-256, no prefix. */
export const isBundleDigest = (value: unknown): value is string => typeof value === "string" && BUNDLE_DIGEST.test(value)
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

/**
 * `POST /api/v1/pages/{slug}/workbook/sessions`. The server opens sessions only on an imported
 * workbook (revision 1 or later). `bundle_digest` stays empty until an editor origin is configured.
 */
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

export type RejectCode = "precondition_failed" | "invalid_ops" | "conflict" | "history_quota_exceeded" | "too_many_pending" | "not_confirmed" | "effect_out_of_mandate"

/** Editor → host, after validation. */
export interface FrameMutation { seq: number; base_rev: number; ops: CellOp[] }
export interface CellRef { sheet: string; cell: string }
export type FrameMessage =
  | { type: "mutation"; mutation: FrameMutation }
  | { type: "resume"; last_rev: number; pending: FrameMutation[] }
  /** The cells with an open, uncommitted cell editor in the frame now (addresses only); empty when none. */
  | { type: "draft"; cells: CellRef[] }

/**
 * One cell of a refused edit: the person's value, and for a cell another writer changed meanwhile
 * the server's value with its revision and writer (absent for the edit's other cells).
 */
export interface RefusedCell { sheet: string; cell: string; mine: string; theirs?: string; rev?: number; actor_label?: string }
/** Why an edit was refused; `reloaded`: a snapshot reload overtook a keep-mine write before it was saved. */
export type RefusalReason = RejectCode | "reloaded"
/**
 * A refused edit waiting for the person's decision, with every cell it wrote. `id` names it for
 * `keepMine` and `takeTheirs`; `seq` is the editor's edit (absent once the host wrote it again).
 * `sending`: the decision is being written (no second decision until it settles). `retryable`:
 * writing the same cells again can succeed (not for invalid ops or an effect out of mandate).
 */
export interface Refusal { id: number; seq?: number; reason: RefusalReason; state: "open" | "sending"; retryable: boolean; cells: RefusedCell[] }
/** Another writer's revision changed a cell with an open draft (the draft is still in the editor). */
export type DraftConflict = { kind: "changed"; sheet: string; cell: string; input: string; rev: number; actor_label: string }

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
 * connect. `timeout`: no hello (or no snapshot) within `BRIDGE_HANDSHAKE_TIMEOUT_MS`. `bundle`:
 * the frame's hello named another bundle than the session's pin (or none).
 * `disabled`, `expired` and `access` are server refusals (`EditorRefusal`).
 */
export type KillReason = "handshake" | "bundle" | "protocol" | "generation" | "navigation" | "load" | "timeout" | EditorRefusal | "closed"

// ---------------------------------------------------------------------------------------------
// Validation

export function parseWorkbookSession(value: unknown): WorkbookSession | null {
  if (!object(value) || !text(value.session_id) || typeof value.actor_label !== "string" || !positive(value.rev) || !positive(value.generation)) return null
  if (value.protocol !== BRIDGE_PROTOCOL || !isBundleDigest(value.bundle_digest)) return null
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
  if (value.type === "draft") {
    if (!shaped(value, ["type", "protocol", "cells"]) || !Array.isArray(value.cells) || value.cells.length > BRIDGE_MAX_OPS) return { ok: false, reason: "protocol" }
    const cells: CellRef[] = []
    for (const item of value.cells) {
      if (!object(item) || !shaped(item, ["sheet", "cell"]) || !text(item.sheet, BRIDGE_MAX_SHEET_LENGTH) || typeof item.cell !== "string" || !CELL.test(item.cell)) return { ok: false, reason: "protocol" }
      cells.push({ sheet: item.sheet, cell: item.cell })
    }
    return { ok: true, value: { type: "draft", cells } }
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

/** What `reopenSession` found: a new session for the same Page, or the refusal that ended the old one for good. */
export type ReopenResult = { ok: true; session: WorkbookSession; transport: WorkbookTransport } | { ok: false; refusal?: EditorRefusal }

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
  /** Another writer changed a cell with the person's open draft (see `DraftConflict`). */
  onConflict?: (conflict: DraftConflict) => void
  /**
   * The refused edits waiting for the person's decision, after every change to them; `overflow`:
   * edits refused past `BRIDGE_MAX_REFUSALS` whose values only the editor holds.
   */
  onRefusals?: (refusals: Refusal[], overflow: number) => void
  /** The editor reported which cells have an open draft now (after every `draft` message). */
  onDrafts?: (cells: CellRef[]) => void
  /** The editor got a fresh snapshot: warnings about the content it replaced no longer apply (open refusals do). */
  onReload?: () => void
  /** The session is over: destroy the frame. `unsaved` writes never got a receipt. */
  onKill?: (kill: { reason: KillReason; unsaved: number }) => void
  /** Opens a new session after `editor_session_expired`; without it an expired session ends the editor. */
  reopenSession?: () => Promise<ReopenResult>
  /** Tests only; defaults to `BRIDGE_HANDSHAKE_TIMEOUT_MS`. */
  handshakeTimeoutMs?: number
}

export type BridgePhase = "waiting" | "connecting" | "live" | "killed"

export interface WorkbookBridge {
  readonly phase: BridgePhase
  /** The session edits go out under now; it changes when an expired session is replaced. */
  readonly session: WorkbookSession
  /** Last revision delivered to the editor, in order. */
  readonly confirmedRev: number
  /** Mutations without a receipt. */
  readonly pending: number
  /** Refused edits waiting for the person's decision, oldest first. */
  readonly refusals: Refusal[]
  /** Edits refused past `BRIDGE_MAX_REFUSALS`: only the editor holds their values. */
  readonly overflow: number
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
  /** The host learned that this session expired (the heartbeat): replace it as an edit's `editor_session_expired` would. */
  sessionExpired(sessionId?: string): void
  /**
   * The person keeps their values for refusal `id`: every cell of the refused edit is written
   * against the head as one host edit. False when the session is not live, the refusal is not
   * open, or it cannot succeed again (`retryable` false).
   */
  keepMine(id: number): boolean
  /**
   * The person takes the server's values for refusal `id`: its conflicting cells are dropped and
   * its other cells written against the head as one host edit; with no conflicting cell the whole
   * edit is dropped. Once no refusal is open and nothing is unsent, the save state leaves
   * "refused" and, when no cell editor is open, the editor reloads the head, the only message on
   * which it drops its own refusal notices. False when the session is not live or the refusal is
   * not open.
   */
  takeTheirs(id: number): boolean
  /**
   * The person discards the edits refused past `BRIDGE_MAX_REFUSALS` (only the editor holds them):
   * the count is cleared and the editor reloads the head once nothing else is open or unsent. False
   * when the session is not live or there is nothing to discard.
   */
  discardOverflow(): boolean
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
  /** Ids this write had under sessions that expired; a receipt for any of them settles it. */
  aliases: string[]
  /** Of those, the ids that may have reached the server: their status is asked before sending. */
  priors: string[]
  /** A keep-mine edit of the host (`<session>:h<n>`), not the editor's: `seq` is 0 and the editor hears only its revision. */
  host?: number
  /** The refusal a host edit writes the person's decision for. */
  refusal?: number
}

type Outcome = { type: "ack"; rev: number } | { type: "rejected"; message: HostMessage }

type Handling =
  | { action: "retry" }
  | { action: "kill"; reason: KillReason }
  | { action: "reject"; code: RejectCode; cells?: WorkbookConflict[] }
  | { action: "reconcile"; headRev: number; cells: WorkbookConflict[]; final: boolean }
  | { action: "snapshot" }
  /** `sessionId`: the session the refused attempt went out under. */
  | { action: "renew"; sessionId: string }

/** A failed send: how the write queue counts it, and what the bridge does about it. */
class BridgeFailure extends Error {
  constructor(readonly failure: SendFailure, readonly handling: Handling) { super(failure.kind) }
}
const fail = (failure: SendFailure, handling: Handling): never => { throw new BridgeFailure(failure, handling) }

function retryAfter(body: unknown): number {
  return object(body) && count(body.retry_after_ms) ? body.retry_after_ms : BRIDGE_DEFAULT_RETRY_MS
}

/** Maps a non-200 edit answer, sent under `sessionId`, onto queue accounting and bridge handling. */
function editFailure(response: TransportResponse, sessionId: string): BridgeFailure {
  const { status, body } = response
  const error = object(body) && typeof body.error === "string" ? body.error : ""
  // An edit whose recalculation could not be confirmed (volatile formulas): only this edit is refused.
  if (status === 403 && error === "effect_out_of_mandate") return new BridgeFailure({ kind: "rejected" }, { action: "reject", code: "effect_out_of_mandate" })
  const refusal = editorRefusal(response)
  if (refusal === "expired") return new BridgeFailure({ kind: "transient", retryAfterMs: 0 }, { action: "renew", sessionId })
  if (refusal) return new BridgeFailure({ kind: "denied" }, { action: "kill", reason: refusal })
  if (status === 400) return new BridgeFailure({ kind: "rejected" }, { action: "reject", code: "invalid_ops" })
  if (status === 410) return new BridgeFailure({ kind: "conflict" }, { action: "snapshot" })
  if (status === 409) {
    if (error === "stale_generation") return new BridgeFailure({ kind: "denied" }, { action: "kill", reason: "generation" })
    if (error === "in_progress") return new BridgeFailure({ kind: "transient", retryAfterMs: retryAfter(body) }, { action: "retry" })
    if (error === "precondition_failed") {
      const cells = object(body) ? parseConflicts(body.conflicts) : null
      const headRev = object(body) && count(body.head_rev) ? body.head_rev : 0
      // `snapshot_required`: the same packet can never succeed (an import or pruned history in between).
      const final = object(body) && body.snapshot_required === true
      if (cells) return new BridgeFailure({ kind: "conflict" }, { action: "reconcile", headRev, cells, final })
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
  let { session, transport } = options
  const timers = options.timers ?? { now: () => Date.now(), setTimeout: (run, ms) => setTimeout(run, ms), clearTimeout: handle => clearTimeout(handle as ReturnType<typeof setTimeout>) }
  const emit = (event: SaveEvent) => { if (phase !== "killed" || event.type === "denied") options.onSaveEvent?.(event) }
  let mutationPrefix = `${session.session_id}:`
  /** Every session this bridge sent edits under: their revisions in the feed are the editor's own. */
  const ownSessions = new Set([session.session_id])
  let renewing: Promise<void> | undefined, renewRuns = 0
  /** When the current session replaced an expired one, until the server accepted an edit under it. */
  let reopenedAt: number | undefined

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
  let catching: Promise<void> | undefined, catchRuns = 0
  let catchUpTimer: unknown, catchUpFailures = 0
  let driving = false, driveTimer: unknown
  /** Receipt revisions waiting for earlier revisions to reach the editor: rev → seq. */
  const held = new Map<number, number>()
  /** Every seq this session accepted from the editor, by epoch. */
  const known = new Map<number, { epoch: number; frameKey: string }>()
  /** What the editor was told about a seq, to answer a restatement the same way. */
  const outcomes = new Map<number, Outcome>()
  /** Cells with an open draft in the editor, as `sheet\u0000cell`. */
  let drafts = new Set<string>()
  let hostWrites = 0
  const cellKey = (sheet: string, cell: string) => `${sheet}\u0000${cell}`
  /** Refused edits waiting for the person's decision (or being written after it), in refusal order. */
  const refusals = new Map<number, Refusal>()
  let refusalIds = 0
  /** The person took theirs: reload the head once no refusal is open and nothing is unsent. */
  let reloadWanted = false, reloading = false
  /** Edits refused past the refusal bound: the editor alone holds their values (a count only). */
  let overflow = 0
  const openRefusals = () => [...refusals.values()].filter(refusal => refusal.state === "open").length
  const copyRefusals = () => [...refusals.values()].map(refusal => ({ ...refusal, cells: refusal.cells.map(cell => ({ ...cell })) }))

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
    // An open refusal holds the person's values only here: they are lost with the session too.
    const unsaved = queue.size + openRefusals() + overflow
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
    if (!commit.session_id || !ownSessions.has(commit.session_id)) return undefined
    const prefix = `${commit.session_id}:`
    if (!commit.mutation_id?.startsWith(prefix)) return undefined
    const seq = Number(commit.mutation_id.slice(prefix.length))
    return positive(seq) && String(seq) === commit.mutation_id.slice(prefix.length) ? seq : undefined
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
    const ours = seq !== undefined && known.get(seq)?.epoch === epoch
    // An edit refused as unconfirmed may still have committed: the editor then needs its value.
    if (ours && outcomes.get(seq)?.type !== "rejected") { ackFrame(seq, commit.rev); settle(seq) }
    else {
      post({ type: "committed", protocol: 1, rev: commit.rev, actor_label: commit.actor_label, ops: commit.ops.map(op => ({ op: "cell.set", ...op })) })
      // ...and then nothing of it is left for the person to decide.
      if (ours) for (const refusal of refusals.values()) if (refusal.seq === seq && refusal.state === "open") { refusals.delete(refusal.id); reportRefusals() }
      // Another writer, or another window of the same person, changed a cell being edited here.
      if (!(commit.session_id && ownSessions.has(commit.session_id))) for (const op of commit.ops) if (drafts.has(cellKey(op.sheet, op.cell))) options.onConflict?.({ kind: "changed", sheet: op.sheet, cell: op.cell, input: op.input, rev: commit.rev, actor_label: commit.actor_label })
    }
    held.delete(commit.rev)
    confirmedRev = commit.rev
    flushHeld()
  }

  /**
   * Loads the head snapshot: queued edits made against the old content are discarded. `idle`: only
   * while nothing is unsent and no cell editor is open, checked again once the head is read.
   */
  async function reloadSnapshot(idle = false): Promise<boolean> {
    let response: TransportResponse
    try { response = await transport.snapshot() } catch { return false }
    if (phase === "killed") return false
    if (idle && (phase !== "live" || queue.size > 0 || refusals.size > 0 || overflow > 0 || drafts.size > 0 || catching)) return false
    const refusal = editorRefusal(response)
    // Nothing is unsent on an idle reload: a session that expired unnoticed is replaced, then the reload tried again.
    if (refusal === "expired" && idle) { void renew(session.session_id).then(() => { reloading = false; maybeReload() }); return false }
    if (refusal) { kill(refusal); return false }
    const snapshot = response.status === 200 ? parseSnapshot(response.body) : null
    if (!snapshot) return false
    if (snapshot.generation !== session.generation) { kill("generation"); return false }
    // A revision the editor got while the head was read must not be taken back.
    if (idle && snapshot.rev < confirmedRev) return false
    epoch++
    const head = queue.head()
    const discarded: number[] = [], overtaken: BridgeWrite[] = []
    for (const write of queue.writes()) if (queue.remove(write.id)) { if (write.host) overtaken.push(write); else discarded.push(write.seq) }
    // The write in flight belongs to the old content too; its outcome is no longer the editor's.
    // A host write in flight stays the person's: its answer settles it or opens it again (`handle`).
    if (head && queue.head() === head && !head.host) discarded.unshift(head.seq)
    emit({ type: "recount", pending: queue.size })
    held.clear()
    confirmedRev = baseRev = snapshot.rev
    wantRev = Math.max(wantRev, snapshot.rev)
    post({ type: "snapshot", protocol: 1, rev: snapshot.rev, generation: snapshot.generation, snapshot: snapshot.snapshot, discarded })
    reloadWanted = false
    // The person's keep-mine choice was made against the old content: they decide again, it is not dropped.
    for (const write of overtaken) refuse(write, "reloaded")
    options.onReload?.()
    return true
  }

  function scheduleCatchUp() {
    timers.clearTimeout(catchUpTimer)
    catchUpFailures++
    const ms = Math.min(10_000, 250 * 2 ** Math.min(catchUpFailures - 1, 6))
    catchUpTimer = timers.setTimeout(() => { catchUpTimer = undefined; void catchUp(0) }, ms)
  }

  /**
   * Reads the commits feed until the editor holds `rev` (and the head the feed reports). One read
   * loop at a time; a call while it runs only raises the target. The loop starts after a tick, so
   * `catching` is always set before the loop can finish and clear it: a loop that had nothing to
   * do must never be left behind as a settled promise that later calls would wait on instead of
   * reading the feed.
   */
  function catchUp(rev: number): Promise<void> {
    wantRev = Math.max(wantRev, rev)
    if (phase !== "live") return Promise.resolve()
    if (catching) return catching
    if (wantRev <= confirmedRev) return Promise.resolve()
    const run = ++catchRuns
    catching = (async () => {
      await undefined
      try {
        while (phase === "live" && wantRev > confirmedRev) {
          const since = confirmedRev
          let response: TransportResponse
          try { response = await transport.commits(since, BRIDGE_COMMITS_LIMIT) } catch { scheduleCatchUp(); return }
          if (phase !== "live") return
          const refusal = editorRefusal(response)
          if (refusal) { kill(refusal); return }
          if (response.status === 410) {
            // History before `min_since` was pruned: only the snapshot can bring the editor forward.
            if (object(response.body) && count(response.body.head_rev)) wantRev = Math.max(wantRev, response.body.head_rev)
            if (!(await reloadSnapshot())) { if (phase === "live") scheduleCatchUp(); return }
            continue
          }
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
        if (catchRuns === run) { catching = undefined; maybeReload() }
      }
    })()
    return catching
  }

  // ----- sending ------------------------------------------------------------------------------

  /** A receipt for this write under its current id or any id it had under an expired session. */
  function receiptOf(value: unknown, write: BridgeWrite): WorkbookReceipt | null {
    for (const id of [write.id, ...write.aliases]) {
      const receipt = parseDurableReceipt(value, id)
      if (receipt) return receipt
    }
    return null
  }

  async function send(write: BridgeWrite): Promise<unknown> {
    // Ids under expired sessions that may have committed: find out before sending under the new one.
    while (write.priors.length) {
      const id = write.priors[0]
      let status: TransportResponse | undefined
      try { status = await transport.operation(id) } catch { /* unknown: ask again later */ }
      if (!status) return fail({ kind: "offline" }, { action: "retry" })
      if (status.status === 404) { write.priors.shift(); continue } // it never arrived
      const refusal = editorRefusal(status)
      if (refusal && refusal !== "expired") fail({ kind: "denied" }, { action: "kill", reason: refusal })
      if (status.status === 200 && object(status.body)) {
        const receipt = parseDurableReceipt(status.body.receipt, id)
        if (receipt) return receipt
      }
      return fail({ kind: "transient", retryAfterMs: BRIDGE_DEFAULT_RETRY_MS }, { action: "retry" })
    }
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
    const sentUnder = session.session_id
    try { response = await transport.edit(write.id, write.body) } catch { write.uncertain = true; return fail({ kind: "offline" }, { action: "retry" }) }
    // The session answered something other than "expired": it is a working session.
    if (sentUnder === session.session_id && editorRefusal(response) !== "expired") reopenedAt = undefined
    if (response.status === 200) return response.body
    if (response.status >= 500) write.uncertain = true
    throw editFailure(response, sentUnder)
  }

  /**
   * Accepts only a durable receipt for this exact mutation. Anything else is retried with the
   * same bytes (asking its status first) and, after `BRIDGE_MAX_UNCONFIRMED` attempts, refused
   * as `not_confirmed` so the queue moves on to later mutations.
   */
  function verify(result: unknown, write: BridgeWrite): boolean {
    const receipt = receiptOf(result, write)
    if (!receipt) {
      // The commits feed already showed it committed while this attempt was in flight.
      if (outcomes.get(write.seq)?.type === "ack") return true
      write.uncertain = true
      if (++write.unconfirmed < BRIDGE_MAX_UNCONFIRMED) return fail({ kind: "transient" }, { action: "retry" })
      return fail({ kind: "unconfirmed" }, { action: "reject", code: "not_confirmed" })
    }
    // A host write with a receipt is saved, whatever reload happened since: its refusal is decided.
    if (write.host) settleHost(write)
    if (write.epoch !== epoch || phase !== "live") return true
    const rev = receipt.rev
    if (rev <= confirmedRev) return true // the commits feed delivered it already
    // The editor never sent a host edit: it learns the revision from the feed, like any other.
    if (write.host) { void catchUp(rev); return true }
    if (rev === confirmedRev + 1) { confirmedRev = rev; ackFrame(write.seq, rev); flushHeld(); return true }
    held.set(rev, write.seq)
    void catchUp(rev)
    return true
  }

  // ----- open refusals -----------------------------------------------------------------------

  function reportRefusals() {
    if (phase === "killed") return
    options.onRefusals?.(copyRefusals(), overflow)
    emit({ type: "refusals", open: openRefusals() + overflow })
  }

  /** Every cell of a refused write with the person's value; the server's where another writer changed it. */
  function refusalCells(write: BridgeWrite, conflicts: WorkbookConflict[]): RefusedCell[] {
    const { ops } = JSON.parse(write.body) as { ops: ServerCellOp[] }
    return ops.map(({ sheet, cell, input }) => {
      const theirs = conflicts.find(conflict => conflict.cell === cell && (conflict.sheet === undefined || conflict.sheet === sheet))
      return theirs ? { sheet, cell, mine: input, theirs: theirs.input, rev: theirs.rev, actor_label: theirs.actor_label } : { sheet, cell, mine: input }
    })
  }

  /** Opens a refusal for `write`; a host write opens the refusal it was deciding again, with its cells. */
  function refuse(write: BridgeWrite, reason: RefusalReason, conflicts: WorkbookConflict[] = []) {
    const fields = { reason, state: "open" as const, retryable: reason !== "invalid_ops" && reason !== "effect_out_of_mandate", cells: refusalCells(write, conflicts) }
    if (write.host && write.refusal !== undefined) refusals.set(write.refusal, { id: write.refusal, ...fields })
    else { const id = ++refusalIds; refusals.set(id, { id, seq: write.seq, ...fields }) }
    reportRefusals()
  }

  /** A host write got its receipt: the refusal it wrote the decision for is over. */
  function settleHost(write: BridgeWrite) {
    if (write.refusal !== undefined && refusals.delete(write.refusal)) reportRefusals()
  }

  /** Writes `cells` of `refusal` (the person's values) against the head as one host edit. */
  function writeDecision(refusal: Refusal, cells: RefusedCell[]): boolean {
    if (queue.size >= BRIDGE_MAX_PENDING) return false
    const ops: ServerCellOp[] = cells.map(({ sheet, cell, mine }) => ({ kind: "cell.set", sheet, cell, input: mine }))
    const host = ++hostWrites
    const id = `${mutationPrefix}h${host}`
    refusal.state = "sending"
    queue.enqueue({
      id, seq: 0, host, refusal: refusal.id, frameKey: "host", epoch, attempted: false, uncertain: false, rebases: 0, unconfirmed: 0, aliases: [], priors: [],
      body: JSON.stringify({ session_id: session.session_id, mutation_id: id, base_rev: confirmedRev, generation: session.generation, ops }),
    })
    emit({ type: "edit", pending: queue.size })
    reportRefusals()
    void drive()
    return true
  }

  /** After the person took theirs: once nothing is open, unsent or being edited, the editor reloads the head. */
  function maybeReload() {
    // An overflow edit lives only in the editor's notice, which the reload would drop.
    if (!reloadWanted || reloading || phase !== "live" || queue.size > 0 || refusals.size > 0 || overflow > 0 || drafts.size > 0 || catching) return
    reloading = true
    void reloadSnapshot(true).finally(() => { reloading = false })
  }

  /** `hold`: keep it as an open refusal (not past the bound, where only the editor keeps it and the bridge counts it). */
  function reject(write: BridgeWrite, code: RejectCode, cells: WorkbookConflict[] = [], hold = true) {
    queue.remove(write.id)
    emit({ type: "recount", pending: queue.size })
    if (write.host) { refuse(write, write.epoch === epoch ? code : "reloaded", cells); return }
    if (write.epoch !== epoch) return
    if (hold) refuse(write, code, code === "precondition_failed" ? cells : [])
    else { overflow++; emit({ type: "rejected" }); reportRefusals() }
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
      // The person's keep-mine choice is not the editor's to discard: it is theirs to make again.
      if (write.host) refuse(write, "reloaded")
      queue.resume()
      return
    }
    switch (handling.action) {
      case "retry": return
      case "kill": kill(handling.reason); return
      case "reject": reject(write, handling.code); queue.resume(); return
      case "snapshot": await reloadSnapshot(); queue.resume(); return
      case "renew":
        await renew(handling.sessionId)
        if (phase === "live") queue.resume()
        return
      case "reconcile":
        // The editor first sees what the server has, in order; then the verdict on its write.
        await catchUp(handling.headRev)
        if (phase !== "live") return
        if (handling.cells.length || handling.final) reject(write, "precondition_failed", handling.cells)
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
        if (renewing) { await renewing; continue }
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
    maybeReload()
  }

  // ----- session renewal ----------------------------------------------------------------------

  function editBody(write: BridgeWrite): string {
    const { base_rev, generation, ops } = JSON.parse(write.body) as { base_rev: number; generation: number; ops: unknown }
    return JSON.stringify({ session_id: session.session_id, mutation_id: write.id, base_rev, generation, ops })
  }

  /**
   * Replaces the expired session `expiredId` once: a new session of the same generation and bundle,
   * and every pending write moved to it (same seq, base and ops; the new session's mutation id).
   */
  function renew(expiredId: string): Promise<void> {
    if (phase === "killed") return Promise.resolve()
    // Already replaced (the heartbeat and an edit can both find out): use the new one.
    if (expiredId !== session.session_id) return renewing ?? Promise.resolve()
    if (renewing) return renewing
    const settled = reopenedAt === undefined || timers.now() - reopenedAt >= BRIDGE_REOPEN_SETTLE_MS
    if (!options.reopenSession || !settled) { kill("expired"); return Promise.resolve() }
    const reopen = options.reopenSession
    const run = ++renewRuns
    renewing = (async () => {
      await undefined
      try {
        let result: ReopenResult
        try { result = await reopen() } catch { result = { ok: false } }
        const discard = () => { if (result.ok) void result.transport.closeSession().catch(() => {}) }
        if ((phase as BridgePhase) === "killed") { discard(); return }
        if (!result.ok) { kill(result.refusal ?? "expired"); return }
        // The frame holds this generation's content and runs this bundle: anything else needs a new frame.
        if (result.session.generation !== session.generation) { discard(); kill("generation"); return }
        if (!isBundleDigest(result.session.bundle_digest) || result.session.bundle_digest !== session.bundle_digest) { discard(); kill("disabled"); return }
        const expired = transport
        session = result.session
        transport = result.transport
        // The old session is over; closing it is only bookkeeping on the server.
        void expired.closeSession().catch(() => {})
        mutationPrefix = `${session.session_id}:`
        ownSessions.add(session.session_id)
        reopenedAt = timers.now()
        for (const write of queue.writes()) {
          write.aliases.push(write.id)
          if (write.attempted) write.priors.push(write.id)
          write.id = write.host ? `${mutationPrefix}h${write.host}` : `${mutationPrefix}${write.seq}`
          write.body = editBody(write)
          write.uncertain = false
        }
      } finally {
        if (renewRuns === run) renewing = undefined
      }
    })()
    return renewing
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
      id, seq: mutation.seq, frameKey, epoch, attempted: false, uncertain: false, rebases: 0, unconfirmed: 0, aliases: [], priors: [],
      body: JSON.stringify({ session_id: session.session_id, mutation_id: id, base_rev: mutation.base_rev, generation: session.generation, ops: mutation.ops.map(({ sheet, cell, input }) => ({ kind: "cell.set", sheet, cell, input })) }),
    }
    // Unsent edits and open refusals are both bounded: the frame is untrusted and could refuse itself
    // into unbounded memory. Past the refusal bound only the editor keeps the refused edit.
    if (queue.size >= BRIDGE_MAX_PENDING || refusals.size >= BRIDGE_MAX_REFUSALS) { reject(write, "too_many_pending", [], refusals.size < BRIDGE_MAX_REFUSALS); return true }
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
    if (message.type === "draft") {
      drafts = new Set(message.cells.map(ref => cellKey(ref.sheet, ref.cell)))
      options.onDrafts?.(message.cells)
      return
    }
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
    // - the host page's CSP `frame-src` admits only the editor (the dedicated editor origin, or
    //   only the `/_editor/b/` path of the Studio origin, which the server always sandboxes), so
    //   even a frame that navigated itself right after its hello holds a sandboxed editor document.
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
    get session() { return session },
    get confirmedRev() { return confirmedRev },
    get pending() { return queue.size },
    get refusals() { return copyRefusals() },
    get overflow() { return overflow },
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
      // No pin (no editor origin configured) or one written differently never matches: fail closed.
      if (!shaped(data, ["type", "protocol", "bundle"]) || data.protocol !== BRIDGE_PROTOCOL || !isBundleDigest(session.bundle_digest)) { kill("handshake"); return }
      // A well-formed hello from another bundle (or one that cannot name its own): the bundle pin is the cause.
      if (data.bundle !== session.bundle_digest) { kill("bundle"); return }
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
    keepMine(id) {
      const refusal = refusals.get(id)
      if (phase !== "live" || !refusal || refusal.state !== "open" || !refusal.retryable) return false
      return writeDecision(refusal, refusal.cells)
    },
    takeTheirs(id) {
      const refusal = refusals.get(id)
      if (phase !== "live" || !refusal || refusal.state !== "open") return false
      // Their value wins only where they wrote; the person's other cells of the same edit are still written.
      const conflicting = refusal.cells.some(cell => cell.theirs !== undefined)
      const rest = conflicting ? refusal.cells.filter(cell => cell.theirs === undefined) : []
      if (rest.length) { if (!writeDecision(refusal, rest)) return false }
      else { refusals.delete(id); reportRefusals() }
      reloadWanted = true
      maybeReload()
      return true
    },
    discardOverflow() {
      if (phase !== "live" || overflow === 0) return false
      overflow = 0
      reportRefusals()
      reloadWanted = true
      maybeReload()
      return true
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
    sessionExpired(sessionId = session.session_id) {
      if (phase !== "live") return
      void renew(sessionId).then(() => { if (phase === "live") { queue.resume(); void drive() } })
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
