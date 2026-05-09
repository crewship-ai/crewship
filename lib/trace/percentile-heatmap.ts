// Percentile heatmap — Langfuse pattern. Color the border of each
// node by where its cost or duration sits relative to its siblings,
// so the slow / expensive step jumps out without reading numbers.
//
// We compute percentiles over the SIBLING SET (other steps in the
// same run). v2 might compute across all runs of the same pipeline —
// "is this run abnormally slow?" — but v1 keeps the scope local so
// the comparison is meaningful even on a workspace's first run.

export type HeatmapMode = "off" | "cost" | "duration"

interface StepMetrics {
  stepId: string
  cost?: number
  duration?: number
}

// shadeNodes — given a list of step metrics + a mode, return a map of
// step id → hex color for the node border. Steps without a metric
// (e.g. pending, no cost recorded) get no entry; the canvas falls
// back to the default border.
//
// Color ramp (5 buckets): green (p20) → yellow (p60) → orange (p80)
// → red (p95+). Greenish for cheap/fast, reddish for expensive/slow.
export function shadeNodes(
  metrics: StepMetrics[],
  mode: HeatmapMode,
): Map<string, string> {
  const out = new Map<string, string>()
  if (mode === "off") return out

  const values: { stepId: string; v: number }[] = []
  for (const m of metrics) {
    const v = mode === "cost" ? m.cost : m.duration
    if (v === undefined || v === null || v <= 0) continue
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
    out.set(stepId, colorForPercentile(v, p20, p60, p80, p95))
  }
  return out
}

function colorForPercentile(
  v: number,
  p20: number,
  p60: number,
  p80: number,
  p95: number,
): string {
  if (v <= p20) return "rgb(74, 222, 128)" // emerald-400  — cheap/fast
  if (v <= p60) return "rgb(250, 204, 21)" // yellow-400
  if (v <= p80) return "rgb(251, 146, 60)" // orange-400
  if (v <= p95) return "rgb(248, 113, 113)" // red-400
  return "rgb(225, 29, 72)" // rose-600     — outlier
}
