// A cron expression, as a sentence.
//
// There were two private copies of this — one in the crews bottom
// panel, one in the routines schedules tab — and they disagreed. The
// routines copy rendered `0 8 * * 1` as "weekly (dow 1)", which is
// cron jargon with a word wrapped round it rather than a translation
// of it; the reader who asked for "a calendar, not a star and a zero"
// was being shown the star and the zero.
//
// One copy now, covering the patterns the scheduler actually produces.
// Anything it does not understand is returned VERBATIM: a cron string
// is honest about being a cron string, and a confidently wrong
// sentence is worse, because nothing about it looks wrong.

const DOW = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]
const DOW_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]

/** Day-of-week fields accept both 0 and 7 for Sunday. */
function dayIndex(n: string): number | null {
  if (!/^\d+$/.test(n)) return null
  const v = Number(n)
  if (v < 0 || v > 7) return null
  return v === 7 ? 0 : v
}

function ordinal(n: number): string {
  const rem100 = n % 100
  if (rem100 >= 11 && rem100 <= 13) return `${n}th`
  switch (n % 10) {
    case 1:
      return `${n}st`
    case 2:
      return `${n}nd`
    case 3:
      return `${n}rd`
    default:
      return `${n}th`
  }
}

/** "Mon, Wed and Fri" — an Oxford-comma-free list a person would say. */
function joinDays(names: string[]): string {
  if (names.length === 1) return names[0]
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`
}

export function describeCron(expr?: string | null): string {
  if (!expr || !expr.trim()) return "—"
  const raw = expr.trim()
  const parts = raw.split(/\s+/)
  if (parts.length !== 5) return raw
  const [min, hr, dom, mon, dow] = parts

  const time = (h: string, m: string) => `${h.padStart(2, "0")}:${m.padStart(2, "0")}`
  const everyN = (f: string) => (f.startsWith("*/") ? Number(f.slice(2)) : null)
  const fixed = (f: string) => /^\d+$/.test(f)

  // */N minutes — the only pattern where the hour field is a wildcard
  // and the result is still a schedule rather than a mistake.
  const nMin = everyN(min)
  if (nMin && Number.isFinite(nMin) && hr === "*" && dom === "*" && mon === "*" && dow === "*") {
    return nMin === 1 ? "Every minute" : `Every ${nMin} minutes`
  }

  const nHr = everyN(hr)
  if (fixed(min) && nHr && Number.isFinite(nHr) && dom === "*" && mon === "*" && dow === "*") {
    return nHr === 1 ? "Every hour" : `Every ${nHr} hours`
  }

  if (!fixed(min) || !fixed(hr)) return raw
  const at = time(hr, min)

  // Monthly, before the weekday branches: a fixed day-of-month with a
  // wildcard weekday is a monthly schedule.
  if (fixed(dom) && mon === "*" && dow === "*") {
    return `Monthly on the ${ordinal(Number(dom))} at ${at}`
  }
  if (dom !== "*" || mon !== "*") return raw

  if (dow === "*") return `Every day at ${at}`

  // Mon–Fri is common enough to deserve the word people use for it.
  if (dow === "1-5") return `Every weekday at ${at}`
  if (dow === "6,0" || dow === "0,6" || dow === "6-7") return `Every weekend day at ${at}`

  if (dow.includes(",")) {
    const idx = dow.split(",").map(dayIndex)
    if (idx.some((v) => v === null)) return raw
    const names = (idx as number[]).map((v) => DOW_SHORT[v])
    return `Every ${joinDays(names)} at ${at}`
  }

  const single = dayIndex(dow)
  if (single !== null) return `Every ${DOW[single]} at ${at}`

  return raw
}
