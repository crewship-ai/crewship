/**
 * Format a cost value as a dollar string. Returns an em dash for null/zero values.
 * @param cost - The cost in dollars (e.g. 0.0042).
 * @param adaptive - If true, use 2 decimal places for costs >= $0.01; otherwise always 4.
 */
export function formatCost(cost: number | null, adaptive = false): string {
  if (cost == null || cost === 0) return "\u2014"
  if (adaptive && cost >= 0.01) return `$${cost.toFixed(2)}`
  return `$${cost.toFixed(4)}`
}
