import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutinesWorkspace, routineLastState } from "../routines-workspace"

const h = vi.hoisted(() => ({
  runs: [] as { id: string; pipeline_slug: string; pipeline_name: string; status: string }[],
  schedules: [] as {
    id: string
    enabled: boolean
    target_pipeline_slug: string
    cron_expr: string
    timezone: string
    next_run_at?: string
  }[],
}))

vi.mock("@/hooks/use-issue-detail", () => ({ useUrlSelection: () => [null, vi.fn()] }))
vi.mock("@/hooks/use-active-routine-runs", () => ({
  useActiveRoutineRuns: () => ({
    runs: h.runs,
    bySlug: new Map(h.runs.map((r) => [r.pipeline_slug, r])),
  }),
  isAwaitingApproval: (s: string) => s === "waiting" || s === "paused",
}))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({ schedules: h.schedules, loading: false, error: null }),
}))
vi.mock("@/hooks/use-pipeline-runs", () => ({ usePipelineRuns: vi.fn() }))
vi.mock("../routine-calendar", () => ({ RoutineCalendar: () => <div>Calendar</div> }))

const rows = [
  {
    id: "one",
    slug: "report",
    name: "Weekly report",
    description: "Summarize service health",
    step_count: 4,
    invocation_count: 8,
    last_invocation_status: "completed",
    last_invoked_at: new Date(Date.now() - 3600_000).toISOString(),
  },
  {
    id: "two",
    slug: "other",
    name: "Other routine",
    description: "File a receipt",
    step_count: 1,
    invocation_count: 0,
  },
  {
    id: "three",
    slug: "broken",
    name: "Broken routine",
    description: "Fails on purpose",
    step_count: 2,
    invocation_count: 3,
    last_invocation_status: "failed",
    last_invoked_at: new Date(Date.now() - 7200_000).toISOString(),
  },
] as Pipeline[]

const filters = { status: "all" as const, invocations: "all" as const, authorAgentId: null, showEphemeral: false }

describe("<RoutinesWorkspace> — the one list", () => {
  it("shows purpose, step count and how it runs on the row, and selects the recipe", () => {
    h.runs = []
    h.schedules = [
      {
        id: "s1",
        enabled: true,
        target_pipeline_slug: "report",
        cron_expr: "30 2 * * *",
        timezone: "Europe/Prague",
        next_run_at: "2026-09-13T00:30:00Z",
      },
    ]
    const select = vi.fn()
    render(
      <RoutinesWorkspace
        workspaceId="ws"
        routines={rows}
        loading={false}
        error={null}
        onSelect={select}
        search="service"
        filters={filters}
        onFilter={vi.fn()}
      />,
    )
    const list = screen.getByRole("region", { name: "Routine list" })
    const row = within(list).getByRole("button", { name: /Weekly report/ })
    expect(within(row).getByText("Summarize service health")).toBeVisible()
    expect(row.textContent).toMatch(/4 steps · .*Europe\/Prague/)
    expect(within(row).getByText("Completed")).toBeVisible()
    expect(within(row).getByText(/Finished ·/)).toBeVisible()
    expect(within(list).queryByText("Other routine")).not.toBeInTheDocument()
    // No second list, no health dashboard, no explorer buckets.
    expect(screen.queryByRole("tab", { name: /Health/ })).not.toBeInTheDocument()
    expect(screen.queryByText(/Awaiting approval/)).not.toBeInTheDocument()
    fireEvent.click(row)
    expect(select).toHaveBeenCalledWith("report")
  })

  it("puts the waiting decision first, as a banner that opens the run", () => {
    h.runs = [{ id: "run_1", pipeline_slug: "report", pipeline_name: "Weekly report", status: "waiting" }]
    h.schedules = []
    render(
      <RoutinesWorkspace
        workspaceId="ws"
        routines={rows}
        loading={false}
        error={null}
        onSelect={vi.fn()}
        filters={filters}
        onFilter={vi.fn()}
      />,
    )
    const banner = screen.getByRole("status")
    expect(banner).toHaveTextContent("1 run is waiting for your decision")
    expect(within(banner).getByRole("link", { name: /Review and decide/ })).toHaveAttribute(
      "href",
      "/routines?slug=report&run=run_1",
    )
    const list = screen.getByRole("region", { name: "Routine list" })
    expect(within(list).getByText("Waiting for you")).toBeVisible()
    expect(within(list).getByText("Needs your decision")).toBeVisible()
  })

  it("counts the routines that failed last time and filters by result", () => {
    h.runs = []
    h.schedules = []
    const onFilter = vi.fn()
    render(
      <RoutinesWorkspace
        workspaceId="ws"
        routines={rows}
        loading={false}
        error={null}
        onSelect={vi.fn()}
        filters={{ ...filters, status: "failed" }}
        onFilter={onFilter}
      />,
    )
    expect(screen.getByText("failed last time").previousSibling).toHaveTextContent("1")
    const list = screen.getByRole("region", { name: "Routine list" })
    expect(within(list).getByText("Broken routine")).toBeVisible()
    expect(within(list).queryByText("Weekly report")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Failed", pressed: true })).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Never run" }))
    expect(onFilter).toHaveBeenCalledWith("never")
    expect(screen.getByRole("textbox", { name: "Search routines" })).toBeInTheDocument()
  })

  it("keeps a door to hidden routines now that the explorer's toggles are gone", () => {
    h.runs = []
    h.schedules = []
    const onToggleHidden = vi.fn()
    render(
      <RoutinesWorkspace
        workspaceId="ws"
        routines={rows}
        loading={false}
        error={null}
        onSelect={vi.fn()}
        filters={filters}
        onFilter={vi.fn()}
        showHidden={false}
        onToggleHidden={onToggleHidden}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Show hidden", pressed: false }))
    expect(onToggleHidden).toHaveBeenCalledWith(true)
  })

  it("says what the empty list means instead of leaving a pane", () => {
    h.runs = []
    h.schedules = []
    render(
      <RoutinesWorkspace workspaceId="ws" routines={[]} loading={false} error={null} onSelect={vi.fn()} />,
    )
    expect(screen.getByText(/No routines yet/)).toBeVisible()
  })
})

describe("routineLastState", () => {
  it("prefers the live run over the stored row, and names outcomes not enums", () => {
    const base = { slug: "x", last_invocation_status: "completed" } as Pipeline
    expect(routineLastState(base, { status: "waiting" })).toEqual({ status: "WAITING", label: "Waiting for you" })
    expect(routineLastState(base, { status: "running" })).toEqual({ status: "RUNNING", label: "Running" })
    expect(routineLastState({ ...base, last_run_outcome: "FAILED" }, null)).toEqual({
      status: "FAILED",
      label: "Result failed",
    })
    expect(routineLastState({ slug: "y" } as Pipeline, null)).toEqual({ status: "PENDING", label: "Never run" })
    expect(routineLastState({ slug: "z", last_invocation_status: "cancelled" } as Pipeline, null)).toEqual({
      status: "CANCELLED",
      label: "Stopped",
    })
  })
})
