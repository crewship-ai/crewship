// Routine checkup and Health rows (Harbor 2 RT-06, RT-07), composed in the
// browser from data the server already returns: the routine's status and
// draft, its newest runs, and GET /pipelines/budget-summary. No endpoint
// exists for a checkup; a check the data cannot prove is not listed.
// Remaining for the backend (#3468): GET .../pipelines/{slug}/checkup with the
// agent / credential / egress / cost checks and a breaker counter, and a
// per-routine 30-day aggregate.

import { runBarTone, type RunBarTone } from "@/lib/routine-page"
import type { BudgetSummary } from "@/hooks/use-routine-budget"

export interface HealthRun {
  id: string
  pipeline_slug: string
  status: string
  outcome?: string
  started_at: string
  error_message?: string
}

export interface HealthRoutine {
  slug: string
  name?: string
  status?: string
  draft?: { revision: number } | null
}

export type CheckTone = "ok" | "warn" | "bad"

export interface Check {
  key: string
  tone: CheckTone
  title: string
  detail: string
  /** A tab of the routine page that fixes it. */
  fix?: { label: string; tab: "overview" | "steps" | "triggers" | "settings" | "versions" }
}

const newestFirst = (runs: HealthRun[]) => [...runs].sort((a, b) => (Date.parse(b.started_at) || 0) - (Date.parse(a.started_at) || 0))
const isFailed = (r: HealthRun) => runBarTone(r).tone === "bad"
const isFinished = (r: HealthRun) => !["running", "queued", "waiting"].includes(r.status)

/** The checks the server's data can prove, worst first. */
export function routineChecks({ routine, runs }: { routine: Pick<HealthRoutine, "status" | "draft">; runs: HealthRun[] }): Check[] {
  const list = newestFirst(runs)
  const out: Check[] = []

  if (routine.status === "disabled")
    out.push({ key: "off", tone: "bad", title: "Turned off", detail: "It does not start until an Owner or Admin turns it on.", fix: { label: "Open settings", tab: "settings" } })
  if (routine.status === "proposed")
    out.push({ key: "proposed", tone: "bad", title: "Waiting for approval", detail: "A Manager or above approves it before it can run; not the proposer.", fix: { label: "Review", tab: "overview" } })

  const last = list[0]
  if (last && isFailed(last) && /missing[_ ]credential|credential/i.test(last.error_message ?? ""))
    out.push({ key: "missing-credential", tone: "bad", title: "A credential is missing", detail: (last.error_message ?? "").split("\n")[0].slice(0, 160), fix: { label: "Open steps", tab: "steps" } })

  let streak = 0
  for (const r of list) {
    if (!isFinished(r)) continue
    if (!isFailed(r)) break
    streak++
  }
  if (streak >= 2)
    out.push({ key: "failures-in-a-row", tone: "warn", title: `${streak} failures in a row`, detail: "The newest finished runs all ended without finishing." })

  if (list.some((r) => r.status === "waiting"))
    out.push({ key: "decision-waiting", tone: "warn", title: "A decision is waiting", detail: "A run is paused until a person decides." })

  if (routine.draft)
    out.push({ key: "untested-changes", tone: "warn", title: "Changes are not live", detail: `Draft revision ${routine.draft.revision} is saved but not published.`, fix: { label: "Open versions", tab: "versions" } })

  if (streak < 2) out.push({ key: "no-failures-in-a-row", tone: "ok", title: "No failures in a row", detail: "The newest finished run did not fail." })
  if (!out.some((c) => c.key === "off" || c.key === "proposed")) out.push({ key: "runnable", tone: "ok", title: "Can run", detail: "It is approved and turned on." })
  if (!routine.draft) out.push({ key: "published", tone: "ok", title: "No unpublished changes", detail: "What runs is what is saved." })
  const order: Record<CheckTone, number> = { bad: 0, warn: 1, ok: 2 }
  return out.sort((a, b) => order[a.tone] - order[b.tone])
}

export function checkupCounts(checks: Check[]) {
  return {
    bad: checks.filter((c) => c.tone === "bad").length,
    warn: checks.filter((c) => c.tone === "warn").length,
    ok: checks.filter((c) => c.tone === "ok").length,
    all: checks.length,
  }
}

export interface HealthBar {
  id: string
  tone: RunBarTone
  label: string
}

export interface HealthRow {
  slug: string
  name: string
  bars: HealthBar[]
  /** Runs in the sample (up to 20). */
  runCount: number
  finished: number
  ok: number
  successPct: number | null
  /** USD spent this calendar month, or null when the summary is unavailable. */
  spentMonth: number | null
  checkup: ReturnType<typeof checkupCounts>
}

/** One row per routine from the newest runs it has in `runs`. */
export function routineHealthRows(routines: HealthRoutine[], runs: HealthRun[], spend: BudgetSummary | null, n = 20): HealthRow[] {
  const bySlug = new Map<string, HealthRun[]>()
  for (const r of newestFirst(runs)) {
    const l = bySlug.get(r.pipeline_slug) ?? []
    if (l.length < n) l.push(r)
    bySlug.set(r.pipeline_slug, l)
  }
  const spent = new Map((spend?.routines ?? []).map((s) => [s.slug, s.spent_usd]))
  const rows = routines.map((routine): HealthRow => {
    const mine = bySlug.get(routine.slug) ?? []
    const done = mine.filter(isFinished).filter((r) => r.status !== "cancelled")
    const ok = done.filter((r) => !isFailed(r)).length
    return {
      slug: routine.slug,
      name: routine.name || routine.slug,
      bars: mine.map((r) => ({ id: r.id, ...runBarTone(r) })).reverse(),
      runCount: mine.length,
      finished: done.length,
      ok,
      successPct: done.length ? Math.round((ok / done.length) * 100) : null,
      spentMonth: spend ? (spent.get(routine.slug) ?? 0) : null,
      checkup: checkupCounts(routineChecks({ routine, runs: mine })),
    }
  })
  return rows.sort((a, b) => b.checkup.bad - a.checkup.bad || b.checkup.warn - a.checkup.warn)
}
