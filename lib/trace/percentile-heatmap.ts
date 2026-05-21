// Percentile heatmap. Color the border of each node by where its
// cost or duration sits relative to its siblings, so the slow /
// expensive step jumps out without reading numbers.
//
// We compute percentiles over the SIBLING SET (other steps in the
// same run). v2 might compute across all runs of the same pipeline —
// "is this run abnormally slow?" — but v1 keeps the scope local so
// the comparison is meaningful even on a workspace's first run.

export type HeatmapMode = "off" | "cost" | "duration"

// Discrete percentile bucket. Five tiers + "none" for steps with no
// metric. The node renderer maps each bucket to a Tailwind border
// class — keeping color decisions in CSS instead of inline styles
// means dark/light theme switches and design-system updates stay
// consistent.
export type HeatmapBucket = "p20" | "p60" | "p80" | "p95" | "outlier"

interface StepMetrics {
  stepId: string
  cost?: number
  duration?: number
}

// shadeNodes — given a list of step metrics + a mode, return a map
// of step id → bucket. Steps without a metric (pending, no cost
// recorded) get no entry; the canvas falls back to the default border.
export function shadeNodes(
  metrics: StepMetrics[],
  mode: HeatmapMode,
): Map<string, HeatmapBucket> {
  const out = new Map<string, HeatmapBucket>()
  if (mode === "off") return out

  // Include cost=0 / duration=0 — http and code steps legitimately
  // cost $0 (no LLM tokens), so excluding them made the heatmap shade
  // ONLY the agent_run nodes and read as "agent_run is expensive"
  // when really every other step was just metered out of the picture.
  // Negative values are still skipped (defensive — the journal can
  // emit -1 sentinels for in-flight steps).
  const values: { stepId: string; v: number }[] = []
  for (const m of metrics) {
    const v = mode === "cost" ? m.cost : m.duration
    if (v === undefined || v === null || v < 0) continue
    values.push({ stepId: m.stepId, v })
  }
  if (values.length === 0) return out

  const sorted = [...values].sort((a, b) => a.v - b.v).map((x) => x.v)
  const at = (p: number): number => {
    if (sorted.length === 0) return 0
    const i = Math.min(sorted.length - 1, Math.floor((sorted.length - 1) * p))
    return sorted[i]
  }
  const p20 = at(0.2)
  const p60 = at(0.6)
  const p80 = at(0.8)
  const p95 = at(0.95)

  for (const { stepId, v } of values) {
    out.set(stepId, bucketForPercentile(v, p20, p60, p80, p95))
  }
  return out
}

function bucketForPercentile(
  v: number,
  p20: number,
  p60: number,
  p80: number,
  p95: number,
): HeatmapBucket {
  if (v <= p20) return "p20"
  if (v <= p60) return "p60"
  if (v <= p80) return "p80"
  if (v <= p95) return "p95"
  return "outlier"
}

// Tailwind class for a bucket's node border. Centralised so node
// renderers stay free of color literals; theming changes happen here.
export const HEATMAP_BORDER_CLASS: Record<HeatmapBucket, string> = {
  p20: "!border-emerald-400/70 !border-2",
  p60: "!border-yellow-400/70 !border-2",
  p80: "!border-orange-400/70 !border-2",
  p95: "!border-red-400/70 !border-2",
  outlier: "!border-rose-600/80 !border-2",
}
