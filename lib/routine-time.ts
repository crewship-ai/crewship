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

/** The zone's offset from UTC in minutes at an instant; null when the zone or date cannot be read. */
function utcOffsetMinutes(instant: Date, timeZone: string): number | null {
  if (Number.isNaN(instant.getTime())) return null
  try {
    const parts = new Intl.DateTimeFormat("en-US", { timeZone, timeZoneName: "longOffset" }).formatToParts(instant)
    const name = parts.find((p) => p.type === "timeZoneName")?.value ?? ""
    if (name === "GMT") return 0
    const m = /^GMT([+-])(\d{1,2}):?(\d{2})?$/.exec(name)
    if (!m) return null
    return (m[1] === "-" ? -1 : 1) * (Number(m[2]) * 60 + Number(m[3] ?? 0))
  } catch {
    return null
  }
}

/**
 * For each start in order: does the zone's clock differ from the one at the
 * start before it? The first start is compared with `reference`, the moment
 * the list was worked out. Computed from the zone's own rules, so it needs no
 * server; anything unreadable is simply not flagged.
 */
export function clockChangeFlags(occurrences: string[], timeZone: string, reference: Date = new Date()): boolean[] {
  let previous = utcOffsetMinutes(reference, timeZone)
  return occurrences.map((o) => {
    const offset = utcOffsetMinutes(new Date(o), timeZone)
    const changed = previous !== null && offset !== null && offset !== previous
    if (offset !== null) previous = offset
    return changed
  })
}
