import { describe, it, expect } from "vitest"
import { formatCost } from "@/lib/utils/format"

// First coverage for lib/utils/format.ts.

describe("formatCost", () => {
  it("renders an em dash for null", () => {
    expect(formatCost(null)).toBe("—")
  })

  it("renders an em dash for zero", () => {
    expect(formatCost(0)).toBe("—")
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
})
