import { describe, it, expect } from "vitest"
import { buildMatcher } from "@/lib/log-search"
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

describe("buildMatcher", () => {
  it("returns null for empty / whitespace query", () => {
    expect(buildMatcher("")).toBeNull()
    expect(buildMatcher("   ")).toBeNull()
    expect(buildMatcher("\t\n")).toBeNull()
  })

  it("matches free-text tokens (AND) on summary + entry_type", () => {
    const m = buildMatcher("pnpm test")
    expect(m).not.toBeNull()
    expect(m!(entry({ summary: "viktor runs pnpm test" }))).toBe(true)
    expect(m!(entry({ summary: "vitest passed" }))).toBe(false)
    // also matches when one token lives in entry_type
    expect(m!(entry({ entry_type: "test.run", summary: "pnpm built" }))).toBe(true)
  })

  it("is case-insensitive for free-text", () => {
    const m = buildMatcher("PNPM")
    expect(m!(entry({ summary: "pnpm test" }))).toBe(true)
  })

  it("supports /regex/ syntax with default i flag", () => {
    const m = buildMatcher("/user \\d+/")
    expect(m!(entry({ summary: "user 42 logged in" }))).toBe(true)
    expect(m!(entry({ summary: "USER 9 OK" }))).toBe(true)
    expect(m!(entry({ summary: "user abc" }))).toBe(false)
  })

  it("respects explicit regex flags", () => {
    const m = buildMatcher("/USER/m")
    expect(m).not.toBeNull()
    // m flag without i means case-sensitive
    expect(m!(entry({ summary: "user 42" }))).toBe(false)
    expect(m!(entry({ summary: "USER 42" }))).toBe(true)
  })

  it("falls back to free-text on invalid regex", () => {
    const m = buildMatcher("/[unbalanced/")
    // shouldn't throw, should match the literal
    expect(m).not.toBeNull()
    expect(m!(entry({ summary: "[unbalanced parens" }))).toBe(true)
  })

  it("matches key:value on direct fields", () => {
    const m = buildMatcher("agent:viktor")
    expect(m!(entry({ agent_id: "viktor-91" }))).toBe(true)
    expect(m!(entry({ agent_id: "milos-12" }))).toBe(false)
  })

  it("supports the long-form key alias", () => {
    expect(buildMatcher("crew_id:devops")!(entry({ crew_id: "crew_devops_7a" }))).toBe(true)
    expect(buildMatcher("severity:warn")!(entry({ severity: "warn" }))).toBe(true)
  })

  // #2206: the server's FTS5 index covers summary AND payload, so a
  // matcher that only reads the summary hides rows the server just
  // found and the panel renders "No entries match" over a non-empty
  // result set.
  it("matches free text against payload text, like the server's FTS does", () => {
    const e = entry({
      summary: "container died",
      payload: { error: "OOMKilled exit 137" },
    })
    expect(buildMatcher("OOMKilled")!(e)).toBe(true)
    expect(buildMatcher("oomkilled")!(e)).toBe(true)
    // Still ANDs: one token from the summary, one from the payload.
    expect(buildMatcher("container OOMKilled")!(e)).toBe(true)
    expect(buildMatcher("container SIGTERM")!(e)).toBe(false)
  })

  it("does not match payload text for entries without a payload", () => {
    expect(buildMatcher("oomkilled")!(entry({ summary: "container died", payload: undefined }))).toBe(false)
  })

  it("matches key:value against payload fields too", () => {
    const m = buildMatcher("path:/etc/passwd")
    expect(m!(entry({ payload: { path: "/etc/passwd" } }))).toBe(true)
    expect(m!(entry({ payload: { path: "/home/agent" } }))).toBe(false)
  })

  it("ANDs free tokens with key:value", () => {
    const m = buildMatcher("ALLOW agent:viktor")
    expect(m!(entry({ summary: "ALLOW read", agent_id: "viktor-91" }))).toBe(true)
    expect(m!(entry({ summary: "ALLOW read", agent_id: "milos-12" }))).toBe(false)
    expect(m!(entry({ summary: "DENY read", agent_id: "viktor-91" }))).toBe(false)
  })

  it("treats key:value as case-insensitive", () => {
    const m = buildMatcher("Agent:VIKTOR")
    expect(m!(entry({ agent_id: "viktor-91" }))).toBe(true)
  })
})
