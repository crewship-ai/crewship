import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, expect, it } from "vitest"
import { TopMissionsChart, type TopMissionEntry } from "../top-missions-chart"
afterEach(cleanup)
const mission: TopMissionEntry = { id: "m", identifier: "R-1", title: "Research", crew_id: "c", crew_color: "blue", cost: 10 }
it("explains empty cost data with default and supplied wording", () => {
  const { rerender } = render(<TopMissionsChart missions={[]} />)
  expect(screen.getByText("No missions with cost data yet")).toBeInTheDocument()
  rerender(<TopMissionsChart missions={[]} emptyLabel="No spend this week" />)
  expect(screen.getByText("No spend this week")).toBeInTheDocument()
})
it("normalizes nonzero bars without implying spending for a zero-cost mission", () => {
  render(<TopMissionsChart missions={[
    { ...mission, href: "/missions/m" },
    { ...mission, id: "small", title: "Small", cost: 0.005, identifier: null, crew_color: null },
    { ...mission, id: "zero", title: "Free", cost: 0 },
  ]} />)
  const link = screen.getByRole("link", { name: /Research/ })
  expect(link).toHaveAttribute("href", "/missions/m")
  expect(within(link).getByText("$10.00")).toBeInTheDocument()
  expect(screen.getByText("<$0.01")).toBeInTheDocument()
  expect(screen.getByText("$0.00")).toBeInTheDocument()
  expect(screen.getByText("—")).toBeInTheDocument()
  expect(screen.getAllByRole("progressbar").map(e => e.getAttribute("aria-valuenow"))).toEqual(["100", "4", "0"])
  expect(screen.getAllByRole("link")).toHaveLength(1)
})
it("supports custom values and a whole window of zero-cost runs", () => {
  render(<TopMissionsChart missions={[{ ...mission, cost: 0 }]} format={n => `${n} credits`} />)
  expect(screen.getByText("0 credits")).toBeInTheDocument()
  expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "0")
})
