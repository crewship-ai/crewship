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
  formatDurationMillis,
  formatDurationRounded,
  formatDurationFloor,
  formatDurationClock,
  formatDurationDecimal,
  formatDurationPrecise,
  formatDurationHm,
  formatDurationBetween,
  formatDurationSpan,
  formatDurationLong,
  formatDurationMinutes,
  relTime,
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

// ===========================================================================
// Consolidated duration variants (each pins one historical format exactly)
// ===========================================================================

describe("formatDurationMillis", () => {
  it("surfaces sub-second as Nms (rounded)", () => {
    expect(formatDurationMillis(820)).toBe("820ms")
    expect(formatDurationMillis(0)).toBe("0ms")
  })
  it("matches formatDuration at/above 1s (rounds, drops 0s tail)", () => {
    expect(formatDurationMillis(45_000)).toBe("45s")
    expect(formatDurationMillis(1_499)).toBe("1s")
    expect(formatDurationMillis(120_000)).toBe("2m")
    expect(formatDurationMillis(192_000)).toBe("3m 12s")
  })
})

describe("formatDurationRounded", () => {
  it("renders raw Nms, rounded seconds, always-shown seconds field", () => {
    expect(formatDurationRounded(820)).toBe("820ms")
    expect(formatDurationRounded(1_500)).toBe("2s")
    expect(formatDurationRounded(65_000)).toBe("1m 5s")
    expect(formatDurationRounded(60_000)).toBe("1m 0s")
  })
})

describe("formatDurationFloor", () => {
  it("floors to seconds + minutes, no hours rollover", () => {
    expect(formatDurationFloor(45_000)).toBe("45s")
    expect(formatDurationFloor(65_000)).toBe("1m 5s")
    expect(formatDurationFloor(3_700_000)).toBe("61m 40s")
  })
})

describe("formatDurationClock", () => {
  it("floors with an hours rollover", () => {
    expect(formatDurationClock(42_000)).toBe("42s")
    expect(formatDurationClock(330_000)).toBe("5m 30s")
    expect(formatDurationClock(9_000_000)).toBe("2h 30m")
  })
})

describe("formatDurationDecimal", () => {
  it("renders Nms / one-decimal seconds / floored Nm Ns", () => {
    expect(formatDurationDecimal(820)).toBe("820ms")
    expect(formatDurationDecimal(1_200)).toBe("1.2s")
    expect(formatDurationDecimal(65_000)).toBe("1m 5s")
  })
})

describe("formatDurationPrecise", () => {
  it("renders Nms / one-decimal seconds / Nm Ns", () => {
    expect(formatDurationPrecise(820)).toBe("820ms")
    expect(formatDurationPrecise(1_200)).toBe("1.2s")
    expect(formatDurationPrecise(65_000)).toBe("1m 5s")
  })
  it("never renders 60.0s or 1m 60s (spill guard)", () => {
    expect(formatDurationPrecise(59_960)).toBe("1m 0s")
    expect(formatDurationPrecise(119_600)).toBe("2m 0s")
  })
  it("returns null for junk input", () => {
    expect(formatDurationPrecise(undefined)).toBeNull()
    expect(formatDurationPrecise(-5)).toBeNull()
  })
})

describe("formatDurationHm", () => {
  it("floors with hour rollover, dropping a zero tail", () => {
    expect(formatDurationHm(45_000)).toBe("45s")
    expect(formatDurationHm(90_000)).toBe("1m")
    expect(formatDurationHm(5_400_000)).toBe("1h 30m")
    expect(formatDurationHm(7_200_000)).toBe("2h")
  })
})

describe("formatDurationBetween", () => {
  it("formats Ns / Nm Ns / Nh Nm between two ISO timestamps", () => {
    expect(formatDurationBetween("2026-01-01T00:00:00Z", "2026-01-01T00:00:42Z")).toBe("42s")
    expect(formatDurationBetween("2026-01-01T00:00:00Z", "2026-01-01T00:05:30Z")).toBe("5m 30s")
    expect(formatDurationBetween("2026-01-01T00:00:00Z", "2026-01-01T02:30:00Z")).toBe("2h 30m")
  })
  it("measures to now when end is null/omitted", () => {
    expect(formatDurationBetween(minus(5 * MIN), null)).toBe("5m 0s")
    expect(formatDurationBetween(minus(5 * MIN))).toBe("5m 0s")
  })
  it("returns the em-dash placeholder for missing/invalid/inverted", () => {
    expect(formatDurationBetween(null)).toBe("—")
    expect(formatDurationBetween("garbage", "2026-01-01T00:00:00Z")).toBe("—")
    expect(formatDurationBetween("2026-01-01T00:00:00Z", "garbage")).toBe("—")
    expect(formatDurationBetween("2026-01-01T01:00:00Z", "2026-01-01T00:00:00Z")).toBe("—")
  })
})

describe("formatDurationSpan", () => {
  it("floors to seconds + minutes (no hours rollover)", () => {
    expect(formatDurationSpan("2026-01-01T00:00:00Z", "2026-01-01T00:00:42Z")).toBe("42s")
    expect(formatDurationSpan("2026-01-01T00:00:00Z", "2026-01-01T01:05:00Z")).toBe("65m 0s")
  })
  it("returns the empty string for invalid/inverted pairs", () => {
    expect(formatDurationSpan("2026-01-01T00:00:00Z", "garbage")).toBe("")
    expect(formatDurationSpan("2026-01-01T01:00:00Z", "2026-01-01T00:00:00Z")).toBe("")
  })
})

describe("formatDurationLong", () => {
  it("drops the seconds tail past a minute and rolls into days", () => {
    expect(formatDurationLong("2026-06-15T11:59:30Z", "2026-06-15T12:00:00Z")).toBe("30s")
    expect(formatDurationLong("2026-06-15T11:55:00Z", "2026-06-15T12:00:00Z")).toBe("5m")
    expect(formatDurationLong("2026-06-15T09:30:00Z", "2026-06-15T12:00:00Z")).toBe("2h 30m")
    expect(formatDurationLong("2026-06-13T06:00:00Z", "2026-06-15T12:00:00Z")).toBe("2d 6h")
  })
  it("measures to now when end is null/omitted", () => {
    expect(formatDurationLong(minus(5 * MIN), null)).toBe("5m")
  })
})

describe("formatDurationMinutes", () => {
  it("uses minute resolution with hour rollover", () => {
    expect(formatDurationMinutes("2026-06-15T11:59:30Z", "2026-06-15T12:00:00Z")).toBe("<1m")
    expect(formatDurationMinutes("2026-06-15T11:55:00Z", "2026-06-15T12:00:00Z")).toBe("5m")
    expect(formatDurationMinutes("2026-06-15T09:30:00Z", "2026-06-15T12:00:00Z")).toBe("2h 30m")
    expect(formatDurationMinutes("2026-06-15T10:00:00Z", "2026-06-15T12:00:00Z")).toBe("2h")
  })
  it("measures to now when end is null/omitted", () => {
    expect(formatDurationMinutes(minus(5 * MIN), null)).toBe("5m")
  })
})

describe("relTime", () => {
  it("renders 'just now' under a minute", () => {
    expect(relTime(minus(30_000))).toBe("just now")
  })
  it("renders past timestamps with an 'ago' suffix", () => {
    expect(relTime(minus(5 * MIN))).toBe("5m ago")
    expect(relTime(minus(3 * HOUR))).toBe("3h ago")
    expect(relTime(minus(2 * DAY))).toBe("2d ago")
  })
  it("renders future timestamps with an 'in' prefix", () => {
    expect(relTime(minus(-5 * MIN))).toBe("in 5m")
    expect(relTime(minus(-3 * HOUR))).toBe("in 3h")
  })
  it("returns the em-dash placeholder for invalid/empty input", () => {
    expect(relTime(undefined)).toBe("—")
    expect(relTime("garbage")).toBe("—")
  })
})
