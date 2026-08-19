/**
 * The /pages overview — PRD §9b.2 (mirrors the Routines overview), §9b.3
 * (empty states are instructions) and §9b.4 (the em-dash rule).
 *
 * The tile band is where this feature is most tempted to lie. "0 stale" off an
 * index that reported no freshness at all is exactly the Pushgateway
 * behaviour §4 rejects, so the distinction the product already draws — `0` is
 * a measured zero, `—` is no basis to compute — is asserted here rather than
 * left to a reviewer's eye.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { PagesOverview } from "@/components/features/pages/pages-overview"
import { EM_DASH } from "@/components/features/pages/panels/freshness"
import { toPageView, type WirePage } from "@/hooks/use-pages"

const NOW = new Date("2026-08-12T12:00:00Z")

const page = (over: Partial<WirePage>) =>
  toPageView({ id: over.slug ?? "p", slug: "p", name: "P", owner: "crew/lookout", ...over })

const POPULATED = [
  page({
    slug: "fleet",
    name: "Flotila .201",
    panels: [
      { id: "a", schema: "status.v1", state: "stale", provenance: { produced_at: "2026-08-12T06:00:00Z" } },
      { id: "b", schema: "metric.v1", state: "fresh", provenance: { produced_at: "2026-08-12T11:55:00Z" } },
    ],
  }),
  page({
    slug: "close",
    name: "Nightly close",
    owner: "crew/finance",
    panels: [{ id: "a", schema: "metric.v1", state: "failed", provenance: { produced_at: "2026-08-10T02:00:00Z" } }],
  }),
]

/** A stat tile's whole card, found by its label. */
function tile(label: string): HTMLElement {
  const els = screen.getAllByText(label)
  for (const el of els) {
    const card = el.closest("[data-slot='card']") as HTMLElement | null
    if (card) return card
  }
  throw new Error(`no stat card around "${label}"`)
}

/** One DashboardCard, by the data-card hook the overview sets on it. */
function card(name: string): HTMLElement {
  const el = document.querySelector(`[data-card="${name}"]`)
  if (!el) throw new Error(`no card [data-card="${name}"]`)
  return el as HTMLElement
}

function renderOverview(pages = POPULATED, over: Record<string, unknown> = {}) {
  const onSelect = vi.fn()
  const onFilterState = vi.fn()
  render(
    <PagesOverview
      pages={pages}
      allPages={pages}
      loading={false}
      error={null}
      onSelect={onSelect}
      onFilterState={onFilterState}
      now={NOW}
      {...over}
    />,
  )
  return { onSelect, onFilterState }
}

describe("PagesOverview", () => {
  beforeEach(() => cleanup())

  it("renders the band of four §9b.2 names, in order", () => {
    renderOverview()
    for (const label of ["Pages", "Stale now", "Updated today", "Needs attention"]) {
      expect(tile(label)).toBeTruthy()
    }
  })

  it("reports measured counts when the wire carried freshness", () => {
    renderOverview()
    expect(tile("Pages").textContent).toContain("2")
    expect(tile("Stale now").textContent).toContain("1 panel past SLA")
    expect(tile("Updated today").textContent).toContain("received a payload")
    expect(tile("Needs attention").textContent).toContain("1 failed")
  })

  it("prints an em dash, never a zero, when there is no basis to compute (§9b.4)", () => {
    // An index that sent a panel COUNT and nothing else says nothing about
    // freshness. "0 stale" would be a claim; "—" is the truth.
    renderOverview([page({ slug: "a", name: "A", panels: 3 })])
    expect(tile("Stale now").textContent).toContain(EM_DASH)
    expect(tile("Stale now").textContent).toContain("freshness not reported")
    expect(tile("Needs attention").textContent).toContain(EM_DASH)
    // The count of pages is measured either way — it is not freshness.
    expect(tile("Pages").textContent).toContain("1")
  })

  it("keeps a measured zero a zero", () => {
    renderOverview([page({ slug: "a", name: "A", panels: [{ id: "1", schema: "metric.v1", state: "fresh" }] })])
    const stale = tile("Stale now").textContent ?? ""
    expect(stale).not.toContain(EM_DASH)
    expect(stale).toContain("all within SLA")
  })

  it("gives every card an ANSWER on the right, never a repeat of the label", () => {
    renderOverview([page({ slug: "a", name: "A", panels: [{ id: "1", schema: "metric.v1", state: "fresh" }] })])
    // §9b.2's idiom: `all fresh`, `nothing pending`, `no pushes yet`.
    expect(screen.getByText("all fresh")).toBeTruthy()
    expect(screen.getByText("nothing pending")).toBeTruthy()
    expect(screen.getByText("no pushes yet")).toBeTruthy()
  })

  it("says 'n stale' on the freshness card once something has gone quiet", () => {
    renderOverview()
    expect(screen.getByText("1 stale")).toBeTruthy()
  })

  it("clicks a freshness row through to the STATUS facet", () => {
    const { onFilterState } = renderOverview()
    fireEvent.click(screen.getByRole("button", { name: /^Stale/ }))
    expect(onFilterState).toHaveBeenCalledWith("stale")
  })

  it("opens a page from the not-reporting list", () => {
    const { onSelect } = renderOverview()
    fireEvent.click(within(card("not-reporting")).getByText("Nightly close"))
    expect(onSelect).toHaveBeenCalledWith("close")
  })

  it("does not repeat a tile\'s label as a card title", () => {
    // A card headed "Needs attention" under a tile headed "Needs attention"
    // makes the reader check whether the two numbers mean the same thing.
    renderOverview()
    expect(within(card("not-reporting")).getByText("Not reporting")).toBeTruthy()
    expect(screen.getAllByText("Needs attention")).toHaveLength(1)
  })

  it("never renders a blank card — every empty state names the next action (§9b.3)", () => {
    renderOverview([page({ slug: "a", name: "A", panels: [{ id: "1", schema: "metric.v1", state: "fresh" }] })])
    // Nothing broken, nothing pushed: two cards, two instructions.
    expect(screen.getByText(/opens an issue on its owning crew/i)).toBeTruthy()
    expect(screen.getByText(/crewship page set/i)).toBeTruthy()
  })

  it("tells an empty workspace how to make the first page exist", () => {
    renderOverview([])
    expect(screen.getByText("No pages yet")).toBeTruthy()
    expect(screen.getByText(/crewship page create --file/i)).toBeTruthy()
  })

  it("shows a skeleton in the final geometry while the first load is in flight", () => {
    const { container } = render(
      <PagesOverview
        pages={[]}
        allPages={[]}
        loading
        error={null}
        onSelect={vi.fn()}
        now={NOW}
      />,
    )
    expect(container.querySelectorAll("[data-slot='skeleton']").length).toBeGreaterThan(4)
  })

  it("surfaces a load error instead of rendering an empty workspace", () => {
    renderOverview(POPULATED, { error: "pages: 500" })
    expect(screen.getByText("pages: 500")).toBeTruthy()
  })
})
