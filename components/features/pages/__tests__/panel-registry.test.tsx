/**
 * Panel registry + the three v0 panels (PRD `docs/prd/pages.md` §3, §4, §9, §9b.4).
 *
 * The load-bearing assertions here are the freshness ones. A dashboard that
 * shows an old number as if it were current is the failure this whole feature
 * exists to prevent, and the em-dash rule (§9b.4) is how the product already
 * says "no basis to compute" — `0` is a measured zero, `—` is nothing to
 * measure. These tests pin that distinction so a later refactor cannot
 * quietly collapse the two.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within } from "@testing-library/react"

import {
  PANEL_SCHEMAS,
  isPanelSchema,
  type PanelSchema,
} from "@/components/features/pages/panels/types"
import {
  PANEL_REGISTRY,
  resolvePanelComponent,
  PanelRenderer,
} from "@/components/features/pages/panels/registry"
import { MetricPanel } from "@/components/features/pages/panels/metric-panel"
import { StatusPanel } from "@/components/features/pages/panels/status-panel"
import { TablePanel } from "@/components/features/pages/panels/table-panel"
import { UnknownSchemaPanel } from "@/components/features/pages/panels/fallback-panel"
import { EM_DASH } from "@/components/features/pages/panels/freshness"
import {
  FIXTURE_NOW,
  PANEL_FIXTURES,
  metricFixtures,
  statusFixtures,
  tableFixtures,
} from "@/components/features/pages/panels/fixtures"

function valueNode(container: HTMLElement) {
  const node = container.querySelector('[data-slot="panel-value"]')
  if (!node) throw new Error("no [data-slot=panel-value] rendered")
  return node as HTMLElement
}

describe("panel registry", () => {
  it("resolves every schema in the closed vocabulary to a component", () => {
    for (const schema of PANEL_SCHEMAS) {
      expect(typeof PANEL_REGISTRY[schema]).toBe("function")
      expect(resolvePanelComponent(schema)).toBe(PANEL_REGISTRY[schema])
    }
    expect(PANEL_SCHEMAS).toHaveLength(5)
  })

  it("maps the three v0 schemas to their own components", () => {
    expect(PANEL_REGISTRY["metric.v1"]).toBe(MetricPanel)
    expect(PANEL_REGISTRY["status.v1"]).toBe(StatusPanel)
    expect(PANEL_REGISTRY["table.v1"]).toBe(TablePanel)
  })

  it("resolves an unknown schema to the fallback instead of throwing", () => {
    expect(resolvePanelComponent("chart.v9")).toBe(UnknownSchemaPanel)
    expect(resolvePanelComponent("")).toBe(UnknownSchemaPanel)
    expect(isPanelSchema("chart.v9")).toBe(false)
  })

  it("does not resolve inherited object keys as panel schemas", () => {
    // A user-supplied string must never reach anything but the flat, closed
    // map — `__proto__`, `constructor` and `toString` are on every object.
    for (const hostile of ["__proto__", "constructor", "toString", "hasOwnProperty"]) {
      expect(isPanelSchema(hostile)).toBe(false)
      expect(resolvePanelComponent(hostile)).toBe(UnknownSchemaPanel)
    }
  })

  it("renders an unknown schema without throwing, naming the next action", () => {
    expect(() =>
      render(
        <PanelRenderer
          panel={{ id: "mystery", schema: "chart.v9", title: "Mystery" }}
          data={{ state: "fresh", payload: {} }}
          now={FIXTURE_NOW}
        />,
      ),
    ).not.toThrow()
    expect(screen.getByText(/chart\.v9/)).toBeInTheDocument()
    expect(screen.getByText(/upgrade|update/i)).toBeInTheDocument()
  })

  it("renders every fixture without throwing", () => {
    for (const fixture of PANEL_FIXTURES) {
      expect(() =>
        render(<PanelRenderer panel={fixture.panel} data={fixture.data} now={FIXTURE_NOW} />),
      ).not.toThrow()
    }
    expect(PANEL_FIXTURES.length).toBeGreaterThanOrEqual(12)
  })

  describe("a payload that throws is contained", () => {
    let spy: ReturnType<typeof vi.spyOn>
    beforeEach(() => {
      spy = vi.spyOn(console, "error").mockImplementation(() => {})
    })
    afterEach(() => spy.mockRestore())

    it("catches a render-time throw and shows the fallback", () => {
      const hostile = new Proxy(
        {},
        {
          get() {
            throw new Error("hostile payload")
          },
        },
      )
      expect(() =>
        render(
          <PanelRenderer
            panel={{ id: "boom", schema: "metric.v1", title: "Boom" }}
            data={{ state: "fresh", payload: hostile }}
            now={FIXTURE_NOW}
          />,
        ),
      ).not.toThrow()
      expect(screen.getByText(/could not be rendered/i)).toBeInTheDocument()
    })
  })
})

describe("freshness — the em-dash rule (PRD §4, §9b.4)", () => {
  it("fresh renders the value at full contrast", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.fresh} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value).toHaveTextContent("128")
    expect(value.className).not.toMatch(/opacity-/)
    expect(value.className).not.toMatch(/text-destructive/)
    expect(container.querySelector('[data-panel-state="fresh"]')).toBeTruthy()
  })

  it("a measured zero renders as 0, never as an em dash", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.zero} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value).toHaveTextContent("0")
    expect(value.textContent).not.toContain(EM_DASH)
    expect(value.getAttribute("data-basis")).toBe("measured")
  })

  it("a never-produced panel renders an em dash plus a sentence naming the next action", () => {
    const { container } = render(
      <PanelRenderer {...metricFixtures.neverProduced} now={FIXTURE_NOW} />,
    )
    const value = valueNode(container)
    expect(value.textContent).toContain(EM_DASH)
    expect(value.getAttribute("data-basis")).toBe("none")
    expect(screen.getByText(/crewship page set/i)).toBeInTheDocument()
    expect(container.querySelector('[data-panel-state="never_produced"]')).toBeTruthy()
  })

  it("a measured zero and a never-produced panel render differently", () => {
    const zero = render(<PanelRenderer {...metricFixtures.zero} now={FIXTURE_NOW} />)
    const zeroText = valueNode(zero.container).textContent
    const none = render(<PanelRenderer {...metricFixtures.neverProduced} now={FIXTURE_NOW} />)
    const noneText = valueNode(none.container).textContent
    expect(zeroText).not.toEqual(noneText)
    expect(zeroText).toContain("0")
    expect(noneText).toContain(EM_DASH)
  })

  it("stale dims the value and shows an ABSOLUTE age, never a relative phrase", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.stale} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value).toHaveTextContent("128")
    expect(value.className).toMatch(/opacity-/)

    const age = container.querySelector('[data-slot="panel-age"]')
    expect(age).toBeTruthy()
    // 12:40 → 14:55 on 12 Aug 2026: an exact elapsed amount and the exact
    // instant, both computed, neither fuzzy.
    expect(age!.textContent).toContain("2 h 15 min old")
    expect(age!.textContent).toContain("12 Aug 12:40")
    expect(age!.textContent).not.toMatch(/ago|a while|recently|moments/i)
  })

  it("failed renders the em dash in the destructive tone", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.failed} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value.textContent).toContain(EM_DASH)
    expect(value.className).toMatch(/text-destructive/)
    expect(value.getAttribute("data-basis")).toBe("none")
    expect(screen.getByText(/producer exited 1/)).toBeInTheDocument()
  })

  it("hides the failure reason on a public view but keeps the age", () => {
    render(<PanelRenderer {...metricFixtures.failed} now={FIXTURE_NOW} publicView />)
    expect(screen.queryByText(/producer exited 1/)).toBeNull()
    expect(screen.getByText(/12 Aug 12:40/)).toBeInTheDocument()
  })

  it("uses exactly one no-data glyph across all three schemas and all four states", () => {
    for (const fixture of PANEL_FIXTURES) {
      const { container, unmount } = render(
        <PanelRenderer panel={fixture.panel} data={fixture.data} now={FIXTURE_NOW} />,
      )
      const text = container.textContent ?? ""
      // No fourth glyph: en dash, hyphen-as-value, "n/a", "N/A", "null".
      expect(text).not.toMatch(/\bN\/A\b/i)
      expect(text).not.toContain("–") // en dash
      expect(text).not.toMatch(/\bnull\b/)
      expect(text).not.toMatch(/\bundefined\b/)
      unmount()
    }
  })

  it("carries the provenance footer — producer, run id and timestamp", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.fresh} now={FIXTURE_NOW} />)
    const footer = container.querySelector('[data-slot="panel-provenance"]')
    expect(footer).toBeTruthy()
    expect(footer!.textContent).toContain("routine/nightly-close")
    expect(footer!.textContent).toContain("run_8812")
  })
})

describe("metric.v1", () => {
  it("draws its sparkline as inline SVG in the initial markup — no chart library", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.fresh} now={FIXTURE_NOW} />)
    const svg = container.querySelector('svg[data-slot="sparkline"]')
    expect(svg).toBeTruthy()
    expect(svg!.getAttribute("viewBox")).toBeTruthy()
    // Points are in the markup, not measured from a ResizeObserver on hydrate.
    expect(svg!.querySelector("polyline")?.getAttribute("points")).toMatch(/\d/)
    expect(container.querySelector(".recharts-wrapper")).toBeNull()
  })

  it("renders the unit and a delta with an explicit sign", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.fresh} now={FIXTURE_NOW} />)
    expect(within(container).getByText("invoices")).toBeInTheDocument()
    expect(within(container).getByText(/\+12/)).toBeInTheDocument()
  })

  it("renders a target meter without a chart library", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.fresh} now={FIXTURE_NOW} />)
    const meter = container.querySelector('[data-slot="panel-target"]')
    expect(meter).toBeTruthy()
    expect(meter!.getAttribute("aria-valuenow")).toBe("128")
    expect(meter!.getAttribute("aria-valuemax")).toBe("150")
  })

  it("renders an em dash when the payload carries no value at all", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.noValue} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value.textContent).toContain(EM_DASH)
    expect(value.getAttribute("data-basis")).toBe("none")
  })

  it("survives a sparkline with non-finite points", () => {
    expect(() =>
      render(<PanelRenderer {...metricFixtures.brokenSparkline} now={FIXTURE_NOW} />),
    ).not.toThrow()
  })
})

describe("status.v1", () => {
  it("gives every item a glyph and a word, never colour alone", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.fresh} now={FIXTURE_NOW} />)
    const items = container.querySelectorAll('[data-slot="status-item"]')
    expect(items).toHaveLength(3)
    for (const item of items) {
      const glyph = item.querySelector('[data-slot="status-glyph"]')
      expect(glyph?.textContent?.trim()).toBeTruthy()
      const word = item.querySelector('[data-slot="status-word"]')
      expect(word?.textContent?.trim()).toBeTruthy()
    }
    expect(screen.getByText("api")).toBeInTheDocument()
    expect(screen.getByText(/critical/i)).toBeInTheDocument()
  })

  it("degrades an unknown item state to a neutral row instead of throwing", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.unknownState} now={FIXTURE_NOW} />)
    const item = container.querySelector('[data-slot="status-item"]')
    expect(item).toBeTruthy()
    expect(item!.getAttribute("data-state")).toBe("unknown")
    expect(item!.textContent).not.toMatch(/\bundefined\b/)
  })

  it("dims the whole grid when stale and shows the absolute age", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.stale} now={FIXTURE_NOW} />)
    expect(valueNode(container).className).toMatch(/opacity-/)
    expect(container.querySelector('[data-slot="panel-age"]')!.textContent).toContain("12 Aug 12:40")
  })

  it("renders the em dash in the destructive tone when the producer failed", () => {
    const { container } = render(<PanelRenderer {...statusFixtures.failed} now={FIXTURE_NOW} />)
    const value = valueNode(container)
    expect(value.textContent).toContain(EM_DASH)
    expect(value.className).toMatch(/text-destructive/)
  })

  it("never-produced names the next action", () => {
    render(<PanelRenderer {...statusFixtures.neverProduced} now={FIXTURE_NOW} />)
    expect(screen.getByText(/crewship page set/i)).toBeInTheDocument()
  })
})

describe("table.v1", () => {
  it("renders a semantic table and a card list, switching on its OWN width", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const scope = container.querySelector('[data-slot="panel-container"]')
    expect(scope).toBeTruthy()
    // @container/panel + a named container variant: the panel reflows on its
    // own width, not the viewport's.
    expect(scope!.className).toMatch(/@container\/panel/)

    const table = container.querySelector("table")
    expect(table).toBeTruthy()
    expect(table!.querySelectorAll("thead th")).toHaveLength(3)
    expect(table!.querySelectorAll("tbody tr")).toHaveLength(3)
    expect(table!.className).toMatch(/@\w+\/panel:/)

    const cards = container.querySelector('[data-slot="table-cards"]')
    expect(cards).toBeTruthy()
    expect(cards!.className).toMatch(/@\w+\/panel:hidden/)
    expect(cards!.querySelectorAll('[data-slot="table-card"]')).toHaveLength(3)
  })

  it("renders a measured zero as 0 and a missing cell as an em dash", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const table = container.querySelector("table")!
    const cells = table.querySelectorAll('tbody [data-slot="table-cell"]')
    const zero = Array.from(cells).find((c) => c.getAttribute("data-key") === "open" && c.textContent === "0")
    expect(zero).toBeTruthy()
    expect(zero!.getAttribute("data-basis")).toBe("measured")

    const missing = Array.from(cells).find((c) => c.getAttribute("data-basis") === "none")
    expect(missing).toBeTruthy()
    expect(missing!.textContent).toBe(EM_DASH)
  })

  it("honours per-column alignment", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const th = container.querySelector('thead [data-key="open"]')
    expect(th!.className).toMatch(/text-right/)
  })

  it("accepts positional rows as well as keyed rows", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.positionalRows} now={FIXTURE_NOW} />)
    expect(container.querySelector("table")!.querySelectorAll("tbody tr")).toHaveLength(2)
    expect(within(container).getAllByText("ucetni").length).toBeGreaterThan(0)
  })

  it("shows an empty-state sentence when the producer pushed zero rows", () => {
    render(<PanelRenderer {...tableFixtures.emptyRows} now={FIXTURE_NOW} />)
    expect(screen.getByText(/no rows/i)).toBeInTheDocument()
  })

  it("dims the table when stale and keeps the absolute age", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.stale} now={FIXTURE_NOW} />)
    expect(valueNode(container).className).toMatch(/opacity-/)
    expect(container.querySelector('[data-slot="panel-age"]')!.textContent).toContain("2 h 15 min old")
  })

  it("renders the em dash in the destructive tone when failed, and no table", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.failed} now={FIXTURE_NOW} />)
    expect(container.querySelector("table")).toBeNull()
    expect(valueNode(container).className).toMatch(/text-destructive/)
  })

  it("never-produced names the next action", () => {
    render(<PanelRenderer {...tableFixtures.neverProduced} now={FIXTURE_NOW} />)
    expect(screen.getByText(/crewship page set/i)).toBeInTheDocument()
  })
})

describe("the card-header idiom (PRD §9b.2)", () => {
  it("puts an icon and an 11px uppercase tracked label left, a muted status word right", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const label = container.querySelector('[data-slot="panel-label"]')
    expect(label).toBeTruthy()
    expect(label!.className).toMatch(/text-\[11px\]/)
    expect(label!.className).toMatch(/uppercase/)
    expect(label!.className).toMatch(/tracking-wider/)
    expect(label!.querySelector("svg")).toBeTruthy()

    const word = container.querySelector('[data-slot="panel-status-word"]')
    expect(word).toBeTruthy()
    expect(word!.className).toMatch(/text-muted-foreground/)
    // The right-hand word is the answer, never a repeat of the label.
    expect(word!.textContent!.toLowerCase()).not.toContain("crews")
  })

  it.each(PANEL_SCHEMAS as readonly PanelSchema[])(
    "%s renders a header label and a status word",
    (schema) => {
      const { container } = render(
        <PanelRenderer
          panel={{ id: "p", schema, title: "Panel" }}
          data={{ state: "never_produced" }}
          now={FIXTURE_NOW}
        />,
      )
      expect(container.querySelector('[data-slot="panel-label"]')).toBeTruthy()
      expect(container.querySelector('[data-slot="panel-status-word"]')).toBeTruthy()
    },
  )
})
