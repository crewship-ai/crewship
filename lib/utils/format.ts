export function formatCost(cost: number | null, adaptive = false): string {
  if (cost == null || cost === 0) return "\u2014"
  if (adaptive && cost >= 0.01) return `$${cost.toFixed(2)}`
  return `$${cost.toFixed(4)}`
}
