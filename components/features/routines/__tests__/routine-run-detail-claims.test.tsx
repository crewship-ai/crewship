import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"
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
  traceRefresh: vi.fn(),
  approvalRefresh: vi.fn(),
  push: vi.fn(),
  agents: [] as Record<string, unknown>[],
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {} }))
vi.mock("@/hooks/use-workspace-agent-directory", () => ({
  useWorkspaceAgentDirectory: () => ({ agents: h.agents, error: false }),
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
    refresh: h.traceRefresh,
  }),
}))
vi.mock("@/hooks/use-pending-approval", () => ({
  usePendingApproval: () => ({
    waitpoint: h.waitpoint,
    deciding: false,
    decide: vi.fn(),
    refresh: h.approvalRefresh,
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
  h.agents = []
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
    // One word everywhere: Stop, in the header and in the banner.
    expect(screen.getAllByRole("button", { name: "Stop" })).toHaveLength(2)
    expect(screen.queryByRole("button", { name: "Stop run" })).toBeNull()
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
  await waitFor(() => expect(screen.getByRole("button", { name: "Run now" })).not.toBeDisabled())
  fireEvent.click(screen.getByRole("button", { name: "Run now" }))
  await screen.findByText("Response lost")
  fireEvent.click(screen.getByRole("button", { name: "Run now" }))
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
  it("leads with a way back to the routine and the list, even when deep-linked", () => {
    h.run = baseRun({ pipeline_slug: "daily-triage", pipeline_name: "Daily triage" })
    render(<RoutineRunDetail workspaceId="ws1" runId="run-1" />)
    const crumbs = screen.getByRole("navigation", { name: "Run location" })
    expect(within(crumbs).getByRole("link", { name: "Routines" })).toHaveAttribute("href", "/routines")
    expect(within(crumbs).getByRole("link", { name: "Daily triage" })).toHaveAttribute(
      "href",
      "/routines?slug=daily-triage",
    )
  })

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

    // Without the server's failure projection there is no plain reason, so
    // today's words stay: the verdict, the failed step and the raw error.
    const verdict = screen.getByText("This run could not finish")
    const error = screen.getByRole("alert")
    expect(error.textContent).toBe("Agent did not answer")
    expect(error.parentElement?.textContent).toContain("Failed step")
    expect(error.parentElement?.textContent).toContain("write")
    expect(order(verdict, error)).toBe(-1)
    expect(order(error, screen.getByTestId("run-artifacts"))).toBe(-1)
    expect(order(error, screen.getByTestId("run-technical-details"))).toBe(-1)

    // What to do next: three branches, each with its own way out.
    const next = screen.getByTestId("run-next-step")
    expect(next.textContent).toContain("If the input was wrong")
    expect(next.textContent).toContain("If a rule is too strict")
    expect(next.textContent).toContain("Keeps failing?")
    expect(next.textContent).toContain("crewship routine draft monthly-billing")
    expect(order(error, next)).toBe(-1)
    expect(screen.queryByRole("link", { name: "Edit recipe" })).toBeNull()
    expect(within(next).getByRole("link", { name: "History" }).getAttribute("href")).toBe(
      "/routines?slug=monthly-billing&view=history",
    )
    // No lead in the directory: the link goes to the chat and the prompt is copied.
    expect(screen.getByRole("link", { name: "Ask the lead to fix it" }).getAttribute("href")).toBe("/chat")
    // No per-step retry is offered; "run again" is the whole-run action.
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull()
    fireEvent.click(within(next).getByRole("button", { name: "run again" }))
    expect(vi.mocked(apiFetch).mock.calls[0][0]).toContain("/pipelines/monthly-billing")
    // The raw error is also in Technical details, so it is never lost.
    expect(screen.getByTestId("run-raw-error").textContent).toBe("Agent did not answer")
  })

  it("uses the server's failure projection: plain reason, Kept / Not done, raw error only in Technical details", () => {
    h.dsl = {
      steps: [
        { id: "extract", name: "Read the invoice", type: "agent_run" },
        { id: "verify", name: "Check the extraction", type: "agent_run" },
        { id: "post", name: "Post to the ledger", type: "script" },
      ],
    }
    h.run = baseRun({
      status: "failed",
      failed_at_step: "verify",
      error_message: 'step verify: checker rejected: criterion "total_equals_lines"',
      failure: {
        kind: "checker_rejected",
        step_id: "verify",
        step_name: "Check the extraction",
        summary: "The checker rejected the result after exhausting the allowed model tiers: total_equals_lines.",
        kept_step_ids: ["extract"],
        not_done_step_ids: ["post"],
      },
    })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const banner = screen.getByTestId("run-banner")
    expect(banner).toHaveAttribute("data-tone", "destructive")
    expect(within(banner).getByRole("heading").textContent).toBe("Stopped at step 2, “Check the extraction”")
    expect(banner.textContent).toContain("The checker rejected the result after exhausting the allowed model tiers: total_equals_lines.")
    expect(banner.textContent).toContain("Kept: Read the invoice. Not done: Post to the ledger.")
    // The engine's message is not in the banner, but it is kept in Technical details.
    expect(banner.textContent).not.toContain("criterion")
    expect(screen.queryByRole("alert")).toBeNull()
    const details = screen.getByTestId("run-technical-details")
    expect(details.contains(screen.getByTestId("run-raw-error"))).toBe(true)
    expect(details.textContent).toContain('criterion "total_equals_lines"')
    expect(details.textContent).toContain("checker_rejected")
    expect(screen.getByRole("link", { name: "Ask the lead to fix it" }).getAttribute("href")).toBe("/chat")
  })

  it("sends Ask the lead to the crew lead's chat with the run and the step in the prompt", () => {
    h.agents = [
      { id: "a1", slug: "worker", name: "Worker", crew_id: "crew_fin", agent_role: "AGENT" },
      { id: "a2", slug: "nora-lead", name: "Nora", crew_id: "crew_fin", agent_role: "LEAD" },
    ]
    h.run = baseRun({
      status: "failed",
      invoking_crew_id: "crew_fin",
      failed_at_step: "write",
      failure: {
        kind: "timeout",
        step_id: "write",
        step_name: "Write the report",
        summary: "The step did not finish within 10 minutes.",
      },
    })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    const href = screen.getByRole("link", { name: "Ask the lead to fix it" }).getAttribute("href")!
    expect(href.startsWith("/chat/nora-lead?prompt=")).toBe(true)
    const prompt = decodeURIComponent(href.split("?prompt=")[1])
    expect(prompt).toContain("Run run_1")
    expect(prompt).toContain('step "Write the report"')
    expect(prompt).toContain("The step did not finish within 10 minutes.")
    expect(prompt).toContain("save_routine_draft")
  })

  it("names who needs to decide, why, and when it expires while a run waits", () => {
    h.dsl = {
      steps: [
        { id: "extract", name: "Read the invoice", type: "agent_run" },
        { id: "decide", name: "Ask Finance", type: "wait", wait: { kind: "approval", approval_title: "Finance" } },
      ],
    }
    h.run = baseRun({ status: "waiting", current_step_id: "decide" })
    h.waitpoint = {
      step_id: "decide",
      token: "t",
      prompt: "The total is over the auto-approve limit.",
      timeout_at: new Date(Date.now() + 90 * 60_000).toISOString(),
    }
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const banner = screen.getByTestId("run-banner")
    expect(banner).toHaveAttribute("data-tone", "warn")
    expect(within(banner).getByRole("heading").textContent).toBe("Finance needs to decide")
    expect(banner.textContent).toContain(
      "The total is over the auto-approve limit. Nothing after this step has happened yet. This is the same decision shown in Inbox. Expires in 1 h 30 min.",
    )
    // The decision form stays below the banner.
    expect(order(banner, screen.getByTestId("approval-banner"))).toBe(-1)
  })

  it("says where the work is while it runs, and offers Stop with a confirmation", () => {
    h.dsl = {
      steps: [
        { id: "extract", name: "Read the invoice", type: "agent_run" },
        { id: "verify", name: "Check the extraction", type: "agent_run" },
      ],
    }
    h.run = baseRun({ status: "running", current_step_id: "verify" })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const banner = screen.getByTestId("run-banner")
    expect(banner).toHaveAttribute("data-tone", "blue")
    expect(within(banner).getByRole("heading").textContent).toBe(
      "Work in progress · step 2 of 2, “Check the extraction”",
    )
    fireEvent.click(within(banner).getByRole("button", { name: "Stop" }))
    expect(screen.getByRole("dialog").textContent).toContain("Stop this run?")
    expect(screen.getByRole("button", { name: "Keep running" })).toBeTruthy()
  })

  it("says what a completed run did, skipped and notified", () => {
    h.dsl = {
      steps: [
        { id: "extract", name: "Read the invoice", type: "agent_run" },
        { id: "decide", name: "Ask Finance", type: "wait", if: "total > limit" },
        { id: "notify", name: "Tell #finance", type: "notify", notify: { to: "#finance" } },
      ],
    }
    h.run = baseRun({
      status: "completed",
      step_outputs: { extract: "ok", notify: "sent" },
      step_outputs_available: true,
    })
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const banner = screen.getByTestId("run-banner")
    expect(banner).toHaveAttribute("data-tone", "success")
    expect(within(banner).getByRole("heading").textContent).toBe("Done · Completed")
    expect(banner.textContent).toContain("Skipped: Ask Finance (its condition was not met). Notified #finance.")
    expect(screen.queryByTestId("run-next-step")).toBeNull()
  })

  it("says when a newer version is published than the one this run used", () => {
    h.run = baseRun({ pipeline_version: 2 })
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      json: async () => ({ slug: "monthly-billing", head_version: 3, definition: { inputs: [] } }),
    } as Response)
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)
    return waitFor(() =>
      expect(screen.getByTestId("run-facts").textContent).toContain("recipe v2 ↗ (v3 is published now)"),
    )
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
    expect(screen.queryByRole("link", { name: "Ask the lead to fix it" })).toBeNull()
  })

  it("keeps the decision directly under the verdict, before the result", () => {
    h.run = baseRun({ status: "waiting" })
    h.waitpoint = { step_id: "write", token: "t", inbox_item_id: "inb_1" }
    render(<RoutineRunDetail workspaceId="ws" runId="run_1" />)

    const banner = screen.getByTestId("approval-banner")
    expect(order(screen.getByText("A person needs to decide"), banner)).toBe(-1)
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
    // Each Retry reaches its own hook, not "two calls somewhere".
    expect(h.traceRefresh).toHaveBeenCalledTimes(1)
    expect(h.approvalRefresh).toHaveBeenCalledTimes(1)
  })
})
