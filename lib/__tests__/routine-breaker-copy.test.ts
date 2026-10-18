import { describe, it, expect } from "vitest"
import { BREAKER_ON_AGAIN, BREAKER_OFF_HINT } from "@/lib/routine-governance"

// ODCH-RT-12: re-enabling a schedule that the circuit breaker turned off
// zeroes the failure count at once (updateSchedule), not after the next run.
describe("circuit breaker copy", () => {
  it("does not say the count resets after the next successful run", () => {
    for (const text of [BREAKER_ON_AGAIN, BREAKER_OFF_HINT]) {
      expect(text).not.toMatch(/next successful run/i)
    }
    expect(BREAKER_ON_AGAIN).toMatch(/starts again from zero/i)
    expect(BREAKER_OFF_HINT).toMatch(/starts again from zero/i)
  })
})
