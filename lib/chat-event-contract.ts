import { z } from "zod"

const id = z.string().min(1).max(256)
const time = z.iso.datetime({ offset: true }).refine(value => /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(value) && Number.isFinite(Date.parse(value)))
const seq = z.number().int().nonnegative().max(Number.MAX_SAFE_INTEGER)
const ref = z.object({ kind: id, id, revision: id.optional() }).strict()
export const thinkingSchema = z.object({
  visibility: z.enum(["summary", "redacted", "unavailable"]),
  state: z.enum(["running", "completed", "cut_off", "unavailable"]),
  summary: z.string().max(65536).optional(),
  intervals: z.array(z.object({ started_at: time, ended_at: time.optional() }).strict()).max(1024).optional(),
}).strict().refine(value => value.visibility === "summary" || value.summary === undefined)
const detail = z.string().max(16384).refine(value => new TextEncoder().encode(value).byteLength <= 16384)
export const activitySchema = z.object({
  kind: id, name: id.optional(), parameters: z.record(z.string(), z.string().max(4096)).optional(),
  target: z.string().max(4096).optional(),
  state: z.enum(["input_streaming", "running", "awaiting_approval", "succeeded", "failed", "denied", "interrupted", "unknown"]),
  output_ref: ref.optional(), output: detail.optional(), diff: detail.optional(), truncated: z.boolean().optional(),
}).strict()
export const turnStateSchema = z.enum(["accepted", "queued", "starting", "running", "completed", "failed", "cancelled", "interrupted"])
export const outputSchema = z.object({ state: z.enum(["preparing", "partial", "available", "failed", "unavailable"]), ref: ref.optional(), label: z.string().max(4096).optional() }).strict()
const base = { version: z.literal(1), event_id: id, run_id: id, turn_id: id, part_id: id.optional(), seq: seq.positive(), occurred_at: time, delivery: z.enum(["ephemeral", "durable"]), tool_call_id: id.optional(), parent_tool_call_id: id.optional() }
export const chatEventSchema = z.discriminatedUnion("type", [
  z.object({ ...base, type: z.literal("text"), part_id: id, payload: z.object({ text: z.object({ content: z.string().max(65536), operation: z.enum(["delta", "replace"]) }).strict() }).strict() }).strict(),
  z.object({ ...base, type: z.literal("thinking"), part_id: id, payload: z.object({ thinking: thinkingSchema }).strict() }).strict(),
  z.object({ ...base, type: z.literal("activity"), part_id: id, tool_call_id: id, payload: z.object({ activity: activitySchema }).strict() }).strict(),
  z.object({ ...base, type: z.literal("turn"), payload: z.object({ turn: z.object({ state: turnStateSchema }).strict() }).strict() }).strict(),
  z.object({ ...base, type: z.literal("output"), part_id: id, payload: z.object({ output: outputSchema }).strict() }).strict(),
])
export type ChatEvent = z.infer<typeof chatEventSchema>
export type Thinking = z.infer<typeof thinkingSchema>
export type Activity = z.infer<typeof activitySchema>
export type TurnState = z.infer<typeof turnStateSchema>
export interface CanonicalPartFields {
  id?: string; run_id?: string; turn_id?: string; first_seq?: number; last_seq?: number
  tool_call_id?: string; parent_tool_call_id?: string
  thinking?: Thinking; activity?: Activity; output?: z.infer<typeof outputSchema>
}
export const checkpointSchema = z.object({ version: z.literal(1), run_id: id, turn_id: id, through_seq: seq, turn_state: turnStateSchema })
export type TurnCheckpoint = z.infer<typeof checkpointSchema> & { event?: ChatEvent }
export const terminalTurn = (state: TurnState) => ["completed", "failed", "cancelled", "interrupted"].includes(state)
export function parseChatEvent(value: unknown): ChatEvent | null {
  try {
    // Bound validation work as well as the pending-event buffer.
    if (JSON.stringify(value).length > 131072) return null
    const result = chatEventSchema.safeParse(value)
    return result.success ? result.data : null
  } catch { return null }
}
/** Persisted paired intervals are the only authority for displayed duration.
 * Tool activity and time between passes never enter this sum. */
export function reasoningSeconds(thinking?: Thinking): number | undefined {
  if (!thinking?.intervals?.length) return undefined
  let total = 0
  let previousEnd = -Infinity
  for (const interval of thinking.intervals) {
    if (!interval.ended_at) return undefined
    const start = Date.parse(interval.started_at), end = Date.parse(interval.ended_at)
    if (!Number.isFinite(start) || !Number.isFinite(end) || end < start || start < previousEnd) return undefined
    total += end - start
    previousEnd = end
  }
  return total / 1000
}
export function safeThinking(value: unknown): Thinking {
  const parsed = thinkingSchema.safeParse(value)
  return parsed.success ? parsed.data : { visibility: "unavailable", state: "unavailable" }
}
