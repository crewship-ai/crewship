// The overview pane's figures: the week strip, the seven bars, recent runs,
// what is coming up, drafts to publish. Numbers come from the run list the
// page already loads — the test pins how they are counted.

import { describe, it, expect, vi } from "vitest"
import { render, screen, within, fireEvent } from "@testing-library/react"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { RoutinesDashboard, weekFigures, type DashboardRun } from "../routines-dashboard"

vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("@/components/ui/crew-icon", () => ({ CrewIcon: () => <span data-testid="icon" /> }))

const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()
const routines = [
  { id: "1", slug: "invoice", name: "Invoice intake", head_version: 3, invocation_count: 9, draft: { id: "d", revision: 2, updated_at: hoursAgo(1) } },
  { id: "2", slug: "briefing", name: "Morning briefing", head_version: 5, invocation_count: 4 },
] as Pipeline[]
const runs: DashboardRun[] = [
  { id: "r1", pipeline_slug: "invoice", pipeline_name: "Invoice intake", status: "completed", started_at: hoursAgo(2), duration_ms: 60_000, cost_usd: 0.04 },
  { id: "r2", pipeline_slug: "invoice", pipeline_name: "Invoice intake", status: "completed", outcome: "FAILED", started_at: hoursAgo(30), duration_ms: 120_000, cost_usd: 0.05 },
  { id: "r3", pipeline_slug: "briefing", pipeline_name: "Morning briefing", status: "cancelled", started_at: hoursAgo(50), duration_ms: 10_000 },
  { id: "r4", pipeline_slug: "briefing", pipeline_name: "Morning briefing", status: "waiting", started_at: hoursAgo(0.1) },
  { id: "old", pipeline_slug: "briefing", pipeline_name: "Morning briefing", status: "completed", started_at: hoursAgo(24 * 9), duration_ms: 999_000, cost_usd: 9 },
  { id: "other", pipeline_slug: "not-mine", pipeline_name: "Elsewhere", status: "failed", started_at: hoursAgo(1) },
]
const schedules = [
  { id: "s1", name: "x", enabled: true, cron_expr: "0 8 * * 1-5", timezone: "Europe/Prague", target_pipeline_slug: "invoice", next_run_at: new Date(Date.now() + 3_600_000).toISOString() },
  { id: "s2", name: "y", enabled: false, cron_expr: "0 9 * * *", timezone: "UTC", target_pipeline_slug: "briefing", next_run_at: new Date(Date.now() + 7_200_000).toISOString() },
] as unknown as PipelineSchedule[]

describe("weekFigures", () => {
  it("counts the last seven days by effective result and ignores older runs", () => {
    const f = weekFigures(runs.filter((r) => r.pipeline_slug !== "not-mine"))
    expect(f).toMatchObject({ total: 4, completed: 1, failed: 1, stopped: 1, waiting: 1, running: 0 })
    // r2 completed with a failed result counts as could-not-finish, not completed.
    expect(f.cost).toBeCloseTo(0.09)
    // Median of 60 s, 120 s, 10 s → 60 s; the waiting run has no duration yet.
    expect(f.median).toBe(60_000)
  })
})

describe("<RoutinesDashboard>", () => {
  it("draws the week strip, the bars, recent runs, coming up and drafts", () => {
    const select = vi.fn()
    const showRuns = vi.fn()
    render(<RoutinesDashboard routines={routines} runs={runs} schedules={schedules} onSelect={select} onShowRuns={showRuns} />)
    const strip = screen.getByText(/Last 7 days/).parentElement!
    expect(strip).toHaveTextContent("4 runs")
    expect(strip).toHaveTextContent("1 completed")
    expect(strip).toHaveTextContent("1 could not finish")
    expect(strip).toHaveTextContent("1 stopped")
    expect(strip).toHaveTextContent("1 waiting for a person now")
    expect(strip).toHaveTextContent("$0.09 spent")
    // The bars name every colour in a legend, and the busiest day sets the scale.
    expect(screen.getByRole("img", { name: /Runs per day/ })).toBeInTheDocument()
    expect(screen.getByText("still going")).toBeInTheDocument()
    // Recent runs: live first, then newest; runs of routines not on the page are absent.
    const recent = within(screen.getByText("Recent runs").closest("section, div")!.parentElement!)
    const links = recent.getAllByRole("link")
    expect(links[0]).toHaveTextContent("Morning briefing")
    expect(links[0]).toHaveTextContent("Waiting")
    expect(recent.queryByText("Elsewhere")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "4 runs" }))
    expect(showRuns).toHaveBeenCalled()
    // Coming up lists only enabled schedules of routines on the page.
    const coming = within(screen.getByText("Coming up").closest("section, div")!.parentElement!)
    expect(coming.getAllByRole("link")).toHaveLength(1)
    expect(coming.getByRole("link")).toHaveAttribute("href", "/routines?slug=invoice&view=plan")
    // Drafts to publish opens the routine.
    fireEvent.click(screen.getByRole("button", { name: /Invoice intake/ }))
    expect(select).toHaveBeenCalledWith("invoice")
  })

  it("says so when nothing ran and nothing is planned", () => {
    render(<RoutinesDashboard routines={routines.slice(1)} runs={[]} schedules={[]} onSelect={vi.fn()} onShowRuns={vi.fn()} />)
    expect(screen.getByText("No runs yet.")).toBeInTheDocument()
    expect(screen.getByText(/No schedule is on/)).toBeInTheDocument()
    expect(screen.queryByText("Drafts to publish")).toBeNull()
  })
})
