export function formatCost(cost: number | null): string {
  if (cost == null || cost === 0) return "\u2014"
  return `$${cost.toFixed(4)}`
}
