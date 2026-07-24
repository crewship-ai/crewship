import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import { RoutineBudgetSummaryCard } from "../routine-budget-summary-card"
import type { BudgetSummary } from "@/hooks/use-routine-budget"

const h = vi.hoisted(() => ({
  summary: null as BudgetSummary | null,
  loading: false,
}))

vi.mock("@/hooks/use-routine-budget", () => ({
  useBudgetSummary: () => ({ summary: h.summary, loading: h.loading, error: null, refresh: vi.fn() }),
}))

beforeEach(() => {
  h.summary = null
  h.loading = false
})

describe("RoutineBudgetSummaryCard", () => {
  it("shows an empty state when nothing has a budget or spend", () => {
    h.summary = { month: "2026-07", routines: [], total_budget_usd: 0, total_spent_usd: 0 }
    render(<RoutineBudgetSummaryCard workspaceId="ws1" onSelect={vi.fn()} />)
    expect(screen.getByText(/No routine has a monthly budget/)).toBeInTheDocument()
  })

  it("lists routines with spend/budget and a workspace total", () => {
    h.summary = {
      month: "2026-07",
      routines: [
        { slug: "a", monthly_budget_usd: 100, spent_usd: 40, pct_used: 40, over_budget: false },
        { slug: "b", monthly_budget_usd: 20, spent_usd: 22, pct_used: 110, over_budget: true },
        { slug: "c", monthly_budget_usd: 0, spent_usd: 5 },
      ],
      total_budget_usd: 120,
      total_spent_usd: 67,
    }
    render(<RoutineBudgetSummaryCard workspaceId="ws1" onSelect={vi.fn()} />)
    expect(screen.getByText("a")).toBeInTheDocument()
    expect(screen.getByText("b")).toBeInTheDocument()
    expect(screen.getByText("c")).toBeInTheDocument()
    expect(screen.getByText("110%")).toBeInTheDocument()
    expect(screen.getByText(/\$67\.00/)).toBeInTheDocument()
    expect(screen.getByText(/\$120\.00/)).toBeInTheDocument()
  })

  it("invokes onSelect when a row is clicked", () => {
    h.summary = {
      month: "2026-07",
      routines: [{ slug: "clickable", monthly_budget_usd: 10, spent_usd: 1, pct_used: 10 }],
      total_budget_usd: 10,
      total_spent_usd: 1,
    }
    const onSelect = vi.fn()
    render(<RoutineBudgetSummaryCard workspaceId="ws1" onSelect={onSelect} />)
    fireEvent.click(screen.getByLabelText("Open routine clickable"))
    expect(onSelect).toHaveBeenCalledWith("clickable")
  })
})
