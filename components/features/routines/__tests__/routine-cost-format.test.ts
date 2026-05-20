import { describe, it, expect } from "vitest"
import {
  extractStepMeta,
  extractStepID,
  formatStepCost,
  formatStepDuration,
} from "../routine-cost-format"

// ---------------------------------------------------------------------------
// routine-cost-format.ts pins the parsing + rendering surface used by
// the Runs tab waterfall, the Overview tab, and the dry-run report.
// A regression in any of these helpers silently corrupts cost / duration
// columns across three surfaces at once — these tests are the alarm.
// ---------------------------------------------------------------------------

describe("extractStepMeta", () => {
  it("returns empty struct for null / undefined payload", () => {
    expect(extractStepMeta(null)).toEqual({ stepId: "", costUSD: 0, durationMs: 0 })
    expect(extractStepMeta(undefined)).toEqual({ stepId: "", costUSD: 0, durationMs: 0 })
  })

  it("parses the common object shape from pipeline.step.completed", () => {
    // Mirror what internal/pipeline/journal.go:emitStepCompleted emits.
    const payload = { step_id: "extract", cost_usd: 0.0123, duration_ms: 4321 }
    expect(extractStepMeta(payload)).toEqual({
      stepId: "extract",
      costUSD: 0.0123,
      durationMs: 4321,
    })
  })

  it("tolerates double-encoded JSON-string payload shape", () => {
    // Upstream sometimes serialises payload twice (a journal writer
    // bug we've seen before); the extractor must survive it rather
    // than silently rendering "—".
    const wirePayload = JSON.stringify({
      step_id: "summarise",
      cost_usd: 0.001,
      duration_ms: 200,
    })
    expect(extractStepMeta(wirePayload)).toEqual({
      stepId: "summarise",
      costUSD: 0.001,
      durationMs: 200,
    })
  })

  it("returns zeros + empty step_id when JSON-string payload is malformed", () => {
    expect(extractStepMeta("not json at all")).toEqual({
      stepId: "",
      costUSD: 0,
      durationMs: 0,
    })
  })

  it("returns zeros when payload object lacks expected fields", () => {
    // pipeline.step.started carries only step_id — cost/duration land
    // on .completed. Without the type guards we'd render NaN/undefined.
    expect(extractStepMeta({ step_id: "only-id" })).toEqual({
      stepId: "only-id",
      costUSD: 0,
      durationMs: 0,
    })
  })

  it("rejects non-numeric cost / duration to prevent NaN propagation", () => {
    // If a renderer regresses upstream and stuffs "0.05" into cost_usd
    // as a string, we should refuse it rather than try to coerce —
    // silent coercion masks the real bug.
    expect(
      extractStepMeta({ step_id: "x", cost_usd: "0.05", duration_ms: "100" }),
    ).toEqual({ stepId: "x", costUSD: 0, durationMs: 0 })
  })
})

describe("extractStepID", () => {
  it("returns just the step_id (back-compat shim over extractStepMeta)", () => {
    expect(extractStepID({ step_id: "fetch", cost_usd: 0.01 })).toBe("fetch")
    expect(extractStepID(null)).toBe("")
    expect(extractStepID(JSON.stringify({ step_id: "wire" }))).toBe("wire")
  })
})

describe("formatStepDuration", () => {
  it("renders em-dash for non-positive durations (started / failed events)", () => {
    expect(formatStepDuration(0)).toBe("—")
    expect(formatStepDuration(-1)).toBe("—")
    expect(formatStepDuration(Number.NaN)).toBe("—")
    expect(formatStepDuration(Number.POSITIVE_INFINITY)).toBe("—")
  })

  it("rounds sub-second values to integer milliseconds", () => {
    expect(formatStepDuration(7)).toBe("7ms")
    expect(formatStepDuration(456.7)).toBe("457ms")
    expect(formatStepDuration(999)).toBe("999ms")
  })

  it("renders sub-10s with 2 decimals, 10-60s with 1 decimal", () => {
    expect(formatStepDuration(1234)).toBe("1.23s")
    expect(formatStepDuration(9999)).toBe("10.00s") // 9.999 rounds to 10.00 with toFixed(2)
    expect(formatStepDuration(12345)).toBe("12.3s")
    expect(formatStepDuration(59000)).toBe("59.0s")
  })

  it("renders minute-precision over 60s", () => {
    expect(formatStepDuration(60000)).toBe("1m00s")
    expect(formatStepDuration(125000)).toBe("2m05s")
    expect(formatStepDuration(610000)).toBe("10m10s")
  })

  it("carries the rounded-seconds rollover into minutes", () => {
    // Regression for the "1m60s" bug — Math.round on the seconds
    // remainder could yield 60 when the floor of (s/60) and the
    // rounded remainder were computed independently. Pin the
    // boundary so a future refactor can't reintroduce it.
    expect(formatStepDuration(119999)).toBe("2m00s")
    expect(formatStepDuration(179500)).toBe("3m00s") // 2m59.5s → carry
    expect(formatStepDuration(59999)).toBe("60.0s") // just under 60s threshold, stays in "<60s" branch
  })
})

describe("formatStepCost", () => {
  it("renders em-dash for non-positive cost (started / failed / zero)", () => {
    expect(formatStepCost(0)).toBe("—")
    expect(formatStepCost(-0.01)).toBe("—")
    expect(formatStepCost(Number.NaN)).toBe("—")
  })

  it("renders 4-decimal USD so micro-runs stay legible", () => {
    // Pin the 4dp choice — overview tab + waterfall + dry-run report
    // all rely on this for column-aligned tabular-nums rendering.
    expect(formatStepCost(0.0001)).toBe("$0.0001")
    expect(formatStepCost(0.0123)).toBe("$0.0123")
    expect(formatStepCost(1.5)).toBe("$1.5000")
  })

  it("preserves precision across realistic dry-run estimates", () => {
    // estimateStepCost in executor_render.go computes
    //   (chars/4 + chars/4 * 0.25) / 1_000_000
    // For a 4000-char prompt that's 0.00125 — must not round to 0.
    expect(formatStepCost(0.00125)).toBe("$0.0013") // banker's rounding tolerant
  })
})
