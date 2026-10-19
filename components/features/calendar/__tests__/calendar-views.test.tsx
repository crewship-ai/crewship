import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { CalendarAgenda, CalendarMonth, CalendarWeek } from "../calendar-views"
import { at, entry, items } from "./fixtures"

const NOW = new Date(2026, 9, 15, 8, 16) // Thu 15 Oct 2026, 08:16
const MONDAY = new Date(2026, 9, 12)

describe("calendar-week", () => {
  const week = items([
    entry({ id: "a", at: at(2026, 10, 15, 8), slug: "scan" }),
    entry({ id: "b", at: at(2026, 10, 15, 2), slug: "triage", kind: "run", status: "failed" }),
    entry({ id: "c", at: at(2026, 10, 16, 14), slug: "scan", kind: "pending" }),
    ...Array.from({ length: 5 }, (_, i) => entry({ id: `d${i}`, at: at(2026, 10, 17, 9, 0), slug: "triage" })),
    entry({ id: "h", at: at(2026, 10, 14, 7, 30), slug: "scan" }),
  ])

  it("has a row for each time that has an item (07:30, not 07:00), caps a cell and plans from an empty slot", () => {
    const onOpenDay = vi.fn()
    const onPlan = vi.fn()
    render(<CalendarWeek items={week} from={MONDAY} selectedId={null} onPick={vi.fn()} onOpenDay={onOpenDay} onPlan={onPlan} now={NOW} />)
    const grid = screen.getByRole("grid", { name: "Week" })
    expect([...grid.querySelectorAll("[data-hour-label]")].map((n) => n.textContent)).toEqual(["02:00", "07:30", "08:00", "09:00", "14:00"])
    // No date-only items yet: no All day row.
    expect(within(grid).queryByText("All day")).toBeNull()
    const busy = screen.getByTestId("week-cell-2026-10-17-09:00")
    expect(within(busy).getAllByRole("button", { pressed: false })).toHaveLength(3)
    fireEvent.click(within(busy).getByRole("button", { name: "+2" }))
    expect(onOpenDay).toHaveBeenCalledWith("2026-10-17")
    fireEvent.click(screen.getByRole("button", { name: "Schedule a start on Fri 16 Oct at 14:00" }))
    expect(onPlan).toHaveBeenCalledWith(new Date(2026, 9, 16, 14, 0))
    fireEvent.click(screen.getByRole("button", { name: "Schedule a start on Wed 14 Oct at 07:30" }))
    expect(onPlan).toHaveBeenCalledWith(new Date(2026, 9, 14, 7, 30))
  })

  it("draws the now line only in the current week", () => {
    const { rerender } = render(<CalendarWeek items={week} from={MONDAY} selectedId={null} onPick={vi.fn()} onOpenDay={vi.fn()} now={NOW} />)
    expect(screen.getByTestId("week-now")).toHaveTextContent("08:16")
    rerender(<CalendarWeek items={week} from={new Date(2026, 9, 19)} selectedId={null} onPick={vi.fn()} onOpenDay={vi.fn()} now={NOW} />)
    expect(screen.queryByTestId("week-now")).toBeNull()
  })

  it("names an item's state with an icon label, not colour alone, and picks it", () => {
    const onPick = vi.fn()
    render(<CalendarWeek items={week} from={MONDAY} selectedId={null} onPick={onPick} onOpenDay={vi.fn()} now={NOW} />)
    const failed = screen.getByTestId("week-cell-2026-10-15-02:00")
    expect(within(failed).getByRole("img", { name: "Could not finish" })).toBeInTheDocument()
    fireEvent.click(within(failed).getByRole("button", { name: /Nightly triage/ }))
    expect(onPick).toHaveBeenCalledWith("run:b")
  })
})

describe("calendar-agenda", () => {
  const list = items([
    entry({ id: "a", at: at(2026, 10, 15, 8), slug: "scan" }),
    entry({ id: "b", at: at(2026, 10, 15, 7), slug: "triage", kind: "run", status: "completed" }),
    entry({ id: "c", at: at(2026, 10, 20, 8), slug: "triage" }),
    ...Array.from({ length: 4 }, (_, i) => entry({ id: `e${i}`, at: at(2026, 10, 16, 9 + i), slug: "triage" })),
  ])

  it("groups by day, sorts by time, marks Mine only in Workspace scope", () => {
    const { rerender } = render(<CalendarAgenda items={list} scope="workspace" selectedId={null} onPick={vi.fn()} now={NOW} />)
    const sections = screen.getAllByRole("region")
    expect(sections.map((s) => s.getAttribute("aria-label"))).toEqual(["Today", "Tomorrow", "Tue 20 Oct"])
    const today = within(sections[0]).getAllByRole("button")
    expect(today.map((b) => b.textContent)).toEqual([expect.stringMatching(/^07:00.*Nightly triage/), expect.stringMatching(/^08:00.*Weekly scan.*Mine$/)])
    // Four planned starts of one routine in a day fold into one row.
    expect(within(sections[1]).getByRole("button")).toHaveTextContent("×4")
    expect(within(sections[1]).getByRole("button")).toHaveTextContent("09:00–12:00")
    rerender(<CalendarAgenda items={list.filter((i) => i.mine)} scope="mine" selectedId={null} onPick={vi.fn()} now={NOW} />)
    expect(screen.queryByText("Mine")).toBeNull()
  })
})

describe("calendar-month", () => {
  it("caps 3 items per day with +N more and a risk dot", () => {
    const onOpenDay = vi.fn()
    const list = items([
      ...Array.from({ length: 5 }, (_, i) => entry({ id: `p${i}`, at: at(2026, 10, 15, 10 + i), slug: "scan" })),
      entry({ id: "f", at: at(2026, 10, 15, 7), slug: "triage", kind: "run", status: "failed" }),
      entry({ id: "n", at: at(2026, 10, 2, 7), slug: "triage" }),
    ])
    render(<CalendarMonth items={list} anchor={new Date(2026, 9, 1)} selectedId={null} onPick={vi.fn()} onOpenDay={onOpenDay} now={NOW} />)
    expect(screen.getAllByRole("gridcell")).toHaveLength(35)
    const day = screen.getByTestId("month-day-2026-10-15")
    expect(within(day).getAllByRole("button", { pressed: false })).toHaveLength(3)
    expect(within(day).getByRole("img", { name: "Something could not finish" })).toBeInTheDocument()
    fireEvent.click(within(day).getByRole("button", { name: "+3 more" }))
    expect(onOpenDay).toHaveBeenCalledWith("2026-10-15")
    expect(within(screen.getByTestId("month-day-2026-10-02")).queryByRole("img", { name: "Something could not finish" })).toBeNull()
  })
})
