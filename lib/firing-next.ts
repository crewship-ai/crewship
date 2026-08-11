// What is about to run.
//
// The Routines lens answers "what ran". The other half of a schedule-driven
// workspace is "what is about to", and on a routine firing every minute that is
// the difference between reading a page and watching one.
//
// Deliberately small: four rows, a countdown, and nothing else. A schedule has
// a cron expression, a timezone, a catch-up policy, a wake gate and eight
// telemetry fields; all of that belongs on the routine's Triggers card, where
// it can be changed. Here it is a glance.

/** The subset of a pipeline schedule this module reads. */
export interface FiringSchedule {
  id: string
  name: string
  cron_expr: string
  enabled: boolean
  /** Server-computed next fire. Absent when the server has not computed one. */
  next_run_at?: string
  target_pipeline_slug?: string
}

export interface FiringRow {
  id: string
  /** What the cron was called by whoever wrote it. */
  name: string
  /** The routine it fires — what the reader is actually looking for. */
  slug?: string
  cron: string
  /** "in 12s" · "in 45m" · "in 3h". */
  dueIn: string
  /** Millis until it fires, for anything that wants the raw number. */
  inMs: number
}

/** How many rows a glance holds. */
const MAX_ROWS = 4

/**
 * The schedules about to fire, soonest first.
 *
 * Three kinds of row are dropped rather than shown, and each for the same
 * reason: the card's heading is a claim about the future, and a row that cannot
 * support it weakens every row that can.
 *
 *   · disabled — it is not firing next, it is not firing;
 *   · no next_run_at — the server computed none, and absent sorting as 0 would
 *     put the row that knows the least at the top of a list ordered by
 *     imminence;
 *   · already past — reachable between a scheduler tick and a page refresh, and
 *     rendering it would be a countdown running backwards.
 */
export function firingNext(schedules: FiringSchedule[], now: number): FiringRow[] {
  const rows: FiringRow[] = []
  for (const s of schedules) {
    if (!s.enabled || !s.next_run_at) continue
    const at = Date.parse(s.next_run_at)
    if (Number.isNaN(at)) continue
    const inMs = at - now
    if (inMs <= 0) continue
    rows.push({
      id: s.id,
      name: s.name,
      slug: s.target_pipeline_slug,
      cron: s.cron_expr,
      dueIn: countdown(inMs),
      inMs,
    })
  }
  return rows.sort((a, b) => a.inMs - b.inMs || a.id.localeCompare(b.id)).slice(0, MAX_ROWS)
}

/**
 * A countdown in one unit.
 *
 * One unit, never two: "in 1h 23m" is more precise and reads slower, and the
 * only decision this number supports is "do I wait for it or not". Rounded
 * down, so a row never claims to be sooner than it is.
 */
function countdown(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `in ${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `in ${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `in ${h}h`
  return `in ${Math.floor(h / 24)}d`
}
