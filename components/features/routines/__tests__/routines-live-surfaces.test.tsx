import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen } from "@testing-library/react"
import { RoutinesExplorer } from "../routines-explorer"
import { RoutinesListView } from "../routines-list-view"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// Live surfaces on /routines: the explorer sidebar row grows a
// "▶ <current step> · <elapsed>" sub-line while a routine has an
// active run (amber ⏸ variant for awaiting approval), and the list
// table's status cell swaps the historical pill for a live Running
// pill with step · elapsed · cost. Both read the shared
// useActiveRoutineRuns hook, matched by pipeline_slug.

const h = vi.hoisted(() => ({
  runs: [] as unknown[],
}))

vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return {
    ...mod,
    useActiveRoutineRuns: () => ({
      ...mod.deriveActiveRoutineRuns(h.runs as PipelineRun[]),
      loading: false,
      error: null,
      refresh: vi.fn(),
    }),
  }
})

// importOriginal drags in use-realtime → use-websocket → api-fetch;
// stub the realtime layer so the chain stays flat.
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

function pipeline(overrides: Partial<Pipeline>): Pipeline {
  return {
    id: "pipe-1",
    slug: "classify-ticket",
    name: "Classify support ticket",
    dsl_version: "1",
    definition_hash: "h",
    ephemeral: false,
    workspace_visible: true,
    invocation_count: 12,
    authored_via: "user_api",
    created_at: "2026-07-01T10:00:00Z",
    updated_at: "2026-07-01T10:00:00Z",
    last_invocation_status: "completed",
    last_invoked_at: "2026-07-02T09:00:00Z",
    ...overrides,
  } as Pipeline
}

function activeRun(overrides: Partial<PipelineRun>): PipelineRun {
  return {
    id: "run-live-1",
    pipeline_id: "pipe-1",
    pipeline_slug: "classify-ticket",
    pipeline_name: "Classify support ticket",
    status: "running",
    mode: "run",
    started_at: new Date(Date.now() - 12_000).toISOString(),
    ended_at: "",
    current_step_id: "ask-casey",
    step_outputs: null,
    cost_usd: 0.011,
    duration_ms: 0,
    triggered_via: "manual",
    triggered_by_id: "",
    invoking_crew_id: "",
    invoking_agent_id: "",
    invoking_user_id: "",
    error_message: "",
    failed_at_step: "",
    issue_identifier: "",
    ...overrides,
  } as PipelineRun
}

const EXPLORER_PROPS = {
  search: "",
  onSearchChange: vi.fn(),
  selectedSlug: null,
  onSelectRoutine: vi.fn(),
  filters: {
    status: "all" as const,
    invocations: "all" as const,
    authorAgentId: null,
    showEphemeral: false,
  },
  onChange: vi.fn(),
}

describe("<RoutinesExplorer> live rows", () => {
  beforeEach(() => {
    h.runs = []
  })

  it("shows no live sub-line when the routine has no active run", () => {
    render(<RoutinesExplorer routines={[pipeline({})]} {...EXPLORER_PROPS} />)
    expect(screen.queryByText(/ask-casey/)).not.toBeInTheDocument()
    expect(screen.queryByText(/awaiting approval/)).not.toBeInTheDocument()
  })

  it("renders the current step + elapsed sub-line for a running routine", () => {
    h.runs = [activeRun({})]
    render(<RoutinesExplorer routines={[pipeline({})]} {...EXPLORER_PROPS} />)
    const sub = screen.getByText(/ask-casey/)
    expect(sub).toBeInTheDocument()
    // Elapsed rides along in the same sub-line (12s → "12.0s").
    expect(sub.textContent).toMatch(/·\s*12\.0s/)
  })

  it("renders the amber awaiting-approval sub-line for a parked routine", () => {
    h.runs = [activeRun({ status: "waiting" })]
    render(<RoutinesExplorer routines={[pipeline({})]} {...EXPLORER_PROPS} />)
    expect(screen.getByText(/awaiting approval/)).toBeInTheDocument()
    // The running-step form must not render for a parked run.
    expect(screen.queryByText(/ask-casey/)).not.toBeInTheDocument()
  })

  it("only marks the routine whose slug matches the active run", () => {
    h.runs = [activeRun({ pipeline_slug: "other-routine" })]
    render(<RoutinesExplorer routines={[pipeline({})]} {...EXPLORER_PROPS} />)
    expect(screen.queryByText(/ask-casey/)).not.toBeInTheDocument()
  })
})

describe("<RoutinesListView> live status cell", () => {
  const LIST_PROPS = {
    loading: false,
    error: null,
    selectedSlug: null,
    onSelect: vi.fn(),
    onRefresh: vi.fn(),
  }

  beforeEach(() => {
    h.runs = []
  })

  it("keeps the historical status pill when nothing is live", () => {
    render(<RoutinesListView routines={[pipeline({})]} {...LIST_PROPS} />)
    expect(screen.getByText("completed")).toBeInTheDocument()
    expect(screen.queryByText("Running")).not.toBeInTheDocument()
  })

  it("swaps in a Running pill with step · elapsed · cost while a run is live", () => {
    h.runs = [activeRun({})]
    render(<RoutinesListView routines={[pipeline({})]} {...LIST_PROPS} />)
    expect(screen.getByText("Running")).toBeInTheDocument()
    const sub = screen.getByText(/ask-casey/)
    expect(sub.textContent).toMatch(/12\.0s/)
    expect(sub.textContent).toMatch(/\$0\.0110/)
    // The stale historical pill is replaced, not duplicated.
    expect(screen.queryByText("completed")).not.toBeInTheDocument()
  })

  it("shows the amber Awaiting approval pill for a parked run", () => {
    h.runs = [activeRun({ status: "waiting" })]
    render(<RoutinesListView routines={[pipeline({})]} {...LIST_PROPS} />)
    expect(screen.getByText("Awaiting approval")).toBeInTheDocument()
  })

  it("bubbles routines with a live run to the top of the table", () => {
    const idle = pipeline({ id: "p-idle", slug: "idle-routine", name: "Idle routine", invocation_count: 999 })
    const live = pipeline({ id: "p-live", slug: "live-routine", name: "Live routine", invocation_count: 1 })
    h.runs = [activeRun({ pipeline_slug: "live-routine" })]
    render(<RoutinesListView routines={[idle, live]} {...LIST_PROPS} />)
    const rows = screen.getAllByRole("button", { name: /open routine/i })
    // Default sort is invocation_count desc — idle would win without
    // the live-first bubble.
    expect(rows[0]).toHaveAccessibleName("Open routine Live routine")
    expect(rows[1]).toHaveAccessibleName("Open routine Idle routine")
  })
})
