// @vitest-environment node
import { describe, expect, it } from "vitest"
import { summarizeMilliseconds } from "./metrics"

describe("WordCraft response-time summaries", () => {
  it("computes p50/p95/max by nearest rank while preserving actual sample order", () => {
    const samples = [9, 1, 4, 2, 3, 8, 7, 5, 6, 10, 20, 12, 11, 13, 19, 15, 14, 17, 18, 16]
    expect(summarizeMilliseconds(samples)).toEqual({ unit: "ms", percentileMethod: "nearest-rank", count: 20, p50: 10, p95: 19, max: 20, raw: samples })
    expect(samples[0]).toBe(9)
  })
  it("retains fractional milliseconds and supports one observed sample", () => {
    expect(summarizeMilliseconds([0.25])).toMatchObject({ p50: 0.25, p95: 0.25, max: 0.25, count: 1 })
    expect(summarizeMilliseconds([0, 0.5, 1.75])).toMatchObject({ p50: 0.5, p95: 1.75, max: 1.75 })
  })
  it.each([[], [-1], [NaN], [Infinity], [1, -0.1]].map(samples => ({ samples })))("rejects absent or invalid evidence: $samples", ({ samples }) => {
    expect(() => summarizeMilliseconds(samples)).toThrow("nonempty, finite and nonnegative")
  })
})
