"use client"

import * as React from "react"
import { ChartColumn } from "lucide-react"

import {
  panelMotion,
  useTweenedValues,
  type TweenFrame,
} from "@/components/features/pages/panel-motion"
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
 * `series.v1` as this panel reads it.
 *
 * Kept as a named alias after #1958 widened `SeriesPayload.labels` itself, so
 * the reader of this file still meets the sparse axis by name at the point the
 * payload is cast.
 */
type SeriesPayloadWithSparseAxis = SeriesPayload

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
 * ## The bars grow (epic #1935)
 *
 * A bar's height is geometry, and geometry is the safest thing on a page to
 * animate: a rectangle that is briefly the wrong height claims nothing a still
 * frame would not also claim, because the rectangle is not the claim — the
 * axis, the tooltip and the direct label are, and none of them move.
 *
 * So the split here is exact, and it is the reason nothing in this file needs
 * an asterisk about truth:
 *
 *  · **Only the geometry travels.** The rects' `y`/`height`, and the zero line
 *    and scale they are measured against, are computed from the tweened frame.
 *  · **Every number printed on the chart is the payload's, from the first
 *    frame.** The direct labels, the `<title>` tooltips and the `aria-label`
 *    summary all read the payload, never the frame — a chart cannot show a
 *    figure nobody measured, only a rectangle on its way to one.
 *  · **A point that changed CATEGORY cuts.** `null` (no basis to compute, drawn
 *    as an em dash) and a measured number are different claims (§9b.4), so a
 *    bar never grows out of a gap and a gap never collapses out of a bar. That
 *    falls out of the keying: a point only tweens when it carried a number in
 *    both payloads. See `panel-motion.ts`.
 *
 * These are still plain `<rect>` elements with plain numeric attributes rather
 * than `motion.rect`. That is not conservatism: `motion` renders an animated
 * `height` through the CSS value-type map, which stamps it as `height="42px"`,
 * and it takes `x`/`y` on an SVG child to mean a transform rather than the
 * attribute. Driving the numbers and leaving the markup alone keeps the whole
 * point of the header above — that the geometry is IN the exported HTML, in
 * user units, with no client-side measurement.
 *
 * ## The axis is a negotiation (`labels[]` may carry nulls)
 *
 * A producer with a 24-point window used to have to send 24 names, because
 * `labels[]` demanded a non-empty string per tick — and 24 names across a
 * half-width panel were drawn as "-1…" apiece. The producer cannot fix that:
 * it does not know the panel's width. So the decision is split at the seam
 * where the knowledge is:
 *
 *  · **The producer names the ticks it wants named.** `null` is a tick it
 *    declines to name — a category that exists and carries a value in every
 *    series, with no name of its own. `""` stays illegal on the wire, because
 *    an empty string is what a broken format expression produces and a schema
 *    that accepted it could not tell a deliberate blank from a bug.
 *  · **The renderer decides how many of those names it can draw.** `planAxis`
 *    thins the named ticks by an even stride until what is left is legible.
 *
 * Thinning labels is honest; thinning data is not. Every category the payload
 * declares keeps its group and every point keeps its bar, whatever the axis
 * does — and the name of a tick the axis dropped is still on that tick's own
 * bars, in the `<title>`.
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
  const payload = (data.payload ?? {}) as SeriesPayloadWithSparseAxis

  // A tick is either NAMED (a non-empty string the producer chose) or UNNAMED
  // (`null`). Anything else that arrives — an empty string from an older
  // producer, a number, a stored payload from before the schema widened — is
  // read as unnamed rather than drawn as a blank tick, because a blank drawn
  // label is indistinguishable from a rendering fault.
  const labels: (string | null)[] = Array.isArray(payload.labels)
    ? payload.labels.map((l) => (typeof l === "string" && l.trim() !== "" ? l : null))
    : []
  const unit = typeof payload.unit === "string" ? payload.unit.trim() : ""
  const drawn = mergeOverflow(Array.isArray(payload.series) ? payload.series : [], labels.length)
  const colors = assignSeriesColors(drawn.map((s) => s.name))

  // The tween is keyed on (series NAME, label index), never on an ordinal into
  // the payload's array — the same reason colour is. A producer that reorders
  // its series must not make every bar travel to a neighbour's height.
  const motion = panelMotion(panel, data)
  const tweenTarget = new Map<string, number>()
  for (const s of drawn) {
    s.values.forEach((v, li) => {
      if (v !== null) tweenTarget.set(pointKey(s.name, li), v)
    })
  }
  const frames = useTweenedValues(tweenTarget, motion.tween)

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
            <BarChart
              labels={labels}
              series={drawn}
              colors={colors}
              unit={unit}
              frames={frames}
            />
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

/**
 * A point's identity for the growth tween: the series' NAME and the category
 * index, never a position in the payload's array.
 *
 * `\u0000` cannot occur in a JSON string a producer sends, so a series called
 * `a` at index `1` can never collide with one called `a\u00001` at index `0`.
 */
function pointKey(name: string, labelIndex: number): string {
  return `${name}\u0000${labelIndex}`
}

const VIEW_W = 320
const VIEW_H = 150
const PAD_L = 4
const PAD_R = 4
const PAD_TOP = 14
const AXIS_H = 16
const GROUP_GAP = 6

// ── the axis ──────────────────────────────────────────────────────────────

/** The axis label size, in the same user units everything else is drawn in. */
const AXIS_FONT = 8

/**
 * The width one glyph of `AXIS_FONT` takes, near enough.
 *
 * Nothing here measures text, and that is deliberate rather than lazy: a
 * `ResizeObserver` or a `getComputedTextLength` would move the axis out of the
 * server-rendered markup, which is the property the whole chart is built for
 * (see the header). An 8px sans glyph averages a little over half its size;
 * 4.4 is the average advance rounded UP, so the estimate errs towards fewer
 * labels rather than towards two that touch.
 */
const AXIS_CHAR_W = 4.4

/** Clear space between two neighbouring labels. Below this they read as one. */
const AXIS_LABEL_GAP = 4

/**
 * The least a drawn label may say: four glyphs and an ellipsis.
 *
 * This is the number the original bug violated — 24 labels across a half-width
 * panel left room for two glyphs, so every one of them was drawn as "-1…",
 * which is not a label but a rumour of one. When a label cannot be given this
 * much room, the answer is to draw FEWER labels, never smaller ones.
 */
export const AXIS_MIN_LABEL_CHARS = 5

/** What the renderer decided to draw on the axis. */
export interface AxisPlan {
  /** Parallel to `labels`: the text to draw at each tick, or `null` for none. */
  ticks: (string | null)[]
  /** How many ticks the producer named. */
  named: number
  /** How many names survived the fit. */
  drawn: number
  /** True when the renderer dropped names the producer did send. */
  thinned: boolean
}

/**
 * Choose the axis labels, and their truncation, from the payload alone.
 *
 * Two decisions, in this order:
 *
 *  1. **Which ticks are candidates.** Only the ones the producer named. An
 *     unnamed tick is never given a name here — inventing one would put a
 *     string on the chart that nobody measured, which is the same class of
 *     error as a direct label that counted up.
 *  2. **How many of them fit.** The smallest even stride whose drawn labels
 *     each clear `AXIS_MIN_LABEL_CHARS`. Even, because an axis whose gaps vary
 *     reads as data — §11b.16 makes even spacing a contract for the points, and
 *     a reader extends the same assumption to the ticks. Anchored on the LAST
 *     named tick, walking backwards: a payload's last category is the one a
 *     reader looks for first (it is "now" in every rolling window), and
 *     anchoring at one end is what keeps the surviving gaps identical —
 *     forcing both ends instead leaves one short gap that looks like a
 *     measurement.
 *
 * The stride is decided in the fixed `viewBox`'s user units, which is why it
 * needs no measurement to be correct at any width: the whole chart is scaled
 * uniformly by the browser, so a label that clears its neighbour in user units
 * clears it on a phone and on a 4K display alike.
 *
 * What it will not do is drop a data point. `ticks` is exactly as long as
 * `labels`, every category keeps its group, and the name of a tick that is not
 * drawn stays on that tick's bars in their `<title>`.
 */
export function planAxis(labels: readonly (string | null)[]): AxisPlan {
  const empty: AxisPlan = { ticks: labels.map(() => null), named: 0, drawn: 0, thinned: false }
  if (labels.length === 0) return empty

  const named: number[] = []
  labels.forEach((l, i) => {
    if (typeof l === "string" && l.trim() !== "") named.push(i)
  })
  // The server refuses an axis with no name anywhere; a payload stored before
  // it did, or one that lost its names to a producer bug, still has to render.
  if (named.length === 0) return empty

  const groupW = (VIEW_W - PAD_L - PAD_R) / labels.length

  let keep = named
  let width = availableWidth(named, groupW)
  for (let stride = 2; stride <= named.length && width < AXIS_MIN_LABEL_CHARS * AXIS_CHAR_W; stride++) {
    keep = everyNthFromTheEnd(named, stride)
    width = availableWidth(keep, groupW)
  }

  const chars = Math.max(1, Math.floor(width / AXIS_CHAR_W))
  const ticks = labels.map(() => null as string | null)
  for (const i of keep) ticks[i] = truncate(labels[i] as string, chars)

  return { ticks, named: named.length, drawn: keep.length, thinned: keep.length < named.length }
}

/**
 * The width a label may occupy at each of `keep`, which is the tightest gap
 * between two of them — the labels are centred on their ticks, so two
 * neighbours a apart may each be `a - gap` wide before they touch.
 *
 * A single label is bounded by the plot instead, not by the viewBox edge: an
 * edge label is nudged inwards when it is drawn (see `tickX`), which costs a
 * few user units of alignment and is what every chart does, rather than
 * costing the label itself.
 */
function availableWidth(keep: readonly number[], groupW: number): number {
  const plotW = VIEW_W - PAD_L - PAD_R
  let w = plotW
  for (let i = 1; i < keep.length; i++) {
    w = Math.min(w, (keep[i] - keep[i - 1]) * groupW - AXIS_LABEL_GAP)
  }
  return Math.max(0, w)
}

/** Every `stride`-th candidate, anchored on the last one. */
function everyNthFromTheEnd(named: readonly number[], stride: number): number[] {
  const keep: number[] = []
  for (let i = named.length - 1; i >= 0; i -= stride) keep.push(named[i])
  return keep.reverse()
}

/**
 * Where a tick's label is centred, nudged just far enough to stay inside the
 * viewBox. An SVG root clips, so an un-nudged label on the last category of a
 * dense axis would lose its final glyphs to the edge — a nudge of a few user
 * units is visible to nobody, and a clipped label is visible to everybody.
 */
function tickX(center: number, text: string): number {
  const half = (text.length * AXIS_CHAR_W) / 2
  return Math.min(Math.max(center, PAD_L + half), VIEW_W - PAD_R - half)
}

/**
 * Grouped columns, drawn in user units into a fixed `viewBox` and scaled
 * uniformly by the browser (`preserveAspectRatio` defaults to `xMidYMid
 * meet`). Uniform, deliberately: `preserveAspectRatio="none"` — which the
 * sparkline can afford because it is a bare polyline — would squash the text
 * in this chart at every panel width.
 *
 * Nothing here measures anything. There is no `ResizeObserver`, so the whole
 * chart is present in the server-rendered markup: the tween is driven from the
 * panel above and arrives as plain numbers, and with no tween in flight those
 * numbers ARE the payload's.
 */
function BarChart({
  labels,
  series,
  colors,
  unit,
  frames,
}: {
  labels: (string | null)[]
  series: DrawnSeries[]
  colors: Map<string, string>
  unit: string
  frames: ReadonlyMap<string, TweenFrame>
}) {
  // What the axis can say at this width. Decided before anything is drawn, and
  // it decides nothing about the bars: `labels.length` groups are drawn either
  // way (§9b.4's sibling — a chart may show less of its axis than it was given,
  // never less of its data).
  const axis = planAxis(labels)
  // §3: direct labels at ≤ 4 series. Past that the numbers collide with each
  // other and the legend is doing the work anyway.
  const directLabels = series.length <= 4 && labels.length * series.length <= 24

  // The geometry the bars are drawn AT this frame. A `null` stays `null` — a
  // gap is not a magnitude and has nothing to travel towards (§9b.4).
  const drawnAt = series.map((s) => ({
    name: s.name,
    values: s.values.map((v, li) =>
      v === null ? null : (frames.get(pointKey(s.name, li))?.value ?? v),
    ),
  }))

  // The domain always includes zero, so a bar's LENGTH is its magnitude and a
  // negative value hangs below the same line a zero sits on. A chart whose
  // baseline is the smallest value exaggerates every difference on it.
  //
  // Computed from the FRAME, not from the payload: a bar growing past a scale
  // that had already jumped to the final domain would visibly undershoot for
  // 240 ms and then catch up, which reads as a rendering fault rather than as
  // growth. With no tween in flight the frame is the payload, so the settled
  // domain is unchanged.
  const points = drawnAt.flatMap((s) => s.values).filter((v): v is number => v !== null)
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
      aria-label={chartSummary(labels, series, unit, axis)}
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
        const tick = axis.ticks[li]
        return (
          <g
            key={li}
            data-slot="series-group"
            data-label={label ?? undefined}
            // Three states, and they are three different facts: the producer
            // named this tick and the axis drew it; the producer named it and
            // the axis could not fit it; the producer did not name it.
            data-label-state={tick !== null ? "drawn" : label !== null ? "thinned" : "unnamed"}
          >
            {series.map((s, si) => {
              // `measured` is what the producer sent and is what every piece of
              // TEXT below reads. `v` is where the rectangle is this frame, and
              // is the only thing allowed to be an interpolation.
              const measured = s.values[li]
              const v = drawnAt[si].values[li]
              const x = gx + si * barW
              const cx = x + barW / 2
              if (v === null || measured === null) {
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
                    {/* `measured`, not `v`: the tooltip is a number a reader
                        will quote, and it must be the producer's own.

                        This is also where the name of a category the AXIS
                        could not fit stays reachable — the label is thinned
                        off the axis, never off the data — and where a tick
                        the producer left unnamed simply has no name to give,
                        rather than an empty one between two separators. */}
                    <title>{barTitle(s.name, label, measured, unit)}</title>
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
                      {/* Rides up with the bar, but says what was measured
                          the whole way. A direct label that counted up is a
                          number on a chart that nobody sent. */}
                      {formatPoint(measured)}
                    </text>
                  ) : null}
                </g>
              )
            })}
            {tick !== null ? (
              <text
                data-slot="series-label"
                data-tick-index={li}
                x={tickX(PAD_L + li * groupW + groupW / 2, tick)}
                y={VIEW_H - 4}
                textAnchor="middle"
                fontSize={AXIS_FONT}
                fill="currentColor"
                className="text-muted-foreground-soft"
              >
                {tick}
                {/* Only when the drawn text is not the whole name. A tooltip
                    that repeats what is already on screen teaches a reader
                    that tooltips say nothing. */}
                {tick !== label ? <title>{label}</title> : null}
              </text>
            ) : null}
          </g>
        )
      })}
    </svg>
  )
}

/** One bar's tooltip. A tick with no name contributes no name. */
function barTitle(seriesName: string, label: string | null, value: number, unit: string): string {
  const where = label === null ? seriesName : `${seriesName} · ${label}`
  return `${where}: ${value}${unit ? ` ${unit}` : ""}`
}

/**
 * The alt text. A chart that only exists as geometry is a chart a screen
 * reader cannot read, and §7.3.2b's "always carries when its data was
 * produced" has a sibling here: always carries what it shows.
 *
 * It also carries what it did NOT show. A sighted reader can see that the axis
 * names five ticks of twenty-four; a reader who cannot would otherwise be told
 * "twenty-four categories" by a summary and shown five names by the axis, with
 * nothing to reconcile the two. Thinning that is disclosed is a rendering
 * decision; thinning that is not is a chart quietly disagreeing with itself.
 */
function chartSummary(
  labels: (string | null)[],
  series: DrawnSeries[],
  unit: string,
  axis: AxisPlan,
): string {
  const names = series.map((s) => s.name).join(", ")
  const measured = series.reduce((n, s) => n + s.values.filter((v) => v !== null).length, 0)
  const total = series.length * labels.length
  const gaps = total - measured
  return (
    `Bar chart${unit ? ` in ${unit}` : ""}: ${series.length} series (${names}) ` +
    `over ${labels.length} categories` +
    (gaps > 0 ? `, ${gaps} of ${total} points with no data` : "") +
    (axis.thinned
      ? `; the axis names ${axis.drawn} of its ${axis.named} labels at this width — every point is drawn`
      : axis.drawn < labels.length
        ? `; ${axis.drawn} of the ${labels.length} categories are named — every point is drawn`
        : "")
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
