/** The calendar uses browser-local days; every instant names its zone. */
export function routineTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"
}
export function formatRoutineTime(
  value: string | Date,
  timeZone = routineTimeZone(),
): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return "Time unavailable"
  try {
    return `${new Intl.DateTimeFormat("en-GB", {
      day: "numeric",
      month: "short",
      year: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      timeZone,
    }).format(date)} · ${timeZone}`
  } catch {
    return formatRoutineTime(date, "UTC")
  }
}
