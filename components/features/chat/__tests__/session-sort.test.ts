import { describe, expect, it } from "vitest"
import { parseSessionTimestamp, sortSessionsByActivity } from "../session-sort"

describe("parseSessionTimestamp", () => {
  it("parses ISO timestamps as UTC", () => {
    expect(parseSessionTimestamp("2026-07-01T10:00:00.000Z")).toBe(
      Date.UTC(2026, 6, 1, 10, 0, 0, 0),
    )
  })

  it("parses legacy space-separated SQLite timestamps as UTC", () => {
    // datetime('now') writes "YYYY-MM-DD HH:MM:SS" in UTC — must NOT be
    // interpreted in the browser's local zone or ordering drifts by the
    // user's UTC offset.
    expect(parseSessionTimestamp("2026-07-01 10:00:00")).toBe(
      Date.UTC(2026, 6, 1, 10, 0, 0, 0),
    )
  })

  it("returns 0 for garbage so sorting stays total", () => {
    expect(parseSessionTimestamp("not-a-date")).toBe(0)
    expect(parseSessionTimestamp(undefined)).toBe(0)
    expect(parseSessionTimestamp(null)).toBe(0)
  })
})

describe("sortSessionsByActivity", () => {
  const s = (id: string, started_at: string, last_activity_at?: string | null) => ({
    id,
    started_at,
    last_activity_at,
  })

  it("orders by last_activity_at descending", () => {
    const rows = [
      s("stale", "2026-06-01 00:00:00", "2026-06-02T00:00:00.000Z"),
      s("fresh", "2026-01-01 00:00:00", "2026-07-01T00:00:00.000Z"),
    ]
    expect(sortSessionsByActivity(rows).map((r) => r.id)).toEqual(["fresh", "stale"])
  })

  it("falls back to started_at when last_activity_at is missing", () => {
    const rows = [
      s("older", "2026-05-01 00:00:00", null),
      s("newer", "2026-06-01 00:00:00", undefined),
    ]
    expect(sortSessionsByActivity(rows).map((r) => r.id)).toEqual(["newer", "older"])
  })

  it("mixes legacy and ISO formats correctly", () => {
    const rows = [
      s("legacy-latest", "2026-07-01 23:00:00", null),
      s("iso-earlier", "2026-07-01 01:00:00", "2026-07-01T01:00:00.000Z"),
    ]
    expect(sortSessionsByActivity(rows).map((r) => r.id)).toEqual([
      "legacy-latest",
      "iso-earlier",
    ])
  })

  it("does not mutate the input array", () => {
    const rows = [
      s("a", "2026-01-01 00:00:00", null),
      s("b", "2026-02-01 00:00:00", null),
    ]
    const before = rows.map((r) => r.id)
    sortSessionsByActivity(rows)
    expect(rows.map((r) => r.id)).toEqual(before)
  })
})
