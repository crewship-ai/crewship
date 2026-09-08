import { describe, it, expect } from "vitest"
import { calendarRange, calendarWindows, moveCalendar, dateKey, parseCalendarDate, scheduledInstant } from "../routine-calendar"

describe("routine calendar dates", () => {
  it("uses Monday weeks across year boundaries", () => {
    const range = calendarRange(new Date(2027, 0, 1), "week")
    expect(dateKey(range.from)).toBe("2026-12-28")
    expect(dateKey(range.to)).toBe("2027-01-04")
  })
  it("splits a leap year into twelve complete non-overlapping API windows", () => {
    const { from, to } = calendarRange(new Date(2028, 5, 12), "year")
    const windows = calendarWindows(from, to)
    expect(windows).toHaveLength(12)
    expect(dateKey(windows[1].to)).toBe("2028-03-01")
    windows.forEach((w, i) => {
      expect(w.to.getTime() - w.from.getTime()).toBeLessThanOrEqual(32 * 86400000)
      if (i) expect(w.from).toEqual(windows[i - 1].to)
    })
    expect(windows[11].to).toEqual(to)
  })
  it("moves months without overflowing the last day and moves three days exactly", () => {
    expect(dateKey(moveCalendar(new Date(2026, 0, 31), "month", 1))).toBe("2026-02-01")
    expect(dateKey(moveCalendar(new Date(2026, 11, 31), "three-days", 1))).toBe("2027-01-03")
  })
  it("refuses invalid dates and times instead of normalizing them silently", () => {
    expect(parseCalendarDate("2026-02-30")).toBeNull()
    expect(scheduledInstant("2026-09-10", "24:00")).toBeNull()
    const instant = scheduledInstant("2026-09-10", "09:30")!
    expect(dateKey(instant)).toBe("2026-09-10")
    expect(instant.getHours()).toBe(9)
    expect(instant.getMinutes()).toBe(30)
  })
})
