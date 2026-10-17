import { describe, it, expect, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { DashboardKpis } from "../dashboard-overview"

afterEach(cleanup)

const EMPTY = { completed: 0, successPct: null, successOk: 0, successTotal: 0, p95Ms: 0, p50Ms: 0 }

// Four tiles of "0 / — / — / $0.00" took a full row on dev3 and said one
// thing: nothing ran. Say that thing once, and keep the tiles for when there
// is something to measure.
describe("DashboardKpis", () => {
  it("collapses to one line when the window has no agent runs", () => {
    render(<DashboardKpis data={EMPTY} window="24h" spendUsd={null} />)
    expect(screen.getByText(/No agent runs in the last 24h/)).toBeTruthy()
    expect(screen.queryByText("P95 duration")).toBeNull()
  })

  it("keeps the four tiles once anything ran", () => {
    render(<DashboardKpis data={{ ...EMPTY, completed: 3, successOk: 3, successTotal: 3, successPct: 100 }} window="24h" spendUsd={null} />)
    expect(screen.getByText("P95 duration")).toBeTruthy()
    expect(screen.queryByText(/No agent runs/)).toBeNull()
  })

  it("keeps the tiles when runs finished but none succeeded", () => {
    render(<DashboardKpis data={{ ...EMPTY, successTotal: 2, successPct: 0 }} window="7d" spendUsd={null} />)
    expect(screen.getByText("Success")).toBeTruthy()
  })

  it("sparkline only where a series exists", () => {
    const ran = { ...EMPTY, completed: 3, successOk: 3, successTotal: 4, successPct: 75, p95Ms: 60_000, p50Ms: 20_000 }
    const { container, rerender } = render(<DashboardKpis data={ran} window="24h" spendUsd={null} />)
    expect(container.querySelectorAll('[data-slot="kpi-spark"]')).toHaveLength(0)
    rerender(<DashboardKpis data={ran} window="24h" spendUsd={null} completedSeries={[1, 0, 2, 4]} completedSeriesLabel="Runs started per hour" />)
    const sparks = container.querySelectorAll('[data-slot="kpi-spark"]')
    expect(sparks).toHaveLength(1)
    expect(sparks[0].closest('[data-slot="kpi"]')?.textContent).toContain("Completed")
    // An all-zero series is no trend.
    rerender(<DashboardKpis data={ran} window="24h" spendUsd={null} completedSeries={[0, 0, 0]} />)
    expect(container.querySelectorAll('[data-slot="kpi-spark"]')).toHaveLength(0)
  })

  it("puts context, not invented change, under each figure", () => {
    render(<DashboardKpis data={{ ...EMPTY, completed: 3, successOk: 3, successTotal: 4, successPct: 75, p95Ms: 60_000, p50Ms: 20_000 }} window="24h" spendUsd={1.2} spendPerRun={0.3} />)
    expect(screen.getByText("1 failed")).toBeTruthy()
    expect(screen.getByText("median 20s")).toBeTruthy()
    expect(screen.getByText("per run $0.30")).toBeTruthy()
    expect(screen.queryByText(/budget/)).toBeNull()
  })

  it("never shows $0.00 for an unmetered or unread ledger", () => {
    const ran = { ...EMPTY, completed: 1, successOk: 1, successTotal: 1, successPct: 100 }
    const { rerender } = render(<DashboardKpis data={ran} window="24h" spendUsd={null} />)
    expect(screen.getByText("not metered")).toBeTruthy()
    rerender(<DashboardKpis data={ran} window="24h" spendUsd={undefined} />)
    expect(screen.getByText("unavailable")).toBeTruthy()
    expect(screen.queryByText("$0.00")).toBeNull()
  })
})
