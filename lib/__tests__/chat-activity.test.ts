import { describe, it, expect } from "vitest"
import type { TurnPart } from "@/hooks/use-chat"
import { parentToolIdOf, summarizeGroup, toolIdOf, toolState, needsAttention } from "../chat-activity"

const p = (type: TurnPart["type"], metadata: Record<string, unknown> = {}): TurnPart => ({
  id: Math.random().toString(36).slice(2),
  type,
  content: "",
  timestamp: new Date(),
  metadata,
})

describe("toolState", () => {
  it("maps results to succeeded / failed / denied", () => {
    const call = p("tool_call", { tool_id: "a" })
    expect(toolState({ call, result: p("tool_result") }, false)).toBe("succeeded")
    expect(toolState({ call, result: p("tool_result", { is_error: true }) }, false)).toBe("failed")
    expect(toolState({ call, result: p("tool_result", { denied: true, is_error: true }) }, false)).toBe("denied")
  })
  it("is running while the turn streams and unknown once it ended without a result", () => {
    const call = p("tool_call")
    expect(toolState({ call }, true)).toBe("running")
    expect(toolState({ call }, false)).toBe("unknown")
  })
  it("prefers the typed activity state", () => {
    const typed = (state: string) => ({ ...p("activity"), activity: { kind: "shell", state } }) as never
    expect(toolState({ call: typed("interrupted") }, true)).toBe("interrupted")
    expect(toolState({ call: typed("input_streaming") }, true)).toBe("running")
    expect(toolState({ call: typed("failed"), result: p("tool_result") }, false)).toBe("failed")
  })
  it("degrades a state token it does not know to unknown, never to success", () => {
    const typed = (state: string) => ({ ...p("activity"), activity: { kind: "shell", state } }) as never
    for (const state of ["paused", "SUCCEEDED", "constructor", "toString", "__proto__"]) {
      expect(toolState({ call: typed(state), result: p("tool_result") }, false)).toBe("unknown")
      expect(toolState({ call: typed(state) }, true)).toBe("unknown")
    }
    expect(summarizeGroup(["succeeded", toolState({ call: typed("paused") }, false)]).title).not.toBe("Worked")
  })
  it("a typed running step is unknown once the turn stopped streaming", () => {
    const typed = (state: string) => ({ ...p("activity"), activity: { kind: "shell", state } }) as never
    expect(toolState({ call: typed("running") }, true)).toBe("running")
    expect(toolState({ call: typed("running") }, false)).toBe("unknown")
    expect(toolState({ call: typed("input_streaming") }, false)).toBe("unknown")
  })
  it("flags approvals", () => {
    expect(needsAttention({ ...p("activity"), activity: { kind: "shell", state: "awaiting_approval" } } as never)).toBe(true)
    expect(needsAttention(p("tool_call"))).toBe(false)
  })
})

describe("summarizeGroup", () => {
  it("is ok only when every step succeeded", () => {
    expect(summarizeGroup(["succeeded", "succeeded"]).tone).toBe("ok")
  })
  it("never reads green with a failure inside", () => {
    const s = summarizeGroup(["succeeded", "failed", "denied"])
    expect(s.tone).toBe("attention")
    expect(s.title).toBe("Activity incomplete")
    expect(s.detail).toBe("1 failed, 1 denied")
  })
  it("unknown and interrupted are not success", () => {
    expect(summarizeGroup(["succeeded", "unknown"]).tone).toBe("neutral")
    expect(summarizeGroup(["succeeded", "unknown"]).title).not.toBe("Worked")
    expect(summarizeGroup(["interrupted"]).tone).toBe("attention")
  })
  it("running group stays running; approval takes precedence", () => {
    expect(summarizeGroup(["succeeded", "running"]).tone).toBe("running")
    expect(summarizeGroup(["running", "awaiting_approval"]).title).toBe("Needs approval")
  })
})

describe("correlation ids", () => {
  it("reads typed fields first, then the legacy wire keys", () => {
    expect(toolIdOf({ ...p("tool_call", { tool_id: "m" }), tool_call_id: "t" })).toBe("t")
    expect(toolIdOf(p("tool_result", { tool_use_id: "u" }))).toBe("u")
    expect(parentToolIdOf({ ...p("tool_call"), parent_tool_call_id: "t" })).toBe("t")
    expect(parentToolIdOf(p("tool_call", { parent_tool_use_id: "u" }))).toBe("u")
    expect(toolIdOf(p("tool_call"))).toBeUndefined()
  })
})
