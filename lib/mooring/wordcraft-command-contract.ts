/** Shared C1 DATA validation only. No network, credentials or authority. */
import { record } from "./channel"

export const COMMAND_FORMAT = "wordcraft-c1-v2"
export const COMMAND_ENGINE = "7ef2633a7e84956522d1ca47b99fe6feef16c1db"
export const COMMAND_MESSAGE_BYTES = 8 * 1024 * 1024
export const COMMAND_INPUT_BYTES = 65536
const bytes = (raw: string) => new TextEncoder().encode(raw).length
export const commandID = (v: unknown): v is string => typeof v === "string" && v.length > 0 && v.length <= 200 && bytes(v) <= 200 && !v.includes("\0")
const revision = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 0
const digest = (v: unknown): v is string => typeof v === "string" && /^sha256:[0-9a-f]{64}$/.test(v)
export const exactCommandKeys = (v: Record<string, unknown>, names: string[]) => Object.keys(v).length === names.length && names.every(name => name in v)
export interface CommandProjection { generation: string; revision: number; ids: string[]; native_json: string }
export interface CommandOutcome { op_id: string; fingerprint: string; input_digest: string; revision: number; receipt: Record<string, unknown> }
export interface CommandResponse {
  view: string; actor: string; format: typeof COMMAND_FORMAT; engine: typeof COMMAND_ENGINE
  projection: CommandProjection; outcome: CommandOutcome | null
  editor: { bundle_digest: string; url: string }
}
export function parseCommandProjection(value: unknown): CommandProjection {
  if (!record(value) || !exactCommandKeys(value, ["generation", "revision", "ids", "native_json"])
    || !commandID(value.generation) || value.generation.includes(":") || !revision(value.revision)
    || !Array.isArray(value.ids) || !value.ids.length || value.ids.length > 4096 || !value.ids.every(commandID) || new Set(value.ids).size !== value.ids.length
    || typeof value.native_json !== "string" || !value.native_json.length || value.native_json.length > 4 * 1024 * 1024 || bytes(value.native_json) > 4 * 1024 * 1024) throw new Error("invalid_projection")
  // Native JSON remains opaque bytes. The actual Rust native_io boundary
  // validates it; parsing/re-serializing here would round large native numbers.
  return value as unknown as CommandProjection
}
export function parseCommandResponse(value: unknown): CommandResponse {
  if (!record(value) || !exactCommandKeys(value, ["view", "actor", "format", "engine", "projection", "outcome", "editor"])
    || !commandID(value.view) || !commandID(value.actor) || !value.actor.startsWith("human:") || value.format !== COMMAND_FORMAT || value.engine !== COMMAND_ENGINE
    || !record(value.editor) || !exactCommandKeys(value.editor, ["bundle_digest", "url"]) || typeof value.editor.bundle_digest !== "string" || !/^[0-9a-f]{64}$/.test(value.editor.bundle_digest) || typeof value.editor.url !== "string") throw new Error("invalid_response")
  parseCommandProjection(value.projection)
  if (value.outcome !== null) {
    const o = value.outcome
    if (!record(o) || !exactCommandKeys(o, ["op_id", "fingerprint", "input_digest", "revision", "receipt"]) || !commandID(o.op_id) || !digest(o.fingerprint) || !digest(o.input_digest) || !revision(o.revision) || !record(o.receipt)) throw new Error("invalid_outcome")
  }
  return value as unknown as CommandResponse
}
/** RFC8785 uses ECMAScript number/string serialization and UTF16 key order.
 * This only emits our bounded, JSON-parsed command DATA; native P stays opaque. */
export function commandCanonicalJSON(value: unknown): string {
  const encode = (v: unknown): string => {
    if (Array.isArray(v)) return "[" + v.map(encode).join(",") + "]"
    // Serialize in sorted key order directly. Building another JS object
    // would enumerate integer-like properties numerically again (2 before10).
    if (record(v)) return "{" + Object.keys(v).sort().map(key => JSON.stringify(key) + ":" + encode(v[key])).join(",") + "}"
    if (v === null || typeof v === "string" || typeof v === "boolean" || (typeof v === "number" && Number.isFinite(v))) return JSON.stringify(v)
    throw new Error("invalid_command_json")
  }
  return encode(value)
}
export function commandOwnSelection(response: CommandResponse, original: string, inputDigest: string): string {
  const request: unknown = JSON.parse(original), out = response.outcome
  if (!record(request) || !out || out.input_digest !== inputDigest || !digest(inputDigest) || out.op_id !== request.op_id || out.revision !== response.projection.revision || out.revision !== Number(request.base_revision) + 1 || request.generation !== response.projection.generation || request.participant !== response.actor) throw new Error("unconfirmed_original")
  const r = out.receipt
  if (r.state !== "host_committed" || r.op_id !== out.op_id || r.actor !== response.actor || r.author !== response.actor.slice("human:".length) || r.fingerprint !== out.fingerprint || r.revision !== out.revision || !record(r.selection)) throw new Error("unconfirmed_original")
  const selection = JSON.stringify(r.selection)
  if (bytes(selection) > 2048 || !exactCommandKeys(r.selection, ["anchor", "focus"])) throw new Error("invalid_selection")
  for (const point of [r.selection.anchor, r.selection.focus]) {
    if (!record(point) || !exactCommandKeys(point, ["story", "path", "off"]) || point.story !== "body" || !Array.isArray(point.path) || point.path.length !== 1 || !revision(point.path[0]) || Number(point.path[0]) >= response.projection.ids.length || !revision(point.off)) throw new Error("invalid_selection")
  }
  return selection
}
