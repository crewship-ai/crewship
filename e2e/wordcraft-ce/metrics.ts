export interface MillisecondSummary {
  unit: "ms"
  percentileMethod: "nearest-rank"
  count: number
  p50: number
  p95: number
  max: number
  raw: number[]
}

/** Retain sample order in raw; nearest-rank percentiles use a separate sort. */
export function summarizeMilliseconds(samples: readonly number[]): MillisecondSummary {
  if (!samples.length || samples.some(value => !Number.isFinite(value) || value < 0)) throw new Error("latency samples must be nonempty, finite and nonnegative")
  const sorted = [...samples].sort((a, b) => a - b)
  const percentile = (fraction: number) => sorted[Math.ceil(sorted.length * fraction) - 1]
  return { unit: "ms", percentileMethod: "nearest-rank", count: samples.length, p50: percentile(0.5), p95: percentile(0.95), max: sorted[sorted.length - 1], raw: [...samples] }
}
