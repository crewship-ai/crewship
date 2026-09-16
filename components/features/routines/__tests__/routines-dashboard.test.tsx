// The overview pane is built from /dashboard's own pieces — the attention
// strip, the outcome KPI tiles, the run-volume chart, Up next — so the two
// pages read as one. The tests pin the numbers and the wiring; the pieces
// themselves are covered where they live.

import { afterEach, beforeEach, describe, it, expect, vi } from "vitest"
import { render, screen, fireEvent } from "@testing-library/react"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"
import { RoutinesDashboard, outcomeKpis, runOutcomesByDay, type DashboardRun } from "../routines-dashboard"

vi.mock("next/link", () => ({ default: ({ children, href }: { children: React.ReactNode; href: string }) => <a href={href}>{children}</a> }))
vi.mock("@/components/ui/crew-icon", () => ({ CrewIcon: () => <span data-testid="icon" /> }))
// recharts measures a container jsdom does not have; the chart's own tests live with it.
vi.mock("@/components/features/dashboard/run-volume-chart", () => ({
  RunVolumeChart: ({ series, buckets }: { series: { label: string }[]; buckets: unknown[] }) => (
    <div data-testid="run-volume" data-series={series.map((s) => s.label).join("|")} data-buckets={buckets.length} />
  ),
}))

const NOW = new Date(2026, 8, 16, 12).getTime()
beforeEach(() => { vi.useFakeTimers({ toFake: ["Date"] }); vi.setSystemTime(NOW) })
afterEach(() => vi.useRealTimers())
const hoursAgo = (h: number) => new Date(NOW - h * 3_600_000).toISOString()
const routines = [
  { id: "1", slug: "invoice", name: "Invoice intake", head_version: 3, invocation_count: 9, draft: { id: "d", revision: 2, updated_at: hoursAgo(1) } },
  { id: "2", slug: "briefing", name: "Morning briefing", head_version: 5, invocation_count: 4, last_invocation_status: "failed", last_invoked_at: hoursAgo(3) },
] as Pipeline[]
const runs: DashboardRun[] = [
  { id: "r1", pipeline_slug: "invoice", pipeline_name: "Invoice intake", status: "completed", started_at: hoursAgo(2), duration_ms: 60_000, cost_usd: 0.04 },
  { id: "r2", pipeline_slug: "invoice", pipeline_name: "Invoice intake", status: "completed", outcome: "FAILED", started_at: hoursAgo(30), duration_ms: 120_000, cost_usd: 0.05 },
  { id: "r3", pipeline_slug: "briefing", pipeline_name: "Morning briefing", status: "cancelled", started_at: hoursAgo(50), duration_ms: 10_000 },
  { id: "r4", pipeline_slug: "briefing", pipeline_name: "Morning briefing", status: "waiting", started_at: hoursAgo(0.1) },
  { id: "r5", pipeline_slug: "briefing", pipeline_name: "Morning briefing", status: "running", started_at: hoursAgo(0.05), current_step_id: "draft_text" },
  { id: "old", pipeline_slug: "briefing", pipeline_name: "Morning briefing", status: "completed", started_at: hoursAgo(24 * 9), duration_ms: 999_000, cost_usd: 9 },
  { id: "other", pipeline_slug: "not-mine", pipeline_name: "Elsewhere", status: "failed", started_at: hoursAgo(1) },
]
const schedules = [
  { id: "s1", name: "x", enabled: true, cron_expr: "0 8 * * 1-5", timezone: "Europe/Prague", target_pipeline_slug: "invoice", next_run_at: new Date(NOW + 3_600_000).toISOString() },
  { id: "s2", name: "y", enabled: false, cron_expr: "0 9 * * *", timezone: "UTC", target_pipeline_slug: "briefing", next_run_at: new Date(NOW + 7_200_000).toISOString() },
] as unknown as PipelineSchedule[]
const mine = runs.filter((r) => r.pipeline_slug !== "not-mine")

describe("outcomeKpis", () => {
  it("counts the window by effective result, in the shape the dashboard tiles take", () => {
    const k = outcomeKpis(mine)
    // 5 runs in the window (the 9-day-old one is out); finished = completed + failed-result = 2.
    expect(k).toMatchObject({ total: 5, completed: 1, successOk: 1, successTotal: 2, successPct: 50 })
    expect(k.spendUsd).toBeCloseTo(0.09)
    expect(k.p95Ms).toBe(120_000)
  })
})

describe("runOutcomesByDay", () => {
  it("buckets the window by day with one series per outcome that occurred", () => {
    const v = runOutcomesByDay(mine)
    expect(v.buckets).toHaveLength(7)
    // Today: r1 completed, r4 waiting + r5 running (still going).
    const today = v.buckets[6]
    expect(today).toMatchObject({ completed: 1, live: 2, failed: 0, stopped: 0 })
    // Yesterday-ish: the completed-with-failed-result run counts as could not finish.
    expect(v.buckets.reduce((n, b) => n + Number(b.failed), 0)).toBe(1)
    expect(v.buckets.reduce((n, b) => n + Number(b.stopped), 0)).toBe(1)
    expect(v.series.map((s) => s.label)).toEqual(["Completed", "Could not finish", "Stopped", "Still going"])
  })
})

describe("<RoutinesDashboard>", () => {
  it("is built from the dashboard's strip, tiles, chart and Up next", () => {
    const select = vi.fn()
    render(<RoutinesDashboard routines={routines} runs={runs} schedules={schedules} onSelect={select} />)
    // The attention strip, with routine-shaped items and their verbs.
    expect(screen.getByText("Needs your attention")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /1 decision waiting.*Review/ })).toHaveAttribute("href", "/routines?slug=briefing&run=r4")
    expect(screen.getByRole("link", { name: /1 could not finish.*Inspect/ })).toHaveAttribute("href", "/routines?slug=briefing")
    expect(screen.getByRole("link", { name: /Next start.*Review/ })).toHaveAttribute("href", "/routines?slug=invoice&view=plan")
    // A fourth item is named under the strip, as on /dashboard.
    expect(screen.getByText("1 more:")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "1 draft to publish" })).toHaveAttribute("href", "/routines?slug=invoice&view=versions")
    // The outcome tiles.
    expect(screen.getByText("Routine run summary")).toBeInTheDocument()
    expect(screen.getAllByText("Completed").length).toBeGreaterThan(0)
    expect(screen.getByText("50%")).toBeInTheDocument()
    expect(screen.getByText("P95 duration")).toBeInTheDocument()
    expect(screen.getByText("$0.09")).toBeInTheDocument()
    // The run-volume chart by routine, and the running-now card.
    expect(screen.getByTestId("run-volume")).toHaveAttribute("data-buckets", "7")
    const runningRow = screen.getByText(/draft text/).closest("a")!
    expect(runningRow).toHaveAttribute("href", "/routines?slug=briefing&run=r5")
    expect(runningRow).toHaveTextContent("Morning briefing")
    expect(runningRow).toHaveTextContent("View")
    expect(screen.getByText("1 running")).toBeInTheDocument()
    expect(screen.queryByText("Elsewhere")).toBeNull()
    // Up next lists the enabled schedule only.
    expect(screen.getByText("Up next")).toBeInTheDocument()
    // Latest results: finished runs of the window, newest first, each opening its run.
    const results = screen.getAllByRole("link", { name: /Open run/ })
    expect(results.map((a) => a.getAttribute("href"))).toEqual([
      "/routines?slug=invoice&run=r1",
      "/routines?slug=invoice&run=r2",
      "/routines?slug=briefing&run=r3",
    ])
    expect(results[1]).toHaveTextContent("Result failed")
    expect(results[2]).toHaveTextContent("Stopped")
    // Drafts open the routine.
    fireEvent.click(screen.getByRole("button", { name: /Invoice intake.*Publish/ }))
    expect(select).toHaveBeenCalledWith("invoice")
    // Runs live in Activity.
    expect(screen.getAllByRole("link", { name: /Activity/ })[0]).toHaveAttribute("href", "/activity?lens=routines")
  })

  it("says so when nothing needs anyone and nothing runs", () => {
    render(<RoutinesDashboard routines={routines.slice(1).map((r) => ({ ...r, last_invocation_status: "completed" }))} runs={[]} schedules={[]} onSelect={vi.fn()} />)
    expect(screen.getByText(/nothing blocking your crews/)).toBeInTheDocument()
    expect(screen.getByText("No routines are running right now.")).toBeInTheDocument()
    expect(screen.getByText("Nothing finished in the last 7 days.")).toBeInTheDocument()
    expect(screen.queryByText("Drafts to publish")).toBeNull()
  })
})
