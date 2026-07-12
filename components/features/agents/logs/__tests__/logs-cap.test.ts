import { describe, expect, it } from "vitest"

import { capLogs, MAX_LOG_ENTRIES } from "../logs-viewer"
import type { LogEntry } from "@/lib/utils/log-format"

function makeEntries(n: number): LogEntry[] {
  return Array.from({ length: n }, (_, i) => ({
    ts: new Date(1700000000000 + i * 1000).toISOString(),
    level: "info",
    agent: "viktor",
    event: "log",
    content: `line ${i}`,
  }))
}

describe("capLogs", () => {
  it("returns the array unchanged (same identity) under the cap", () => {
    const entries = makeEntries(10)
    expect(capLogs(entries)).toBe(entries)
  })

  it("keeps only the most recent MAX_LOG_ENTRIES entries when over the cap", () => {
    const over = 250
    const entries = makeEntries(MAX_LOG_ENTRIES + over)
    const capped = capLogs(entries)
    expect(capped).toHaveLength(MAX_LOG_ENTRIES)
    // The oldest `over` entries are dropped; the newest survive.
    expect(capped[0].content).toBe(`line ${over}`)
    expect(capped[capped.length - 1].content).toBe(`line ${MAX_LOG_ENTRIES + over - 1}`)
  })

  it("handles the exact-cap boundary without slicing", () => {
    const entries = makeEntries(MAX_LOG_ENTRIES)
    expect(capLogs(entries)).toBe(entries)
  })
})
