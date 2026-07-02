// Pure cost aggregation for the /routines Insights tab. Kept
// framework-free (mirrors lib/runs-insights.ts) so the spend numbers
// are unit-testable without rendering the view.
//
// Input is the workspace-wide pipeline-runs list (hooks/
// use-pipeline-runs — capped at the most recent 100 runs), so the
// stats read as "spend over the recent run sample", not an all-time
// ledger. Callers surface the sample size (`runCount`) next to the
// number so the window is explicit.

export interface RunCostStats {
  /** Total spend across the sampled runs, in USD. */
  totalUsd: number
  /** Mean cost per run over the whole sample, or null when empty. */
  avgPerRunUsd: number | null
  /** How many runs the aggregate covers (the sample size). */
  runCount: number
}

/**
 * aggregateRunCosts sums per-run `cost_usd` and derives the mean.
 * Free runs (missing / null / zero cost) still count toward the
 * denominator — a workspace where most runs cost nothing should show
 * a low average, not hide it. NaN and negative values are treated as
 * zero so one malformed row can't corrupt the KPI.
 */
export function aggregateRunCosts(
  runs: { cost_usd?: number | null }[],
): RunCostStats {
  let total = 0
  for (const r of runs) {
    const c = r.cost_usd
    if (typeof c === "number" && Number.isFinite(c) && c > 0) total += c
  }
  const count = runs.length
  return {
    totalUsd: total,
    avgPerRunUsd: count > 0 ? total / count : null,
    runCount: count,
  }
}

/**
 * formatUsd renders a dollar amount for the KPI tiles: 2 decimals from
 * $1 up, 4 below so micro-runs ($0.0004) stay legible (same tolerance
 * as formatStepCost in routine-cost-format.ts). Zero is "$0.00";
 * non-finite / negative input renders as "—".
 */
export function formatUsd(usd: number): string {
  if (!Number.isFinite(usd) || usd < 0) return "—"
  if (usd === 0) return "$0.00"
  return usd >= 1 ? `$${usd.toFixed(2)}` : `$${usd.toFixed(4)}`
}
