import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen } from "@testing-library/react"
import { RoutinesExplorer } from "../routines-explorer"
import type { Pipeline } from "@/hooks/use-pipelines"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

// Live surfaces on /routines: the explorer sidebar row grows a
// "▶ <current step> · <elapsed>" sub-line while a routine has an
// active run (amber ⏸ variant for awaiting approval), read from the
// shared useActiveRoutineRuns hook and matched by pipeline_slug.
//
// The catalog table that used to sit beside it is gone — the overview
// replaced it, and its live-row behaviour is covered in
// routines-overview.test.tsx.

const h = vi.hoisted(() => ({
  runs: [] as unknown[],
}))

// Frozen "now". The elapsed sub-lines are derived from
// Date.now() - started_at inside the component, so the fixture and the
// render have to agree on the clock. Building started_at from a live
// Date.now() left ~50ms of headroom before "12.0s" tipped over to
// "12.1s" (issue #1253) — under load or coverage instrumentation that
// headroom is gone. Freeze the clock instead: shouldAdvanceTime:false
// pins Date.now() for the whole test, and every fixture timestamp is
// derived from NOW rather than from the wall clock.
const NOW = new Date("2026-07-02T09:00:00.000Z").getTime()

// sleepRealMs burns real wall-clock time synchronously. Atomics.wait is
// not faked by vi.useFakeTimers, so this is genuine skew between the
// fixture construction and the render — exactly the drift that made the
// elapsed assertions flaky. With the clock frozen it must be invisible.
function sleepRealMs(ms: number): void {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms)
}

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
    created_at: new Date(NOW - 86_400_000).toISOString(),
    updated_at: new Date(NOW - 86_400_000).toISOString(),
    last_invocation_status: "completed",
    last_invoked_at: new Date(NOW - 3_600_000).toISOString(),
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
    started_at: new Date(NOW - 12_000).toISOString(),
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

beforeEach(() => {
  // shouldAdvanceTime:false — the clock does not tick on its own, so the
  // component's Date.now() at render is byte-identical to the NOW the
  // fixtures were built from. The 1s useTick interval also stays parked,
  // which keeps these renders single-pass.
  vi.useFakeTimers({ shouldAdvanceTime: false })
  vi.setSystemTime(NOW)
})

afterEach(() => {
  vi.useRealTimers()
})

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
    // Real time passes between fixture and render. Against a live clock
    // this alone flips "12.0s" to "12.1s"; the frozen clock absorbs it.
    sleepRealMs(60)
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
