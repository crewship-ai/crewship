import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import type { Pipeline } from "@/hooks/use-pipelines"
import { RoutinesWorkspace, routineLastState } from "../routines-workspace"

const h = vi.hoisted(() => ({
  runs: [] as {
    id: string
    pipeline_slug: string
    pipeline_name: string
    status: string
    started_at?: string
  }[],
  schedules: [] as {
    id: string
    enabled: boolean
    target_pipeline_slug: string
    cron_expr: string
    timezone: string
    next_run_at?: string
  }[],
  automations: [] as { id: string; enabled: boolean; action: { routine_slug: string } }[],
  recorded: [] as { id: string; pipeline_slug: string; pipeline_name?: string; status: string; started_at: string; duration_ms?: number; cost_usd?: number }[],
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
vi.mock("@/hooks/use-pending-starts", () => ({ usePendingStarts: () => ({ starts: [], loading: false, error: null, refresh: vi.fn() }) }))
vi.mock("@/hooks/use-pipeline-runs", () => ({ usePipelineRuns: () => ({ runs: h.recorded ?? [], loading: false, error: null }) }))
vi.mock("@/hooks/use-automations", () => ({
  useAutomations: () => ({ automations: h.automations, loading: false, error: null }),
}))
vi.mock("../routine-calendar", () => ({ RoutineCalendar: () => <div>Calendar</div> }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "MANAGER" }) }))
vi.mock("@/hooks/use-workspace-waitpoints", () => ({ useWorkspaceWaitpoints: () => ({ waitpoints: [], decide: vi.fn(async () => true), refresh: vi.fn() }) }))
vi.mock("@/hooks/use-routine-health", () => ({ useRoutineHealth: () => null }))
vi.mock("@/hooks/use-routine-budget", () => ({ useBudgetSummary: () => ({ summary: null, loading: false, error: null, refresh: vi.fn() }) }))
vi.mock("@/components/features/dashboard/run-volume-chart", () => ({ RunVolumeChart: () => <div data-testid="run-volume" /> }))

const rows = [
  {
    id: "one",
    slug: "report",
    name: "Weekly report",
    description: "Summarize service health",
    step_count: 4,
    invocation_count: 8,
    head_version: 3,
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
    head_version: 1,
  },
  {
    id: "three",
    slug: "broken",
    name: "Broken routine",
    description: "Fails on purpose",
    step_count: 2,
    invocation_count: 3,
    head_version: 2,
    last_invocation_status: "failed",
    last_invoked_at: new Date(Date.now() - 7200_000).toISOString(),
  },
] as Pipeline[]

const filters = { status: "all" as const, invocations: "all" as const, authorAgentId: null, showEphemeral: false }

function renderPane(props: Partial<React.ComponentProps<typeof RoutinesWorkspace>> = {}) {
  return render(
    <RoutinesWorkspace
      workspaceId="ws"
      routines={rows}
      loading={false}
      error={null}
      onSelect={vi.fn()}
      filters={filters}
      {...props}
    />,
  )
}

describe("<RoutinesWorkspace> — the overview pane", () => {
  beforeEach(() => {
    h.recorded = []
    h.runs = []
    h.schedules = []
    h.automations = []
  })

  it("is the dashboard, not a second copy of the sidebar's list, with runs living in Activity", () => {
    renderPane()
    // PAR-046: v67 has no Overview | Calendar strip; Calendar is a sidebar destination.
    expect(screen.queryByRole("navigation", { name: "Routines views" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Overview" })).toBeNull()
    expect(screen.queryByRole("button", { name: "Calendar" })).toBeNull()
    expect(screen.getByTestId("routines-dashboard")).toBeInTheDocument()
    // No catalog rows and no third tab: the explorer owns the list, Activity the runs.
    expect(screen.queryByText("Summarize service health")).toBeNull()
    expect(screen.queryByRole("button", { name: /Recent runs/ })).toBeNull()
    // The Latest results card links to every run in Activity.
    expect(screen.getByRole("link", { name: /All runs/ })).toHaveAttribute("href", "/activity?lens=routines")
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
  })

  it("feeds the overview board and the dashboard the runs and schedules the page loads, filtered like the sidebar", () => {
    h.runs = [{ id: "run_1", pipeline_slug: "report", pipeline_name: "Weekly report", status: "waiting", started_at: new Date(Date.now() - 4 * 60_000).toISOString() }]
    h.recorded = [
      { id: "run_1", pipeline_slug: "report", pipeline_name: "Weekly report", status: "waiting", started_at: new Date(Date.now() - 4 * 60_000).toISOString() },
      { id: "run_2", pipeline_slug: "broken", pipeline_name: "Broken routine", status: "failed", started_at: new Date(Date.now() - 60_000).toISOString() },
    ]
    h.schedules = [
      { id: "s1", enabled: true, target_pipeline_slug: "report", cron_expr: "30 2 * * *", timezone: "Europe/Prague", next_run_at: new Date(Date.now() + 3_600_000).toISOString() },
    ]
    const onOpenRun = vi.fn()
    renderPane({ onOpenRun })
    const needs = screen.getByRole("region", { name: "Needs you" })
    expect(within(needs).getByText(/is waiting for a decision/)).toBeInTheDocument()
    fireEvent.click(within(needs).getByRole("button", { name: "Open run" }))
    expect(onOpenRun).toHaveBeenCalledWith("report", "run_1")
    expect(within(screen.getByRole("region", { name: "Next 24 hours" })).getAllByRole("listitem")).toHaveLength(1)
    // The explorer's status filter narrows the pane too.
    cleanup()
    renderPane({ filters: { ...filters, status: "failed" } })
    expect(within(screen.getByRole("region", { name: "Needs you" })).queryByText(/is waiting for a decision/)).toBeNull()
    expect(within(screen.getByRole("region", { name: "Needs you" })).getByText(/could not finish/)).toBeInTheDocument()
    expect(screen.getAllByRole("link", { name: /Open run/ })).toHaveLength(1)
  })

  // The run list is the 200 newest rows. A long run that started before them
  // is still running, so it comes from the active feed, and the summary says
  // the figures cover only those rows.
  it("keeps an older active run on the dashboard behind 200 newer results", () => {
    const now = Date.now()
    h.runs = [{ id: "long", pipeline_slug: "report", pipeline_name: "Weekly report", status: "running", started_at: new Date(now - 3 * 86_400_000).toISOString() }]
    h.recorded = Array.from({ length: 200 }, (_, i) => ({
      id: `done_${i}`, pipeline_slug: "report", pipeline_name: "Weekly report", status: "completed", started_at: new Date(now - (i + 1) * 60_000).toISOString(),
    }))
    renderPane()
    expect(screen.getByText("1 running")).toBeInTheDocument()
    expect(screen.getByTestId("run-summary-scope")).toHaveTextContent("newest 200 runs")
  })

  it("says what the empty workspace means instead of leaving a pane", () => {
    render(
      <RoutinesWorkspace workspaceId="ws" routines={[]} loading={false} error={null} onSelect={vi.fn()} />,
    )
    expect(screen.getByText(/No routines yet/)).toBeVisible()
  })

  it("empty workspace shows New routine and the CLI hint; without the role there is no New routine", () => {
    const onNew = vi.fn()
    render(<RoutinesWorkspace workspaceId="ws" routines={[]} loading={false} error={null} onSelect={vi.fn()} onNew={onNew} />)
    screen.getByRole("button", { name: "New routine" }).click()
    expect(onNew).toHaveBeenCalled()
    expect(screen.getByText("crewship routine init")).toBeInTheDocument()
    cleanup()
    render(<RoutinesWorkspace workspaceId="ws" routines={[]} loading={false} error={null} onSelect={vi.fn()} />)
    expect(screen.queryByRole("button", { name: "New routine" })).not.toBeInTheDocument()
  })

  it("an empty workspace explains routines under the empty state, without templates or figures (RT-09)", () => {
    render(<RoutinesWorkspace workspaceId="ws" routines={[]} loading={false} error={null} onSelect={vi.fn()} />)
    expect(screen.getByText(/No routines yet/)).toBeVisible()
    expect(screen.getByRole("heading", { name: "From a sentence to a routine in three steps" })).toBeInTheDocument()
    expect(screen.getByText("Runs without you")).toBeInTheDocument()
    expect(screen.queryByText(/template/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/In this workspace this week/)).not.toBeInTheDocument()
  })

  it("a load error offers Retry", () => {
    const onRetry = vi.fn()
    render(<RoutinesWorkspace workspaceId="ws" routines={[]} loading={false} error="boom" onSelect={vi.fn()} onRetry={onRetry} />)
    expect(screen.getByRole("alert")).toHaveTextContent("Routines could not load")
    screen.getByRole("button", { name: "Retry" }).click()
    expect(onRetry).toHaveBeenCalled()
  })
})

describe("routineLastState", () => {
  it("prefers the live run over the stored row, and names outcomes not enums", () => {
    const base = { slug: "x", last_invocation_status: "completed" } as Pipeline
    expect(routineLastState(base, { status: "waiting" })).toEqual({
      status: "WAITING",
      label: "Waiting for a person",
    })
    expect(routineLastState(base, { status: "running" })).toEqual({ status: "RUNNING", label: "Running" })
    expect(routineLastState({ ...base, last_invocation_status: "failed" }, null)).toEqual({
      status: "FAILED",
      label: "Could not finish",
    })
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
