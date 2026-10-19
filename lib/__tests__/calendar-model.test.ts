import { describe, expect, it } from "vitest"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { CalendarEntry } from "@/lib/routine-calendar-groups"
import {
  EMPTY_CALENDAR_FILTERS,
  agendaLayout,
  apiWindows,
  calendarCounts,
  calendarHrefFromRoutines,
  dayLabel,
  filterItems,
  monthLayout,
  movePage,
  needYouSoon,
  nowLineAfter,
  pageRange,
  rangeTitle,
  routineItems,
  weekLayout,
} from "@/lib/calendar/model"

const routines = [
  { slug: "scan", name: "Weekly scan", author_user_id: "me", author_crew_id: "research" },
  { slug: "triage", name: "Nightly triage", author_user_id: "other", author_crew_id: "ops" },
] as Pipeline[]
const at = (y: number, m: number, d: number, h = 9, min = 0) => new Date(y, m - 1, d, h, min).toISOString()
const ev = (e: Partial<CalendarEntry> & { id: string; at: string; slug: string }): CalendarEntry => ({ kind: "planned", ...e })

describe("routineItems", () => {
  it("drops events whose routine the viewer cannot open and marks Mine by author", () => {
    const items = routineItems(
      [
        ev({ id: "a", at: at(2026, 10, 15, 8), slug: "scan" }),
        ev({ id: "b", at: at(2026, 10, 15, 2), slug: "triage", kind: "run", status: "failed" }),
        ev({ id: "c", at: at(2026, 10, 15, 3), slug: "hidden" }),
      ],
      routines,
      "me",
    )
    expect(items.map((i) => i.id)).toEqual(["run:b", "planned:a"])
    expect(items.map((i) => [i.mine, i.status, i.crewId])).toEqual([
      [false, "bad", "ops"],
      [true, "plan", "research"],
    ])
    expect(items[1]).toMatchObject({ day: "2026-10-15", time: "08:00", title: "Weekly scan", subtitle: "Repeating schedule" })
  })

  it("names a one-time start and a waiting run in words", () => {
    const [pending, run] = routineItems(
      [ev({ id: "p", kind: "pending", at: at(2026, 10, 16, 14), slug: "scan", pinned_version: 4 }), ev({ id: "r", kind: "run", status: "waiting", at: at(2026, 10, 17), slug: "scan" })],
      routines,
      "me",
    )
    expect(pending.subtitle).toBe("One-time start · v4")
    expect(run.status).toBe("wait")
    expect(run.subtitle).toMatch(/^Run · /)
  })
})

describe("ranges", () => {
  it("week starts on Monday, agenda is two weeks from the day, month is whole weeks", () => {
    const thu = new Date(2026, 9, 15)
    expect(pageRange(thu, "week")).toEqual({ from: new Date(2026, 9, 12), to: new Date(2026, 9, 19) })
    expect(pageRange(thu, "agenda")).toEqual({ from: new Date(2026, 9, 15), to: new Date(2026, 9, 29) })
    // October 2026: Thu 1 … Sat 31 → Mon 28 Sep … Sun 1 Nov, five rows.
    expect(pageRange(thu, "month")).toEqual({ from: new Date(2026, 8, 28), to: new Date(2026, 10, 2) })
    // March 2027 starts on a Monday and ends on a Wednesday: still whole weeks.
    const march = pageRange(new Date(2027, 2, 10), "month")
    expect(march.from).toEqual(new Date(2027, 2, 1))
    expect(march.to).toEqual(new Date(2027, 3, 5))
  })

  it("splits a month grid into windows the API accepts (≤ 32 days each)", () => {
    const { from, to } = pageRange(new Date(2026, 7, 1), "month") // Aug 2026: six rows, 42 days
    const windows = apiWindows(from, to)
    expect(windows).toHaveLength(2)
    expect(windows[0].from).toEqual(from)
    expect(windows.at(-1)!.to).toEqual(to)
    for (const w of windows) expect(w.to.getTime() - w.from.getTime()).toBeLessThanOrEqual(32 * 86_400_000)
    expect(apiWindows(...Object.values(pageRange(new Date(2026, 9, 15), "week")) as [Date, Date])).toHaveLength(1)
  })

  it("moves by the view and titles the range", () => {
    const thu = new Date(2026, 9, 15)
    expect(movePage(thu, "week", 1)).toEqual(new Date(2026, 9, 22))
    expect(movePage(thu, "agenda", -1)).toEqual(new Date(2026, 9, 1))
    expect(movePage(thu, "month", 1)).toEqual(new Date(2026, 10, 1))
    expect(rangeTitle(thu, "week")).toBe("12 – 18 October 2026")
    expect(rangeTitle(new Date(2026, 9, 1), "week")).toBe("28 September – 4 October 2026")
    expect(rangeTitle(thu, "month")).toBe("October 2026")
    expect(rangeTitle(thu, "agenda", thu)).toBe("Next 2 weeks")
    expect(dayLabel("2026-10-15", thu)).toBe("Today")
    expect(dayLabel("2026-10-16", thu)).toBe("Tomorrow")
    expect(dayLabel("2026-10-20", thu)).toBe("Tue 20 Oct")
  })
})

describe("filters and counts", () => {
  const items = routineItems(
    [
      ev({ id: "1", at: at(2026, 10, 15, 8), slug: "scan" }),
      ev({ id: "2", at: at(2026, 10, 15, 2), slug: "triage", kind: "run", status: "failed" }),
      ev({ id: "3", at: at(2026, 10, 15, 9), slug: "triage", kind: "run", status: "waiting" }),
      ev({ id: "4", at: at(2026, 10, 14, 9), slug: "scan", kind: "run", status: "completed" }),
    ],
    routines,
    "me",
  )

  it("counts per scope and crew; Status narrows, none checked = all", () => {
    const ws = { ...EMPTY_CALENDAR_FILTERS, scope: "workspace" as const }
    const counts = calendarCounts(items, ws)
    expect([counts.mine, counts.workspace]).toEqual([2, 4])
    expect(Object.fromEntries(counts.crews)).toEqual({ research: 2, ops: 2 })
    expect(counts.statuses).toEqual({ risk: 1, waiting: 1, planned: 1, done: 1 })
    expect(filterItems(items, ws)).toHaveLength(4)
    expect(filterItems(items, { ...ws, statuses: ["risk", "waiting"] }).map((i) => i.id)).toEqual(["run:2", "run:3"])
    // A status pick never zeroes its siblings' counts.
    expect(calendarCounts(items, { ...ws, statuses: ["risk"] }).statuses.done).toBe(1)
    expect(filterItems(items, EMPTY_CALENDAR_FILTERS).every((i) => i.mine)).toBe(true)
    expect(filterItems(items, { ...ws, crewId: "ops", query: "night" })).toHaveLength(2)
  })

  it("need you soon: waiting runs and runs that failed today, at most four", () => {
    const now = new Date(2026, 9, 15, 12)
    expect(needYouSoon(items, now).map((i) => i.id)).toEqual(["run:2", "run:3"])
    expect(needYouSoon(items, new Date(2026, 9, 16, 12)).map((i) => i.id)).toEqual(["run:3"])
  })
})

describe("views", () => {
  const many = routineItems(
    [
      ...Array.from({ length: 5 }, (_, i) => ev({ id: `p${i}`, at: at(2026, 10, 15, 10 + i), slug: "scan" })),
      ev({ id: "x", at: at(2026, 10, 15, 7, 30), slug: "triage", kind: "run", status: "failed" }),
      ev({ id: "y", at: at(2026, 10, 18, 7, 45), slug: "triage" }),
    ],
    routines,
    "me",
  )

  it("week has a row for each time that has an item (07:30, 07:45) and an All day row for date-only items", () => {
    const week = weekLayout(many, new Date(2026, 9, 12))
    expect(week.times).toEqual(["07:30", "07:45", "10:00", "11:00", "12:00", "13:00", "14:00"])
    expect(week.allDay).toEqual([])
    expect(week.days).toHaveLength(7)
    expect(nowLineAfter(week.times, new Date(2026, 9, 15, 8, 16))).toBe(1)
    expect(nowLineAfter(week.times, new Date(2026, 9, 15, 7, 29))).toBe(-1)
  })

  it("month caps three items per day with +N more and a risk dot", () => {
    const cells = monthLayout(many, new Date(2026, 9, 1))
    expect(cells).toHaveLength(35)
    const thu = cells.find((c) => c.key === "2026-10-15")!
    expect([thu.shown.length, thu.more, thu.risk]).toEqual([3, 3, true])
    expect(cells[0]).toMatchObject({ key: "2026-09-28", inMonth: false })
  })

  it("agenda groups by day, skips empty days and folds a routine's many planned starts", () => {
    const days = agendaLayout(many)
    expect(days.map((d) => d.key)).toEqual(["2026-10-15", "2026-10-18"])
    expect(days[0].rows.map((r) => r.kind)).toEqual(["item", "group"])
    const group = days[0].rows[1]
    expect(group.kind === "group" && group.items).toHaveLength(5)
  })

  it("maps Routines › Calendar views onto the page", () => {
    expect(calendarHrefFromRoutines("year", "2026-01-01")).toBe("/calendar?view=month&date=2026-01-01&layers=routine")
    expect(calendarHrefFromRoutines("three-days", "bad")).toBe("/calendar?view=week&layers=routine")
    expect(calendarHrefFromRoutines(null, null)).toBe("/calendar?view=month&layers=routine")
  })
})
