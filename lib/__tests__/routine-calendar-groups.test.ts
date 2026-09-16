import { describe, expect, it } from "vitest"
import {
  agendaItems,
  cadenceLabel,
  calendarFilterCounts,
  calendarOutcome,
  daySummary,
  groupByRoutine,
  matchesCalendarFilter,
  timeRange,
  type CalendarEntry,
} from "../routine-calendar-groups"

// Local-time instants so the clocks below are stable in any zone.
const at = (h: number, m = 0) => new Date(2026, 8, 16, h, m).toISOString()

let seq = 0
function planned(slug: string, h: number, m = 0): CalendarEntry {
  return { id: `p${seq++}`, kind: "planned", at: at(h, m), slug, name: slug }
}
function ran(slug: string, h: number, status: string, outcome?: string): CalendarEntry {
  return { id: `r${seq++}`, kind: "run", at: at(h), slug, name: slug, status, outcome }
}

describe("calendarOutcome", () => {
  it("maps a planned start and each run verdict to one of the day colours", () => {
    expect(calendarOutcome(planned("a", 8))).toBe("planned")
    expect(calendarOutcome(ran("a", 8, "completed"))).toBe("completed")
    expect(calendarOutcome(ran("a", 8, "failed"))).toBe("failed")
    expect(calendarOutcome(ran("a", 8, "completed", "FAILED"))).toBe("failed")
    expect(calendarOutcome(ran("a", 8, "waiting"))).toBe("waiting")
    expect(calendarOutcome(ran("a", 8, "running"))).toBe("running")
    expect(calendarOutcome(ran("a", 8, "cancelled"))).toBe("stopped")
  })
})

describe("routine groups", () => {
  it("orders groups by their first start and gives each a time range", () => {
    const groups = groupByRoutine([planned("b", 10), planned("a", 9), planned("b", 8, 30)])
    expect(groups.map((g) => g.slug)).toEqual(["b", "a"])
    expect(timeRange(groups[0].entries)).toBe("08:30–10:00")
    expect(timeRange(groups[1].entries)).toBe("09:00")
  })

  it("names a regular cadence and stays silent about an irregular one", () => {
    const regular = Array.from({ length: 5 }, (_, i) => planned("c", 8 + Math.floor(i / 4), (i % 4) * 15))
    expect(cadenceLabel(regular)).toBe("every 15 min")
    expect(cadenceLabel([planned("c", 8), planned("c", 9), planned("c", 10)])).toBe("every hour")
    expect(cadenceLabel([planned("c", 8), planned("c", 10), planned("c", 12)])).toBe("every 2 h")
    expect(cadenceLabel([planned("c", 8), planned("c", 9), planned("c", 11)])).toBeNull()
    expect(cadenceLabel([planned("c", 8), planned("c", 9)])).toBeNull()
    // Runs that already happened have no cadence to promise.
    expect(cadenceLabel([ran("c", 8, "completed"), ran("c", 9, "completed"), ran("c", 10, "completed")])).toBeNull()
  })
})

describe("day agenda", () => {
  it("keeps routines with one or two entries as rows and collapses three or more", () => {
    const items = agendaItems([
      planned("briefing", 7, 30),
      planned("invoice", 8),
      planned("invoice", 14),
      planned("classify", 8),
      planned("classify", 8, 15),
      planned("classify", 8, 30),
      ran("audit", 3, "failed"),
    ])
    expect(items.map((i) => (i.kind === "group" ? `group:${i.group.slug}` : `entry:${i.entry.slug}`))).toEqual([
      "entry:audit",
      "entry:briefing",
      "entry:invoice",
      "entry:invoice",
      "group:classify",
    ])
    const group = items[4]
    if (group.kind === "group") expect(group.group.entries).toHaveLength(3)
  })

  it("summarises the day in words, omitting zero counts", () => {
    expect(daySummary([])).toBe("")
    expect(daySummary([planned("a", 8)])).toBe("1 planned")
    expect(daySummary([ran("a", 8, "failed"), ran("a", 9, "waiting"), ran("a", 10, "completed")])).toBe(
      "3 ran · 1 failed · 1 waiting",
    )
  })
})

describe("filters", () => {
  const entries = [
    planned("a", 8),
    ran("a", 9, "completed"),
    ran("a", 10, "failed"),
    ran("a", 11, "waiting"),
    { id: "once", kind: "pending", at: at(12), slug: "a", pinned_version: 2 } as CalendarEntry,
  ]
  it("counts every chip against the unfiltered day", () => {
    expect(calendarFilterCounts(entries)).toEqual({ all: 5, planned: 2, ran: 3, waiting: 1, failed: 1 })
  })
  it("matches planned starts, runs, waiting and failed runs", () => {
    expect(entries.filter((e) => matchesCalendarFilter(e, "all"))).toHaveLength(5)
    expect(entries.filter((e) => matchesCalendarFilter(e, "planned")).map((e) => e.id)).toEqual([entries[0].id, "once"])
    expect(entries.filter((e) => matchesCalendarFilter(e, "ran"))).toHaveLength(3)
    expect(entries.filter((e) => matchesCalendarFilter(e, "waiting")).map((e) => e.status)).toEqual(["waiting"])
    expect(entries.filter((e) => matchesCalendarFilter(e, "failed")).map((e) => e.status)).toEqual(["failed"])
  })
})
