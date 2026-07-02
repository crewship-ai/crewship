import { describe, it, expect } from "vitest"
import { aggregateRunCosts, formatUsd } from "@/lib/routines-insights"

describe("aggregateRunCosts", () => {
  it("returns zero totals and a null average for an empty sample", () => {
    expect(aggregateRunCosts([])).toEqual({
      totalUsd: 0,
      avgPerRunUsd: null,
      runCount: 0,
    })
  })

  it("sums cost_usd across runs and averages over the whole sample", () => {
    const stats = aggregateRunCosts([
      { cost_usd: 0.5 },
      { cost_usd: 1.25 },
      { cost_usd: 0.25 },
    ])
    expect(stats.totalUsd).toBeCloseTo(2.0)
    expect(stats.avgPerRunUsd).toBeCloseTo(2.0 / 3)
    expect(stats.runCount).toBe(3)
  })

  it("treats missing / null cost_usd as zero but still counts the run", () => {
    const stats = aggregateRunCosts([{ cost_usd: 1 }, {}, { cost_usd: null }])
    expect(stats.totalUsd).toBeCloseTo(1)
    // Averaging over ALL runs (not just costed ones) keeps the number
    // honest: free runs pull the per-run spend down.
    expect(stats.avgPerRunUsd).toBeCloseTo(1 / 3)
    expect(stats.runCount).toBe(3)
  })

  it("ignores NaN and negative costs instead of corrupting the total", () => {
    const stats = aggregateRunCosts([
      { cost_usd: Number.NaN },
      { cost_usd: -5 },
      { cost_usd: 0.1 },
    ])
    expect(stats.totalUsd).toBeCloseTo(0.1)
    expect(stats.runCount).toBe(3)
  })
})

describe("formatUsd", () => {
  it("renders zero as plain $0.00", () => {
    expect(formatUsd(0)).toBe("$0.00")
  })

  it("uses 2 decimals from $1 upward", () => {
    expect(formatUsd(1)).toBe("$1.00")
    expect(formatUsd(12.3456)).toBe("$12.35")
  })

  it("uses 4 decimals below $1 so micro-costs stay legible", () => {
    expect(formatUsd(0.1234)).toBe("$0.1234")
    expect(formatUsd(0.0001)).toBe("$0.0001")
  })

  it("falls back to an em dash for non-finite or negative input", () => {
    expect(formatUsd(Number.NaN)).toBe("—")
    expect(formatUsd(Number.POSITIVE_INFINITY)).toBe("—")
    expect(formatUsd(-1)).toBe("—")
  })
})
