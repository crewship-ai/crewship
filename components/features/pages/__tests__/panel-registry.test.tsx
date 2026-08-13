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
import { readFileSync, readdirSync } from "node:fs"
import path from "node:path"

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, within } from "@testing-library/react"

import {
  PANEL_SCHEMAS,
  isPanelSchema,
  type MetricPayload,
  type PanelSchema,
} from "@/components/features/pages/panels/types"
import {
  PANEL_REGISTRY,
  resolvePanelComponent,
  PanelRenderer,
} from "@/components/features/pages/panels/registry"
import { EmbedPanel } from "@/components/features/pages/panels/embed-panel"
import { MetricPanel } from "@/components/features/pages/panels/metric-panel"
import { StatusPanel } from "@/components/features/pages/panels/status-panel"
import { TablePanel } from "@/components/features/pages/panels/table-panel"
import {
  PendingSchemaPanel,
  UnknownSchemaPanel,
} from "@/components/features/pages/panels/fallback-panel"
import {
  EM_DASH,
  provenanceProducedAt,
  provenanceRunId,
} from "@/components/features/pages/panels/freshness"
import {
  FIXTURE_NOW,
  PANEL_FIXTURES,
  metricFixtures,
  statusFixtures,
  tableFixtures,
} from "@/components/features/pages/panels/fixtures"
import metricSchema from "@/schemas/panel.metric.v1.json"
import tableSchema from "@/schemas/panel.table.v1.json"

const PANELS_DIR = path.resolve(__dirname, "../panels")

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
    // Five from §3 plus `embed.v1`, reserved from the first migration (§3.1).
    expect(PANEL_SCHEMAS).toHaveLength(6)
  })

  it("carries embed.v1, reserved in Go and in the migration's CHECK (§3.1)", () => {
    // internal/pages/schema.go declares SchemaEmbed and the pages migration's
    // CHECK accepts 'embed.v1', so a page carrying one is valid and stored.
    // Leaving it out of the TS vocabulary rendered it as an UNKNOWN schema —
    // "this version of Crewship does not render embed.v1" — when the true
    // answer is about this INSTANCE, not about this build: the escape hatch
    // draws a frame where an operator has declared a vetted destination and
    // refuses, saying so, where none is declared. `EmbedPanel` owns both
    // answers; the fallback would only ever give the wrong one.
    expect(isPanelSchema("embed.v1")).toBe(true)
    expect(PANEL_REGISTRY["embed.v1"]).toBe(EmbedPanel)
    expect(resolvePanelComponent("embed.v1")).not.toBe(UnknownSchemaPanel)
    expect(resolvePanelComponent("embed.v1")).not.toBe(PendingSchemaPanel)

    const { container } = render(
      <PanelRenderer
        panel={{ id: "grafana", schema: "embed.v1", title: "Embed" }}
        data={{ state: "never_produced" }}
        now={FIXTURE_NOW}
      />,
    )
    expect(screen.queryByText(/does not render/i)).toBeNull()
    // Nothing was ever pushed here, so there is no frame and no destination —
    // the em-dash empty state, exactly as for the other five (§9b.4).
    expect(container.querySelector("iframe")).toBeNull()
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
    expect(screen.getAllByText(/12 Aug 12:40/).length).toBeGreaterThan(0)
  })

  /**
   * §7.3.2b: *"A public panel always carries when its data was produced."* The
   * age was rendered only when `gate.dimmed`, i.e. only for `stale`, so a
   * FAILED panel showed the em dash and a bare footer timestamp — an outsider
   * could read a dead panel without ever seeing how long it had been dead,
   * which is the one thing that section exists to prevent.
   */
  describe("a failed panel carries its age (§7.3.2b)", () => {
    const failed = [
      ["metric.v1", metricFixtures.failed],
      ["status.v1", statusFixtures.failed],
      ["table.v1", tableFixtures.failed],
    ] as const

    it.each(failed)("%s shows an absolute age next to the em dash", (_name, fixture) => {
      const { container } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      const age = container.querySelector('[data-slot="panel-age"]')
      expect(age).toBeTruthy()
      expect(age!.textContent).toContain("2 h 15 min old")
      expect(age!.textContent).not.toMatch(/ago|a while|recently|moments/i)
    })

    it("keeps the age on a public view, where the reason is withheld", () => {
      const { container } = render(
        <PanelRenderer {...metricFixtures.failed} now={FIXTURE_NOW} publicView />,
      )
      expect(container.querySelector('[data-slot="panel-age"]')!.textContent).toContain(
        "2 h 15 min old",
      )
      expect(container.textContent).not.toContain("producer exited 1")
    })
  })

  /**
   * §10b.1: a panel restored by a rollback *"renders dimmed, in a 'waiting for
   * first data' state"*. `never_produced` IS that state — a rollback never
   * resurrects old payloads — so it must not sit at the contrast of a measured
   * value. A rollback is exactly when someone believes what they see.
   */
  it("a never-produced panel renders dimmed, not at full contrast", () => {
    for (const fixture of [
      metricFixtures.neverProduced,
      statusFixtures.neverProduced,
      tableFixtures.neverProduced,
    ]) {
      const { container, unmount } = render(<PanelRenderer {...fixture} now={FIXTURE_NOW} />)
      expect(valueNode(container).className).toMatch(/opacity-/)
      unmount()
    }
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
  /**
   * The size, the case and the tracking used to be asserted as three separate
   * class names because the header wrote them out as three separate class
   * names. They are one role now — `.type-page-label` in the Pages register
   * (`app/globals.css`), which carries all three — so this asserts the role and
   * `type-register.test.tsx` asserts what the role is made of. Restating
   * `uppercase` here would only prove the header had stopped using the register.
   */
  it("puts an icon and the register's uppercase label left, a muted status word right", () => {
    const { container } = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const label = container.querySelector('[data-slot="panel-label"]')
    expect(label).toBeTruthy()
    expect(label!.className).toMatch(/\btype-page-label\b/)
    expect(label!.className).not.toMatch(/text-\[/)
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

/**
 * The em dash is the product's load-bearing glyph, and the client, the schema
 * and the server have to mean the same thing by it.
 *
 * `schemas/panel.table.v1.json` says *"`null` is no data and renders as an em
 * dash, which is a different claim from `0` or an empty string"*, and Go agrees
 * — `Cell.IsNoData()` (internal/pages/payload.go) is true for JSON null and
 * nothing else. The client used to add `value === ""` to that test, so a cell
 * the producer deliberately emptied was reported as "we have nothing to look
 * at". §9b.4: *"`0` is a measured zero, `—` is no basis to compute."*
 */
describe("an empty string is measured data, not an em dash (§9b.4)", () => {
  it("renders an empty table cell as empty, and only the null cell as an em dash", () => {
    const { container } = render(
      <PanelRenderer {...tableFixtures.emptyStringCell} now={FIXTURE_NOW} />,
    )
    const cells = Array.from(
      container.querySelectorAll('table tbody [data-slot="table-cell"]'),
    ) as HTMLElement[]

    const empty = cells.find((c) => c.getAttribute("data-key") === "crew")
    expect(empty).toBeTruthy()
    expect(empty!.textContent).toBe("")
    expect(empty!.textContent).not.toContain(EM_DASH)
    expect(empty!.getAttribute("data-basis")).toBe("measured")

    // The null cell in the same payload still IS an em dash — the two claims
    // stay distinguishable.
    const dashes = cells.filter((c) => c.textContent === EM_DASH)
    expect(dashes).toHaveLength(1)
    expect(dashes[0].getAttribute("data-basis")).toBe("none")
    expect(dashes[0].getAttribute("data-key")).toBe("open")
  })

  it("renders an empty metric value as measured, not as an em dash", () => {
    const { container } = render(
      <PanelRenderer {...metricFixtures.emptyStringValue} now={FIXTURE_NOW} />,
    )
    const value = valueNode(container)
    expect(value.getAttribute("data-basis")).toBe("measured")
    expect(value.textContent).not.toContain(EM_DASH)
  })

  it("still calls null and undefined no data, on both panels", () => {
    const metric = render(<PanelRenderer {...metricFixtures.noValue} now={FIXTURE_NOW} />)
    expect(valueNode(metric.container).getAttribute("data-basis")).toBe("none")

    const table = render(<PanelRenderer {...tableFixtures.fresh} now={FIXTURE_NOW} />)
    const missing = Array.from(
      table.container.querySelectorAll('table tbody [data-slot="table-cell"]'),
    ).filter((c) => c.getAttribute("data-basis") === "none")
    expect(missing).toHaveLength(1)
    expect(missing[0].textContent).toBe(EM_DASH)
  })
})

/**
 * §11b.9: *"An optional `delta_good: "up" | "down"` opts into
 * success/destructive colour… Green-up on an error rate would be a lie, so the
 * payload has to say which way is good."*
 *
 * Two halves had to hold for that mechanism to exist at all: the schema has to
 * admit the property (it is `additionalProperties: false`, so an undeclared
 * `delta_good` is a schema violation and the producer's push is rejected), and
 * the client has to read the WIRE name. It read `deltaGood`, a spelling the
 * PRD never uses, so the one control that stops a lie could never fire.
 */
describe("delta_good (§11b.9)", () => {
  const properties = metricSchema.properties as Record<string, unknown>

  it("is a declared property of metric.v1, so a producer can send it", () => {
    expect(metricSchema.additionalProperties).toBe(false)
    expect(Object.keys(properties)).toContain("delta_good")
    expect((properties.delta_good as { enum: string[] }).enum).toEqual(["up", "down"])

    // Every key of a real producer payload is declared — which is exactly what
    // `additionalProperties: false` checks.
    const payload = { value: 4.2, unit: "%", delta: 0.3, delta_good: "down" }
    for (const key of Object.keys(payload)) {
      expect(Object.keys(properties), `${key} is not a declared property`).toContain(key)
    }
  })

  it("never names the camelCase spelling anywhere in the schema", () => {
    expect(JSON.stringify(metricSchema)).not.toContain("deltaGood")
  })

  it("colours a rise green when the producer said up is good", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.deltaGoodUp} now={FIXTURE_NOW} />)
    const delta = container.querySelector('[data-slot="panel-delta"]')!
    expect(delta.className).toMatch(/text-success/)
    expect(delta.textContent).toContain("+12")
  })

  it("colours the same rise destructive when down is good — an error rate", () => {
    const { container } = render(
      <PanelRenderer {...metricFixtures.deltaGoodDown} now={FIXTURE_NOW} />,
    )
    const delta = container.querySelector('[data-slot="panel-delta"]')!
    expect(delta.className).toMatch(/text-destructive/)
  })

  it("stays muted when the payload does not say which way is good", () => {
    const { container } = render(<PanelRenderer {...metricFixtures.fresh} now={FIXTURE_NOW} />)
    const delta = container.querySelector('[data-slot="panel-delta"]')!
    expect(delta.className).toMatch(/text-muted-foreground/)
    expect(delta.className).not.toMatch(/text-success|text-destructive/)
  })

  it("ignores a camelCase deltaGood, which is not a name on the wire", () => {
    const payload = { value: 128, delta: 12, deltaGood: "up" } as unknown as MetricPayload
    const { container } = render(
      <PanelRenderer
        panel={metricFixtures.fresh.panel}
        data={{ state: "fresh", payload }}
        now={FIXTURE_NOW}
      />,
    )
    const delta = container.querySelector('[data-slot="panel-delta"]')!
    expect(delta.className).toMatch(/text-muted-foreground/)
  })
})

/**
 * §11b exists verbatim to stop this: *"an ambiguity here becomes a client and a
 * server that both pass their own tests."* SLA is `sla_seconds` (decision 3),
 * provenance is `{producer, run_id, produced_at}` (decision 4), and
 * `scripts/test-harness/test-pages.sh` probes for those two keys.
 */
describe("wire names are snake_case (§11b.3, §11b.4)", () => {
  it("keeps the fixtures — the shapes the API serves — in the wire spelling", () => {
    const prov = metricFixtures.fresh.data.provenance as Record<string, unknown>
    expect(Object.keys(prov)).toContain("run_id")
    expect(Object.keys(prov)).toContain("produced_at")
    expect(Object.keys(prov)).not.toContain("runId")
    expect(Object.keys(prov)).not.toContain("producedAt")

    const spec = metricFixtures.fresh.panel as Record<string, unknown>
    expect(Object.keys(spec)).toContain("sla_seconds")
    expect(Object.keys(spec)).not.toContain("slaSeconds")
  })

  it("renders the run id and the age from the wire names alone", () => {
    const { container } = render(
      <PanelRenderer
        panel={{ id: "p", schema: "metric.v1", title: "P", sla_seconds: 60 }}
        data={{
          state: "stale",
          payload: { value: 7 },
          provenance: {
            producer: "routine/rollup",
            run_id: "run_4242",
            produced_at: new Date(2026, 7, 12, 12, 40),
          },
        }}
        now={FIXTURE_NOW}
      />,
    )
    const footer = container.querySelector('[data-slot="panel-provenance"]')!
    expect(footer.textContent).toContain("run_4242")
    expect(container.querySelector('[data-slot="panel-age"]')!.textContent).toContain(
      "2 h 15 min old",
    )
  })

  it("reads only the wire name — the camelCase spelling is not a fallback", () => {
    // There was briefly a shim reading `runId`/`producedAt` too, because
    // `hooks/use-pages.ts` emitted camelCase. Both sides now speak the wire
    // names, and the shim is gone: a payload carrying only the camelCase
    // spelling must read as absent, not as data. A tolerant reader here is how
    // a server that stops sending `run_id` goes unnoticed.
    expect(provenanceRunId({ run_id: "wire" })).toBe("wire")
    expect(provenanceProducedAt({ produced_at: "2026-08-12" })).toBe("2026-08-12")
    expect(provenanceRunId({ runId: "legacy" } as never)).toBeNull()
    expect(provenanceProducedAt({ producedAt: "1999-01-01" } as never)).toBeNull()
    expect(provenanceRunId(null)).toBeNull()
  })
})

/**
 * §11b.12: *"`table.v1` carries a row cap of 200. §10b.3 caps bytes only, and
 * 64 KiB is roughly a thousand rows — more than anyone reads and more than we
 * will virtualise."* The schema shipped 500, with prose defending 500.
 */
describe("table.v1 row cap (§11b.12)", () => {
  it("caps rows at 200, and says 200 in its prose", () => {
    const rows = tableSchema.properties.rows
    expect(rows.maxItems).toBe(200)
    expect(rows.description).toContain("200")
    expect(rows.description).not.toContain("500")
  })
})

/**
 * §8 rule 10: *"Text renders through a React element renderer, never
 * `innerHTML`. No `dangerouslySetInnerHTML` anywhere in the panel registry."*
 * It is true today; nothing made it stay true. §9 repeats it for the dispatch
 * table — no `eval`, no dynamic `import()` of a user-supplied path.
 *
 * The scan is over the source of `panels/`, because the rule is about what may
 * be WRITTEN there, and a behavioural test can only catch the payloads someone
 * thought to write a fixture for. Comments are stripped first so the rule can
 * go on being quoted in a doc comment.
 */
describe("the panel registry never reaches innerHTML (§8 rule 10)", () => {
  function sourcesOf(dir: string): { file: string; code: string }[] {
    return readdirSync(dir)
      .filter((f) => f.endsWith(".ts") || f.endsWith(".tsx"))
      .map((f) => {
        const raw = readFileSync(path.join(dir, f), "utf8")
        const code = raw
          .replace(/\/\*[\s\S]*?\*\//g, "")
          .split("\n")
          .filter((line) => {
            const t = line.trim()
            return !t.startsWith("//") && !t.startsWith("*")
          })
          .join("\n")
        return { file: f, code }
      })
  }

  const sources = sourcesOf(PANELS_DIR)

  it("scans every source file in panels/", () => {
    expect(sources.length).toBeGreaterThanOrEqual(9)
    expect(sources.map((s) => s.file)).toContain("registry.tsx")
  })

  it.each(["dangerouslySetInnerHTML", "innerHTML", "eval("])(
    "no panel source uses %s",
    (forbidden) => {
      const offenders = sources.filter((s) => s.code.includes(forbidden)).map((s) => s.file)
      expect(offenders, `${forbidden} in ${offenders.join(", ")}`).toEqual([])
    },
  )
})
