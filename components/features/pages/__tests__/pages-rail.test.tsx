/**
 * The /pages filter rail — PRD §9b.1.
 *
 * The point of these tests is not that the rail renders. It is that Pages is
 * the SECOND surface on the shared filter panel and not the sixth hand-rolled
 * one (#1776): the panel stays open after a pick, a pick never touches a
 * sibling facet, and both facets are multi-select. Credentials'
 * `set({category}); setFilterOpen(false)` is the behaviour being ruled out, and
 * it is invisible to a test that only asserts what is on screen at rest.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import { PagesRail } from "@/components/features/pages/pages-rail"
import * as sidebarKit from "@/components/layout/sidebar-kit"
import { toPageView, EMPTY_PAGE_FILTERS, type PageFilters } from "@/hooks/use-pages"

const PAGES = [
  toPageView({
    id: "p1",
    slug: "fleet-201",
    name: "Flotila .201",
    owner: "crew/lookout",
    panels: [
      { id: "a", schema: "status.v1", state: "stale" },
      { id: "b", schema: "metric.v1", state: "fresh" },
    ],
  }),
  toPageView({
    id: "p2",
    slug: "nightly-close",
    name: "Nightly close",
    owner: "crew/finance",
    panels: [{ id: "a", schema: "metric.v1", state: "fresh" }],
  }),
]

function renderRail(over: Partial<React.ComponentProps<typeof PagesRail>> = {}) {
  const onFiltersChange = vi.fn()
  const onSelectPage = vi.fn()
  const onSearchChange = vi.fn()
  const props = {
    pages: PAGES,
    search: "",
    onSearchChange,
    filters: EMPTY_PAGE_FILTERS as PageFilters,
    onFiltersChange,
    selectedSlug: null,
    onSelectPage,
    ...over,
  }
  render(<PagesRail {...props} />)
  return { onFiltersChange, onSelectPage, onSearchChange }
}

// `^Filter` and not `/filter/i`: the active-filter chips carry "Remove filter"
// buttons, and a loose match would grab one of those instead of the trigger.
const openPanel = () => fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
const panel = () => screen.queryByRole("group", { name: /filter pages/i })

describe("PagesRail", () => {
  beforeEach(() => cleanup())

  it("is built from the shared kit rather than a private popover", () => {
    // A structural assertion on purpose: the drift #1776 tracks is a surface
    // that stops importing the kit, and that is not visible in the DOM.
    expect(typeof sidebarKit.SidebarFilterPopover).toBe("function")
    expect(typeof sidebarKit.SidebarFacet).toBe("function")
    expect(typeof sidebarKit.SidebarFacetOption).toBe("function")
    renderRail()
    // The panel the kit renders carries role="group" + its accessible name.
    openPanel()
    expect(panel()).toBeTruthy()
  })

  it("offers exactly the STATUS options §9b.1 names", () => {
    renderRail()
    openPanel()
    const p = panel()!
    // "All" is the facet's reset row (exact name — "All crews" is the OWNER
    // facet's own reset); the four states carry their count in the name.
    expect(within(p).getByRole("button", { name: "All" })).toBeTruthy()
    for (const label of ["Fresh", "Stale", "Failed", "Never produced"]) {
      expect(within(p).getByRole("button", { name: new RegExp(`^${label}`) })).toBeTruthy()
    }
  })

  it("keeps the panel open after a pick", () => {
    renderRail()
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /^Stale/i }))
    expect(panel()).toBeTruthy()
  })

  it("adds to a facet instead of replacing it, and leaves its sibling alone", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale"], owners: ["crew/finance"] } })
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /^Fresh/i }))
    expect(onFiltersChange).toHaveBeenCalledWith({
      states: ["stale", "fresh"],
      owners: ["crew/finance"], // untouched — the whole point of #1776
    })
  })

  it("toggles a picked option off without clearing the rest", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale", "fresh"], owners: [] } })
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: /^Stale/i }))
    expect(onFiltersChange).toHaveBeenCalledWith({ states: ["fresh"], owners: [] })
  })

  it("resets one facet without touching the other", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale"], owners: ["crew/finance"] } })
    openPanel()
    fireEvent.click(within(panel()!).getByRole("button", { name: "All" }))
    expect(onFiltersChange).toHaveBeenCalledWith({ states: [], owners: ["crew/finance"] })
  })

  it("builds the OWNER facet per crew, from the loaded pages", () => {
    const { onFiltersChange } = renderRail()
    openPanel()
    const p = panel()!
    expect(within(p).getByRole("button", { name: /^lookout/ })).toBeTruthy()
    fireEvent.click(within(p).getByRole("button", { name: /^finance/ }))
    expect(onFiltersChange).toHaveBeenCalledWith({ states: [], owners: ["crew/finance"] })
  })

  it("counts facet options over the whole list, not the filtered view", () => {
    // With "stale" picked, "Fresh" must still report the 2 pages it would
    // match — a menu whose unpicked options all read 0 argues with itself.
    renderRail({ filters: { states: ["stale"], owners: [] } })
    openPanel()
    const fresh = within(panel()!).getByRole("button", { name: /^Fresh/i })
    expect(fresh.textContent).toContain("2")
  })

  it("shows what is narrowing the list as removable chips", () => {
    const { onFiltersChange } = renderRail({ filters: { states: ["stale"], owners: ["crew/lookout"] } })
    expect(screen.getByText("Stale")).toBeTruthy()
    expect(screen.getByText("lookout")).toBeTruthy()
    fireEvent.click(screen.getAllByRole("button", { name: /remove filter/i })[0])
    expect(onFiltersChange).toHaveBeenCalledWith({ states: [], owners: ["crew/lookout"] })
  })

  it("lists the pages the filter leaves, and opens one on click", () => {
    const { onSelectPage } = renderRail({ filters: { states: ["stale"], owners: [] } })
    expect(screen.getByText("Flotila .201")).toBeTruthy()
    expect(screen.queryByText("Nightly close")).toBeNull()
    fireEvent.click(screen.getByText("Flotila .201"))
    expect(onSelectPage).toHaveBeenCalledWith("fleet-201")
  })

  it("never renders a blank list — an empty result names the next action (§9b.3)", () => {
    renderRail({ filters: { states: ["failed"], owners: [] } })
    expect(screen.getByText(/clear a facet/i)).toBeTruthy()

    cleanup()
    renderRail({ pages: [] })
    expect(screen.getByText(/crewship page create/i)).toBeTruthy()
  })
})
