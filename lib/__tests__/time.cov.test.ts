import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import {
  timeAgo,
  formatDuration,
  formatTimeout,
  formatDate,
  formatShortDate,
  formatDateTime,
  formatRelativeTime,
  formatCommentTime,
} from "@/lib/time"

const NOW = new Date("2026-06-15T12:00:00Z")

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(NOW)
})

afterEach(() => {
  vi.useRealTimers()
})

const minus = (ms: number) => new Date(NOW.getTime() - ms).toISOString()
const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR

describe("timeAgo — hour/day ranges", () => {
  it("formats hours", () => {
    expect(timeAgo(minus(3 * HOUR))).toBe("3h ago")
  })

  it("returns 'yesterday' for exactly one day", () => {
    expect(timeAgo(minus(25 * HOUR))).toBe("yesterday")
  })

  it("formats multiple days", () => {
    expect(timeAgo(minus(3 * DAY))).toBe("3d ago")
  })
})

describe("formatDuration", () => {
  it("formats sub-minute durations in seconds", () => {
    expect(formatDuration(45_000)).toBe("45s")
  })

  it("rounds milliseconds to the nearest second", () => {
    expect(formatDuration(1_499)).toBe("1s")
    expect(formatDuration(1_500)).toBe("2s")
  })

  it("formats zero", () => {
    expect(formatDuration(0)).toBe("0s")
  })

  it("formats whole minutes without a seconds remainder", () => {
    expect(formatDuration(120_000)).toBe("2m")
  })

  it("formats minutes with a seconds remainder", () => {
    expect(formatDuration(192_000)).toBe("3m 12s")
  })
})

describe("formatTimeout", () => {
  it("formats sub-hour values in minutes", () => {
    expect(formatTimeout(1800)).toBe("30 min")
    expect(formatTimeout(60)).toBe("1 min")
  })

  it("formats hour values", () => {
    expect(formatTimeout(3600)).toBe("1h")
    expect(formatTimeout(7200)).toBe("2h")
  })

  it("rounds to the nearest hour above the threshold", () => {
    expect(formatTimeout(5400)).toBe("2h") // 1.5h rounds up
  })
})

describe("absolute date formatters — valid input", () => {
  // Locale output varies by runner; assert on the stable pieces.
  it("formatDate includes day and year", () => {
    const out = formatDate("2026-03-15T12:00:00Z")
    expect(out).toContain("15")
    expect(out).toContain("2026")
  })

  it("formatShortDate includes the day but not the year", () => {
    const out = formatShortDate("2026-03-15T12:00:00Z")
    expect(out).toContain("15")
    expect(out).not.toContain("2026")
  })

  it("formatDateTime includes day, year and minutes", () => {
    const out = formatDateTime("2026-03-15T12:30:00Z")
    expect(out).toContain("15")
    expect(out).toContain("2026")
    expect(out).toContain("30")
  })
})

describe("formatRelativeTime", () => {
  it("formats seconds", () => {
    expect(formatRelativeTime(minus(45_000))).toBe("45s ago")
  })

  it("clamps future timestamps to 0s ago instead of negatives", () => {
    expect(formatRelativeTime(minus(-90_000))).toBe("0s ago")
  })

  it("formats minutes", () => {
    expect(formatRelativeTime(minus(5 * MIN))).toBe("5m ago")
  })

  it("formats hours", () => {
    expect(formatRelativeTime(minus(3 * HOUR))).toBe("3h ago")
  })

  it("formats days", () => {
    expect(formatRelativeTime(minus(4 * DAY))).toBe("4d ago")
  })
})

describe("formatCommentTime", () => {
  it("returns 'just now' under a minute", () => {
    expect(formatCommentTime(minus(20_000))).toBe("just now")
  })

  it("formats minutes", () => {
    expect(formatCommentTime(minus(5 * MIN))).toBe("5m ago")
  })

  it("formats hours", () => {
    expect(formatCommentTime(minus(3 * HOUR))).toBe("3h ago")
  })

  it("formats days under a week", () => {
    expect(formatCommentTime(minus(3 * DAY))).toBe("3d ago")
  })

  it("switches to an absolute locale date after 7 days", () => {
    const out = formatCommentTime(minus(10 * DAY))
    expect(out).not.toContain("ago")
    expect(out).toBe(new Date(NOW.getTime() - 10 * DAY).toLocaleDateString())
  })
})
