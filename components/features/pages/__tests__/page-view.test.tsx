/**
 * One page's panel grid — PRD §9 (rendering) and §9b.3 (empty states).
 *
 * Two of these assertions exist because the bug they catch looks like a
 * styling nit and is not:
 *
 *  · `col-span-${n}` built as a template literal is invisible to Tailwind's
 *    scanner, and the page ships with every panel full width. The classes are
 *    a literal map for that reason, and this pins it.
 *
 *  · A bare `col-span-8` in a `grid-cols-1` mobile grid does not clamp — it
 *    creates seven implicit columns and the page scrolls sideways. The span
 *    class is therefore `md:`-prefixed, which is also what "single column
 *    below the tablet breakpoint" means.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import { PageView, spanClass } from "@/components/features/pages/page-view"
import { EM_DASH } from "@/components/features/pages/panels/freshness"
import { toPageView, type WirePage } from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

const WIRE: WirePage = {
  id: "cpage1",
  slug: "fleet-201",
  name: "Flotila .201",
  owner: "crew/lookout",
  panels: [
    {
      id: "sluzby",
      schema: "status.v1",
      title: "Jede to?",
      owner: "crew/lookout",
      sla_seconds: 30,
      span: 8,
      state: "fresh",
      data: { items: [{ name: "api", state: "ok", label: "200 OK" }] },
      provenance: {
        producer: "script/watch-services.sh",
        run_id: "crun1",
        produced_at: "2026-08-12T11:59:40Z",
      },
    },
    {
      id: "uptime",
      schema: "metric.v1",
      title: "Uptime",
      sla_seconds: 60,
      span: 4,
      state: "never_produced",
    },
  ],
}

function renderPage(over: Partial<React.ComponentProps<typeof PageView>> = {}) {
  const onBack = vi.fn()
  const result = render(
    <PageView
      page={toPageView(WIRE)}
      slug="fleet-201"
      loading={false}
      error={null}
      notFound={false}
      onBack={onBack}
      now={NOW}
      {...over}
    />,
  )
  return { ...result, onBack }
}

describe("spanClass", () => {
  it("maps every legal span to a static, md-prefixed class", () => {
    for (let n = 1; n <= 12; n += 1) {
      expect(spanClass(n)).toBe(`md:col-span-${n}`)
    }
  })

  it("falls back to full width for anything the spec did not fix", () => {
    // internal/pages/spec.go defaults a missing span to 12; a zero-width panel
    // would be a panel that renders as nothing.
    expect(spanClass(undefined)).toBe("md:col-span-12")
    expect(spanClass(0)).toBe("md:col-span-12")
    expect(spanClass(99)).toBe("md:col-span-12")
    expect(spanClass(-3)).toBe("md:col-span-1")
  })
})

describe("PageView", () => {
  beforeEach(() => cleanup())

  it("lays the panels out on a 12-column grid, one column below tablet", () => {
    const { container } = renderPage()
    const grid = container.querySelector("[data-slot='panel-grid']")!
    expect(grid.className).toContain("grid-cols-1")
    expect(grid.className).toContain("md:grid-cols-12")
  })

  it("drives each cell's width from the spec's span", () => {
    const { container } = renderPage()
    const cells = Array.from(container.querySelectorAll("[data-slot='panel-cell']"))
    expect(cells).toHaveLength(2)
    expect(cells[0].className).toContain("md:col-span-8")
    expect(cells[1].className).toContain("md:col-span-4")
    // Never a bare col-span — that does not clamp in the one-track mobile grid.
    expect(cells[0].className).not.toMatch(/(^|\s)col-span-/)
  })

  it("makes each cell a named container, so a panel reflows on ITS width", () => {
    // §9: container queries for in-panel reflow. The panels' own `@md/panel:`
    // rules resolve against this, which is how a span-4 table collapses to a
    // card list while the span-12 one beside it keeps its table.
    const { container } = renderPage()
    for (const cell of container.querySelectorAll("[data-slot='panel-cell']")) {
      expect(cell.className).toContain("@container/panel")
      expect(cell.className).toContain("min-w-0")
    }
  })

  it("dispatches each panel through the registry", () => {
    renderPage()
    // status.v1 rendered its item, metric.v1 with no payload rendered the
    // em dash plus the sentence that says how to make data arrive (§9b.3).
    expect(screen.getByText("api")).toBeTruthy()
    expect(screen.getByText(EM_DASH)).toBeTruthy()
    expect(screen.getByText(/crewship page set/i)).toBeTruthy()
  })

  it("carries the server-attached provenance into the panel footer (§4.5)", () => {
    renderPage()
    expect(screen.getByText("script/watch-services.sh")).toBeTruthy()
    expect(screen.getByText("crun1")).toBeTruthy()
  })

  it("names the page, its panel count and its owner", () => {
    renderPage()
    expect(screen.getAllByText("Flotila .201").length).toBeGreaterThan(0)
    expect(screen.getByText(/2 panels/)).toBeTruthy()
    expect(screen.getByText("lookout")).toBeTruthy()
  })

  it("goes back to the index", () => {
    const { onBack } = renderPage()
    fireEvent.click(screen.getByRole("button", { name: /back to pages/i }))
    expect(onBack).toHaveBeenCalled()
  })

  it("tells a slug that names nothing apart from a page with no panels", () => {
    renderPage({ page: null, notFound: true })
    expect(screen.getByText(/No page is addressed/)).toBeTruthy()
    expect(screen.getByText(/crewship page list/)).toBeTruthy()

    cleanup()
    renderPage({ page: toPageView({ ...WIRE, panels: [] }) })
    expect(screen.getByText(/declares no panels/i)).toBeTruthy()
    expect(screen.getByText(/crewship page update/i)).toBeTruthy()
  })

  it("shows the failure rather than an empty grid", () => {
    renderPage({ page: null, error: "page: 500" })
    expect(screen.getByText("page: 500")).toBeTruthy()
  })

  it("skeletons the grid while the first load is in flight", () => {
    const { container } = renderPage({ page: null, loading: true })
    expect(container.querySelectorAll("[data-slot='skeleton']").length).toBe(3)
  })
})
