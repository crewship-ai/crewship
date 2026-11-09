import type { PreviewResult } from "./document-code-blocks"

/**
 * Chart preview for Docs code blocks whose language is `chart`.
 *
 * The source is a strict JSON spec:
 * `{"type": "bar" | "line", "title"?: string, "x": string[], "series": [{"name": string, "values": number[]}]}`.
 * Unknown keys, wrong types, non-finite numbers and specs over the limits are
 * refused with the reason. The chart is an SVG built with DOM calls: every
 * label is a text node, colours are the product's series tokens, and nothing
 * is fetched. The SVG is an image with a summary for screen readers, and the
 * values are also offered as a table behind "Show data table".
 */

export const CHART_LIMITS = { chars: 50_000, series: 8, points: 200, text: 200 }

type Spec = { type: "bar" | "line"; title?: string; x: string[]; series: { name: string; values: number[] }[] }

class Invalid extends Error {}

const SVG = "http://www.w3.org/2000/svg"
const W = 640, H = 320
const LABEL_CHARS = 14

const isObject = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value)

function only(value: Record<string, unknown>, keys: string[], where: string) {
  for (const key of Object.keys(value)) if (!keys.includes(key)) throw new Invalid(`unknown key ${JSON.stringify(key.slice(0, 40))}${where}`)
}

function text(value: unknown, what: string, badType: string): string {
  if (typeof value !== "string") throw new Invalid(badType)
  if (value.length > CHART_LIMITS.text) throw new Invalid(`${what} is longer than ${CHART_LIMITS.text} characters`)
  return value
}

/** The spec, checked strictly; throws `Invalid` with the reason. */
function parse(source: string): Spec {
  let raw: unknown
  try { raw = JSON.parse(source) } catch { throw new Invalid("it is not valid JSON") }
  if (!isObject(raw)) throw new Invalid('the spec must be an object with "type", "x" and "series"')
  if (raw.type !== "bar" && raw.type !== "line") throw new Invalid('"type" must be "bar" or "line"')
  only(raw, ["type", "title", "x", "series"], "")
  const title = raw.title === undefined ? undefined : text(raw.title, '"title"', '"title" must be text')
  if (!Array.isArray(raw.x)) throw new Invalid('"x" must be a list of labels')
  if (raw.x.length > CHART_LIMITS.points) throw new Invalid(`a chart has at most ${CHART_LIMITS.points} points per series`)
  const x = raw.x.map((label, i) => text(label, `label ${i + 1}`, `label ${i + 1} must be text`))
  if (!Array.isArray(raw.series) || raw.series.length === 0) throw new Invalid("a chart needs at least one series")
  if (raw.series.length > CHART_LIMITS.series) throw new Invalid(`a chart has at most ${CHART_LIMITS.series} series`)
  const series = raw.series.map((entry, s) => {
    const n = s + 1
    if (!isObject(entry)) throw new Invalid(`series ${n} must be an object with "name" and "values"`)
    only(entry, ["name", "values"], ` in series ${n}`)
    const name = text(entry.name, `the name of series ${n}`, `series ${n} needs a "name"`)
    if (!Array.isArray(entry.values)) throw new Invalid(`series ${n} needs "values", a list of numbers`)
    if (entry.values.length > CHART_LIMITS.points) throw new Invalid(`a chart has at most ${CHART_LIMITS.points} points per series`)
    if (entry.values.length !== x.length) throw new Invalid(`series ${n} has ${entry.values.length} values, "x" has ${x.length}`)
    const values = entry.values.map((value, i) => {
      if (typeof value !== "number" || !Number.isFinite(value)) throw new Invalid(`series ${n}, value ${i + 1} must be a finite number`)
      return value
    })
    return { name, values }
  })
  if (x.length === 0) throw new Invalid('"x" needs at least one label')
  return { type: raw.type, title, x, series }
}

const format = (value: number) => {
  const rounded = Math.round(value * 1e6) / 1e6
  return Object.is(rounded, -0) ? "0" : String(rounded)
}

/** About five round tick values covering [min, max], zero included. */
function ticks(min: number, max: number): number[] {
  const lo = Math.min(0, min), hi = Math.max(0, max)
  if (lo === hi) return [0, 1]
  const raw = (hi - lo) / 5, magnitude = 10 ** Math.floor(Math.log10(raw))
  const step = [1, 2, 2.5, 5, 10].map(f => f * magnitude).find(s => s >= raw) ?? 10 * magnitude
  const out: number[] = []
  for (let v = Math.floor(lo / step) * step; v <= hi + step * 1e-9; v += step) out.push(Math.round(v / step) * step)
  if (out[out.length - 1] < hi) out.push(out[out.length - 1] + step)
  return out
}

const shorten = (label: string) => label.length > LABEL_CHARS ? `${label.slice(0, LABEL_CHARS - 1)}…` : label

export function renderChart(source: string): PreviewResult {
  if (!source.trim()) return { ok: false, error: "Nothing to show yet. Write a chart in Source." }
  if (source.length > CHART_LIMITS.chars) return { ok: false, error: `This chart is longer than ${CHART_LIMITS.chars.toLocaleString("en-US")} characters, so only its source is shown.` }
  let spec: Spec
  try { spec = parse(source) } catch (cause) {
    return { ok: false, error: `Cannot show this chart: ${cause instanceof Invalid ? cause.message : "it could not be read"}.` }
  }
  const all = spec.series.flatMap(s => s.values)
  const min = Math.min(...all), max = Math.max(...all)
  const kind = spec.type === "bar" ? "Bar chart" : "Line chart"
  const names = spec.series.map(s => s.name).join(", ")
  const range = spec.x.length === 1 ? `(${spec.x[0]})` : `(${spec.x[0]} to ${spec.x[spec.x.length - 1]})`
  const summary = `${kind}${spec.title ? `: ${spec.title}` : ""}. ${spec.series.length} series (${names}) over ${spec.x.length} ${spec.x.length === 1 ? "category" : "categories"} ${range}; values from ${format(min)} to ${format(max)}.`

  const el = <K extends keyof SVGElementTagNameMap>(tag: K, attrs: Record<string, string | number> = {}, parent?: Element) => {
    const node = document.createElementNS(SVG, tag)
    for (const [key, value] of Object.entries(attrs)) node.setAttribute(key, String(value))
    parent?.append(node)
    return node
  }
  const label = (parent: Element, value: string, attrs: Record<string, string | number>) => { const node = el("text", attrs, parent); node.textContent = value; return node }
  const tip = (parent: Element, value: string) => { const title = el("title", {}, parent); title.textContent = value }

  const svg = el("svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": summary, "data-chart": spec.type })
  svg.setAttribute("width", "100%")
  svg.style.maxWidth = `${W}px`
  svg.style.display = "block"
  const legend = spec.series.length > 1
  const top = spec.title ? 32 : 12, bottom = H - 36 - (legend ? 22 : 0), left = 52, right = W - 12
  if (spec.title) label(svg, spec.title, { x: left, y: 20, "font-size": 14, "font-weight": 600, fill: "currentColor", "data-chart-title": "" })

  const tickValues = ticks(min, max), lo = tickValues[0], hi = tickValues[tickValues.length - 1]
  const y = (value: number) => bottom - ((value - lo) / (hi - lo)) * (bottom - top)
  const axis = el("g", { "font-size": 11, fill: "var(--muted-foreground, currentColor)", "data-chart-y": "" }, svg)
  for (const value of tickValues) {
    el("line", { x1: left, x2: right, y1: y(value), y2: y(value), stroke: "var(--border, currentColor)", "stroke-opacity": value === 0 ? 1 : 0.6, "stroke-width": 1 }, axis)
    label(axis, format(value), { x: left - 6, y: y(value) + 4, "text-anchor": "end" })
  }

  const slot = (right - left) / spec.x.length, centre = (i: number) => left + slot * (i + 0.5)
  const every = Math.max(1, Math.ceil(spec.x.length / Math.floor((right - left) / 72)))
  const xAxis = el("g", { "font-size": 11, fill: "var(--muted-foreground, currentColor)", "data-chart-x": "" }, svg)
  spec.x.forEach((value, i) => {
    if (i % every) return
    // A shortened label keeps its full text as a hover title on its group.
    const short = shorten(value), group = short === value ? xAxis : el("g", {}, xAxis)
    if (group !== xAxis) tip(group, value)
    label(group, short, { x: centre(i), y: bottom + 16, "text-anchor": "middle" })
  })

  const colour = (s: number) => `var(--series-${s + 1})`
  if (spec.type === "bar") {
    const group = slot * 0.8, width = group / spec.series.length
    spec.series.forEach((series, s) => {
      const g = el("g", { "data-chart-series": series.name }, svg)
      series.values.forEach((value, i) => {
        const top = y(Math.max(0, value)), height = Math.abs(y(value) - y(0))
        const bar = el("rect", { x: centre(i) - group / 2 + width * s, y: top, width: Math.max(1, width - 1), height: Math.max(value === 0 ? 0 : 1, height), fill: colour(s), "data-chart-value": format(value) }, g)
        tip(bar, `${series.name}, ${spec.x[i]}: ${format(value)}`)
      })
    })
  } else {
    spec.series.forEach((series, s) => {
      const g = el("g", { "data-chart-series-points": series.name }, svg)
      el("polyline", { points: series.values.map((value, i) => `${centre(i)},${y(value)}`).join(" "), fill: "none", stroke: colour(s), "stroke-width": 2, "stroke-linejoin": "round", "data-chart-series": series.name }, g)
      series.values.forEach((value, i) => {
        const point = el("circle", { cx: centre(i), cy: y(value), r: spec.x.length > 60 ? 2 : 3.5, fill: colour(s), "data-chart-value": format(value) }, g)
        tip(point, `${series.name}, ${spec.x[i]}: ${format(value)}`)
      })
    })
  }

  if (legend) {
    const g = el("g", { "font-size": 11, fill: "currentColor", "data-chart-legend": "" }, svg)
    let at = left
    spec.series.forEach((series, s) => {
      el("rect", { x: at, y: H - 20, width: 10, height: 10, rx: 2, fill: colour(s) }, g)
      const short = shorten(series.name), holder = short === series.name ? g : el("g", {}, g)
      if (holder !== g) tip(holder, series.name)
      label(holder, short, { x: at + 14, y: H - 11 })
      at += 24 + Math.min(series.name.length, LABEL_CHARS) * 6.5
    })
  }

  // The values themselves, for screen readers and anyone who wants the numbers.
  const details = document.createElement("details"), toggle = document.createElement("summary")
  toggle.textContent = "Show data table"
  const table = document.createElement("table"), head = document.createElement("thead"), body = document.createElement("tbody"), headRow = document.createElement("tr")
  if (spec.title) { const caption = document.createElement("caption"); caption.textContent = spec.title; table.append(caption) }
  const cell = (tag: "th" | "td", value: string, scope?: string) => { const node = document.createElement(tag); node.textContent = value; if (scope) node.setAttribute("scope", scope); return node }
  headRow.append(cell("th", "", "col"), ...spec.series.map(series => cell("th", series.name, "col")))
  head.append(headRow)
  spec.x.forEach((value, i) => {
    const row = document.createElement("tr")
    row.append(cell("th", value, "row"), ...spec.series.map(series => cell("td", format(series.values[i]))))
    body.append(row)
  })
  table.append(head, body)
  details.append(toggle, table)

  const root = document.createElement("div")
  root.dataset.chartPreview = ""
  root.append(svg, details)
  return { ok: true, node: root }
}
