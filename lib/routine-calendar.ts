export const CALENDAR_VIEWS = ["day", "three-days", "week", "month", "year"] as const
export type CalendarView = typeof CALENDAR_VIEWS[number]
export const CALENDAR_LABELS: Record<CalendarView, string> = { day: "Day", "three-days": "3 days", week: "Week", month: "Month", year: "Year" }
export const dateKey = (date: Date) => `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`
export function parseCalendarDate(value: string | null): Date | null {
  if (!value || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return null
  const [y, m, d] = value.split("-").map(Number)
  const date = new Date(y, m - 1, d)
  return dateKey(date) === value ? date : null
}
export function addDays(date: Date, count: number) { return new Date(date.getFullYear(), date.getMonth(), date.getDate() + count) }
export function calendarRange(date: Date, view: CalendarView) {
  let from = addDays(date, 0)
  if (view === "week") from = addDays(from, -((from.getDay() + 6) % 7))
  if (view === "month") from = new Date(date.getFullYear(), date.getMonth(), 1)
  if (view === "year") from = new Date(date.getFullYear(), 0, 1)
  const to = view === "year" ? new Date(from.getFullYear() + 1, 0, 1) : view === "month" ? new Date(from.getFullYear(), from.getMonth() + 1, 1) : addDays(from, view === "week" ? 7 : view === "three-days" ? 3 : 1)
  return { from, to }
}
/** Split large views into API-sized local months; DST never changes day identity. */
export function calendarWindows(from: Date, to: Date) {
  const windows: { from: Date; to: Date }[] = []
  for (let cursor = from; cursor < to;) {
    const next = new Date(cursor.getFullYear(), cursor.getMonth() + 1, 1)
    const end = next < to ? next : to
    windows.push({ from: cursor, to: end }); cursor = end
  }
  return windows
}
export function moveCalendar(date: Date, view: CalendarView, direction: number) {
  if (view === "year") return new Date(date.getFullYear() + direction, 0, 1)
  if (view === "month") return new Date(date.getFullYear(), date.getMonth() + direction, 1)
  return addDays(date, direction * (view === "week" ? 7 : view === "three-days" ? 3 : 1))
}
export function scheduledInstant(day: string, time: string): Date | null {
  if (!parseCalendarDate(day) || !/^([01]\d|2[0-3]):[0-5]\d$/.test(time)) return null
  const instant = new Date(`${day}T${time}:00`)
  // Reject a nonexistent local hour at the spring DST transition.
  return dateKey(instant) === day && `${String(instant.getHours()).padStart(2, "0")}:${String(instant.getMinutes()).padStart(2, "0")}` === time ? instant : null
}
