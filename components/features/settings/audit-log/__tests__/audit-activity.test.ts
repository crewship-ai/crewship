import { describe, it, expect } from "vitest"

import { addDays, chartBuckets, dayInBounds, facetCounts, lastDays, monthGrid, tallyByDay, type ActivityEvent } from "../audit-activity"
import { DEFAULT_AUDIT_FILTERS, type AuditFilters } from "../audit-filters"

const f = (over: Partial<AuditFilters> = {}): AuditFilters => ({ ...DEFAULT_AUDIT_FILTERS, ...over })
const ev = (at: string, action: string, entityType: string, userId: string | null = null): ActivityEvent => ({ at, action, entityType, userId })

describe("calendar days", () => {
  it("lays a month out Monday-first", () => {
    // 1 September 2026 is a Tuesday: one blank, then thirty days.
    const cells = monthGrid(2026, 8)
    expect(cells[0]).toBeNull()
    expect(cells[1]).toBe("2026-09-01")
    expect(cells).toHaveLength(31)
  })

  it("counts back whole UTC days, oldest first", () => {
    expect(lastDays(3, new Date("2026-09-29T23:30:00Z"))).toEqual(["2026-09-27", "2026-09-28", "2026-09-29"])
    expect(addDays("2026-09-30", 1)).toBe("2026-10-01")
  })

  // The end a custom range sends is the start of the next day, so that day is out.
  it("reads range bounds as days", () => {
    const b = { from: "2026-09-20T00:00:00.000Z", to: "2026-09-25T00:00:00.000Z" }
    expect(dayInBounds("2026-09-20", b)).toBe(true)
    expect(dayInBounds("2026-09-24", b)).toBe(true)
    expect(dayInBounds("2026-09-25", b)).toBe(false)
    expect(dayInBounds("2026-01-01", {})).toBe(true)
  })
})

describe("tallyByDay", () => {
  it("splits each day into finished and failed runs", () => {
    const t = tallyByDay([
      ev("2026-09-29T07:00:00Z", "agent.run.failed", "agent_run"),
      ev("2026-09-29T08:00:00Z", "agent.run.completed", "agent_run"),
      ev("2026-09-29T09:00:00Z", "crew.update", "CREW"),
      ev("2026-09-28T09:00:00Z", "crew.update", "CREW"),
    ])
    expect(t.get("2026-09-29")).toEqual({ total: 3, completed: 1, failed: 1 })
    expect(t.get("2026-09-28")).toEqual({ total: 1, completed: 0, failed: 0 })
  })
})

// Each facet's number is what picking that value would show: every other
// filter applies, its own does not, and the time range always does.
describe("facetCounts", () => {
  const events = [
    ev("2026-09-29T07:00:00Z", "agent.run.failed", "agent_run"),
    ev("2026-09-29T08:00:00Z", "agent.run.completed", "agent_run"),
    ev("2026-09-29T09:00:00Z", "crew.update", "CREW", "u1"),
    ev("2026-09-10T09:00:00Z", "crew.update", "CREW", "u1"),
  ]
  const week = { from: "2026-09-22T12:00:00.000Z" }

  it("counts within the time range", () => {
    const c = facetCounts(events, f(), week)
    expect(c.category).toMatchObject({ all: 3, agent_run: 2, CREW: 1 })
    expect(c.person).toEqual({ u1: 1 })
    expect(c.result).toEqual({ failed: 1, completed: 1 })
  })

  it("applies the other facets but not its own", () => {
    const c = facetCounts(events, f({ category: "CREW", result: "failed" }), week)
    expect(c.result).toEqual({})
    expect(c.category).toMatchObject({ all: 1, agent_run: 1 })
    expect(c.category.CREW).toBeUndefined()
  })
})

describe("chartBuckets — the chart covers exactly the range listed", () => {
  const now = new Date("2026-09-15T12:30:00Z")
  const ev = (at: string, action = "agent.update") => ({ at, action, entityType: "AGENT", userId: null })

  it("draws a day per bar for a week, counting runs that finished or failed", () => {
    const { buckets, unit, clipped } = chartBuckets(
      [ev("2026-09-14T10:00:00Z", "agent.run.failed"), ev("2026-09-14T11:00:00Z"), ev("2026-09-15T01:00:00Z", "agent.run.completed")],
      { from: "2026-09-08T12:30:00.000Z" }, "2026-08-12", now,
    )
    expect(unit).toBe("day")
    expect(clipped).toBe(false)
    expect(buckets.map((b) => b.day)).toEqual(["2026-09-08", "2026-09-09", "2026-09-10", "2026-09-11", "2026-09-12", "2026-09-13", "2026-09-14", "2026-09-15"])
    expect(buckets[6]).toMatchObject({ total: 2, failed: 1, completed: 0 })
    expect(buckets[7]).toMatchObject({ total: 1, completed: 1 })
  })

  it("draws hours for a day and five-minute bars for an hour", () => {
    expect(chartBuckets([], { from: "2026-09-14T12:30:00.000Z" }, "2026-08-12", now).unit).toBe("hour")
    const hour = chartBuckets([ev("2026-09-15T12:07:00Z")], { from: "2026-09-15T11:30:00.000Z" }, "2026-08-12", now)
    expect(hour.unit).toBe("minute")
    expect(hour.buckets).toHaveLength(12)
    expect(hour.buckets.reduce((n, b) => n + b.total, 0)).toBe(1)
  })

  it("starts at the sampled window and says so for a longer range", () => {
    const r = chartBuckets([], { from: "2026-06-17T12:30:00.000Z" }, "2026-08-12", now)
    expect(r.clipped).toBe(true)
    expect(r.buckets[0].day).toBe("2026-08-12")
  })
})
