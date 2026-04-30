import { describe, it, expect } from "vitest"
import {
  formatDuration,
  formatRelativeShort,
  statusLabel,
  toCanonicalStatus,
} from "@/lib/runs-format"

describe("toCanonicalStatus", () => {
  it("maps RUNNING to IN_PROGRESS", () => {
    expect(toCanonicalStatus("RUNNING")).toBe("IN_PROGRESS")
  })
  it("maps TIMEOUT to FAILED", () => {
    expect(toCanonicalStatus("TIMEOUT")).toBe("FAILED")
  })
  it("passes through other statuses", () => {
    expect(toCanonicalStatus("COMPLETED")).toBe("COMPLETED")
    expect(toCanonicalStatus("FAILED")).toBe("FAILED")
    expect(toCanonicalStatus("CANCELLED")).toBe("CANCELLED")
    expect(toCanonicalStatus("PENDING")).toBe("PENDING")
    expect(toCanonicalStatus("UNKNOWN_FUTURE")).toBe("UNKNOWN_FUTURE")
  })
})

describe("statusLabel", () => {
  it("title-cases known statuses", () => {
    expect(statusLabel("RUNNING")).toBe("Running")
    expect(statusLabel("COMPLETED")).toBe("Completed")
    expect(statusLabel("FAILED")).toBe("Failed")
    expect(statusLabel("CANCELLED")).toBe("Cancelled")
    expect(statusLabel("TIMEOUT")).toBe("Timeout")
    expect(statusLabel("PENDING")).toBe("Pending")
  })
  it("returns input unchanged for unknown values", () => {
    expect(statusLabel("WEIRD_STATE")).toBe("WEIRD_STATE")
  })
})

describe("formatDuration", () => {
  it("returns em-dash when start is missing", () => {
    expect(formatDuration(null, "2026-01-01T00:00:30Z")).toBe("—")
  })

  it("formats sub-minute durations as Ns", () => {
    const start = "2026-01-01T00:00:00Z"
    const end = "2026-01-01T00:00:42Z"
    expect(formatDuration(start, end)).toBe("42s")
  })

  it("formats sub-hour durations as Mm Ss", () => {
    const start = "2026-01-01T00:00:00Z"
    const end = "2026-01-01T00:05:30Z"
    expect(formatDuration(start, end)).toBe("5m 30s")
  })

  it("formats multi-hour durations as Hh Mm", () => {
    const start = "2026-01-01T00:00:00Z"
    const end = "2026-01-01T02:30:00Z"
    expect(formatDuration(start, end)).toBe("2h 30m")
  })

  it("treats null end as 'now' (still running)", () => {
    // We can't pin Date.now without vi.useFakeTimers, but we can at
    // least confirm the format token shape is one of the three.
    const start = new Date(Date.now() - 5000).toISOString()
    const result = formatDuration(start, null)
    expect(result).toMatch(/^\d+s$|^\d+m \d+s$|^\d+h \d+m$/)
  })

  it("returns em-dash for unparseable start timestamp", () => {
    expect(formatDuration("garbage", "2026-01-01T00:00:00Z")).toBe("—")
  })

  it("returns em-dash for unparseable end timestamp", () => {
    expect(formatDuration("2026-01-01T00:00:00Z", "garbage")).toBe("—")
  })

  it("returns em-dash when end is before start (inverted pair)", () => {
    expect(formatDuration("2026-01-01T01:00:00Z", "2026-01-01T00:00:00Z")).toBe("—")
  })
})

describe("formatRelativeShort", () => {
  it("returns em-dash for null/undefined input", () => {
    expect(formatRelativeShort(null)).toBe("—")
    expect(formatRelativeShort(undefined)).toBe("—")
  })

  it("returns em-dash for unparseable strings", () => {
    expect(formatRelativeShort("not-a-date")).toBe("—")
  })

  it("formats seconds within the last minute", () => {
    const iso = new Date(Date.now() - 5_000).toISOString()
    const result = formatRelativeShort(iso)
    expect(result).toMatch(/^\d+s ago$/)
  })

  it("formats minutes between 1 and 60", () => {
    const iso = new Date(Date.now() - 120_000).toISOString()
    expect(formatRelativeShort(iso)).toMatch(/^\d+m ago$/)
  })

  it("formats hours up to a day", () => {
    const iso = new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString()
    expect(formatRelativeShort(iso)).toMatch(/^\d+h ago$/)
  })

  it("formats days beyond a day", () => {
    const iso = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString()
    expect(formatRelativeShort(iso)).toMatch(/^\d+d ago$/)
  })
})
