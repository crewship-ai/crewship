// Pure helpers for the Journal "Runs" fleet-operations overview. Kept
// framework-free so the numeric logic (success rate, bar widths, model
// label shortening) is unit-testable without rendering the view.

export type RunWindow = "24h" | "7d" | "30d"

export interface RunInsightCategory {
  key: string
  total: number
  failed: number
}

export interface RunInsightCrew {
  id: string
  name: string
  total: number
  failed: number
}

export interface RunInsightAgent {
  id: string
  name: string
  crew_name: string
  total: number
  failed: number
}

export interface RunInsights {
  window: RunWindow
  totals: { total: number; succeeded: number; failed: number; running: number }
  duration: { p50_ms: number; p95_ms: number }
  by_trigger: RunInsightCategory[]
  by_model: RunInsightCategory[]
  by_crew: RunInsightCrew[]
  top_agents: RunInsightAgent[]
  truncated: boolean
}

export const RUN_WINDOWS: RunWindow[] = ["24h", "7d", "30d"]

export const WINDOW_LABEL: Record<RunWindow, string> = {
  "24h": "last 24h",
  "7d": "last 7 days",
  "30d": "last 30 days",
}

/**
 * Success rate over completed+failed runs, as an integer percent. Running
 * and cancelled runs are excluded from the denominator (they're not an
 * outcome yet / not a pass-fail signal). Returns null when there's no
 * completed-or-failed run to rate — callers render "—".
 */
export function successRate(succeeded: number, failed: number): number | null {
  const denom = succeeded + failed
  if (denom <= 0) return null
  return Math.round((succeeded / denom) * 100)
}

/** Tailwind text color for a success-rate value (green ≥90, amber ≥70, red below). */
export function successRateColor(rate: number | null): string | undefined {
  if (rate === null) return undefined
  if (rate >= 90) return "rgb(52, 211, 153)"
  if (rate >= 70) return "rgb(251, 191, 36)"
  return "rgb(248, 113, 113)"
}

/** Percentage width (0–100) of a bucket relative to the largest bucket total. */
export function barPercent(value: number, max: number): number {
  if (max <= 0) return 0
  return Math.max(2, Math.round((value / max) * 100))
}

/** Largest total across breakdown buckets (for bar scaling). */
export function maxTotal(cats: { total: number }[]): number {
  return cats.reduce((m, c) => (c.total > m ? c.total : m), 0)
}

/**
 * Shorten a resolved model id to a chip label: "claude-opus-4-8" → "opus-4-8",
 * "claude-sonnet-4-5" → "sonnet-4-5". Leaves non-claude / already-short ids and
 * the "unknown" sentinel untouched.
 */
export function shortModel(model: string): string {
  if (!model || model === "unknown") return model || "unknown"
  return model.replace(/^claude-/, "")
}

/** Per-bucket failure rate as integer percent (0 when no runs). */
export function failRate(total: number, failed: number): number {
  if (total <= 0) return 0
  return Math.round((failed / total) * 100)
}
