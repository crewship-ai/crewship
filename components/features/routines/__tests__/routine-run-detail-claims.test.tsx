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
  error: null as string | null,
  waitpoint: null as Record<string, unknown> | null,
  approvalError: null as string | null,
  refresh: vi.fn(),
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
  useTrace: () => ({
    run: h.run,
    dsl: h.dsl,
    loading: false,
    error: h.error,
    refresh: h.refresh,
  }),
}))
vi.mock("@/hooks/use-pending-approval", () => ({
  usePendingApproval: () => ({
    waitpoint: h.waitpoint,
    deciding: false,
    decide: vi.fn(),
    refresh: h.refresh,
    error: h.approvalError,
  }),
}))
vi.mock("../routine-approval-banner", () => ({
  RoutineApprovalBanner: () => <div data-testid="approval-banner" />,
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
  h.error = null
  h.waitpoint = null
  h.approvalError = null
})

/** jsdom does not toggle <details> on a summary click; drive it the way the
 * browser would end up: flip `open`, then fire the toggle event React listens to. */
function openDisclosure(details: HTMLDetailsElement) {
  details.open = true
  fireEvent(details, new Event("toggle"))
}

/** Document order of two nodes: negative when a comes first. */
function order(a: Element, b: Element) {
  return a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1
}

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

it.each(["current", "3"])("Run again retries an uncertain start with the same key and selected version %s", async (selectedVersion) => {
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
  fireEvent.change(screen.getByLabelText("Recipe version"), { target: { value: selectedVersion } })
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
  expect(JSON.parse(retry.body)).toEqual({
    inputs: { month: "2026-08" },
    ...(selectedVersion === "current" ? {} : { pinned_version: 3 }),
  })
})

describe("routine run detail — one page, one order (#2519)", () => {
  it("reads the run without a second row of tabs, and keeps the facts as one line", () => {
    h.run = baseRun({ pipeline_version: 3 })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    expect(document.querySelector('nav[aria-label="Routine detail"]')).toBeNull()
    const facts = screen.getByTestId("run-facts")
    expect(facts.textContent).toMatch(/^started .*Manual start.*recipe v3/)
    // Times name their zone (formatRoutineTime), never a bare en-GB number.
    expect(facts.textContent).toMatch(/2026/)
    expect(facts.textContent).toMatch(/UTC|\//)
    expect(screen.getByRole("link", { name: /recipe v3/ }).getAttribute("href")).toBe(
      "/routines?slug=monthly-billing&view=versions&version=3",
    )
    // The explanation is visible prose, not behind "What this means".
    const detail = screen.getByText(/Completion does not independently prove/)
    expect(detail.closest("details")).toBeNull()
    expect(screen.queryByText("What this means")).toBeNull()
  })

  it("keeps activity, attempts and identifiers in one collapsed technical disclosure", () => {
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const disclosure = screen.getByTestId("run-technical-details") as HTMLDetailsElement
    expect(disclosure.tagName).toBe("DETAILS")
    expect(disclosure.open).toBe(false)
    expect(disclosure.textContent).toContain("Technical details")
    expect(disclosure.contains(screen.getByTestId("run-executions-section"))).toBe(true)
    expect(disclosure.contains(screen.getByTestId("run-activity-section"))).toBe(true)
    expect(disclosure.contains(screen.getByText("run_1"))).toBe(true)
    // Only one disclosure of evidence on the page.
    expect(
      document.querySelectorAll("details[data-testid='run-technical-details']"),
    ).toHaveLength(1)
    expect(screen.queryByText("All recorded executions and attempts")?.closest("details")).toBe(
      disclosure,
    )
    // Both lazy lists mount on the first open, not before.
    expect(screen.queryByTestId("execution-history")).toBeNull()
    expect(screen.queryByTestId("run-activity")).toBeNull()
    openDisclosure(disclosure)
    expect(screen.getByTestId("execution-history")).toBeTruthy()
    expect(screen.getByTestId("run-activity")).toBeTruthy()
  })

  it("keeps live activity on the page while the run is going", () => {
    h.run = baseRun({ status: "running" })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const live = screen.getByTestId("run-activity")
    expect(live.closest("details")).toBeNull()
    expect(screen.queryByTestId("run-activity-section")).toBeNull()
    expect(screen.getByTestId("run-technical-details").contains(live)).toBe(false)
  })

  it("puts the verdict, the failed step and the error above everything, then says what to do", () => {
    h.run = baseRun({
      status: "failed",
      failed_at_step: "write",
      error_message: "Agent did not answer",
    })
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      json: async () => ({ slug: "monthly-billing", definition: { inputs: [] } }),
    } as Response)
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const verdict = screen.getByText("This run could not finish")
    const error = screen.getByRole("alert")
    expect(error.textContent).toBe("Agent did not answer")
    expect(error.parentElement?.textContent).toContain("Failed step")
    expect(error.parentElement?.textContent).toContain("write")
    expect(order(verdict, error)).toBe(-1)
    expect(order(error, screen.getByTestId("run-artifacts"))).toBe(-1)
    expect(order(error, screen.getByTestId("run-technical-details"))).toBe(-1)

    const next = screen.getByTestId("run-next-step")
    expect(next.textContent).toBe("What you can do: fix the step in Edit recipe, then run again.")
    expect(order(error, next)).toBe(-1)
    expect(screen.getByRole("link", { name: "Edit recipe" }).getAttribute("href")).toBe(
      "/routines?slug=monthly-billing&view=edit",
    )
    // No per-step retry is offered; "run again" is the whole-run action.
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "run again" }))
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toContain("/pipelines/monthly-billing")
  })

  it("calls a stopped run stopped and offers no fix for it", () => {
    h.run = baseRun({
      status: "cancelled",
      outcome: "CANCELLED",
      failed_at_step: "write",
      error_message: "cancelled by operator",
    })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    expect(screen.getByText("Run stopped")).toBeTruthy()
    expect(screen.getByText("Stopped")).toBeTruthy()
    expect(screen.getByRole("alert").parentElement?.textContent).toContain("Stopped at step")
    expect(screen.queryByText(/Failed step/)).toBeNull()
    expect(screen.queryByTestId("run-next-step")).toBeNull()
  })

  it("keeps the decision directly under the verdict, before the result", () => {
    h.run = baseRun({ status: "waiting" })
    h.waitpoint = { step_id: "write", token: "t", inbox_item_id: "inb_1" }
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const banner = screen.getByTestId("approval-banner")
    expect(order(screen.getByText("A review is needed"), banner)).toBe(-1)
    expect(order(banner, screen.getByTestId("run-artifacts"))).toBe(-1)
    expect(order(banner, screen.getByTestId("run-technical-details"))).toBe(-1)
    expect(screen.getByRole("link", { name: /same decision in Inbox/ }).getAttribute("href")).toBe(
      "/inbox?item=inb_1",
    )
  })

  it("names the unconfirmed completion instead of dressing it as success", () => {
    h.run = baseRun({
      status: "completed",
      outcome: "FAILED",
      error_message: "no outcome reported",
      output: "Report text",
    })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    expect(screen.getByText("A result was recorded, but completion was not confirmed")).toBeTruthy()
    expect(screen.getByText("Result failed")).toBeTruthy()
    expect(screen.getByText(/required completion signal is missing/).closest("details")).toBeNull()
  })

  it("keeps every load error visible with a Retry that refreshes", () => {
    h.error = "network"
    h.approvalError = "approvals: 500"
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const alerts = screen.getAllByRole("alert").map((a) => a.textContent ?? "")
    expect(alerts.some((t) => /Updates unavailable/.test(t))).toBe(true)
    expect(alerts.some((t) => /Could not load or update the decision/.test(t))).toBe(true)
    for (const retry of screen.getAllByRole("button", { name: "Retry" })) fireEvent.click(retry)
    expect(h.refresh).toHaveBeenCalledTimes(2)
  })
})
