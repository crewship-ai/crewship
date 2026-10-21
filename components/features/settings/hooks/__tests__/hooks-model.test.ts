import { describe, it, expect } from "vitest"

import { HOOK_EVENTS, draftFrom, draftProblems, eventInfo, hookMatches, outcomeTone, runsByHook, shortTarget, writeBody, DEFAULT_HOOK_FILTERS, type HookRow } from "../hooks-model"

const hook = (over: Partial<HookRow> = {}): HookRow => ({
  id: "hk1", workspace_id: "ws", event: "post_agent_stop", handler_kind: "http", handler_config: { url: "https://hooks.slack.com/services/T/B/x" },
  enabled: true, blocking: false, created_at: "2026-10-12T10:00:00Z", updated_at: "2026-10-12T10:00:00Z", ...over,
})

describe("event catalogue", () => {
  it("offers the 14 dispatchable events, and only pre_* with a veto can block", () => {
    expect(HOOK_EVENTS).toHaveLength(14)
    expect(HOOK_EVENTS.filter((e) => e.canBlock).map((e) => e.id).sort()).toEqual(
      ["pre_agent_start", "pre_llm_call", "pre_memory_write", "pre_peer_conversation", "pre_task_delegation"])
  })
  it("names a retired event honestly", () => {
    expect(eventInfo("pre_tool_call").label).toMatch(/never fires/)
  })
})

describe("runsByHook", () => {
  it("groups journal entries per hook, newest first, keeping outcome and latency", () => {
    const runs = runsByHook([
      { ts: "2026-10-12T10:00:00Z", entry_type: "hook.fired", payload: { hook_id: "hk1", outcome: "pass", latency_ms: 12 } },
      { ts: "2026-10-12T11:00:00Z", entry_type: "hook.fired", payload: { hook_id: "hk1", outcome: "error", message: "HTTP 502" } },
      { ts: "bad", entry_type: "hook.fired", payload: { hook_id: "hk1", outcome: "pass" } },
    ])
    expect(runs.hk1.map((r) => r.outcome)).toEqual(["error", "pass"])
    expect(runs.hk1[0].message).toBe("HTTP 502")
    expect(runs.hk1[1].latencyMs).toBe(12)
  })
  it("tells a decision from a failure", () => {
    expect(outcomeTone("pass")).toBe("ok")
    expect(outcomeTone("block")).toBe("warn")
    expect(outcomeTone("error")).toBe("bad")
    expect(outcomeTone(undefined)).toBe("none")
  })
})

describe("filters", () => {
  const runs = { hk2: [{ hookId: "hk2", outcome: "error", ts: "2026-10-12T10:00:00Z" }] }
  const a = hook()
  const b = hook({ id: "hk2", event: "pre_llm_call", handler_kind: "shell", handler_config: { command: "/bin/gate.sh" }, blocking: true })
  const pick = (f: Partial<typeof DEFAULT_HOOK_FILTERS>) => [a, b].filter((h) => hookMatches(h, { ...DEFAULT_HOOK_FILTERS, ...f }, runs)).map((h) => h.id)
  it.each([
    [{ group: "llm" as const }, ["hk2"]],
    [{ kind: "http" as const }, ["hk1"]],
    [{ state: "blocking" as const }, ["hk2"]],
    [{ state: "failing" as const }, ["hk2"]],
    [{ q: "slack" }, ["hk1"]],
    [{ q: "gate.sh" }, ["hk2"]],
    [{ q: "model call" }, ["hk2"]],
  ])("%j", (f, want) => expect(pick(f)).toEqual(want))

  it("shortens a webhook URL to host and path", () => {
    expect(shortTarget(a)).toBe("hooks.slack.com/services/T/B/x")
  })
})

describe("the write body", () => {
  it("never sends blocking for an event that cannot block, and only relevant matchers", () => {
    const d = { ...draftFrom(), event: "post_tool_call", url: "https://x.example/h", blocking: true, severities: ["high"], tools: "Bash, Write.*" }
    expect(writeBody(d)).toEqual({
      event: "post_tool_call", handler_kind: "http", handler_config: { url: "https://x.example/h" },
      crew_id: "", matcher: { tools: ["Bash", "Write.*"] }, blocking: false, enabled: true,
    })
  })
  it("sends blocking and severities where they apply, and the shell command", () => {
    expect(writeBody({ ...draftFrom(), event: "pre_llm_call", kind: "shell", command: " /bin/gate.sh ", blocking: true, crewId: "c1" }))
      .toMatchObject({ handler_kind: "shell", handler_config: { command: "/bin/gate.sh" }, blocking: true, crew_id: "c1" })
    expect(writeBody({ ...draftFrom(), event: "on_guardrail_triggered", url: "https://x.example", severities: ["critical"] }).matcher)
      .toEqual({ severities: ["critical"] })
  })
  it("keeps a stored secret when the field is left empty on an edit", () => {
    const editing = hook({ handler_config: { url: "https://x.example", secret: "s3" } })
    expect(writeBody(draftFrom(editing), editing).handler_config).toEqual({ url: "https://x.example", secret: "s3" })
  })
  it("says what is missing", () => {
    expect(draftProblems(draftFrom())).toEqual(["Pick when the hook runs.", "Enter the URL to send the event to."])
    expect(draftProblems({ ...draftFrom(), event: "post_agent_stop", kind: "shell" })).toEqual(["Enter the command to run."])
    expect(draftProblems({ ...draftFrom(), event: "post_agent_stop", url: "ftp://x" })).toEqual(["The URL must start with https:// or http://."])
  })
})
