import { describe, expect, it } from "vitest"
import { parseChatEvent, reasoningSeconds } from "../chat-event-contract"
const event = { version: 1, event_id: "e", run_id: "r", turn_id: "t", part_id: "p", seq: 1, occurred_at: "2026-10-18T12:00:00.123456789Z", delivery: "durable", type: "text", payload: { text: { content: "same", operation: "delta" } } }
describe("closed v1 event contract", () => {
  it("accepts exact server identity and nanosecond timestamp", () => { expect(parseChatEvent(event)).toEqual(event) })
  it.each([
    { type: "activity", payload: { activity: { kind: "shell", state: "running" } } },
    { version: 2 }, { seq: Number.MAX_SAFE_INTEGER + 1 }, { seq: -1 }, { seq: 1.5 }, { occurred_at: "invalid" }, { occurred_at: "2026-02-30T12:00:00Z" },
    { payload: { text: { content: "safe", operation: "delta" }, thinking: { visibility: "summary", state: "completed", summary: "unexpected" } } },
    { type: "thinking", payload: { thinking: { visibility: "redacted", state: "completed", summary: "must not pass" } } },
  ])("rejects malformed identity or mixed payload %j", patch => { expect(parseChatEvent({ ...event, ...patch })).toBeNull() })
  it("counts only persisted paired reasoning intervals, excluding the tool gap", () => {
    expect(reasoningSeconds({ visibility: "summary", state: "completed", intervals: [
      { started_at: "2026-10-18T12:00:00Z", ended_at: "2026-10-18T12:00:02Z" },
      { started_at: "2026-10-18T12:01:00Z", ended_at: "2026-10-18T12:01:03Z" },
    ] })).toBe(5)
  })
  it("never invents a duration for absent or open intervals", () => {
    expect(reasoningSeconds()).toBeUndefined()
    expect(reasoningSeconds({ visibility: "unavailable", state: "running", intervals: [{ started_at: "2026-10-18T12:00:00Z" }] })).toBeUndefined()
  })
})

describe("safe bounded activity details", () => {
  const activity = (details: Record<string, unknown>) => ({ ...event, type: "activity", tool_call_id: "call", payload: { activity: { kind: "read", state: "succeeded", ...details } } })
  it("accepts the frozen typed detail fields at the exact UTF8 limit", () => {
    expect(parseChatEvent(activity({ output: "é".repeat(8192), diff: "safe diff", truncated: true }))).not.toBeNull()
  })
  it.each([ { output: "é".repeat(8193) }, { diff: "x".repeat(16385) }, { output: 4 }, { truncated: "true" } ])("rejects oversized or malformed details %#", details => {
    expect(parseChatEvent(activity(details))).toBeNull()
  })
})
