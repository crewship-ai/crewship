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
  waitpoints: [] as unknown[],
  refreshWaitpoints: vi.fn(),
  decide: vi.fn(async () => ({ ok: true })),
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
vi.mock("@/hooks/use-run-waitpoints", () => ({
  useWorkspaceWaitpoints: () => ({ waitpoints: h.waitpoints, refresh: h.refreshWaitpoints }),
}))
vi.mock("@/lib/api/waitpoints", () => ({
  waitpointDecide: (...args: unknown[]) => h.decide(...args),
}))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
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
}

describe("<RoutinesOverview>", () => {
  beforeEach(() => {
    h.runs = []
    h.schedules = []
    h.live = new Map()
    h.waitpoints = []
    h.refreshWaitpoints = vi.fn()
    h.decide = vi.fn(async () => ({ ok: true }))
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

  // "Waiting on you" sits beside Recent runs because it answers the
  // same question a second later: what stopped halfway and needs a
  // person. A parked run holds a live process and expires.

  it("offers the decision on a parked run without leaving the page", async () => {
    h.waitpoints = [
      {
        token: "tok-1",
        pipeline_run_id: "r1",
        step_id: "gate",
        kind: "approval",
        prompt: "Send the invoice?",
        timeout_at: at(4, 18),
      },
    ]
    h.runs = [run({ id: "r1", status: "waiting" })]
    render(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 1 })]} />)

    expect(screen.getByText("Send the invoice?")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: /approve/i }))
    expect(h.decide).toHaveBeenCalledWith("ws-1", "tok-1", true)
  })

  it("sends an explicit false on Deny rather than omitting the field", async () => {
    // The endpoint treats an absent `approved` as false, so a caller
    // that omits it happens to work — until the default changes.
    h.waitpoints = [
      { token: "tok-1", pipeline_run_id: "r1", step_id: "gate", kind: "approval", prompt: "Ship?" },
    ]
    h.runs = [run({ id: "r1", status: "waiting" })]
    render(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 1 })]} />)
    fireEvent.click(screen.getByRole("button", { name: /deny/i }))
    expect(h.decide).toHaveBeenCalledWith("ws-1", "tok-1", false)
  })

  it("names the routine a parked run belongs to", () => {
    h.waitpoints = [
      { token: "t", pipeline_run_id: "r1", step_id: "gate", kind: "approval", prompt: "?" },
    ]
    h.runs = [run({ id: "r1", status: "waiting" })]
    const { rerender } = render(
      <RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 1 })]} />,
    )
    // Once in Recent runs, once on the parked row. A waitpoint carries
    // a run id and no slug, so the second one only appears if the
    // run→routine lookup worked.
    expect(screen.getAllByText("Nightly digest")).toHaveLength(2)

    h.waitpoints = []
    rerender(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 1 })]} />)
    expect(screen.getAllByText("Nightly digest")).toHaveLength(1)
  })

  it("lists a routine awaiting review and opens it on click", () => {
    const onSelect = vi.fn()
    render(
      <RoutinesOverview
        {...PROPS}
        onSelect={onSelect}
        routines={[routine({ slug: "risky", name: "Risky routine", status: "proposed" })]}
      />,
    )
    expect(screen.getByText("definition needs review")).toBeInTheDocument()
    fireEvent.click(screen.getByText("Risky routine"))
    expect(onSelect).toHaveBeenCalledWith("risky")
  })

  it("says nothing is pending when the queue is empty", () => {
    render(<RoutinesOverview {...PROPS} routines={[routine({})]} />)
    expect(screen.getByText(/Nothing is waiting on a decision/i)).toBeInTheDocument()
  })

  // One chart replaced two cards about money. What matters at this
  // level is that the card counts the week honestly and says so when
  // the week was empty — the stacking and the axes are the chart
  // library's job, the arithmetic is pinned in lib/routines-overview.

  it("heads the week with its run count and its spend", () => {
    h.runs = [
      run({ id: "a", started_at: at(4, 9), cost_usd: 0.02 }),
      run({ id: "b", started_at: at(3, 9), status: "failed", cost_usd: 0.01 }),
    ]
    render(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 2 })]} />)
    expect(screen.getByText(/2 runs · \$0\.0300/)).toBeInTheDocument()
  })

  it("says the week was empty rather than drawing an empty frame", () => {
    render(<RoutinesOverview {...PROPS} routines={[routine({})]} />)
    expect(screen.getByText(/Nothing ran in the last 7 days/i)).toBeInTheDocument()
  })

  it("no longer carries a workspace budget roll-up", () => {
    // A third card about money on one row. The cap moved to the
    // routine that owns it.
    h.runs = [run({})]
    render(<RoutinesOverview {...PROPS} routines={[routine({ invocation_count: 1 })]} />)
    expect(screen.queryByText(/Budgets/i)).not.toBeInTheDocument()
  })
})
