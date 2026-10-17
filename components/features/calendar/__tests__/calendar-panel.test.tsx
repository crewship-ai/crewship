import { useState } from "react"
import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { CalendarPanel } from "../calendar-panel"
import { EMPTY_CALENDAR_FILTERS, type CalendarFilters } from "@/lib/calendar/model"
import { at, entry, items } from "./fixtures"

const list = items([
  entry({ id: "1", at: at(2026, 10, 15, 8), slug: "scan" }),
  entry({ id: "2", at: at(2026, 10, 15, 2), slug: "triage", kind: "run", status: "failed" }),
  entry({ id: "3", at: at(2026, 10, 15, 9), slug: "triage", kind: "run", status: "waiting" }),
])
const CREWS = [
  { id: "research", name: "Research" },
  { id: "ops", name: "Ops" },
]

function Harness({ initial = EMPTY_CALENDAR_FILTERS }: { initial?: CalendarFilters }) {
  const [filters, setFilters] = useState(initial)
  return (
    <>
      <CalendarPanel items={list} filters={filters} crews={CREWS} onChange={(p) => setFilters((f) => ({ ...f, ...p }))} />
      <output data-testid="filters">{JSON.stringify(filters)}</output>
    </>
  )
}
const state = () => JSON.parse(screen.getByTestId("filters").textContent!) as CalendarFilters

describe("calendar-panel", () => {
  it("counts per scope and crew; the Status facet narrows, none checked = all", () => {
    render(<Harness />)
    expect(screen.getByRole("button", { name: /^Mine/ })).toHaveTextContent("1")
    expect(screen.getByRole("button", { name: /^Workspace/ })).toHaveTextContent("3")
    // Crew counts follow the scope: in Mine only the viewer's routine counts.
    expect(screen.getByRole("button", { name: /^Ops/ })).toHaveTextContent("0")
    fireEvent.click(screen.getByRole("button", { name: /^Workspace/ }))
    expect(state().scope).toBe("workspace")
    expect(screen.getByRole("button", { name: /^Ops/ })).toHaveTextContent("2")

    fireEvent.click(screen.getByRole("button", { name: "Filter" }))
    fireEvent.click(screen.getByRole("button", { name: "Status" }))
    const status = screen.getByRole("group", { name: "Status" })
    for (const b of within(status).getAllByRole("button")) expect(b).toHaveAttribute("aria-pressed", "false")
    fireEvent.click(within(status).getByRole("button", { name: /At risk/ }))
    fireEvent.click(within(status).getByRole("button", { name: /Waiting for someone/ }))
    expect(state().statuses).toEqual(["risk", "waiting"])
    // The block stays open and neither pick zeroed the other's count.
    expect(within(status).getByRole("button", { name: /Planned/ })).toHaveTextContent("1")
  })

  // CAL-03 (#3495, PRD PNL-02 / COM-01): Filter opens the facets INSIDE the
  // panel, under the toolbar — the kit facet block — never a floating popover.
  it("Filter opens the facet block inside the panel, not a popover", () => {
    const { container } = render(<Harness />)
    const filter = screen.getByRole("button", { name: "Filter" })
    expect(filter).toHaveAttribute("aria-expanded", "false")
    fireEvent.click(filter)
    expect(filter).toHaveAttribute("aria-expanded", "true")
    const block = screen.getByRole("group", { name: "Filter calendar" })
    expect(block).toHaveAttribute("data-slot", "facet-block")
    // Rendered in the panel's own tree, not portalled to the body.
    expect(container.contains(block)).toBe(true)
    expect(document.querySelector("[data-radix-popper-content-wrapper]")).toBeNull()
    expect(screen.queryByRole("dialog")).toBeNull()
    // Picking a status names it in the facet header and offers its clear.
    fireEvent.click(screen.getByRole("button", { name: "Status" }))
    fireEvent.click(within(block).getByRole("button", { name: /At risk/ }))
    expect(within(block).getByRole("button", { name: "Clear Status" })).toBeInTheDocument()
    fireEvent.click(within(block).getByRole("button", { name: "Clear all filters" }))
    expect(state().statuses).toEqual([])
    // Escape closes the block and returns focus to Filter.
    fireEvent.keyDown(within(block).getByRole("button", { name: "Status" }), { key: "Escape" })
    expect(screen.queryByRole("group", { name: "Filter calendar" })).toBeNull()
    expect(document.activeElement).toBe(filter)
  })

  it("shows active filters as chips whose × names what it removes", () => {
    render(<Harness initial={{ ...EMPTY_CALENDAR_FILTERS, scope: "workspace", crewId: "ops", statuses: ["risk"] }} />)
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Ops" }))
    expect(state().crewId).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Remove filter At risk" }))
    expect(state().statuses).toEqual([])
  })

  it("picking a crew filters by it, picking it again clears it", () => {
    render(<Harness initial={{ ...EMPTY_CALENDAR_FILTERS, scope: "workspace" }} />)
    fireEvent.click(screen.getByRole("button", { name: /^Research/ }))
    expect(state().crewId).toBe("research")
    fireEvent.click(screen.getByRole("button", { name: /^Research/ }))
    expect(state().crewId).toBeNull()
  })
})
