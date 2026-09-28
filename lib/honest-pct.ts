/** A success rate that never rounds a failure away: 199 of 200 is 99%, not
 * 100%, and one pass in 300 is 1%, not 0%. Null when nothing finished. */
export function honestPct(ok: number, total: number): number | null {
  if (total <= 0) return null
  const pct = Math.round((ok / total) * 100)
  return Math.min(ok < total ? 99 : 100, Math.max(ok > 0 ? 1 : 0, pct))
}
