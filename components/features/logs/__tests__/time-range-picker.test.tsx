import { describe, it, expect } from "vitest"
import { sinceFromTimeRange, untilFromTimeRange } from "@/components/features/logs/time-range-picker"

describe("sinceFromTimeRange", () => {
  it("returns undefined for 'all'", () => {
    expect(sinceFromTimeRange("all")).toBeUndefined()
  })

  it("returns approximately now − range for presets", () => {
    const now = Date.now()
    const since1h = sinceFromTimeRange("1h")
    expect(since1h).toBeDefined()
    const t = new Date(since1h!).getTime()
    expect(now - t).toBeGreaterThanOrEqual(60 * 60 * 1000 - 1000)
    expect(now - t).toBeLessThanOrEqual(60 * 60 * 1000 + 1000)
  })

  it("uses customRange.fromMs for custom", () => {
    const fromMs = new Date("2026-05-05T10:00:00Z").getTime()
    const toMs = new Date("2026-05-05T12:00:00Z").getTime()
    const since = sinceFromTimeRange("custom", { fromMs, toMs })
    expect(since).toBe(new Date(fromMs).toISOString())
  })

  it("returns undefined for custom without a range", () => {
    expect(sinceFromTimeRange("custom")).toBeUndefined()
    expect(sinceFromTimeRange("custom", null)).toBeUndefined()
  })
})

describe("untilFromTimeRange", () => {
  it("returns now for non-custom presets", () => {
    const before = Date.now()
    const v = untilFromTimeRange("1h")
    const after = Date.now()
    expect(v).toBeGreaterThanOrEqual(before)
    expect(v).toBeLessThanOrEqual(after)
  })

  it("returns customRange.toMs for custom", () => {
    const toMs = new Date("2026-05-05T12:00:00Z").getTime()
    expect(untilFromTimeRange("custom", { fromMs: 0, toMs })).toBe(toMs)
  })

  it("falls back to now if custom is selected without a range", () => {
    const v = untilFromTimeRange("custom", null)
    expect(v).toBeGreaterThan(0)
  })
})
