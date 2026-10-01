import { describe, it, expect } from "vitest"

import { addDays, dayInBounds, facetCounts, lastDays, monthGrid, tallyByDay, type ActivityEvent } from "../audit-activity"
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
