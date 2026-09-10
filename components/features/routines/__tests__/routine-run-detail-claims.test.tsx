import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { apiFetch } from "@/lib/api-fetch"
import { RoutineRunDetail } from "../routine-run-detail"

// Guards for the run-detail claims in
// docs/prd/HANDOFF-2026-09-08-ROUTINES-WORKSPACE.md. The shared run surface
// had no component test of its own; these pin the four sentences a reader of
// that document would take as promises about what the page shows.

const h = vi.hoisted(() => ({
  run: null as Record<string, unknown> | null,
  dsl: null as Record<string, unknown> | null,
  push: vi.fn(),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({
  useWorkspaceAgentDirectory: () => ({ agents: [], error: false }),
}))
vi.mock("@/hooks/use-run-executions", () => ({
  useRunExecutions: () => ({ byStep: new Map(), error: false, truncated: false, refresh: vi.fn() }),
}))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: h.push }) }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-trace", () => ({
  useTrace: () => ({ run: h.run, dsl: h.dsl, loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-pending-approval", () => ({
  usePendingApproval: () => ({
    waitpoint: null,
    deciding: false,
    decide: vi.fn(),
    refresh: vi.fn(),
    error: null,
  }),
}))
// The canvas and the timeline have their own suites; stub them so a failure
// here is a failure of the run surface.
vi.mock("@/components/features/activity/trace-canvas", () => ({
  TraceCanvas: () => <div data-testid="trace-canvas" />,
}))
vi.mock("@/components/features/activity/run-activity-timeline", () => ({
  RunActivityTimeline: () => <div data-testid="run-activity" />,
}))
vi.mock("../routine-execution-history", () => ({
  RoutineExecutionHistory: () => <div data-testid="execution-history" />,
}))
vi.mock("../routine-run-artifacts", () => ({
  RoutineRunArtifacts: () => <div data-testid="run-artifacts" />,
}))

function baseRun(overrides: Record<string, unknown> = {}) {
  return {
    id: "run_1",
    pipeline_slug: "monthly-billing",
    pipeline_name: "Monthly billing",
    status: "completed",
    mode: "run",
    started_at: "2026-09-08T08:00:00Z",
    duration_ms: 1200,
    cost_usd: 0.02,
    triggered_via: "manual",
    step_outputs: {},
    inputs: {},
    ...overrides,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(apiFetch).mockResolvedValue({ ok: false } as Response)
  h.run = baseRun()
  h.dsl = { steps: [{ id: "write", type: "agent_run" }] }
})

describe("routine run detail — handoff presentation", () => {
  // "Handoff results lead with their readable summary; raw protocol text is
  // expandable."
  it("shows the summary as prose and keeps the protocol block collapsed", () => {
    h.run = baseRun({
      output:
        "noise\n---HANDOFF---\nsummary: Invoices for August are ready for review\nconfidence: high\n---END HANDOFF---",
    })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const summary = screen.getByText("Invoices for August are ready for review")
    expect(summary.tagName).toBe("P")
    // The raw block is present but behind a disclosure, not the lead.
    const disclosure = screen.getByText("Full recorded response")
    expect(disclosure.closest("details")).not.toBeNull()
    expect(disclosure.closest("details")?.hasAttribute("open")).toBe(false)
    expect(disclosure.closest("details")?.textContent).toContain("---HANDOFF---")
  })

  it("shows a response with no handoff block verbatim, with nothing to expand", () => {
    h.run = baseRun({ output: "Plain answer with no protocol framing." })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    expect(screen.getByText("Plain answer with no protocol framing.")).toBeTruthy()
    expect(screen.queryByText("Full recorded response")).toBeNull()
  })
})

describe("routine run detail — unavailable history", () => {
  // "Old runs without an archive say so."
  it("says the historical recipe is unavailable instead of drawing a graph", () => {
    h.dsl = null
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    expect(screen.getByText(/historical recipe is unavailable/i)).toBeTruthy()
    expect(screen.queryByTestId("trace-canvas")).toBeNull()
  })

  // "Output-loading failures are distinct from an empty result."
  it("distinguishes outputs that failed to load from a run that produced none", () => {
    h.run = baseRun({ step_outputs_available: false, step_outputs: {} })
    const { unmount } = render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    expect(screen.getByRole("alert").textContent).toMatch(/could not be loaded/i)
    expect(screen.queryByText(/No response was recorded for this step/i)).toBeNull()
    unmount()

    h.run = baseRun({ step_outputs_available: true, step_outputs: {} })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    expect(screen.getByText(/No response was recorded for this step/i)).toBeTruthy()
  })
})

describe("routine run detail — run again", () => {
  // "Run again creates a new manual run using the current recipe and asks for
  // its declared inputs, initially populated from the historical run."
  it("asks for the current recipe's inputs, prefilled from the historical run", async () => {
    h.run = baseRun({ inputs: { month: "2026-08" } })
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      json: async () => ({
        slug: "monthly-billing",
        definition: { inputs: [{ name: "month", type: "string", default: "2026-09" }] },
      }),
    } as Response)

    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    fireEvent.click(screen.getByRole("button", { name: "Run again" }))

    // The recipe it asks about is the CURRENT one, not the archived run.
    await waitFor(() =>
      expect(vi.mocked(apiFetch).mock.calls[0][0]).toContain("/pipelines/monthly-billing"),
    )
    expect(vi.mocked(apiFetch).mock.calls[0][0]).not.toContain("/pipeline-runs/")
    // The historical value wins over the recipe default as the starting point.
    expect(await screen.findByDisplayValue("2026-08")).toBeTruthy()
  })

  it("does not offer Run again while the run is still going", () => {
    h.run = baseRun({ status: "running" })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    expect(screen.queryByRole("button", { name: "Run again" })).toBeNull()
    expect(screen.getByRole("button", { name: "Stop run" })).toBeTruthy()
  })
})

it("Run again retries an uncertain start with the same key and selected version", async () => {
  h.run = baseRun({ pipeline_version: 3, inputs: { month: "2026-08" } })
  const post = vi
    .fn()
    .mockRejectedValueOnce(new TypeError("Response lost"))
    .mockResolvedValue({ ok: true, json: async () => ({ run_id: "recovered-run" }) } as Response)
  vi.mocked(apiFetch).mockImplementation(async (url, init) => {
    if (init?.method === "POST") return post(url, init)
    return {
      ok: true,
      json: async () => ({
        slug: "monthly-billing",
        definition: { inputs: [{ name: "month", type: "string" }] },
      }),
    } as Response
  })
  render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
  fireEvent.click(screen.getByRole("button", { name: "Run again" }))
  await screen.findByDisplayValue("2026-08")
  fireEvent.change(screen.getByLabelText("Recipe version"), { target: { value: "3" } })
  await waitFor(() => expect(screen.getByRole("button", { name: "Run" })).not.toBeDisabled())
  fireEvent.click(screen.getByRole("button", { name: "Run" }))
  await screen.findByText("Response lost")
  fireEvent.click(screen.getByRole("button", { name: "Run" }))
  await waitFor(() =>
    expect(h.push).toHaveBeenCalledWith("/routines?slug=monthly-billing&run=recovered-run"),
  )
  expect(post).toHaveBeenCalledTimes(2)
  const first = post.mock.calls[0][1]
  const retry = post.mock.calls[1][1]
  expect(new Headers(first.headers).get("Prefer")).toBe("respond-async")
  expect(new Headers(retry.headers).get("Prefer")).toBe("respond-async")
  expect(new Headers(first.headers).get("Idempotency-Key")).toBeTruthy()
  expect(new Headers(retry.headers).get("Idempotency-Key")).toBe(
    new Headers(first.headers).get("Idempotency-Key"),
  )
  expect(JSON.parse(retry.body)).toEqual({ inputs: { month: "2026-08" }, pinned_version: 3 })
})
