import { describe, it, expect, vi } from "vitest"
import { render, screen, within, fireEvent } from "@testing-library/react"

import { RoutinesHealthCard } from "../routines-health-card"
import { RoutineCheckupCard } from "../routine-checkup-card"
import { RoutinesDashboard } from "../routines-dashboard"
import type { Pipeline } from "@/hooks/use-pipelines"

vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("@/components/ui/crew-icon", () => ({ CrewIcon: () => <span /> }))
vi.mock("@/components/features/dashboard/run-volume-chart", () => ({ RunVolumeChart: () => <div /> }))

const T = Date.UTC(2026, 9, 17, 12)
const run = (i: number, over: Record<string, unknown> = {}) => ({
  id: `r${i}`,
  pipeline_slug: "weekly",
  status: "completed",
  started_at: new Date(T - i * 3_600_000).toISOString(),
  ...over,
})
const routines = [
  { id: "1", slug: "weekly", name: "Weekly scan", status: "active" },
  { id: "2", slug: "invoice", name: "Invoice intake", status: "proposed" },
] as unknown as Pipeline[]
const spend = { month: "2026-10", routines: [{ slug: "weekly", monthly_budget_usd: 0, spent_usd: 12.5 }], total_budget_usd: 0, total_spent_usd: 12.5 }

describe("<RoutinesHealthCard> (RT-07)", () => {
  it("lists each routine with its last runs, success over finished runs and spend this month", () => {
    const runs = [run(0), run(1), run(2, { status: "failed" }), run(3)]
    render(<RoutinesHealthCard routines={routines} runs={runs} spend={spend} onSelect={vi.fn()} />)
    const row = screen.getByTestId("health-row-weekly")
    expect(within(row).getByText("Weekly scan")).toBeInTheDocument()
    expect(row).toHaveTextContent("75%")
    expect(row).toHaveTextContent("3 of 4")
    expect(within(row).getAllByTestId("health-bar")).toHaveLength(4)
    expect(row).toHaveTextContent("$12.50 this month")
  })

  it("says this month, never 30 days, and shows no spend without the summary", () => {
    render(<RoutinesHealthCard routines={routines} runs={[run(0)]} spend={null} onSelect={vi.fn()} />)
    expect(screen.getByTestId("routines-health")).not.toHaveTextContent(/30 ?d/i)
    expect(screen.getByTestId("health-row-weekly")).not.toHaveTextContent("this month")
  })

  it("puts routines to fix first with the cross-routine summary, and opens a routine on click", () => {
    const onSelect = vi.fn()
    render(<RoutinesHealthCard routines={routines} runs={[run(0)]} spend={spend} onSelect={onSelect} />)
    const rows = screen.getAllByTestId(/^health-row-/)
    expect(rows[0]).toHaveAttribute("data-testid", "health-row-invoice")
    expect(screen.getByTestId("health-summary")).toHaveTextContent("1 routine to fix")
    fireEvent.click(within(rows[0]).getByRole("button", { name: /Invoice intake/ }))
    expect(onSelect).toHaveBeenCalledWith("invoice")
  })

  it("is part of the Routines overview pane", () => {
    render(<RoutinesDashboard routines={routines} runs={[run(0)]} spend={spend} schedules={[]} onSelect={vi.fn()} />)
    expect(screen.getByTestId("routines-health")).toBeInTheDocument()
  })
})

describe("<RoutineCheckupCard> (RT-06)", () => {
  const base = { slug: "weekly", name: "Weekly", status: "active" as const, draft: { revision: 2 } }
  it("shows n/m, the failing checks first with a fix, and the passed ones folded", () => {
    const onTab = vi.fn()
    render(<RoutineCheckupCard routine={base} runs={[run(0, { status: "failed" }), run(1, { status: "failed" })]} onTab={onTab} />)
    expect(screen.getByTestId("checkup-circle")).toHaveTextContent(/^\d+\/\d+$/)
    expect(screen.getByText("2 failures in a row")).toBeInTheDocument()
    expect(screen.getByText("Changes are not live")).toBeInTheDocument()
    expect(screen.queryByText("Can run")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: /passed/ }))
    expect(screen.getByText("Can run")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Open versions" }))
    expect(onTab).toHaveBeenCalledWith("versions")
  })

  it("does not invent checks it cannot verify", () => {
    render(<RoutineCheckupCard routine={{ slug: "w", status: "active" }} runs={[run(0)]} onTab={vi.fn()} />)
    expect(screen.queryByText(/Agents exist/)).toBeNull()
    expect(screen.queryByText(/turns itself off/i)).toBeNull()
    expect(screen.getByTestId("checkup")).toHaveTextContent("Everything looks right")
  })
})
