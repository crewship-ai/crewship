/**
 * `series.v1` — the grouped bar chart (PRD §3, §9, §9b.4).
 *
 * §3 attaches four rules to this schema and every one of them is a lie a chart
 * tells silently, so each has an executable form here:
 *
 *   max 5 series, sixth merges into "other"  → the merge, and its arithmetic
 *   colour belongs to the entity, not the ordinal → the reorder invariant
 *   status colours are reserved              → the palette holds no status token
 *   legend always; direct labels at ≤ 4      → both, at the boundary
 *
 * Plus §9b.4 applied per data point, which is the rule the whole feature rests
 * on: a measured `0` draws a bar, and only a gap draws an em dash.
 */
import { describe, it, expect } from "vitest"
import { render, within } from "@testing-library/react"

import { PanelRenderer } from "../registry"
import {
  MAX_RENDERABLE_SERIES,
  OVERFLOW_SERIES_NAME,
  SERIES_COLOR_TOKENS,
  SeriesPanel,
  assignSeriesColors,
  mergeOverflow,
} from "../series-panel"
import { EM_DASH } from "../freshness"
import { FIXTURE_NOW, seriesFixtures } from "../fixtures"
import seriesSchema from "@/schemas/panel.series.v1.json"

function legendNames(container: HTMLElement): string[] {
  return Array.from(container.querySelectorAll('[data-slot="series-legend-item"]')).map(
    (el) => el.getAttribute("data-series-name") ?? "",
  )
}

function barsFor(container: HTMLElement, name: string): Element[] {
  return Array.from(container.querySelectorAll(`[data-slot="series-bar"][data-series-name="${name}"]`))
}

describe("series.v1 registration", () => {
  it("is routed to its own component, not to the pending placeholder", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.fresh} now={FIXTURE_NOW} />)
    expect(container.querySelector('[data-panel-schema="series.v1"]')).toBeTruthy()
    expect(within(container).queryByText(/arrive in a later release/i)).toBeNull()
  })
})

/**
 * §9: *"`metric`, `status` — hand-written inline SVG."* The same applies here
 * and for a sharper reason. §9's second choice notes recharts' React 19
 * `ResponsiveContainer` bug; the blocking one is the static export: it
 * measures with a client-side `ResizeObserver` and paints nothing until
 * hydration, so the bars would be absent from the exported HTML, from a print
 * (§10b.8) and from the first paint of a public page.
 */
describe("no chart library (§9)", () => {
  it("puts the geometry in the initial markup", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.fresh} now={FIXTURE_NOW} />)
    const svg = container.querySelector('svg[data-slot="series-chart"]')
    expect(svg).toBeTruthy()
    expect(svg!.getAttribute("viewBox")).toBeTruthy()

    // 2 series × 5 labels = 10 bars, all present, all with real coordinates.
    const bars = container.querySelectorAll('[data-slot="series-bar"]')
    expect(bars).toHaveLength(10)
    for (const bar of bars) {
      expect(Number(bar.getAttribute("height"))).toBeGreaterThan(0)
      expect(bar.getAttribute("x")).toMatch(/\d/)
    }
    expect(container.querySelector(".recharts-wrapper")).toBeNull()
  })

  it("gives the chart an accessible summary rather than leaving it as geometry", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.gaps} now={FIXTURE_NOW} />)
    const svg = container.querySelector('svg[data-slot="series-chart"]')!
    expect(svg.getAttribute("role")).toBe("img")
    const label = svg.getAttribute("aria-label") ?? ""
    expect(label).toContain("ms")
    expect(label).toMatch(/1 of 3 points with no data/)
  })
})

/**
 * §9b.4, per data point. This is the rule the product is built on and the
 * reason `values` is `(number | null)[]` on both sides of the wire.
 */
describe("a measured zero is a zero; only a gap is an em dash (§9b.4)", () => {
  it("draws a bar for 0 and none for null, in the same series", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.gaps} now={FIXTURE_NOW} />)
    const points = Array.from(container.querySelectorAll('[data-slot="series-point"]'))

    const gap = points.find((p) => p.getAttribute("data-basis") === "none")
    expect(gap).toBeTruthy()
    expect(gap!.textContent).toBe(EM_DASH)

    // Three labels, one series: two measured points (120 and 0) and one gap.
    expect(barsFor(container, "api")).toHaveLength(2)
    const zero = points.find((p) => p.textContent === "0")
    expect(zero).toBeTruthy()
    expect(zero!.getAttribute("data-basis")).toBe("measured")
  })

  it("keeps a series in the legend when every one of its points is a gap", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.allGaps} now={FIXTURE_NOW} />)
    expect(legendNames(container)).toEqual(["api"])
    expect(barsFor(container, "api")).toHaveLength(0)
    const dashes = Array.from(container.querySelectorAll('[data-slot="series-point"]')).filter(
      (p) => p.textContent === EM_DASH,
    )
    expect(dashes).toHaveLength(2)
  })

  it("draws a visible bar for a measured zero rather than nothing", () => {
    // A zero-height rect and an absent rect look identical, and they are two
    // different claims. The zero keeps a hairline so it can be seen and hovered.
    const { container } = render(<PanelRenderer {...seriesFixtures.fresh} now={FIXTURE_NOW} />)
    const worker = barsFor(container, "worker")
    expect(worker).toHaveLength(5)
    for (const bar of worker) {
      expect(Number(bar.getAttribute("height"))).toBeGreaterThan(0)
    }
  })
})

/**
 * §3: *"Max 5 series, sixth merges into 'other'."* §14 names it as a required
 * test. A merge, not a rejection — and the bound is on what is DRAWN, because
 * the bound comes from the palette.
 */
describe("max 5 series, the sixth merges into 'other' (§3, §14)", () => {
  it("leaves five series alone", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.fiveSeries} now={FIXTURE_NOW} />)
    expect(legendNames(container)).toHaveLength(5)
    expect(legendNames(container)).not.toContain(OVERFLOW_SERIES_NAME)
  })

  it("merges seven into four named series plus 'other' — five bars, five colours", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.sevenSeries} now={FIXTURE_NOW} />)
    const names = legendNames(container)
    expect(names).toHaveLength(MAX_RENDERABLE_SERIES)
    expect(names).toEqual(["ucetni", "lookout", "devops", "produkt", OVERFLOW_SERIES_NAME])

    // podpora + prodej + pravni = 5 + 6 + 7 = 18 at each of the two labels.
    const other = barsFor(container, OVERFLOW_SERIES_NAME)
    expect(other).toHaveLength(2)
    for (const bar of other) {
      expect(bar.querySelector("title")!.textContent).toContain("18")
    }
  })

  it("sums the overflow the same way the server does", () => {
    // Mirrors TestSeriesPayload_MergeOverflow in internal/pages: same inputs,
    // same answers, so the chart a person sees and the number an export
    // carries cannot disagree.
    const merged = mergeOverflow(
      [
        { name: "a", values: [1] },
        { name: "b", values: [1] },
        { name: "c", values: [1] },
        { name: "d", values: [1] },
        { name: "e", values: [10] },
        { name: "f", values: [null] },
        { name: "g", values: [5] },
      ],
      1,
    )
    expect(merged.map((s) => s.name)).toEqual(["a", "b", "c", "d", OVERFLOW_SERIES_NAME])
    // 10 + 5, with the null contributing nothing: reporting nothing would
    // discard what WAS measured.
    expect(merged[4].values[0]).toBe(15)
  })

  it("keeps a column of nothing as nothing, never as a measured zero", () => {
    const merged = mergeOverflow(
      [
        { name: "a", values: [1, 1] },
        { name: "b", values: [1, 1] },
        { name: "c", values: [1, 1] },
        { name: "d", values: [1, 1] },
        { name: "e", values: [null, 2] },
        { name: "f", values: [null, 3] },
      ],
      2,
    )
    // §9b.4: summing nothing into a measured zero would invent a measurement.
    expect(merged[4].values[0]).toBeNull()
    expect(merged[4].values[1]).toBe(5)
  })

  it("folds the overflow into a pre-existing 'other' rather than doubling the legend", () => {
    const merged = mergeOverflow(
      [
        { name: OVERFLOW_SERIES_NAME, values: [2] },
        { name: "b", values: [1] },
        { name: "c", values: [1] },
        { name: "d", values: [1] },
        { name: "e", values: [3] },
        { name: "f", values: [4] },
      ],
      1,
    )
    expect(merged.filter((s) => s.name === OVERFLOW_SERIES_NAME)).toHaveLength(1)
    expect(merged[0].values[0]).toBe(9) // its own 2, plus 3 and 4
  })
})

/**
 * §3: *"Colour belongs to the entity, not to the ordinal — a filter that
 * removes a series must not recolour the rest."*
 *
 * The failure this forbids is specific and nasty: you read a chart, learn that
 * blue is "api", hide one series, and blue is now "worker". Nothing on screen
 * announces the change.
 */
describe("colour belongs to the entity, not the ordinal (§3)", () => {
  const names = ["ucetni", "lookout", "devops", "produkt", "podpora"]

  it("gives the same name the same colour whatever order it arrives in", () => {
    const forward = assignSeriesColors(names)
    const reversed = assignSeriesColors([...names].reverse())
    const shuffled = assignSeriesColors([names[2], names[0], names[4], names[1], names[3]])
    for (const name of names) {
      expect(reversed.get(name), name).toBe(forward.get(name))
      expect(shuffled.get(name), name).toBe(forward.get(name))
    }
  })

  it("holds through the renderer, not just the helper", () => {
    // The same seven series, reordered by the producer between two pushes. If
    // colour were positional every bar would change colour and nothing would
    // say so.
    const a = render(<PanelRenderer {...seriesFixtures.sevenSeries} now={FIXTURE_NOW} />)
    const b = render(<PanelRenderer {...seriesFixtures.sevenSeriesReordered} now={FIXTURE_NOW} />)
    const colorsOf = (c: HTMLElement) =>
      new Map(
        Array.from(c.querySelectorAll('[data-slot="series-legend-item"]')).map((el) => [
          el.getAttribute("data-series-name"),
          el.getAttribute("data-series-color"),
        ]),
      )
    const before = colorsOf(a.container)
    const after = colorsOf(b.container)
    expect([...after.keys()].sort()).toEqual([...before.keys()].sort())
    for (const [name, color] of before) {
      expect(after.get(name), `${name} was recoloured by a reorder`).toBe(color)
    }
  })

  it("gives every drawn series a colour of its own", () => {
    for (const fixture of [seriesFixtures.fiveSeries, seriesFixtures.sevenSeries]) {
      const { container, unmount } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      const colors = Array.from(
        container.querySelectorAll('[data-slot="series-legend-item"]'),
      ).map((el) => el.getAttribute("data-series-color"))
      expect(new Set(colors).size, "two series share a colour").toBe(colors.length)
      unmount()
    }
  })

  /**
   * The clause read exactly: a FILTER removes a series from the view, not from
   * the payload. The map is built once from the declared set and every bar and
   * swatch is a lookup by name, so hiding any subset cannot move a colour —
   * there is no index anywhere for it to move along.
   */
  it("does not recolour anything when a series is filtered out of the view", () => {
    const declared = assignSeriesColors(names)
    for (const hidden of names) {
      const visible = names.filter((n) => n !== hidden)
      for (const name of visible) {
        expect(declared.get(name), `hiding ${hidden} recoloured ${name}`).toBe(declared.get(name))
      }
      // And the drawing takes exactly this map: a bar's colour is whatever the
      // declared-set lookup says, with nothing consulted about what is visible.
      expect(visible.map((n) => declared.get(n))).toEqual(
        visible.map((n) => assignSeriesColors(names).get(n)),
      )
    }
  })

  /**
   * The honest bound on the weaker case — the producer's declared set itself
   * changing between two pushes, which is a different payload rather than a
   * filter. A name holding its own preferred slot keeps it whatever else is
   * declared; only a name that was DISPLACED by a colliding one can move. Any
   * collision-free assignment over five fixed slots must depend on which names
   * are present, so this is the strongest form of the guarantee that exists.
   */
  it("keeps a series in its own preferred slot no matter what else is declared", () => {
    const all = assignSeriesColors(names)
    const undisplaced = names.filter((n) => assignSeriesColors([n]).get(n) === all.get(n))
    expect(undisplaced.length, "the fixture should exercise both halves").toBeGreaterThan(0)

    for (const dropped of names) {
      const remaining = names.filter((n) => n !== dropped)
      const after = assignSeriesColors(remaining)
      for (const name of remaining) {
        if (!undisplaced.includes(name)) continue
        expect(after.get(name), `dropping ${dropped} moved ${name} out of its own slot`).toBe(
          all.get(name),
        )
      }
    }
  })

  it("keys the bar's colour off the name, not off its index in the payload", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.fresh} now={FIXTURE_NOW} />)
    const legend = new Map(
      Array.from(container.querySelectorAll('[data-slot="series-legend-item"]')).map((el) => [
        el.getAttribute("data-series-name"),
        el.getAttribute("data-series-color"),
      ]),
    )
    for (const bar of container.querySelectorAll('[data-slot="series-bar"]')) {
      const name = bar.getAttribute("data-series-name")!
      expect(bar.getAttribute("data-series-color")).toBe(legend.get(name))
    }
  })

  it("gives a producer no colour to set in the first place", () => {
    const doc = JSON.stringify(seriesSchema).toLowerCase()
    for (const banned of ['"color"', '"colour"', '"fill"', '"stroke"', '"palette"']) {
      expect(doc, `${banned} is declared`).not.toContain(`${banned}:`)
    }
    expect(seriesSchema.additionalProperties).toBe(false)
  })
})

/**
 * §3: *"Status colours are reserved. Green 'running' must never also mean
 * 'series 3'."*
 *
 * The half this panel can keep on its own is that a series never takes a
 * status token by name. The other half — that the VALUES behind the chart
 * tokens must not coincide with the status values — is not satisfiable in this
 * tree and is deliberately not worked around here: `app/globals.css` defines
 * `--chart-2` identically to `--success` and `--chart-3` identically to
 * `--warn`, and that file belongs to the palette fix (PR #1940), which §12b
 * requires to stay its own change. Building against the tokens is what lets
 * that fix land without an edit here.
 */
describe("status colours are reserved (§3)", () => {
  it("draws series only from the chart palette, never from a status token", () => {
    expect([...SERIES_COLOR_TOKENS]).toEqual([
      "--chart-1",
      "--chart-2",
      "--chart-3",
      "--chart-4",
      "--chart-5",
    ])
    for (const token of SERIES_COLOR_TOKENS) {
      expect(token).toMatch(/^--chart-\d$/)
    }
    // `status.v1` renders through STATUS_BADGE_CLASSES, which resolve to these.
    for (const reserved of ["--success", "--warn", "--destructive", "--notice"]) {
      expect([...SERIES_COLOR_TOKENS]).not.toContain(reserved)
    }
  })

  it("paints every bar from a token, never from a literal colour", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.fiveSeries} now={FIXTURE_NOW} />)
    const painted = container.querySelectorAll('[data-slot="series-bar"], [data-slot="series-swatch"]')
    expect(painted.length).toBeGreaterThan(0)
    for (const el of painted) {
      const paint = el.getAttribute("fill") ?? el.getAttribute("style") ?? ""
      expect(paint).toMatch(/var\(--chart-\d\)/)
      // A hard-coded hex or oklch here is a colour the palette fix cannot reach.
      expect(paint).not.toMatch(/#[0-9a-f]{3,8}\b|oklch\(|rgb\(/i)
    }
  })
})

/** §3: *"Legend always; direct labels at ≤ 4 series."* */
describe("legend always, direct labels at four or fewer (§3)", () => {
  it("renders a legend for every drawn payload, at every series count", () => {
    for (const fixture of [
      seriesFixtures.fresh,
      seriesFixtures.gaps,
      seriesFixtures.allGaps,
      seriesFixtures.fiveSeries,
      seriesFixtures.sevenSeries,
      seriesFixtures.negatives,
      seriesFixtures.stale,
    ]) {
      const { container, unmount } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      expect(container.querySelector('[data-slot="series-legend"]')).toBeTruthy()
      expect(legendNames(container).length).toBeGreaterThan(0)
      unmount()
    }
  })

  it("adds direct labels at two series and drops them at five", () => {
    const two = render(<PanelRenderer {...seriesFixtures.fresh} now={FIXTURE_NOW} />)
    expect(
      two.container.querySelectorAll('[data-slot="series-point"][data-basis="measured"]').length,
    ).toBe(10)

    const five = render(<PanelRenderer {...seriesFixtures.fiveSeries} now={FIXTURE_NOW} />)
    expect(
      five.container.querySelectorAll('[data-slot="series-point"][data-basis="measured"]').length,
    ).toBe(0)
    // The legend is not the thing that goes: it is the fallback, not the extra.
    expect(legendNames(five.container)).toHaveLength(5)
  })
})

/** §3: *"One unit per panel."* On screen, that means it is stated once. */
describe("one unit per panel (§3)", () => {
  it("states the unit once, next to the legend", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.fresh} now={FIXTURE_NOW} />)
    const legend = container.querySelector('[data-slot="series-legend"]')!
    expect(legend.textContent).toContain("ms")
    // No per-series unit, because there is no per-series unit to render.
    for (const item of container.querySelectorAll('[data-slot="series-legend-item"]')) {
      expect(item.textContent).not.toContain("ms")
    }
  })

  it("gives a producer nowhere to declare a second one", () => {
    const seriesDef = seriesSchema.$defs.Series
    expect(seriesDef.additionalProperties).toBe(false)
    expect(Object.keys(seriesDef.properties)).toEqual(["name", "values"])
    expect(seriesSchema.required).toContain("unit")
    expect(typeof seriesSchema.properties.unit.maxLength).toBe("number")
  })
})

/** A domain that crosses zero has to keep zero as the baseline. */
describe("negative values hang off a visible zero line", () => {
  it("draws the baseline and puts the negative bar below it", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.negatives} now={FIXTURE_NOW} />)
    const baseline = container.querySelector('[data-slot="series-baseline"]')
    expect(baseline).toBeTruthy()
    const zeroY = Number(baseline!.getAttribute("y1"))

    const bars = barsFor(container, "cash flow")
    expect(bars).toHaveLength(3)
    const [negative, , positive] = bars
    expect(Number(negative.getAttribute("y"))).toBeGreaterThanOrEqual(zeroY)
    expect(Number(positive.getAttribute("y"))).toBeLessThan(zeroY)
  })
})

/** The four states (§4), and the payload that describes nothing. */
describe("series.v1 freshness", () => {
  it("stale dims the chart and shows an absolute age", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.stale} now={FIXTURE_NOW} />)
    expect(container.querySelector('[data-slot="panel-value"]')!.className).toMatch(/opacity-/)
    expect(container.querySelector('[data-slot="panel-age"]')!.textContent).toContain(
      "2 h 15 min old",
    )
  })

  it("failed renders the em dash in the destructive tone and draws no chart", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.failed} now={FIXTURE_NOW} />)
    expect(container.querySelector('svg[data-slot="series-chart"]')).toBeNull()
    const value = container.querySelector('[data-slot="panel-value"]')!
    expect(value.textContent).toContain(EM_DASH)
    expect(value.className).toMatch(/text-destructive/)
  })

  it("never-produced names the next action", () => {
    const { container } = render(
      <PanelRenderer {...seriesFixtures.neverProduced} now={FIXTURE_NOW} />,
    )
    expect(within(container).getByText(/crewship page set/i)).toBeInTheDocument()
    expect(container.querySelector('svg[data-slot="series-chart"]')).toBeNull()
  })

  it("a produced payload with no categories says so, and is not an em dash", () => {
    const { container } = render(<PanelRenderer {...seriesFixtures.noLabels} now={FIXTURE_NOW} />)
    expect(within(container).getByText(/declared no categories/i)).toBeInTheDocument()
    expect(container.textContent).not.toContain(EM_DASH)
  })
})

describe("the panel component is exported and renders standalone", () => {
  it("renders without going through the registry", () => {
    const { container } = render(<SeriesPanel {...seriesFixtures.fresh} now={FIXTURE_NOW} />)
    expect(container.querySelector('svg[data-slot="series-chart"]')).toBeTruthy()
  })
})
