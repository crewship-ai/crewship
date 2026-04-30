import { describe, it, expect } from "vitest"
import {
  timeAgo,
  formatRelativeTime,
  formatCommentTime,
  formatDate,
  formatShortDate,
  formatDateTime,
} from "@/lib/time"

describe("time formatters — invalid input handling", () => {
  // Without a guard, `new Date("garbage").getTime()` returns NaN and the
  // arithmetic falls through every range check, producing "NaNd ago".
  // Every formatter that consumes a date string must surface invalid
  // input as a stable placeholder rather than the raw NaN string.

  for (const input of ["garbage", "", "not-a-date"]) {
    it(`timeAgo returns a stable placeholder for ${JSON.stringify(input)}`, () => {
      const result = timeAgo(input)
      expect(result).not.toContain("NaN")
      expect(result.length).toBeGreaterThan(0)
    })

    it(`formatRelativeTime returns a stable placeholder for ${JSON.stringify(input)}`, () => {
      const result = formatRelativeTime(input)
      expect(result).not.toContain("NaN")
      expect(result.length).toBeGreaterThan(0)
    })

    it(`formatCommentTime returns a stable placeholder for ${JSON.stringify(input)}`, () => {
      const result = formatCommentTime(input)
      expect(result).not.toContain("NaN")
      expect(result.length).toBeGreaterThan(0)
    })

    it(`formatDate returns a stable placeholder for ${JSON.stringify(input)}`, () => {
      const result = formatDate(input)
      expect(result).not.toContain("Invalid")
      expect(result).not.toContain("NaN")
    })

    it(`formatShortDate returns a stable placeholder for ${JSON.stringify(input)}`, () => {
      const result = formatShortDate(input)
      expect(result).not.toContain("Invalid")
      expect(result).not.toContain("NaN")
    })

    it(`formatDateTime returns a stable placeholder for ${JSON.stringify(input)}`, () => {
      const result = formatDateTime(input)
      expect(result).not.toContain("Invalid")
      expect(result).not.toContain("NaN")
    })
  }
})

describe("timeAgo — happy path", () => {
  it("formats minutes", () => {
    const fiveMinAgo = new Date(Date.now() - 5 * 60_000).toISOString()
    expect(timeAgo(fiveMinAgo)).toBe("5m ago")
  })

  it("formats just now for sub-minute", () => {
    expect(timeAgo(new Date().toISOString())).toBe("just now")
  })
})
