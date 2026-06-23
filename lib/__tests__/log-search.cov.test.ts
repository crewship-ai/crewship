import { describe, it, expect } from "vitest"
import { buildMatcher, parseStructuredQuery } from "@/lib/log-search"
import type { JournalEntry } from "@/lib/types/journal"

function entry(overrides: Partial<JournalEntry> = {}): JournalEntry {
  return {
    id: "id-test",
    workspace_id: "ws_test",
    ts: "2026-05-05T12:00:00Z",
    entry_type: "exec.command",
    severity: "info",
    actor_type: "agent",
    summary: "viktor runs pnpm test",
    payload: {},
    refs: {},
    ...overrides,
  }
}

describe("buildMatcher — remaining field aliases", () => {
  it("matches mission and mission_id aliases", () => {
    expect(buildMatcher("mission:m_42")!(entry({ mission_id: "m_42_alpha" }))).toBe(true)
    expect(buildMatcher("mission_id:m_42")!(entry({ mission_id: "m_42_alpha" }))).toBe(true)
    expect(buildMatcher("mission:m_42")!(entry({ mission_id: "m_99" }))).toBe(false)
  })

  it("matches trace and trace_id aliases", () => {
    expect(buildMatcher("trace:tr_7")!(entry({ trace_id: "tr_77" }))).toBe(true)
    expect(buildMatcher("trace_id:tr_7")!(entry({ trace_id: "tr_77" }))).toBe(true)
  })

  it("matches type and sev aliases", () => {
    expect(buildMatcher("type:exec")!(entry({ entry_type: "exec.command" }))).toBe(true)
    expect(buildMatcher("sev:error")!(entry({ severity: "error" }))).toBe(true)
    expect(buildMatcher("sev:error")!(entry({ severity: "info" }))).toBe(false)
  })

  it("fails key:value when the field is absent on the entry", () => {
    expect(buildMatcher("agent:viktor")!(entry({ agent_id: undefined }))).toBe(false)
  })

  it("returns false for a payload key when payload is undefined", () => {
    const e = entry()
    delete (e as Record<string, unknown>).payload
    expect(buildMatcher("tool_name:bash")!(e)).toBe(false)
  })

  it("handles entries with an empty summary", () => {
    expect(buildMatcher("exec")!(entry({ summary: "" }))).toBe(true)
    expect(buildMatcher("nomatch")!(entry({ summary: "" }))).toBe(false)
  })
})

describe("parseStructuredQuery", () => {
  it("returns empty params and query for empty / whitespace input", () => {
    expect(parseStructuredQuery("")).toEqual({ serverParams: {}, clientQuery: "" })
    expect(parseStructuredQuery("   ")).toEqual({ serverParams: {}, clientQuery: "" })
  })

  it("passes /regex/ queries through untouched as client-only", () => {
    const out = parseStructuredQuery("/user \\d+/i")
    expect(out.serverParams).toEqual({})
    expect(out.clientQuery).toBe("/user \\d+/i")
  })

  it("maps each recognized key and alias to its server param", () => {
    const out = parseStructuredQuery(
      "type:exec.command severity:warn actor:agent priority:high agent:viktor crew:devops trace:tr_1",
    )
    expect(out.serverParams).toEqual({
      entry_type: "exec.command",
      severity: "warn",
      actor_type: "agent",
      priority: "high",
      agent_id: "viktor",
      crew_id: "devops",
      trace_id: "tr_1",
    })
    expect(out.clientQuery).toBe("")
  })

  it("supports the long-form aliases", () => {
    const out = parseStructuredQuery(
      "sev:error actor_type:system agent_id:a1 crew_id:c1 trace_id:t1",
    )
    expect(out.serverParams).toEqual({
      severity: "error",
      actor_type: "system",
      agent_id: "a1",
      crew_id: "c1",
      trace_id: "t1",
    })
  })

  it("coalesces repeated keys into a CSV", () => {
    const out = parseStructuredQuery("severity:warn severity:error sev:fatal")
    expect(out.serverParams.severity).toBe("warn,error,fatal")
  })

  it("leaves unknown keys (payload-style) and free text in clientQuery", () => {
    const out = parseStructuredQuery("tool_name:Bash ALLOW agent:viktor")
    expect(out.serverParams).toEqual({ agent_id: "viktor" })
    expect(out.clientQuery).toBe("tool_name:Bash ALLOW")
  })

  it("preserves token order in the remaining clientQuery", () => {
    const out = parseStructuredQuery("foo type:exec bar baz")
    expect(out.clientQuery).toBe("foo bar baz")
    expect(out.serverParams.entry_type).toBe("exec")
  })

  it("keeps a bare word with no colon as free text", () => {
    const out = parseStructuredQuery("severity")
    expect(out.serverParams).toEqual({})
    expect(out.clientQuery).toBe("severity")
  })

  it("keys are matched case-insensitively but values keep their case", () => {
    const out = parseStructuredQuery("SEVERITY:Warn")
    expect(out.serverParams.severity).toBe("Warn")
  })

  it("clientQuery feeds buildMatcher for the residual filtering", () => {
    const { clientQuery, serverParams } = parseStructuredQuery("pnpm agent:viktor")
    expect(serverParams.agent_id).toBe("viktor")
    const m = buildMatcher(clientQuery)
    expect(m).not.toBeNull()
    expect(m!(entry({ summary: "pnpm install ok" }))).toBe(true)
    expect(m!(entry({ summary: "go build ok" }))).toBe(false)
  })
})
