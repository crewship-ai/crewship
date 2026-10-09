// The rail, focused on one routine (#2998).
//
// Selecting a routine's row narrows the rail to that routine: everything else
// goes, and its runs are listed by day with their time and outcome, under the
// same status rows and time window the rail already has. The rules for which
// runs show and how they group live here, where a test can hold them.

import type { ChainSummary } from "@/hooks/use-chains"
import { runTone, type RunTone } from "@/lib/activity-run"

export interface FocusRecord {
  id: string
  status: string
  started_at: string
  duration_ms?: number
  triggered_via?: string
}

export interface FocusRun {
  id: string
  tone: RunTone
  time: string
  startedAt: string
  durationMs?: number
  triggeredVia?: string
}

export interface FocusDay {
  key: string
  label: string
  runs: FocusRun[]
}

type FocusScope = "all" | "active" | "waiting" | "failed" | "done" | "stopped"

const SCOPE_OF: Record<RunTone, Exclude<FocusScope, "all">> = {
  running: "active",
  waiting: "waiting",
  failed: "failed",
  done: "done",
  stopped: "stopped",
}

/**
 * A routine's runs within the window, grouped by local day (newest first),
 * narrowed to one outcome. The counts cover every outcome in the window, so a
 * status row survives its own selection — the rail's rule elsewhere.
 */
export function focusRuns(
  records: FocusRecord[],
  opt: { scope: string; rangeMs: number; now?: number },
): { days: FocusDay[]; counts: Record<Exclude<FocusScope, "all">, number>; total: number } {
  const now = opt.now ?? Date.now()
  const counts = { active: 0, waiting: 0, failed: 0, done: 0, stopped: 0 }
  const inWindow = records
    .filter((r) => {
      const t = Date.parse(r.started_at)
      return Number.isFinite(t) && t >= now - opt.rangeMs && t <= now + 60_000
    })
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
  for (const r of inWindow) counts[SCOPE_OF[runTone(r.status)]]++

  const today = new Date(now)
  today.setHours(0, 0, 0, 0)
  const yesterday = new Date(today)
  yesterday.setDate(yesterday.getDate() - 1)

  const days: FocusDay[] = []
  for (const r of inWindow) {
    const tone = runTone(r.status)
    if (opt.scope !== "all" && SCOPE_OF[tone] !== opt.scope) continue
    const d = new Date(r.started_at)
    const day = new Date(d)
    day.setHours(0, 0, 0, 0)
    const key = day.toDateString()
    const label =
      day.getTime() === today.getTime()
        ? "Today"
        : day.getTime() === yesterday.getTime()
          ? "Yesterday"
          : day.toLocaleDateString(undefined, { weekday: "short", day: "numeric", month: "short" })
    let bucket = days.find((x) => x.key === key)
    if (!bucket) {
      bucket = { key, label, runs: [] }
      days.push(bucket)
    }
    bucket.runs.push({
      id: r.id,
      tone,
      time: d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false }),
      startedAt: r.started_at,
      durationMs: r.duration_ms,
      triggeredVia: r.triggered_via,
    })
  }
  return { days, counts, total: inWindow.length }
}

/**
 * What a rail row IS — a routine, issue work, or agent work — as opposed to
 * what started it. "from QUA-1" alone could not tell a routine started from an
 * issue apart from an agent working that issue.
 */
export function rowKind(c: ChainSummary): { label: "Routine" | "Issue" | "Agent" | "Workflow"; tone: string } {
  if (c.kind === "assignment") {
    return c.started_by_kind === "issue" || c.started_by_kind === "lead_planning"
      ? { label: "Issue", tone: "text-purple" }
      : { label: "Agent", tone: "text-primary" }
  }
  if (c.routine_slug) return { label: "Routine", tone: "text-info" }
  return { label: "Workflow", tone: "text-muted-foreground" }
}
