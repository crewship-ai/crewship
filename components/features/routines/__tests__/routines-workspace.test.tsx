import { fireEvent, render, screen, within } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
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

/** The rows only — the region also holds the "Needs you" tiles, which name routines too. */
const rowsOf = () => within(screen.getByRole("region", { name: "Routine list" }).querySelector("ul")!)

function renderList(props: Partial<React.ComponentProps<typeof RoutinesWorkspace>> = {}) {
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

describe("<RoutinesWorkspace> — the one list", () => {
  it("shows purpose, how it runs and one last-run state per row, then selects the routine", () => {
    h.runs = []
    h.automations = [{ id: "a1", enabled: true, action: { routine_slug: "other" } }]
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
    renderList({ onSelect: select })
    const list = rowsOf()
    const row = list.getByRole("button", { name: /Weekly report/ })
    expect(within(row).getByText("Summarize service health")).toBeVisible()
    expect(within(row).getByText("Every day at 02:30 · Europe/Prague")).toBeInTheDocument()
    // A routine an automation can start is not "Manual".
    expect(list.getByRole("button", { name: /Other routine/ })).toHaveTextContent("1 automation")
    // One state per row: the pill and a relative time, no second sentence.
    expect(within(row).getByText("Completed")).toBeVisible()
    expect(within(row).getByText("1 h ago")).toBeVisible()
    expect(within(row).queryByText(/Finished/)).not.toBeInTheDocument()
    expect(within(row).queryByText(/steps/)).not.toBeInTheDocument()
    expect(list.queryByText("Open →")).not.toBeInTheDocument()
    // The explorer beside this panel owns the buckets and the count.
    expect(screen.queryByRole("tab", { name: /Health/ })).not.toBeInTheDocument()
    expect(screen.queryByText(/^3 routines$/)).not.toBeInTheDocument()
    fireEvent.click(row)
    expect(select).toHaveBeenCalledWith("report")
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
    renderList()
    const needs = screen.getByRole("group", { name: "Needs you" })
    const tile = within(needs).getByRole("link", { name: /Waiting for your decision/ })
    expect(tile).toHaveAttribute("href", "/routines?slug=report&run=run_1")
    expect(tile).toHaveTextContent(/^1/)
    expect(tile).toHaveTextContent("Weekly report")
    const row = rowsOf().getByRole("button", { name: /Weekly report/ })
    expect(within(row).getByText("Waiting for a person")).toBeVisible()
    expect(within(row).getByText("4 min ago")).toBeVisible()
  })

  it("counts the routines that could not finish, opens the newest one, and applies the explorer's filters", () => {
    h.runs = []
    h.schedules = []
    h.automations = []
    const select = vi.fn()
    renderList({ onSelect: select, filters: { ...filters, status: "failed" } })
    const needs = screen.getByRole("group", { name: "Needs you" })
    const tile = within(needs).getByRole("button", { name: /Could not finish last time/ })
    expect(tile).toHaveTextContent(/^1/)
    fireEvent.click(tile)
    expect(select).toHaveBeenCalledWith("broken")
    const list = rowsOf()
    const row = list.getByRole("button", { name: /Broken routine/ })
    expect(within(row).getByText("Could not finish")).toBeVisible()
    expect(list.queryByText("Weekly report")).not.toBeInTheDocument()
    expect(screen.getByText(/1 of 3 match the explorer's filters/)).toBeInTheDocument()
    // The explorer owns search and the buckets; the panel repeats neither.
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument()
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
    renderList()
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
    renderList({ routines: [rows[1]] })
    const needs = screen.getByRole("group", { name: "Needs you" })
    expect(within(needs).queryAllByRole("link")).toHaveLength(0)
    expect(within(needs).queryAllByRole("button")).toHaveLength(0)
    expect(within(needs).getAllByText("Nothing right now")).toHaveLength(2)
    expect(within(needs).getByText("No schedule is on")).toBeInTheDocument()
  })

  it("marks a routine with an unpublished draft, and one that was never published", () => {
    h.runs = []
    h.schedules = []
    h.automations = []
    renderList({
      routines: [
        { ...rows[0], draft: { id: "drf_1", revision: 2, updated_at: "2026-09-15T10:31:00Z" } },
        { ...rows[1], head_version: 0, draft: { id: "drf_2", revision: 1 } },
        rows[2],
      ] as Pipeline[],
    })
    const list = rowsOf()
    const published = list.getByRole("button", { name: /Weekly report/ })
    expect(within(published).getByText("Draft r2")).toBeVisible()
    expect(within(published).getByText("Draft r2").closest("[data-slot=status-pill]")).toHaveAttribute(
      "data-tone",
      "purple",
    )
    const unpublished = list.getByRole("button", { name: /Other routine/ })
    expect(within(unpublished).getByText("Draft")).toBeVisible()
    expect(within(unpublished).getByText("Draft · not published")).toBeInTheDocument()
    expect(within(unpublished).getByText("Never run")).toBeVisible()
    expect(
      within(list.getByRole("button", { name: /Broken routine/ })).queryByText(/Draft/),
    ).not.toBeInTheDocument()
  })

  it("blames the explorer's filters when any facet, not only status, empties the list", () => {
    h.runs = []
    h.schedules = []
    h.automations = []
    renderList({ filters: { ...filters, invocations: "popular" } })
    expect(screen.getByText(/No routines match the explorer's filters/)).toBeVisible()
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
