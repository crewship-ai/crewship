// RT-OV top (routines.md R02, RT-04): the KPI row, Needs you with the
// decision answered in place, Running now with Stop, and Next 24 hours.

import * as React from "react"
import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, fireEvent, within, cleanup, waitFor } from "@testing-library/react"

import { RoutinesOverviewBoard } from "../routines-overview-board"

afterEach(cleanup)

const now = new Date("2026-10-17T08:00:00Z")
const at = (h: number) => new Date(now.getTime() + h * 3_600_000).toISOString()

const routines = [
  { slug: "weekly", name: "Weekly report", status: "active" },
  { slug: "invoice", name: "Invoice intake", status: "proposed" },
  { slug: "pr", name: "Review pull requests", status: "active" },
] as never[]

function props(over: Partial<React.ComponentProps<typeof RoutinesOverviewBoard>> = {}): React.ComponentProps<typeof RoutinesOverviewBoard> {
  return {
    routines,
    activeRuns: [
      { id: "run_w", pipeline_slug: "weekly", pipeline_name: "Weekly report", status: "waiting", started_at: at(-1) },
      { id: "run_p", pipeline_slug: "pr", pipeline_name: "Review pull requests", status: "running", started_at: at(-0.05), current_step_id: "run_tests" },
    ] as never[],
    waitpoints: [{ token: "wp-ref-7781", pipeline_run_id: "run_w", step_id: "approve", kind: "approval", prompt: "Publish the report?", timeout_at: at(23), created_at: at(-1) }],
    schedules: [{ id: "s1", target_pipeline_slug: "weekly", enabled: true, next_run_at: at(2), cron_expr: "0 10 * * *", timezone: "UTC", consecutive_failures: 0, max_consecutive_failures: 5 }] as never[],
    pendingStarts: [],
    kpis: { total: 58, successPct: 93 },
    coveredSince: null,
    spend: { month: "2026-10", total_spent_usd: 32.7, total_budget_usd: 80, routines: [] },
    role: "MANAGER",
    now,
    onDecide: vi.fn(async () => true),
    onStop: vi.fn(async () => true),
    onOpenRun: vi.fn(),
    onSelect: vi.fn(),
    ...over,
  }
}

describe("<RoutinesOverviewBoard>", () => {
  it("shows the four KPIs: Needs you, Running now, Runs · 7 days, Spend this month", () => {
    render(<RoutinesOverviewBoard {...props()} />)
    const kpis = screen.getByRole("list", { name: "Routine figures" })
    const items = within(kpis).getAllByRole("listitem").map((li) => li.textContent)
    expect(items[0]).toMatch(/^Needs you\s*2/)
    expect(items[1]).toMatch(/^Running now\s*1/)
    expect(items[2]).toMatch(/^Runs · 7 days\s*58\s*93 % completed/)
    expect(items[3]).toMatch(/^Spend · October\s*\$32\.70\s*of \$80\.00 budget/)
  })

  it("says the run figure covers only the newest runs when the feed stops inside the window", () => {
    render(<RoutinesOverviewBoard {...props({ coveredSince: at(-30) })} />)
    expect(within(screen.getByRole("list", { name: "Routine figures" })).getAllByRole("listitem")[2]).toHaveTextContent(/since/)
  })

  it("Needs you answers a decision in place for a Manager, never showing its token", async () => {
    const p = props()
    render(<RoutinesOverviewBoard {...p} />)
    const card = screen.getByRole("region", { name: "Needs you" })
    expect(within(card).getByText("Weekly report is waiting for a decision")).toBeInTheDocument()
    expect(within(card).getByText(/Publish the report\?/)).toBeInTheDocument()
    expect(within(card).getByText("Invoice intake is proposed")).toBeInTheDocument()
    expect(document.body.innerHTML).not.toContain("wp-ref-7781")
    fireEvent.click(within(card).getByRole("button", { name: "Approve" }))
    await waitFor(() => expect(p.onDecide).toHaveBeenCalledWith("run_w", true))
    fireEvent.click(within(card).getByRole("button", { name: "Open run" }))
    expect(p.onOpenRun).toHaveBeenCalledWith("weekly", "run_w")
    fireEvent.click(within(card).getByRole("button", { name: "Review" }))
    expect(p.onSelect).toHaveBeenCalledWith("invoice")
  })

  it("a Member only opens the run", () => {
    render(<RoutinesOverviewBoard {...props({ role: "MEMBER" })} />)
    const card = screen.getByRole("region", { name: "Needs you" })
    expect(within(card).queryByRole("button", { name: "Approve" })).not.toBeInTheDocument()
    expect(within(card).getByRole("button", { name: "Open run" })).toBeInTheDocument()
  })

  it("Running now shows the live run with Open run, and Stop only for Owner or Admin after asking", async () => {
    const p = props({ role: "ADMIN" })
    render(<RoutinesOverviewBoard {...p} />)
    const card = screen.getByRole("region", { name: "Running now" })
    expect(within(card).getByText("Review pull requests")).toBeInTheDocument()
    expect(within(card).getByText(/run tests/)).toBeInTheDocument()
    fireEvent.click(within(card).getByRole("button", { name: "Stop" }))
    const dialog = await screen.findByRole("alertdialog")
    expect(p.onStop).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole("button", { name: "Stop run" }))
    await waitFor(() => expect(p.onStop).toHaveBeenCalledWith("run_p"))
    cleanup()
    render(<RoutinesOverviewBoard {...props({ role: "MANAGER" })} />)
    expect(within(screen.getByRole("region", { name: "Running now" })).queryByRole("button", { name: "Stop" })).not.toBeInTheDocument()
  })

  // Moved from the dashboard's attention strip and running card: an accepted
  // start that never ran must not leave the page silently, and a start that
  // waits for a free slot is named as such — no ETA, no position.
  it("names accepted starts that did not run and those waiting for a free slot", () => {
    const pendingStarts = [
      { id: "p_failed", pipeline_slug: "weekly", fire_at: at(-5), status: "failed", last_error: "The routine is no longer active." },
      { id: "p_expired", pipeline_slug: "pr", fire_at: at(-6), status: "expired" },
      { id: "p_old", pipeline_slug: "weekly", fire_at: at(-24 * 10), status: "failed" },
      { id: "p_wait", pipeline_slug: "pr", fire_at: at(-0.1), status: "pending", dispatch_attempts: 2 },
    ] as never[]
    render(<RoutinesOverviewBoard {...props({ pendingStarts, activeRuns: [], waitpoints: [] })} />)
    const needs = screen.getByRole("region", { name: "Needs you" })
    expect(within(needs).getByText("Weekly report: 1 accepted start did not run")).toBeInTheDocument()
    expect(within(needs).getByText("Review pull requests: 1 accepted start did not run")).toBeInTheDocument()
    expect(within(screen.getByRole("region", { name: "Running now" })).getByText(/1 start waits for a free slot/)).toBeInTheDocument()
  })

  it("says so when nothing needs anyone and nothing runs", () => {
    render(<RoutinesOverviewBoard {...props({ routines: [], activeRuns: [], waitpoints: [], schedules: [] })} />)
    expect(screen.getByText(/No decision is waiting/)).toBeInTheDocument()
    expect(screen.getByText("Nothing is running right now.")).toBeInTheDocument()
    expect(screen.getByText("Nothing is planned for the next 24 hours.")).toBeInTheDocument()
  })

  it("Next 24 hours lists the starts due within a day, with a way to the calendar", () => {
    render(<RoutinesOverviewBoard {...props({ pendingStarts: [{ id: "ps", pipeline_slug: "pr", fire_at: at(5), status: "pending" }] as never[] })} />)
    const card = screen.getByRole("region", { name: "Next 24 hours" })
    const rows = within(card).getAllByRole("listitem")
    expect(rows.map((r) => r.textContent)).toEqual([expect.stringMatching(/Weekly report.*Schedule/), expect.stringMatching(/Review pull requests.*One-time/)])
    expect(within(card).getByRole("link", { name: "Open calendar" })).toHaveAttribute("href", "/routines?tab=calendar")
  })
})
