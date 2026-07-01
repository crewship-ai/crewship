import { describe, it, expect } from "vitest"
import {
  successRate,
  successRateColor,
  barPercent,
  maxTotal,
  shortModel,
  failRate,
} from "@/lib/runs-insights"

describe("successRate", () => {
  it("computes an integer percent over completed+failed", () => {
    expect(successRate(90, 10)).toBe(90)
    expect(successRate(7, 3)).toBe(70)
  })
  it("ignores running/cancelled by only using the two args passed", () => {
    expect(successRate(3, 1)).toBe(75)
  })
  it("returns null when there is nothing to rate", () => {
    expect(successRate(0, 0)).toBeNull()
  })
  it("rounds to nearest integer", () => {
    expect(successRate(2, 1)).toBe(67)
  })
})

describe("successRateColor", () => {
  it("is green at ≥90, amber at ≥70, red below, undefined for null", () => {
    expect(successRateColor(95)).toBe("rgb(52, 211, 153)")
    expect(successRateColor(90)).toBe("rgb(52, 211, 153)")
    expect(successRateColor(80)).toBe("rgb(251, 191, 36)")
    expect(successRateColor(50)).toBe("rgb(248, 113, 113)")
    expect(successRateColor(null)).toBeUndefined()
  })
})

describe("barPercent / maxTotal", () => {
  it("scales relative to the max bucket with a 2% floor", () => {
    expect(barPercent(50, 100)).toBe(50)
    expect(barPercent(100, 100)).toBe(100)
    expect(barPercent(0, 100)).toBe(2) // floor so a nonzero-but-tiny bar is visible
  })
  it("returns 0 when max is 0", () => {
    expect(barPercent(0, 0)).toBe(0)
  })
  it("maxTotal finds the largest total", () => {
    expect(maxTotal([{ total: 3 }, { total: 9 }, { total: 5 }])).toBe(9)
    expect(maxTotal([])).toBe(0)
  })
})

describe("shortModel", () => {
  it("strips the claude- prefix", () => {
    expect(shortModel("claude-opus-4-8")).toBe("opus-4-8")
    expect(shortModel("claude-sonnet-4-5")).toBe("sonnet-4-5")
  })
  it("leaves non-claude and unknown untouched", () => {
    expect(shortModel("gpt-4o")).toBe("gpt-4o")
    expect(shortModel("unknown")).toBe("unknown")
    expect(shortModel("")).toBe("unknown")
  })
})

describe("failRate", () => {
  it("is integer percent, 0 when no runs", () => {
    expect(failRate(100, 22)).toBe(22)
    expect(failRate(0, 0)).toBe(0)
  })
})
