import { describe, it, expect } from "vitest"
import { formatCost } from "@/lib/utils/format"

// First coverage for lib/utils/format.ts.
//
// #1939. This file used to pin `formatCost(0) === "—"`, which made a measured
// zero and no-data-at-all the same claim. They are not: a run that genuinely
// cost $0.0000 — agentless, cache-hit, a `code` or `transform` step that never
// called a model — reads identically to a run whose cost we failed to record.
// #1205 was exactly the second case, and this formatter would have rendered
// both the same while it was happening.
//
// The rule is the one the rest of the product already follows (docs/prd/
// pages.md §9b.4, and every other cost formatter in the tree —
// lib/routines-insights.ts's formatUsd, dashboard-helpers, recent-missions-
// table, journal-spend-view, agent-canvas): `null` is "no basis to compute"
// and is the only thing that earns the em dash; `0` is a number we looked up.

describe("formatCost", () => {
  it("renders an em dash for null — the one thing that means no basis to compute", () => {
    expect(formatCost(null)).toBe("—")
    expect(formatCost(null, true)).toBe("—")
  })

  it("renders a measured zero as a zero, not as an em dash (#1939)", () => {
    expect(formatCost(0)).toBe("$0.0000")
  })

  it("keeps a measured zero and no data at all distinguishable", () => {
    expect(formatCost(0)).not.toBe(formatCost(null))
    expect(formatCost(0, true)).not.toBe(formatCost(null, true))
  })

  it("adaptive mode renders a measured zero at cent precision", () => {
    expect(formatCost(0, true)).toBe("$0.00")
  })

  it("uses 4 decimal places by default", () => {
    expect(formatCost(0.0042)).toBe("$0.0042")
    expect(formatCost(1.5)).toBe("$1.5000")
  })

  it("adaptive mode uses 2 decimals for costs >= $0.01", () => {
    expect(formatCost(0.01, true)).toBe("$0.01")
    expect(formatCost(12.345, true)).toBe("$12.35")
  })

  it("adaptive mode keeps 4 decimals for sub-cent costs", () => {
    expect(formatCost(0.0042, true)).toBe("$0.0042")
    expect(formatCost(0.0099, true)).toBe("$0.0099")
  })

  // A sub-cent cost that is not zero must never round down into the zero the
  // rule above just made meaningful — that would reintroduce the collapse from
  // the other side, with "we measured nothing" printed over a real charge.
  it("never rounds a real sub-cent cost into a measured zero", () => {
    expect(formatCost(0.0001)).toBe("$0.0001")
    expect(formatCost(0.0001, true)).toBe("$0.0001")
    expect(formatCost(0.0001)).not.toBe(formatCost(0))
  })

  // NaN/Infinity are no basis to compute, not a value. formatUsd in
  // lib/routines-insights.ts already answers the em dash for them; "$NaN" is
  // the one output that is worse than either.
  it("renders an em dash for a non-finite input", () => {
    expect(formatCost(Number.NaN)).toBe("—")
    expect(formatCost(Number.POSITIVE_INFINITY, true)).toBe("—")
  })
})
