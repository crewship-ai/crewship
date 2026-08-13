"use client"

import * as React from "react"
import { ChartColumn } from "lucide-react"

import { EM_DASH, defaultEmptyHint, panelGate, provenanceProducedAt } from "./freshness"
import {
  FailedValue,
  NeverProducedValue,
  PanelAge,
  PanelFrame,
  PanelValue,
  resolveNow,
} from "./panel-frame"
import type { PanelProps, SeriesEntry, SeriesPayload } from "./types"

/**
 * `series.v1` — a grouped bar chart (§3, bar only in v1).
 *
 * Hand-written inline SVG, not a chart library. §9 proposed recharts, but
 * under `output: "export"` its `ResponsiveContainer` measures with a
 * client-side `ResizeObserver` and paints nothing until hydration: the bars
 * would be missing from the exported HTML, from a print (§10b.8) and from the
 * first paint of a public page. The geometry below is computed at render time
 * and is in the markup, exactly as `metric.v1`'s sparkline is.
 *
 * The three §3 rules this file owns:
 *
 *  - **Max 5 series, sixth merges into "other."** `mergeOverflow` is that rule,
 *    and it mirrors `MergeOverflow` in internal/pages/payload_series.go point
 *    for point — including that summing a column of nothing yields no data
 *    rather than a measured zero. Five is a bound on what is DRAWN, so "other"
 *    takes one of the five slots and four series keep their own name.
 *  - **Colour belongs to the entity, not the ordinal.** `assignSeriesColors`
 *    keys on the series NAME: the preferred slot is a hash of the name, so
 *    reordering the payload changes nothing, and a filter that stops drawing a
 *    series cannot recolour the rest because nothing is looked up by index.
 *  - **Legend always; direct labels at ≤ 4 series.**
 *
 * ⚠ The palette itself is a known open dependency, recorded rather than worked
 * around. §3's fourth rule is *"status colours are reserved — green 'running'
 * must never also mean 'series 3'"*, and today it cannot hold: `app/globals.css`
 * defines `--chart-2` as `oklch(0.72 0.18 152)`, which is byte-identical to
 * `--success`, and `--chart-3` as `oklch(0.78 0.15 75)`, byte-identical to
 * `--warn` — the two colours `status.v1` uses for "ok" and "warning" through
 * `STATUS_BADGE_CLASSES`. That file's own comment says the chart ramp was
 * derived from the semantic palette on purpose. Fixing it is PR #1940 and is
 * explicitly its own change (§12b: *"`app/globals.css` — the palette fix is
 * its own PR and must not be bundled into a slice"*). This panel is therefore
 * built against the TOKENS, never against literal colours, so the fix reaches
 * it with no edit here.
 */
export function SeriesPanel({ panel, data, now, publicView = false, className }: PanelProps) {
  const clock = resolveNow(now)
  const gate = panelGate(data)
  const payload = (data.payload ?? {}) as SeriesPayload

  const labels = Array.isArray(payload.labels)
    ? payload.labels.map((l) => (typeof l === "string" ? l : ""))
    : []
  const unit = typeof payload.unit === "string" ? payload.unit.trim() : ""
  const drawn = mergeOverflow(Array.isArray(payload.series) ? payload.series : [], labels.length)
  const colors = assignSeriesColors(drawn.map((s) => s.name))

  let body: React.ReactNode
  if (gate.kind === "failed") {
    body = (
      <FailedValue
        failure={data.failure}
        publicView={publicView}
        producedAt={provenanceProducedAt(data.provenance)}
        now={clock}
      />
    )
  } else if (gate.kind === "never") {
    body = <NeverProducedValue hint={data.emptyHint?.trim() || defaultEmptyHint(panel)} />
  } else if (labels.length === 0 || drawn.length === 0) {
    // A produced payload that describes nothing. Still not an em dash: the
    // producer did run, and the em dash means "no basis to compute".
    body = (
      <p className="type-page-value text-muted-foreground">
        The latest push declared no categories to plot. Add `labels` and at least one series.
      </p>
    )
  } else {
    body = (
      <div className="flex flex-col gap-2">
        {gate.dimmed ? (
          <PanelAge producedAt={provenanceProducedAt(data.provenance)} now={clock} />
        ) : null}
        <PanelValue basis="measured" dimmed={gate.dimmed} className="flex flex-col gap-2">
          <div data-slot="panel-container" className="@container/panel flex flex-col gap-2">
            <BarChart labels={labels} series={drawn} colors={colors} unit={unit} />
            {/* Legend ALWAYS (§3) — the direct labels are an addition to it, never a
                replacement, and they are the first thing a narrow panel loses. */}
            <ul
              data-slot="series-legend"
              className="flex flex-wrap items-center gap-x-3 gap-y-1 type-page-meta text-muted-foreground"
            >
              {drawn.map((s) => (
                <li
                  key={s.name}
                  data-slot="series-legend-item"
                  data-series-name={s.name}
                  data-series-color={colors.get(s.name)}
                  className="flex min-w-0 items-center gap-1.5"
                >
                  <span
                    data-slot="series-swatch"
                    aria-hidden="true"
                    className="h-2 w-2 shrink-0 rounded-[2px]"
                    style={{ backgroundColor: `var(${colors.get(s.name)})` }}
                  />
                  <span className="truncate">{s.name}</span>
                </li>
              ))}
              {unit ? <li className="ml-auto shrink-0 tabular-nums">{unit}</li> : null}
            </ul>
          </div>
        </PanelValue>
      </div>
    )
  }

  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={clock}
      publicView={publicView}
      className={className}
      icon={ChartColumn}
    >
      {body}
    </PanelFrame>
  )
}

// ── the palette ───────────────────────────────────────────────────────────

/**
 * The five chart tokens, and only the chart tokens.
 *
 * §3: *"Status colours are reserved. Green 'running' must never also mean
 * 'series 3'."* The half of that rule this file can keep is that a series
 * never takes `--success`, `--warn` or `--destructive` by name — those belong
 * to `status.v1`, and a chart that spends one has taught the reader that green
 * means two things. The other half, that the values behind these tokens must
 * not COINCIDE with the status values, is PR #1940's; see the header comment.
 */
export const SERIES_COLOR_TOKENS = [
  "--chart-1",
  "--chart-2",
  "--chart-3",
  "--chart-4",
  "--chart-5",
] as const

/** §3's five. A sixth would have to repeat a colour or take a reserved one. */
export const MAX_RENDERABLE_SERIES = SERIES_COLOR_TOKENS.length

/** What the overflow is summed into. Mirrors `OverflowSeriesName` in Go. */
export const OVERFLOW_SERIES_NAME = "other"

/**
 * A stable 32-bit hash of the series name (FNV-1a). Its only job is to make a
 * colour a property of the ENTITY: the same name gets the same preferred slot
 * in every payload, on every page and in every order.
 */
function hashName(name: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h >>> 0
}

/**
 * Name → colour token, for the whole declared set at once.
 *
 * §3: *"Colour belongs to the entity, not to the ordinal — a filter that
 * removes a series must not recolour the rest."* Two properties are needed and
 * they pull against each other, so what is guaranteed is stated exactly rather
 * than implied:
 *
 *  - **Entity-anchored.** A name's preferred slot is `hash(name) % 5`, which
 *    involves nothing about the payload — not its order, not its length, not
 *    which other series are in it.
 *  - **Collision-free.** A pure hash would satisfy the rule completely and is
 *    unusable in practice: five names into five slots collide about 96 % of
 *    the time, and two series sharing a colour is a chart nobody can read. So
 *    a taken slot probes forward.
 *
 * Those two cannot both hold absolutely — any collision-free assignment over a
 * fixed five-slot palette must depend on which names are present. So the
 * guarantees are:
 *
 *  1. **Order never matters.** The probe walks the names in a canonical order —
 *     by preferred slot, then lexicographically — not in the producer's array
 *     order, so a payload that lists the same series differently produces a
 *     byte-identical assignment. This is §3's sentence read literally: colour
 *     is not the ordinal.
 *  2. **Filtering never recolours.** The map is built ONCE from the payload's
 *     declared set and every bar and legend swatch is a lookup BY NAME, so
 *     hiding a series changes what is drawn and nothing about what colour
 *     anything is. That is the case §3's clause is about, and it is exact.
 *  3. **A name in its own preferred slot keeps it**, whatever else the
 *     producer declares. Only a name that was DISPLACED by a colliding one can
 *     move when the declared set itself changes between pushes — which is a
 *     different payload, not a filter.
 */
export function assignSeriesColors(names: readonly string[]): Map<string, string> {
  const canonical = [...new Set(names)].sort((a, b) => {
    const pa = hashName(a) % MAX_RENDERABLE_SERIES
    const pb = hashName(b) % MAX_RENDERABLE_SERIES
    return pa === pb ? (a < b ? -1 : a > b ? 1 : 0) : pa - pb
  })

  const taken = new Array<string | null>(MAX_RENDERABLE_SERIES).fill(null)
  const out = new Map<string, string>()
  for (const name of canonical) {
    const preferred = hashName(name) % MAX_RENDERABLE_SERIES
    let slot = preferred
    for (let i = 0; i < MAX_RENDERABLE_SERIES && taken[slot] !== null; i++) {
      slot = (preferred + i + 1) % MAX_RENDERABLE_SERIES
    }
    taken[slot] = name
    out.set(name, SERIES_COLOR_TOKENS[slot])
  }
  return out
}

// ── the overflow merge ────────────────────────────────────────────────────

/** One series, normalised: a name and exactly `width` points. */
export interface DrawnSeries {
  name: string
  values: (number | null)[]
}

/**
 * §3: *"Max 5 series, sixth merges into 'other'."*
 *
 * Mirrors `(*SeriesPayload).MergeOverflow` in internal/pages/payload_series.go,
 * including the two decisions that are visible on screen: a point where every
 * merged series has no data stays no data rather than becoming a measured
 * zero (§9b.4), and a pre-existing series named "other" absorbs the overflow
 * instead of becoming a second legend row with the same name.
 *
 * It runs on the client as well as the server because the API serves the
 * payload the producer pushed — merging on the way in would destroy data to
 * satisfy a palette, and a later release with a wider palette could never get
 * it back.
 */
export function mergeOverflow(raw: SeriesEntry[], width: number): DrawnSeries[] {
  const normalised: DrawnSeries[] = []
  const seen = new Set<string>()
  raw.forEach((entry, i) => {
    const name =
      typeof entry?.name === "string" && entry.name.trim() ? entry.name.trim() : `series ${i + 1}`
    if (seen.has(name)) return
    seen.add(name)
    const values: (number | null)[] = []
    for (let j = 0; j < width; j++) {
      const v = Array.isArray(entry?.values) ? entry.values[j] : null
      values.push(typeof v === "number" && Number.isFinite(v) ? v : null)
    }
    normalised.push({ name, values })
  })

  if (normalised.length <= MAX_RENDERABLE_SERIES) return normalised

  // One of the five slots is spent on "other", so four keep their own name.
  const namedSlots = MAX_RENDERABLE_SERIES - 1
  const kept = normalised.slice(0, namedSlots)
  const sums = new Array<number>(width).fill(0)
  const measured = new Array<boolean>(width).fill(false)
  for (const s of normalised.slice(namedSlots)) {
    s.values.forEach((v, j) => {
      if (v === null) return
      sums[j] += v
      measured[j] = true
    })
  }

  const existing = kept.find((s) => s.name === OVERFLOW_SERIES_NAME)
  const target: DrawnSeries = existing ?? {
    name: OVERFLOW_SERIES_NAME,
    values: new Array<number | null>(width).fill(null),
  }
  target.values = target.values.map((v, j) => {
    if (!measured[j]) return v
    return (v ?? 0) + sums[j]
  })
  return existing ? kept : [...kept, target]
}

// ── the drawing ───────────────────────────────────────────────────────────

const VIEW_W = 320
const VIEW_H = 150
const PAD_L = 4
const PAD_R = 4
const PAD_TOP = 14
const AXIS_H = 16
const GROUP_GAP = 6

/**
 * Grouped columns, drawn in user units into a fixed `viewBox` and scaled
 * uniformly by the browser (`preserveAspectRatio` defaults to `xMidYMid
 * meet`). Uniform, deliberately: `preserveAspectRatio="none"` — which the
 * sparkline can afford because it is a bare polyline — would squash the text
 * in this chart at every panel width.
 *
 * Nothing here measures anything. There is no `ResizeObserver`, no `useEffect`
 * and no state, so the whole chart is present in the server-rendered markup.
 */
function BarChart({
  labels,
  series,
  colors,
  unit,
}: {
  labels: string[]
  series: DrawnSeries[]
  colors: Map<string, string>
  unit: string
}) {
  // §3: direct labels at ≤ 4 series. Past that the numbers collide with each
  // other and the legend is doing the work anyway.
  const directLabels = series.length <= 4 && labels.length * series.length <= 24

  // The domain always includes zero, so a bar's LENGTH is its magnitude and a
  // negative value hangs below the same line a zero sits on. A chart whose
  // baseline is the smallest value exaggerates every difference on it.
  const points = series.flatMap((s) => s.values).filter((v): v is number => v !== null)
  const max = Math.max(0, ...points)
  const min = Math.min(0, ...points)
  const span = max - min || 1

  const plotH = VIEW_H - PAD_TOP - AXIS_H
  const plotW = VIEW_W - PAD_L - PAD_R
  const groupW = plotW / labels.length
  const barW = Math.max(1, (groupW - GROUP_GAP) / series.length)
  const yOf = (v: number) => PAD_TOP + ((max - v) / span) * plotH
  const zeroY = yOf(0)

  return (
    <svg
      data-slot="series-chart"
      viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
      role="img"
      focusable="false"
      aria-label={chartSummary(labels, series, unit)}
      className="h-auto w-full"
    >
      {/* The zero line. Every bar is measured from it, so it is drawn even
          when nothing is negative — a baseline you cannot see is a baseline
          you have to take on trust. */}
      <line
        data-slot="series-baseline"
        x1={PAD_L}
        x2={VIEW_W - PAD_R}
        y1={zeroY}
        y2={zeroY}
        stroke="currentColor"
        strokeWidth={0.5}
        className="text-border"
      />

      {labels.map((label, li) => {
        const gx = PAD_L + li * groupW + GROUP_GAP / 2
        return (
          <g key={li} data-slot="series-group" data-label={label}>
            {series.map((s, si) => {
              const v = s.values[li]
              const x = gx + si * barW
              const cx = x + barW / 2
              if (v === null) {
                // §9b.4 per data point: no bar, and an em dash where the
                // number would have been. A gap that drew a zero-height bar
                // would read as a measured zero, which is the one confusion
                // this product refuses to make.
                return (
                  <text
                    key={s.name}
                    data-slot="series-point"
                    data-series-name={s.name}
                    data-basis="none"
                    x={cx}
                    y={zeroY - 2}
                    textAnchor="middle"
                    fontSize={7}
                    fill="currentColor"
                    className="text-muted-foreground-soft"
                  >
                    {EM_DASH}
                  </text>
                )
              }
              const y = v >= 0 ? yOf(v) : zeroY
              const h = Math.max(v === 0 ? 0.75 : 0.75, Math.abs(yOf(v) - zeroY))
              return (
                <g key={s.name}>
                  <rect
                    data-slot="series-bar"
                    data-series-name={s.name}
                    data-series-color={colors.get(s.name)}
                    data-basis="measured"
                    x={x}
                    y={v >= 0 ? y : zeroY}
                    width={Math.max(0.5, barW - 1)}
                    height={h}
                    rx={0.75}
                    fill={`var(${colors.get(s.name)})`}
                  >
                    <title>{`${s.name} · ${label}: ${v}${unit ? ` ${unit}` : ""}`}</title>
                  </rect>
                  {directLabels ? (
                    <text
                      data-slot="series-point"
                      data-series-name={s.name}
                      data-basis="measured"
                      x={cx}
                      y={(v >= 0 ? y : zeroY + h) - (v >= 0 ? 2 : -7)}
                      textAnchor="middle"
                      fontSize={7}
                      fill="currentColor"
                      className="text-muted-foreground tabular-nums"
                    >
                      {formatPoint(v)}
                    </text>
                  ) : null}
                </g>
              )
            })}
            <text
              data-slot="series-label"
              x={PAD_L + li * groupW + groupW / 2}
              y={VIEW_H - 4}
              textAnchor="middle"
              fontSize={8}
              fill="currentColor"
              className="text-muted-foreground-soft"
            >
              {truncate(label, Math.max(3, Math.floor(groupW / 4)))}
              <title>{label}</title>
            </text>
          </g>
        )
      })}
    </svg>
  )
}

/**
 * The alt text. A chart that only exists as geometry is a chart a screen
 * reader cannot read, and §7.3.2b's "always carries when its data was
 * produced" has a sibling here: always carries what it shows.
 */
function chartSummary(labels: string[], series: DrawnSeries[], unit: string): string {
  const names = series.map((s) => s.name).join(", ")
  const measured = series.reduce((n, s) => n + s.values.filter((v) => v !== null).length, 0)
  const total = series.length * labels.length
  const gaps = total - measured
  return (
    `Bar chart${unit ? ` in ${unit}` : ""}: ${series.length} series (${names}) ` +
    `over ${labels.length} categories` +
    (gaps > 0 ? `, ${gaps} of ${total} points with no data` : "")
  )
}

/** Short enough for a 7px label, and never rounded into a different claim. */
function formatPoint(v: number): string {
  if (Number.isInteger(v)) return String(v)
  return String(Math.round(v * 100) / 100)
}

function truncate(s: string, max: number): string {
  return s.length <= max ? s : `${s.slice(0, Math.max(1, max - 1))}…`
}
