// sub-spans — pure helpers for the run-trace drill-down layer.
//
// `GetRun` returns `sub_spans`: a map keyed by step id, each value an
// array of the agent's internal tool calls (bash, write, read, edit,
// mcp_tool, http, tool, think), ordered by `seq`. These helpers turn
// that loosely-typed wire data into the normalized `SubSpan[]` the
// canvas + detail panel render, and lay them out as a waterfall.
//
// Everything here is pure and defensive: a malformed/partial row never
// throws — it's coerced to a best-effort span or dropped. That matters
// because the data is produced by a fast-moving backend and an older
// run may carry a shape the FE didn't expect.

import type {
  SubSpan,
  SubSpanAttributes,
  SubSpanKind,
  SubSpanStatus,
} from "./types"

const KNOWN_KINDS: ReadonlySet<string> = new Set<SubSpanKind>([
  "bash",
  "write",
  "read",
  "edit",
  "mcp_tool",
  "http",
  "tool",
  "think",
])

const KNOWN_STATUS: ReadonlySet<string> = new Set<SubSpanStatus>([
  "ok",
  "error",
  "running",
])

// mapSubSpans — normalize one step's raw sub-span array into typed
// SubSpan[]. Re-sorts by `seq` (stable on the original index) so the
// UI never depends on the wire already being ordered, and silently
// drops anything that isn't an object. Returns [] for null / undefined
// / non-array input (the empty-step and "no drill-down" cases).
export function mapSubSpans(raw: unknown): SubSpan[] {
  if (!Array.isArray(raw)) return []
  const rows: { span: SubSpan; seq: number; idx: number }[] = []
  raw.forEach((item, idx) => {
    const row = toSubSpan(item, idx)
    if (row) rows.push(row)
  })
  rows.sort((a, b) => a.seq - b.seq || a.idx - b.idx)
  return rows.map((r) => r.span)
}

function toSubSpan(
  item: unknown,
  idx: number,
): { span: SubSpan; seq: number; idx: number } | null {
  if (!item || typeof item !== "object" || Array.isArray(item)) return null
  const o = item as Record<string, unknown>

  const kind: SubSpanKind = KNOWN_KINDS.has(o.kind as string)
    ? (o.kind as SubSpanKind)
    : "tool"
  const status: SubSpanStatus = KNOWN_STATUS.has(o.status as string)
    ? (o.status as SubSpanStatus)
    : "ok"
  const name =
    typeof o.name === "string" && o.name.trim() ? o.name : kind
  const detail = typeof o.detail === "string" ? o.detail : undefined
  const startedAt =
    typeof o.started_at === "string" && o.started_at ? o.started_at : undefined
  const durationMs =
    typeof o.duration_ms === "number" && Number.isFinite(o.duration_ms)
      ? o.duration_ms
      : undefined
  const seq =
    typeof o.seq === "number" && Number.isFinite(o.seq) ? o.seq : idx

  return {
    span: {
      kind,
      name,
      detail,
      startedAt,
      durationMs,
      status,
      attributes: toAttributes(o.attributes),
    },
    seq,
    idx,
  }
}

function toAttributes(raw: unknown): SubSpanAttributes {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {}
  const o = raw as Record<string, unknown>
  const attr: SubSpanAttributes = {}
  if (typeof o.tool === "string" && o.tool) attr.tool = o.tool
  if (typeof o.model === "string" && o.model) attr.model = o.model
  if (typeof o.artifact_path === "string" && o.artifact_path)
    attr.artifact_path = o.artifact_path
  if (typeof o.host === "string" && o.host) attr.host = o.host
  return attr
}

// pickModel — the model that ran a step, taken from the first sub-span
// that carries `attributes.model`. Null when none do (e.g. a pure-bash
// step, or a step with no spans).
export function pickModel(spans: SubSpan[]): string | null {
  for (const s of spans) {
    if (s.attributes.model) return s.attributes.model
  }
  return null
}

// ── Waterfall layout ─────────────────────────────────────────────────

export interface SubSpanBar {
  span: SubSpan
  // Percentage offsets within the step's time window, 0–100.
  leftPct: number
  widthPct: number
}

const MIN_BAR_PCT = 1.5

// layoutSubSpans — position each span as a bar inside the step's
// window (earliest start → latest end), matching the mockup's Gantt
// lane. Falls back to even slices when no span carries timing, so the
// waterfall still reads as an ordered sequence rather than collapsing
// to zero-width. Never throws on unparseable timestamps.
export function layoutSubSpans(spans: SubSpan[]): SubSpanBar[] {
  if (spans.length === 0) return []

  const times = spans.map((s) => {
    const start = s.startedAt ? Date.parse(s.startedAt) : NaN
    const dur = typeof s.durationMs === "number" ? Math.max(s.durationMs, 0) : 0
    return { start, end: Number.isNaN(start) ? NaN : start + dur }
  })

  const starts = times.map((t) => t.start).filter((n) => !Number.isNaN(n))

  // No usable timing anywhere — distribute evenly so the lane still
  // shows ordering. Each bar gets an equal slice minus a small gutter.
  if (starts.length === 0) {
    const n = spans.length
    const slice = 100 / n
    return spans.map((span, i) => ({
      span,
      leftPct: i * slice,
      widthPct: Math.max(slice - 1, MIN_BAR_PCT),
    }))
  }

  const ends = times.map((t) => t.end).filter((n) => !Number.isNaN(n))
  const windowStart = Math.min(...starts)
  const windowEnd = Math.max(...ends, windowStart + 1)
  const total = windowEnd - windowStart || 1

  return spans.map((span, i) => {
    const t = times[i]
    if (Number.isNaN(t.start)) {
      // Timed siblings exist but this row has no start — pin it to the
      // window head with a minimal bar rather than dropping it.
      return { span, leftPct: 0, widthPct: MIN_BAR_PCT }
    }
    const leftPct = clamp(((t.start - windowStart) / total) * 100, 0, 100)
    const rawWidth = ((t.end - t.start) / total) * 100
    const widthPct = clamp(
      Math.max(rawWidth, MIN_BAR_PCT),
      MIN_BAR_PCT,
      100 - leftPct,
    )
    return { span, leftPct, widthPct }
  })
}

function clamp(n: number, lo: number, hi: number): number {
  return Math.min(Math.max(n, lo), hi)
}
