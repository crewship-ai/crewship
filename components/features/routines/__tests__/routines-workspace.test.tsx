import { cleanup, render, screen } from "@testing-library/react"
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
vi.mock("@/hooks/use-pipeline-runs", () => ({ usePipelineRuns: () => ({ runs: h.recorded ?? [], loading: false, error: null }) }))
vi.mock("@/hooks/use-automations", () => ({
  useAutomations: () => ({ automations: h.automations, loading: false, error: null }),
}))
vi.mock("../routine-calendar", () => ({ RoutineCalendar: () => <div>Calendar</div> }))
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
    expect(screen.getByRole("button", { name: "Overview" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("routines-dashboard")).toBeInTheDocument()
    // No catalog rows and no third tab: the explorer owns the list, Activity the runs.
    expect(screen.queryByText("Summarize service health")).toBeNull()
    expect(screen.queryByRole("button", { name: /Recent runs/ })).toBeNull()
    expect(screen.getByRole("link", { name: /Runs in Activity/ })).toHaveAttribute("href", "/activity?lens=routines")
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
  })

  it("feeds the dashboard the runs and schedules the page loads, filtered like the sidebar", () => {
    h.recorded = [
      { id: "run_1", pipeline_slug: "report", pipeline_name: "Weekly report", status: "waiting", started_at: new Date(Date.now() - 4 * 60_000).toISOString() },
      { id: "run_2", pipeline_slug: "broken", pipeline_name: "Broken routine", status: "failed", started_at: new Date(Date.now() - 60_000).toISOString() },
    ]
    h.schedules = [
      { id: "s1", enabled: true, target_pipeline_slug: "report", cron_expr: "30 2 * * *", timezone: "Europe/Prague", next_run_at: new Date(Date.now() + 3_600_000).toISOString() },
    ]
    renderPane()
    expect(screen.getByRole("link", { name: /1 decision waiting/ })).toHaveAttribute("href", "/routines?slug=report&run=run_1")
    expect(screen.getByRole("link", { name: /Next start/ })).toHaveAttribute("href", "/routines?slug=report&view=plan")
    // The explorer's status filter narrows the pane too.
    cleanup()
    renderPane({ filters: { ...filters, status: "failed" } })
    expect(screen.queryByRole("link", { name: /decision waiting/ })).toBeNull()
    expect(screen.getByRole("link", { name: /1 could not finish/ })).toBeInTheDocument()
    expect(screen.getAllByRole("link", { name: /Open run/ })).toHaveLength(1)
  })

  it("says what the empty workspace means instead of leaving a pane", () => {
    render(
      <RoutinesWorkspace workspaceId="ws" routines={[]} loading={false} error={null} onSelect={vi.fn()} />,
    )
    expect(screen.getByText(/No routines yet/)).toBeVisible()
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
