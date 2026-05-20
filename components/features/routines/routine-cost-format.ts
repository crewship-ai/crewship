// Cost / duration / step-meta formatting shared between the Runs tab
// waterfall, the dry-run report panel, and the Overview tab. Pulled
// out of routine-runs-tab.tsx so the same parsing tolerances apply
// everywhere a routine surface needs to display step economics — a
// regression that broke parsing in one place would silently render
// "—" or NaN somewhere subtle (Overview's per-run cost column,
// Insights aggregations) without a single failing component.

// extractStepMeta pulls step_id + cost_usd + duration_ms from a
// journal entry's payload field, tolerating three on-the-wire shapes:
// parsed object (the common case), JSON-encoded string (when upstream
// serialised it twice), and absent. The journal emitter populates all
// three on `pipeline.step.completed` (internal/pipeline/journal.go
// emitStepCompleted); we surface cost and duration inline in the
// waterfall so users can spot a step that burned $0.45 of a $0.50
// run at a glance instead of digging into `routine logs <run_id>`.
//
// Costs/durations are optional — only `pipeline.step.completed`
// carries them; started/failed events return zero, which the caller
// renders as "—" rather than "$0.0000".
export function extractStepMeta(payload: unknown): {
  stepId: string
  costUSD: number
  durationMs: number
} {
  const empty = { stepId: "", costUSD: 0, durationMs: 0 }
  if (!payload) return empty
  let obj: Record<string, unknown> | null = null
  if (typeof payload === "string") {
    try {
      obj = JSON.parse(payload) as Record<string, unknown>
    } catch {
      return empty
    }
  } else if (typeof payload === "object") {
    obj = payload as Record<string, unknown>
  }
  if (!obj) return empty
  return {
    stepId: typeof obj.step_id === "string" ? obj.step_id : "",
    costUSD: typeof obj.cost_usd === "number" ? obj.cost_usd : 0,
    durationMs: typeof obj.duration_ms === "number" ? obj.duration_ms : 0,
  }
}

// extractStepID is the legacy single-field shape kept for callers
// that only need the step id (and to preserve the existing import
// surface).
export function extractStepID(payload: unknown): string {
  return extractStepMeta(payload).stepId
}

export function formatStepDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return "—"
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(s < 10 ? 2 : 1)}s`
  // Round the TOTAL seconds first, then split into minutes + seconds.
  // The previous formula computed Math.floor(s/60) and
  // Math.round(s - m*60) separately, which produced "1m60s" for
  // 119999ms because rem rounded to 60 before the minute carried.
  // Working in whole seconds avoids the carry entirely.
  const totalSec = Math.round(s)
  const m = Math.floor(totalSec / 60)
  const rem = totalSec % 60
  return `${m}m${rem.toString().padStart(2, "0")}s`
}

export function formatStepCost(usd: number): string {
  if (!Number.isFinite(usd) || usd <= 0) return "—"
  // 4 dp matches the overview tab's per-run cost rendering — keeps
  // micro-runs ($0.0001) legible without falling back to scientific
  // notation that's harder to scan in a column.
  return `$${usd.toFixed(4)}`
}
