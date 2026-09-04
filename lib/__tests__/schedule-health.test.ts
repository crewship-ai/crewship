import { describe, it, expect } from "vitest"
import { scheduleHealth } from "@/lib/schedule-health"

// F18 / A6 (docs/prd/PRD-ISSUES-AND-ROUTINES-2026.md): PipelineSchedule
// carries disabled_reason, consecutive_failures, max_consecutive_failures —
// the schedules tab rendered none of them, so a schedule the circuit
// breaker had auto-disabled showed the user no reason at all.

describe("scheduleHealth", () => {
  it("surfaces the circuit-breaker reason when the backend reports one", () => {
    const h = scheduleHealth({
      enabled: false,
      disabled_reason: "circuit_breaker",
      consecutive_failures: 5,
      max_consecutive_failures: 5,
    })
    expect(h.tone).toBe("destructive")
    expect(h.label).toContain("circuit breaker")
    expect(h.reason).not.toBeNull()
    expect(h.reason).toContain("5")
  })

  it("degrades gracefully to a plain 'paused' state when disabled_reason is absent (operator disable)", () => {
    const h = scheduleHealth({
      enabled: false,
      disabled_reason: undefined,
      consecutive_failures: 0,
      max_consecutive_failures: 5,
    })
    expect(h.tone).toBe("default")
    expect(h.label).toBe("paused")
    expect(h.reason).toBeNull()
  })

  it("degrades gracefully when disabled_reason is the empty string the API sends for an operator disable", () => {
    const h = scheduleHealth({
      enabled: false,
      disabled_reason: "",
      consecutive_failures: 0,
      max_consecutive_failures: 5,
    })
    expect(h.label).toBe("paused")
    expect(h.reason).toBeNull()
  })

  it("flags a schedule with failures but still enabled as at risk, without waiting for the breaker to trip", () => {
    const h = scheduleHealth({
      enabled: true,
      consecutive_failures: 2,
      max_consecutive_failures: 5,
    })
    expect(h.tone).toBe("warn")
    expect(h.label).toBe("at risk")
    expect(h.reason).toContain("2")
    expect(h.reason).toContain("5")
  })

  it("reports healthy for an enabled schedule with a clean streak", () => {
    const h = scheduleHealth({
      enabled: true,
      consecutive_failures: 0,
      max_consecutive_failures: 5,
    })
    expect(h.tone).toBe("success")
    expect(h.label).toBe("healthy")
    expect(h.reason).toBeNull()
  })

  it("does not crash when max_consecutive_failures is 0/unset (still shows the count)", () => {
    const h = scheduleHealth({
      enabled: false,
      disabled_reason: "circuit_breaker",
      consecutive_failures: 3,
      max_consecutive_failures: 0,
    })
    expect(h.reason).toContain("3")
  })
})
