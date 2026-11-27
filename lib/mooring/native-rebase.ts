import type { NativeExecuteOptions as ReplayExecuteOptions } from "./native-editor-port"
import { NATIVE_EXECUTE_MAX_BYTES, NATIVE_EXECUTE_MAX_COMMANDS, validExecuteLabel, type NativeExecuteCommand, type NativeExecuteConflict, type NativeExecuteOptions, type NativeExecuteResult } from "./native-editor-port"

/** One command from the operations log, as the server recorded it. */
export interface ReplayOp { command: string; params?: Record<string, unknown> }
/**
 * What an application brings to the replay: which of
 * its commands replay over the person's work, with an address that stays put. An application without one replays nothing.
 */
export interface ReplayAdapter { replayable(op: ReplayOp): boolean }

/**
 * Concurrent editing: the versions saved by others since the editor's base are
 * replayed over the person's unsaved work, and the result is saved on top of the newest one. The
 * person's cells win; the cells both changed come back as conflicts for the chat card. Anything the
 * editor cannot replay safely (a layout change on either side, a log that is missing or too large,
 * another engine) is not attempted: the person's work stays as it is and the host offers the choice.
 */
export interface RebaseDeps {
  /** The version the person's unsaved work is on. */
  base: string
  enginePin: string
  /** The editor's saved version the replay is based on (savedRevision); without one nothing is replayed. */
  savedRevision: string | undefined
  /** The application's replay rules; null: it replays nothing, and the person chooses. */
  replay: ReplayAdapter | null
  /** The editor runs the engine build the server runs (sameEngineBuild); otherwise nothing is replayed. */
  sameBuild: boolean
  /** GET …/craft/document/operations?from=<base>: status and parsed body. */
  operations(from: string, signal: AbortSignal): Promise<{ status: number; body: unknown }>
  execute(commands: NativeExecuteCommand[], options: NativeExecuteOptions, signal: AbortSignal): Promise<Pick<NativeExecuteResult, "revision" | "conflicts" | "conflictsTruncated" | "dirty" | "savedRevision">>
  /** The save controller's move of its base under the unsaved work. */
  rebaseBase(version: string, contentHash: string, result?: Awaited<ReturnType<RebaseDeps["execute"]>>): boolean
  /** The undo step's text, e.g. "Morgan: change". */
  label: string
  keep?: ReplayExecuteOptions["keep"]
  signal: AbortSignal
}

export type RebaseOutcome =
  | { kind: "rebased"; version: string; conflicts: NativeExecuteConflict[]; truncated: boolean; operationIds: string[]; actorName?: string }
  /** Nothing newer than the base. */
  | { kind: "current" }
  /** Not replayable over this work (layout, a version without a log): the person chooses; nothing was changed. */
  | { kind: "layout"; head: string; headHash?: string; actorName?: string }
  /** The editor cannot take it at all (too many versions, another engine, a refused or malformed answer): open the version. */
  | { kind: "reload"; reason: string }

const VERSION = /^[1-9][0-9]{0,18}$/
const HASH = /^sha256:[a-f0-9]{64}$/
const object = (v: unknown): v is Record<string, unknown> => Boolean(v) && typeof v === "object" && !Array.isArray(v)
const versionOf = (v: unknown): string | undefined => typeof v === "number" && Number.isSafeInteger(v) && v > 0 ? String(v) : typeof v === "string" && VERSION.test(v) ? v : undefined

interface ParsedLog { head: string; headHash?: string; replayable: boolean; /** Every version names the engine it was written with. */ pinned: boolean; pins: string[]; commands: NativeExecuteCommand[]; operationIds: string[]; actorName?: string }
function parse(body: unknown, base: string, replay: ReplayAdapter | null, agentWins: boolean): ParsedLog | undefined {
  if (!object(body) || !Array.isArray(body.versions)) return undefined
  const head = versionOf(body.head) ?? versionOf(body.to)
  if (!head || versionOf(body.from) !== base) return undefined
  const headHash = typeof body.head_content_hash === "string" && HASH.test(body.head_content_hash) ? body.head_content_hash : undefined
  let replayable = body.replayable === true, pinned = true, expected = BigInt(base)
  const pins: string[] = [], commands: NativeExecuteCommand[] = [], operationIds: string[] = []
  let actorName: string | undefined
  for (const v of body.versions) {
    if (!object(v)) return undefined
    const version = versionOf(v.version), from = versionOf(v.base_version)
    // The versions follow one another from the base, none missing.
    if (!version || from !== String(expected) || BigInt(version) !== expected + 1n) return undefined
    expected = BigInt(version)
    if (agentWins && v.actor_kind !== "agent") replayable = false
    if (typeof v.engine_pin === "string" && v.engine_pin.length > 0) pins.push(v.engine_pin)
    else pinned = false
    if (typeof v.operation_id === "string" && v.operation_id.length <= 128) operationIds.push(v.operation_id)
    if (v.actor_kind === "agent" && typeof v.actor_name === "string" && v.actor_name.trim() && v.actor_name.length <= 64) actorName = v.actor_name.trim()
    if (v.replayable !== true || !Array.isArray(v.ops)) { replayable = false; continue }
    for (const op of v.ops) {
      if (!object(op) || typeof op.command !== "string" || op.params !== undefined && !object(op.params) || !replay || !replay.replayable(op as unknown as ReplayOp)) { replayable = false; break }
      commands.push({ command: op.command as NativeExecuteCommand["command"], ...(op.params ? { params: op.params as Record<string, unknown> } : {}) })
    }
  }
  if (String(expected) !== head) return undefined
  return { head, headHash, replayable, pinned, pins, commands, operationIds, ...(actorName ? { actorName } : {}) }
}

export async function rebaseOverLocal(deps: RebaseDeps): Promise<RebaseOutcome> {
  if (!validExecuteLabel(deps.label)) return { kind: "reload", reason: "invalid_label" }
  const answer = await deps.operations(deps.base, deps.signal)
  if (answer.status === 413) return { kind: "reload", reason: "too_many" }
  if (answer.status !== 200) return { kind: "reload", reason: `operations_${answer.status}` }
  const log = parse(answer.body, deps.base, deps.replay, deps.keep === "agent")
  if (!log) return { kind: "reload", reason: "operations_malformed" }
  if (log.head === deps.base) return { kind: "current" }
  const named = log.actorName ? { actorName: log.actorName } : {}
  const layout = { kind: "layout" as const, head: log.head, ...(log.headHash ? { headHash: log.headHash } : {}), ...named }
  // A version replays only on the engine that wrote it: one without it, or from another, stops the
  // whole replay; the person's work stays as it is and they choose (Keep mine, or open the version).
  if (!log.replayable || log.commands.length === 0 || !log.pinned || log.pins.some(pin => pin !== deps.enginePin)) return layout
  // The content of the replay must come out the same in the editor and on the server.
  if (!deps.sameBuild) return layout
  // A replay keeping the person's cells is based on the saved version the editor confirmed, never on a guess.
  if (deps.savedRevision === undefined) return layout
  if (!log.headHash) return { kind: "reload", reason: "no_head_hash" }
  if (log.commands.length > NATIVE_EXECUTE_MAX_COMMANDS || new TextEncoder().encode(JSON.stringify(log.commands)).length > NATIVE_EXECUTE_MAX_BYTES) return { kind: "reload", reason: "too_large" }
  let result: Awaited<ReturnType<RebaseDeps["execute"]>>
  try {
    // The undo step carries the name the agent wrote under; the caller's label otherwise.
    const label = log.actorName ? `${log.actorName}: change`.slice(0, 80) : deps.label
    result = await deps.execute(log.commands, { keep: deps.keep ?? "human", label, baseRevision: deps.savedRevision }, deps.signal)
  } catch (error) {
    if (deps.signal.aborted) throw error
    const code = error instanceof Error ? error.message : ""
    // Refused atomically: the person's work is exactly as it was.
    if (code === "not_replayable") return layout
    return { kind: "reload", reason: code === "pin_mismatch" || code === "stale_base" ? code : "execute_failed" }
  }
  const moved = deps.keep === "agent" ? deps.rebaseBase(log.head, log.headHash, result) : deps.rebaseBase(log.head, log.headHash)
  if (!moved) return { kind: "reload", reason: "rebase_refused" }
  return { kind: "rebased", version: log.head, conflicts: result.conflicts ?? [], truncated: result.conflictsTruncated === true, operationIds: log.operationIds, ...named }
}

/**
 * Keep mine: the person's work is saved on top of the head as it is, without the
 * other change replayed; that change stays in the head's version history. Nothing moves without the head's hash.
 */
export function keepLocalOver(layout: { head: string; headHash?: string }, rebaseBase: (version: string, contentHash: string) => boolean): boolean {
  return layout.headHash !== undefined && HASH.test(layout.headHash) && rebaseBase(layout.head, layout.headHash)
}

/** The editor and the server run the same engine build: both name it, and the same one. A build not named is another. */
export function sameEngineBuild(editor: string | undefined, server: string | undefined): boolean {
  return editor !== undefined && server !== undefined && editor === server
}
