import { useState } from "react"
import { fireEvent, render, screen } from "@testing-library/react"
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
    expect(screen.getByRole("button", { name: "Any status" })).toHaveAttribute("aria-pressed", "true")
    fireEvent.click(screen.getByRole("button", { name: /At risk/ }))
    fireEvent.click(screen.getByRole("button", { name: /Waiting for someone/ }))
    expect(state().statuses).toEqual(["risk", "waiting"])
    // The panel stays open and neither pick zeroed the other's count.
    expect(screen.getByRole("button", { name: /Planned/ })).toHaveTextContent("1")
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
