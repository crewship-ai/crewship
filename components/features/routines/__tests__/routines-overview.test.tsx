import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, within } from "@testing-library/react"

import type { Pipeline } from "@/hooks/use-pipelines"

// The overview replaced a table of every routine. These cover the
// wiring the pure helpers in lib/routines-overview.ts cannot: that the
// right derived number reaches the right tile, that a live run reads
// as live, and that clicking an arc filters the sidebar.
//
// StatusDonut is stubbed rather than rendered: it is a dashboard
// component with its own SVG concerns, and what is under test here is
// the data handed to it and what comes back out of the click.

const h = vi.hoisted(() => ({
  runs: [] as unknown[],
  schedules: [] as unknown[],
  live: new Map<string, unknown>(),
}))

vi.mock("@/hooks/use-pipeline-runs", () => ({
  usePipelineRuns: () => ({ runs: h.runs, loading: false, error: null }),
}))
vi.mock("@/hooks/use-pipeline-schedules", () => ({
  usePipelineSchedules: () => ({ schedules: h.schedules, loading: false, error: null }),
}))
vi.mock("@/hooks/use-active-routine-runs", () => ({
  useActiveRoutineRuns: () => ({ bySlug: h.live, runs: [], activeCount: 0 }),
}))
vi.mock("@/components/features/dashboard/status-donut", () => ({
  StatusDonut: ({
    data,
    onSelect,
  }: {
    data: { key: string; label: string; count: number }[]
    onSelect?: (k: string) => void
  }) => (
    <div data-testid="donut">
      {data.map((d) => (
        <button key={d.key} type="button" onClick={() => onSelect?.(d.key)}>
          {d.label} {d.count}
        </button>
      ))}
    </div>
  ),
}))

import { RoutinesOverview } from "../routines-overview"

const NOW = new Date(2026, 7, 4, 12, 0, 0)
const at = (d: number, hr: number) => new Date(2026, 7, d, hr, 0, 0).toISOString()

function routine(over: Partial<Pipeline> = {}): Pipeline {
  return {
    id: "p1",
    slug: "nightly",
    name: "Nightly digest",
    dsl_version: "1.0",
    definition_hash: "abc",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 0,
    authored_via: "user_api",
    created_at: at(1, 9),
    updated_at: at(1, 9),
    ...over,
  } as Pipeline
}

function run(over: Record<string, unknown> = {}) {
  return {
    id: "r1",
    pipeline_id: "p1",
    pipeline_slug: "nightly",
    pipeline_name: "Nightly digest",
    status: "completed",
    started_at: at(4, 9),
    ended_at: at(4, 9),
    cost_usd: 0,
    duration_ms: 4200,
    triggered_via: "manual",
    current_step_id: "",
    ...over,
  }
}

const PROPS = {
  workspaceId: "ws-1",
  loading: false,
  error: null,
  onSelect: vi.fn(),
  onRefresh: vi.fn(),
}

describe("<RoutinesOverview>", () => {
  beforeEach(() => {
    h.runs = []
    h.schedules = []
    h.live = new Map()
    vi.useFakeTimers({ now: NOW, shouldAdvanceTime: false })
  })
  afterEach(() => vi.useRealTimers())

  it("shows the success rate with its denominator, never a bare percentage", () => {
    // The tile this replaced read "PASS RATE 100%" off one run.
    h.runs = [run({})]
    render(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 1 })]} />)
    expect(screen.getByText("100%")).toBeInTheDocument()
    expect(screen.getByText("1 of 1")).toBeInTheDocument()
  })

  it("says how many runs happened today rather than since the beginning of time", () => {
    h.runs = [run({ id: "a", started_at: at(4, 9) }), run({ id: "b", started_at: at(1, 9) })]
    render(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 2 })]} />)
    const tile = screen.getByText("Runs today").parentElement!
    expect(within(tile).getByText("1")).toBeInTheDocument()
  })

  it("counts a routine awaiting approval as needing attention", () => {
    render(
      <RoutinesOverview
        {...PROPS}
        routines={[routine({ status: "proposed" }), routine({ id: "p2", slug: "b", name: "B" })]}
      />,
    )
    expect(screen.getByText("1 to approve")).toBeInTheDocument()
  })

  it("marks a running routine's row live with its current step", () => {
    h.runs = [run({ id: "live", status: "running", current_step_id: "draft" })]
    h.live = new Map([["nightly", { status: "running" }]])
    render(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 1 })]} />)
    expect(screen.getByText(/▶ draft/)).toBeInTheDocument()
  })

  it("offers something to do instead of an empty table when nothing has run", () => {
    render(<RoutinesOverview {...PROPS} routines={[routine({})]} />)
    expect(screen.getByText(/Nothing has run yet/i)).toBeInTheDocument()
  })

  it("shows the never-invoked count as its own arc", () => {
    const many = Array.from({ length: 5 }, (_, i) => routine({ id: `p${i}`, slug: `s${i}` }))
    render(<RoutinesOverview {...PROPS} routines={many} />)
    expect(screen.getByText("Never invoked 5")).toBeInTheDocument()
  })

  it("filters the sidebar when an arc is clicked", () => {
    // The donut is the fastest route to "show me the broken ones",
    // and a legend that looks clickable has to actually do it.
    const onFilter = vi.fn()
    render(
      <RoutinesOverview
        {...PROPS}
        onFilter={onFilter}
        routines={[routine({ invocation_count: 1, last_invocation_status: "failed" })]}
      />,
    )
    fireEvent.click(screen.getByText("Failing 1"))
    expect(onFilter).toHaveBeenCalledWith("failed")
  })

  it("names the next schedule in words rather than in cron", () => {
    h.schedules = [
      {
        id: "s1",
        name: "Morning",
        enabled: true,
        cron_expr: "0 8 * * 1-5",
        next_run_at: at(5, 8),
        target_pipeline_slug: "nightly",
      },
    ]
    render(<RoutinesOverview {...PROPS} routines={[routine({})]} />)
    expect(screen.getByText("Every weekday at 08:00")).toBeInTheDocument()
  })

  it("leaves a stale next_run_at out of the upcoming list", () => {
    // A firing time in the past means the scheduler has not caught up.
    // Showing it as "next" reads as a stopped clock.
    h.schedules = [
      {
        id: "s1",
        name: "Morning",
        enabled: true,
        cron_expr: "0 8 * * *",
        next_run_at: at(4, 6),
        target_pipeline_slug: "nightly",
      },
    ]
    render(<RoutinesOverview {...PROPS} routines={[routine({})]} />)
    expect(screen.getByText(/No schedule is due/i)).toBeInTheDocument()
  })
})
