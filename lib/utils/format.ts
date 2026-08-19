/** No basis to compute. The only glyph that means it, and it means nothing else. */
const EM_DASH = "—"

/**
 * Format a cost value as a dollar string.
 *
 * `null` (or a non-finite number) is "no basis to compute" and renders as an em
 * dash. A measured `0` renders as a zero — `$0.0000`, or `$0.00` in adaptive
 * mode — because a run that genuinely cost nothing (agentless, cache-hit, a
 * `code` or `transform` step that never called a model) is a different claim
 * from a run whose cost we failed to record.
 *
 * #1939: this function used to collapse both into the em dash, which is the
 * direction that costs money to misread — #1205 was a real instance of cost
 * silently not being recorded, and it would have looked exactly like a free
 * run. The rule here is the one the rest of the product already follows
 * (docs/prd/pages.md §9b.4; lib/routines-insights.ts's formatUsd renders zero
 * as "$0.00", as do the dashboard, journal-spend and mission tables).
 *
 * A caller that wants an empty state rather than a number must pass `null`;
 * zero no longer stands in for one.
 *
 * @param cost - The cost in dollars (e.g. 0.0042), or null when it is unknown.
 * @param adaptive - If true, use 2 decimal places for costs >= $0.01 (and for
 *   an exact zero); otherwise always 4, so a sub-cent charge never rounds into
 *   the measured zero above.
 */
export function formatCost(cost: number | null, adaptive = false): string {
  if (cost == null || !Number.isFinite(cost)) return EM_DASH
  if (adaptive && (cost === 0 || cost >= 0.01)) return `$${cost.toFixed(2)}`
  return `$${cost.toFixed(4)}`
}
