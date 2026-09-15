import { fireEvent, render, screen, within } from "@testing-library/react"
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
  })

  it("is a dashboard, not a second copy of the sidebar's list", () => {
    h.runs = []
    h.schedules = []
    h.automations = []
    renderPane()
    expect(screen.getByRole("button", { name: "Overview" })).toHaveAttribute("aria-pressed", "true")
    expect(screen.getByTestId("routines-dashboard")).toBeInTheDocument()
    // No catalog rows: the explorer beside this pane owns them.
    expect(screen.queryByText("Summarize service health")).toBeNull()
    expect(screen.queryByText("Routine and purpose")).toBeNull()
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
  })

  it("puts the waiting decision first, as a tile that opens the newest waiting run", () => {
    h.runs = [
      {
        id: "run_1",
        pipeline_slug: "report",
        pipeline_name: "Weekly report",
        status: "waiting",
        started_at: new Date(Date.now() - 4 * 60_000).toISOString(),
      },
    ]
    h.schedules = []
    h.automations = []
    renderPane()
    const needs = screen.getByRole("group", { name: "Needs you" })
    const tile = within(needs).getByRole("link", { name: /Waiting for your decision/ })
    expect(tile).toHaveAttribute("href", "/routines?slug=report&run=run_1")
    expect(tile).toHaveTextContent(/^1/)
    expect(tile).toHaveTextContent("Weekly report")
  })

  it("counts the routines that could not finish, opens the newest one, and applies the explorer's filters", () => {
    h.runs = []
    h.schedules = []
    h.automations = []
    const select = vi.fn()
    renderPane({ onSelect: select, filters: { ...filters, status: "failed" } })
    const needs = screen.getByRole("group", { name: "Needs you" })
    const tile = within(needs).getByRole("button", { name: /Could not finish last time/ })
    expect(tile).toHaveTextContent(/^1/)
    fireEvent.click(tile)
    expect(select).toHaveBeenCalledWith("broken")
    // The dashboard's "could not finish" list follows the same filter: the
    // broken routine is there, the healthy ones are not.
    const dashboard = within(screen.getByTestId("routines-dashboard"))
    expect(dashboard.getByRole("button", { name: /Broken routine/ })).toBeInTheDocument()
    expect(dashboard.queryByText("Weekly report")).toBeNull()
    expect(screen.queryByRole("button", { name: "Never run" })).not.toBeInTheDocument()
  })

  it("names the next planned start and opens that routine's Plan", () => {
    h.runs = []
    h.automations = []
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
    renderPane()
    const needs = screen.getByRole("group", { name: "Needs you" })
    const tile = within(needs).getByRole("link", { name: /Next planned start/ })
    expect(tile).toHaveAttribute("href", "/routines?slug=report&view=plan")
    expect(tile).toHaveTextContent("02:30")
    expect(tile).toHaveTextContent(/13 Sept 2026, 02:30 · Europe\/Prague · Weekly report/)
  })

  it("says when nothing needs anyone, without a click that goes nowhere", () => {
    h.runs = []
    h.schedules = []
    h.automations = []
    renderPane({ routines: [rows[1]] })
    const needs = screen.getByRole("group", { name: "Needs you" })
    expect(within(needs).queryAllByRole("link")).toHaveLength(0)
    expect(within(needs).queryAllByRole("button")).toHaveLength(0)
    expect(within(needs).getAllByText("Nothing right now")).toHaveLength(2)
    expect(within(needs).getByText("No schedule is on")).toBeInTheDocument()
  })

  it("lists drafts to publish, published and not, and opens the routine", () => {
    h.runs = []
    h.schedules = []
    h.automations = []
    const select = vi.fn()
    renderPane({
      onSelect: select,
      routines: [
        { ...rows[0], draft: { id: "drf_1", revision: 2, updated_at: "2026-09-15T10:31:00Z" } },
        { ...rows[1], head_version: 0, draft: { id: "drf_2", revision: 1 } },
        rows[2],
      ] as Pipeline[],
    })
    expect(screen.getByText("Drafts to publish")).toBeInTheDocument()
    const dashboard = within(screen.getByTestId("routines-dashboard"))
    const draftRow = dashboard.getAllByRole("button", { name: /Weekly report/ })[0]
    expect(draftRow).toHaveTextContent(/Draft r2 · published v3/)
    fireEvent.click(draftRow)
    expect(select).toHaveBeenCalledWith("report")
    expect(dashboard.getByRole("button", { name: /Other routine/ })).toHaveTextContent("Not published yet")
  })

  it("says what the empty workspace means instead of leaving a pane", () => {
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
