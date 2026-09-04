// Pure helpers + palettes shared by the dashboard page.

export const CREW_PALETTE: Record<string, string> = {
  blue: "rgb(96, 165, 250)",
  emerald: "rgb(52, 211, 153)",
  violet: "rgb(167, 139, 250)",
  amber: "rgb(251, 191, 36)",
  rose: "rgb(251, 113, 133)",
  cyan: "rgb(34, 211, 238)",
  lime: "rgb(163, 230, 53)",
  fuchsia: "rgb(232, 121, 249)",
}
// A crew's colour is EITHER a palette id ("blue") or a raw hex — the same
// rule lib/crew-icons.ts's crewColorHex documents. Only the ids were handled
// here, so every hex-coloured crew (most of them) drew its dots and its run
// series in the fallback grey.
export function crewColor(color: string | null | undefined): string {
  if (!color) return "rgb(148, 163, 184)"
  const palette = CREW_PALETTE[color]
  if (palette) return palette
  const hex = color.startsWith("#") ? color : `#${color}`
  return /^#[0-9a-fA-F]{6}$/.test(hex) ? hex : "rgb(148, 163, 184)"
}

export const STATUS_PALETTE = {
  BACKLOG: "rgb(96, 165, 250)",
  TODO: "rgb(34, 211, 238)",
  IN_PROGRESS: "rgb(167, 139, 250)",
  REVIEW: "rgb(251, 191, 36)",
  COMPLETED: "rgb(52, 211, 153)",
  FAILED: "rgb(248, 113, 113)",
  CANCELLED: "rgb(148, 163, 184)",
} as const

/** How many crews the run-volume chart stacks before folding the rest. */
export const RUN_VOLUME_SERIES_LIMIT = 8
export const RUN_VOLUME_OTHER_KEY = "__other"

/**
 * Pure: keep the busiest crews as their own series and fold everything else
 * into one "Other crews" series. A stacked bar with a hundred colours is a
 * texture, not a chart; eight is the most the legend can name and the eye can
 * follow (see the dataviz rule on categorical hue count).
 */
export function foldRunVolumeSeries<B extends { ts: string; [key: string]: string | number }>(
  buckets: B[],
  series: Array<{ key: string; label: string; color: string }>,
  limit = RUN_VOLUME_SERIES_LIMIT,
): { buckets: B[]; series: Array<{ key: string; label: string; color: string }>; folded: number } {
  if (series.length <= limit) return { buckets, series, folded: 0 }
  const totals = new Map<string, number>()
  for (const s of series) totals.set(s.key, buckets.reduce((sum, b) => sum + Number(b[s.key] ?? 0), 0))
  const ranked = [...series].sort((a, b) => (totals.get(b.key) ?? 0) - (totals.get(a.key) ?? 0) || a.label.localeCompare(b.label))
  const keep = ranked.slice(0, limit)
  const fold = ranked.slice(limit)
  const foldKeys = new Set(fold.map((s) => s.key))
  const foldedBuckets = buckets.map((b) => {
    const out: Record<string, string | number> = { ts: b.ts }
    let other = 0
    for (const [k, v] of Object.entries(b)) {
      if (k === "ts") continue
      if (foldKeys.has(k)) other += Number(v)
      else out[k] = v
    }
    out[RUN_VOLUME_OTHER_KEY] = other
    return out as B
  })
  return {
    buckets: foldedBuckets,
    series: [...keep, { key: RUN_VOLUME_OTHER_KEY, label: `Other (${fold.length} crews)`, color: "rgb(148, 163, 184)" }],
    folded: fold.length,
  }
}

export function formatCost(cost: number): string {
  if (cost === 0) return "$0.00"
  if (cost < 0.01) return "<$0.01"
  return `$${cost.toFixed(2)}`
}

export function formatRelativeShort(iso: string | null | undefined): string {
  if (!iso) return ""
  const ts = new Date(iso).getTime()
  if (isNaN(ts)) return ""
  const diffSec = Math.floor((Date.now() - ts) / 1000)
  if (diffSec < 60) return `${diffSec}s`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)}m`
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)}h`
  return `${Math.floor(diffSec / 86400)}d`
}

