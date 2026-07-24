import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { RoutineBudgetCard } from "../routine-budget-card"
import type { RoutineBudget } from "@/hooks/use-routine-budget"

const h = vi.hoisted(() => ({
  budget: null as RoutineBudget | null,
  loading: false,
  setBudget: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

vi.mock("@/hooks/use-routine-budget", () => ({
  useRoutineBudget: () => ({
    budget: h.budget,
    loading: h.loading,
    error: null,
    refresh: vi.fn(),
    setBudget: h.setBudget,
  }),
}))

beforeEach(() => {
  h.budget = null
  h.loading = false
  h.setBudget.mockReset()
})

describe("RoutineBudgetCard", () => {
  it("renders nothing while loading with no prior data", () => {
    h.loading = true
    const { container } = render(<RoutineBudgetCard workspaceId="ws1" slug="my-routine" />)
    expect(container).toBeEmptyDOMElement()
  })

  it("shows a quiet nudge when no budget is set", () => {
    h.budget = {
      slug: "my-routine", has_budget: false, monthly_budget_usd: 0,
      month: "2026-07", spent_usd: 3.4,
    }
    render(<RoutineBudgetCard workspaceId="ws1" slug="my-routine" />)
    expect(screen.getByText(/No budget set/)).toBeInTheDocument()
    expect(screen.getByText(/\$3\.40/)).toBeInTheDocument()
  })

  it("shows the meter with percentage when a budget is set", () => {
    h.budget = {
      slug: "my-routine", has_budget: true, monthly_budget_usd: 50,
      month: "2026-07", spent_usd: 12.5, pct_used: 25, over_budget: false,
    }
    render(<RoutineBudgetCard workspaceId="ws1" slug="my-routine" />)
    expect(screen.getByText("$12.50 of $50.00")).toBeInTheDocument()
    expect(screen.getByText("25%")).toBeInTheDocument()
  })

  it("flags over-budget distinctly", () => {
    h.budget = {
      slug: "my-routine", has_budget: true, monthly_budget_usd: 20,
      month: "2026-07", spent_usd: 22, pct_used: 110, over_budget: true,
    }
    render(<RoutineBudgetCard workspaceId="ws1" slug="my-routine" />)
    expect(screen.getByText("110% over")).toBeInTheDocument()
  })

  it("lets the operator set a budget inline", async () => {
    h.budget = {
      slug: "my-routine", has_budget: false, monthly_budget_usd: 0,
      month: "2026-07", spent_usd: 0,
    }
    h.setBudget.mockResolvedValue({
      slug: "my-routine", has_budget: true, monthly_budget_usd: 30,
      month: "2026-07", spent_usd: 0, pct_used: 0,
    })
    render(<RoutineBudgetCard workspaceId="ws1" slug="my-routine" />)

    fireEvent.click(screen.getByText("Set one"))
    const input = screen.getByPlaceholderText("0 = no cap")
    fireEvent.change(input, { target: { value: "30" } })
    fireEvent.click(screen.getByText("Save"))

    await waitFor(() => expect(h.setBudget).toHaveBeenCalledWith(30))
    await waitFor(() => expect(toast.success).toHaveBeenCalled())
  })

  it("rejects a negative amount client-side without calling setBudget", async () => {
    h.budget = {
      slug: "my-routine", has_budget: false, monthly_budget_usd: 0,
      month: "2026-07", spent_usd: 0,
    }
    render(<RoutineBudgetCard workspaceId="ws1" slug="my-routine" />)
    fireEvent.click(screen.getByText("Set one"))
    fireEvent.change(screen.getByPlaceholderText("0 = no cap"), { target: { value: "-5" } })
    fireEvent.click(screen.getByText("Save"))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    expect(h.setBudget).not.toHaveBeenCalled()
  })
})
