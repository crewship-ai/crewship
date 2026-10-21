import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { EMPTY_FILTERS, type FeedFilters } from "@/lib/activity-feed"

import { ActivityPanel, type PanelOption } from "../activity-panel"

// ACT-06 (PNL-02, v67 `panel()` → `pk-fx`): the Filter button opens the
// facets INSIDE the panel, under the toolbar — the kit facet block (COM-01) —
// not a floating popover.

const routines: PanelOption[] = Array.from({ length: 8 }, (_, i) => ({ id: `r${i + 1}`, label: `Routine ${i + 1}`, count: 8 - i }))
const agents: PanelOption[] = [{ id: "a1", label: "Scout", count: 3 }]
const crews: PanelOption[] = [{ id: "c1", label: "Research", count: 3 }]

function show(filters: FeedFilters = EMPTY_FILTERS) {
  const onFilters = vi.fn()
  const view = render(
    <ActivityPanel
      filters={filters}
      onFilters={onFilters}
      viewCounts={{ all: 4, need: 1, live: 1, failed: 1 }}
      routines={routines}
      agents={agents}
      crews={crews}
      sources={[{ id: "schedule", count: 4 }]}
    />,
  )
  return { onFilters, ...view }
}

const filterButton = () => screen.getByRole("button", { name: /^Filter/ })

afterEach(() => cleanup())

describe("ActivityPanel facets inline (ACT-06)", () => {
  it("Filter opens the facet block inside the panel, above the views, not a popover", () => {
    const { container } = show()
    expect(screen.queryByRole("group", { name: "Filter activity" })).toBeNull()
    fireEvent.click(filterButton())
    const block = screen.getByRole("group", { name: "Filter activity" })
    expect(filterButton()).toHaveAttribute("aria-expanded", "true")
    // In the panel's own DOM, not portalled out of it.
    expect(container.contains(block)).toBe(true)
    expect(block).toHaveAttribute("data-slot", "facet-block")
    expect(document.querySelector("[data-slot=popover-content]")).toBeNull()
    // Under the toolbar and above the views.
    const views = screen.getByRole("list", { name: "Views" })
    expect(block.compareDocumentPosition(views) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    for (const label of ["What", "Status", "Routine", "Who", "Crew", "Work started by"]) {
      expect(within(block).getByRole("button", { name: new RegExp(`^${label}`) })).toBeInTheDocument()
    }
  })

  it("a facet lists at most six values and offers the rest as All N…", () => {
    show()
    fireEvent.click(filterButton())
    const block = screen.getByRole("group", { name: "Filter activity" })
    fireEvent.click(within(block).getByRole("button", { name: /^Routine/ }))
    const facet = within(block).getByRole("group", { name: "Routine" })
    expect(within(facet).getAllByRole("button", { pressed: false })).toHaveLength(6)
    expect(within(facet).getByRole("button", { name: "All 8…" })).toBeInTheDocument()
  })

  it("picking a value sets that facet only and keeps the block open", () => {
    const { onFilters } = show({ ...EMPTY_FILTERS, crew: "c1" })
    fireEvent.click(filterButton())
    const block = screen.getByRole("group", { name: "Filter activity" })
    fireEvent.click(within(block).getByRole("button", { name: /^Routine/ }))
    fireEvent.click(within(block).getByRole("button", { name: /Routine 2/ }))
    expect(onFilters).toHaveBeenLastCalledWith({ ...EMPTY_FILTERS, crew: "c1", routine: "r2" })
    expect(screen.getByRole("group", { name: "Filter activity" })).toBeInTheDocument()
  })

  it("a set facet names its choice and clears alone; Clear empties every facet but keeps the view", () => {
    const { onFilters } = show({ ...EMPTY_FILTERS, crew: "c1", agent: "a1", status: "failed" })
    fireEvent.click(filterButton())
    const block = screen.getByRole("group", { name: "Filter activity" })
    expect(within(block).getByRole("button", { name: /^Crew\s*Research/ })).toBeInTheDocument()
    fireEvent.click(within(block).getByRole("button", { name: "Clear Crew" }))
    expect(onFilters).toHaveBeenLastCalledWith({ ...EMPTY_FILTERS, agent: "a1", status: "failed" })
    fireEvent.click(within(block).getByRole("button", { name: "Clear all filters" }))
    // "Could not finish" is a view, not a facet: Clear keeps it.
    expect(onFilters).toHaveBeenLastCalledWith({ ...EMPTY_FILTERS, status: "failed" })
  })

  it("Escape closes the block and returns focus to Filter", () => {
    show()
    fireEvent.click(filterButton())
    const block = screen.getByRole("group", { name: "Filter activity" })
    fireEvent.keyDown(within(block).getByRole("button", { name: /^What/ }), { key: "Escape" })
    expect(screen.queryByRole("group", { name: "Filter activity" })).toBeNull()
    expect(filterButton()).toHaveFocus()
  })

  it("while closed, each set facet shows as a chip that removes it", () => {
    const { onFilters } = show({ ...EMPTY_FILTERS, crew: "c1" })
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Crew: Research" }))
    expect(onFilters).toHaveBeenLastCalledWith({ ...EMPTY_FILTERS })
  })
})
