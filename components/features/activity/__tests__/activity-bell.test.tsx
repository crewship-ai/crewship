import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { ActivityBell } from "../activity-bell"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import type { ActiveRunItem } from "@/hooks/use-active-runs"
import { deriveActiveRoutineRuns } from "@/hooks/use-active-routine-runs"

// Retargeted from the removed header LiveRoutinesChip (feedback
// 2026-07-02): live routine runs now surface inside the existing
// Activity dropdown — badge on the icon, LIVE + RECENT sections in
// the ~400px panel. Same data layer (useActiveRoutineRuns), same
// cancel contract, so the chip's assertions carry over.

// Radix DropdownMenu relies on pointer-capture + scrollIntoView, which
// happy-dom doesn't implement. Polyfill so the menu can open here.
beforeEach(() => {
  if (!Element.prototype.hasPointerCapture) {
    Element.prototype.hasPointerCapture = () => false
  }
  if (!Element.prototype.releasePointerCapture) {
    Element.prototype.releasePointerCapture = () => {}
  }
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {}
  }
})

// Hoisted holder so the vi.mock factories can read per-test state.
const h = vi.hoisted(() => ({
  runs: [] as unknown[],
  agentItems: [] as unknown[],
  refresh: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

// importOriginal on use-active-routine-runs drags in use-realtime →
// use-websocket; stub the realtime layer so the mock chain stays flat.
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-1" }),
}))

// The dropdown consumes the shared hook — feed it the same derivation
// the provider would produce, over per-test rows.
vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return {
    ...mod,
    useActiveRoutineRuns: () => ({
      ...mod.deriveActiveRoutineRuns(h.runs as PipelineRun[]),
      recentRuns: mod.deriveRecentTerminalRuns(h.runs as PipelineRun[]),
      loading: false,
      error: null,
      refresh: h.refresh,
    }),
  }
})

// Agent runs still come from the legacy active-runs feed.
vi.mock("@/hooks/use-active-runs", () => ({
  useActiveRuns: () => ({
    runs: h.agentItems as ActiveRunItem[],
    count: (h.agentItems as ActiveRunItem[]).length,
    loading: false,
  }),
}))

// next/link → plain anchor so href assertions are trivial.
vi.mock("next/link", () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: ({ href, children, ...rest }: any) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}))

// motion renders spread animation props into the DOM in tests; strip them.
vi.mock("motion/react", () => ({
  AnimatePresence: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  motion: {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    span: ({ children, initial: _i, animate: _a, exit: _e, ...rest }: any) => (
      <span {...rest}>{children}</span>
    ),
  },
}))

function run(overrides: Partial<PipelineRun>): PipelineRun {
  return {
    id: "run_cmr3gd000000000000000",
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

function openDropdown() {
  // Radix DropdownMenu opens on pointerdown (primary button), not click.
  const trigger = screen.getByRole("button", { name: /^activity/i })
  fireEvent.pointerDown(trigger, { button: 0, ctrlKey: false })
}

describe("<ActivityBell> badge", () => {
  beforeEach(() => {
    h.runs = []
    h.agentItems = []
  })

  it("hides the badge when nothing is live", () => {
    render(<ActivityBell />)
    expect(screen.queryByTestId("activity-live-badge")).not.toBeInTheDocument()
  })

  it("badges the live routine-run count in blue", () => {
    h.runs = [run({ id: "r1" }), run({ id: "r2", pipeline_slug: "digest" })]
    render(<ActivityBell />)
    const badge = screen.getByTestId("activity-live-badge")
    expect(badge).toHaveTextContent("2")
    expect(badge.className).toContain("bg-blue-500")
  })

  it("turns the badge amber when a run awaits approval", () => {
    h.runs = [run({ id: "r1" }), run({ id: "r2", status: "waiting" })]
    render(<ActivityBell />)
    const badge = screen.getByTestId("activity-live-badge")
    expect(badge).toHaveTextContent("2")
    expect(badge.className).toContain("bg-amber-500")
  })

  it("keeps the emerald badge semantics for agent-only activity", () => {
    h.agentItems = [
      // Agent runs deep-link to the agent's chat, not the pipeline trace (#846).
      { id: "a1", kind: "agent", label: "Casey", href: "/chat/casey" },
    ]
    render(<ActivityBell />)
    const badge = screen.getByTestId("activity-live-badge")
    expect(badge).toHaveTextContent("1")
    expect(badge.className).toContain("bg-emerald-500")
  })
})

describe("<ActivityBell> dropdown", () => {
  beforeEach(() => {
    h.runs = []
    h.agentItems = []
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ cancel_requested: true }),
      text: async () => "",
    } as unknown as Response)
  })

  it("shows LIVE rows with current step, cost, review + trace links", async () => {
    h.runs = [
      run({ id: "r1" }),
      run({
        id: "r2",
        pipeline_slug: "approval-gate",
        pipeline_name: "Approval gate demo",
        status: "waiting",
        current_step_id: "wait-for-human",
      }),
    ]
    render(<ActivityBell />)
    openDropdown()

    await waitFor(() => {
      expect(screen.getByText("Classify support ticket")).toBeInTheDocument()
    })
    // Current step id rendered for the running row.
    expect(screen.getByText(/ask-casey/)).toBeInTheDocument()
    // Cost is on the row meta (mono, right).
    expect(screen.getAllByText(/\$0\.0110/).length).toBeGreaterThan(0)
    // Waiting row gets the amber hint + Review link into the routine.
    // Exact string — the dropdown header contains "awaiting approval"
    // as part of its longer count text.
    expect(screen.getByText("awaiting approval")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /review/i })).toHaveAttribute(
      "href",
      "/routines?slug=approval-gate",
    )
    // Trace deep-link per row.
    const traceLinks = screen.getAllByRole("link", { name: /open trace/i })
    expect(traceLinks[0]).toHaveAttribute("href", "/activity?run=r1")
  })

  it("caps LIVE at 6 rows and deep-links the footer to the active bucket", async () => {
    h.runs = Array.from({ length: 8 }, (_, i) =>
      run({
        id: `r${i}`,
        pipeline_slug: `routine-${i}`,
        pipeline_name: `Routine ${i}`,
        started_at: new Date(Date.now() - i * 1000).toISOString(),
      }),
    )
    render(<ActivityBell />)
    openDropdown()

    await waitFor(() => {
      expect(screen.getAllByRole("link", { name: /open trace/i })).toHaveLength(6)
    })
    const viewAll = screen.getByRole("link", { name: /view all activity/i })
    expect(viewAll).toHaveAttribute("href", "/activity?status=active")
  })

  it("renders the RECENT section with terminal runs", async () => {
    h.runs = [
      run({ id: "live" }),
      run({
        id: "done-1",
        pipeline_slug: "cost-spike-probe",
        pipeline_name: "Cost spike probe",
        status: "completed",
        ended_at: new Date(Date.now() - 12 * 60_000).toISOString(),
        cost_usd: 0.0001,
      }),
      run({
        id: "boom-1",
        pipeline_slug: "flaky-sync",
        pipeline_name: "Flaky sync",
        status: "failed",
        ended_at: new Date(Date.now() - 30 * 60_000).toISOString(),
        cost_usd: 0.02,
      }),
    ]
    render(<ActivityBell />)
    openDropdown()

    await waitFor(() => {
      expect(screen.getByText("Cost spike probe")).toBeInTheDocument()
    })
    expect(screen.getByText("Flaky sync")).toBeInTheDocument()
    expect(screen.getByText(/completed · 12m ago · \$0\.0001/)).toBeInTheDocument()
    expect(screen.getByText(/failed · 30m ago/)).toBeInTheDocument()
  })

  it("links the footer to plain /activity when nothing is live", async () => {
    h.runs = [
      run({
        id: "done-1",
        status: "completed",
        ended_at: new Date(Date.now() - 5 * 60_000).toISOString(),
      }),
    ]
    render(<ActivityBell />)
    openDropdown()

    const viewAll = await screen.findByRole("link", { name: /view all activity/i })
    expect(viewAll).toHaveAttribute("href", "/activity")
  })

  it("POSTs the workspace-scoped cancel endpoint and refreshes", async () => {
    h.runs = [run({ id: "r-cancel-me" })]
    render(<ActivityBell />)
    openDropdown()

    const cancelBtn = await screen.findByRole("button", { name: /cancel run/i })
    fireEvent.click(cancelBtn)

    await waitFor(() => {
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws-1/pipelines/runs/r-cancel-me/cancel",
        expect.objectContaining({ method: "POST" }),
      )
      expect(toast.success).toHaveBeenCalled()
      expect(h.refresh).toHaveBeenCalled()
    })
  })

  it("surfaces a permission toast when cancel returns 403", async () => {
    vi.mocked(apiFetch).mockResolvedValue({
      ok: false,
      status: 403,
      statusText: "Forbidden",
      json: async () => ({}),
      text: async () => "Forbidden",
    } as unknown as Response)
    h.runs = [run({ id: "r-denied" })]
    render(<ActivityBell />)
    openDropdown()

    const cancelBtn = await screen.findByRole("button", { name: /cancel run/i })
    fireEvent.click(cancelBtn)

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(
        "Cancel failed",
        expect.objectContaining({ description: expect.stringMatching(/permission/i) }),
      )
    })
  })
})

// Sanity: the derivation the mock uses matches what the real provider
// exports (guards against the mock drifting from the hook contract).
describe("mock parity", () => {
  it("deriveActiveRoutineRuns is importable and pure", () => {
    const d = deriveActiveRoutineRuns([run({ id: "x" })])
    expect(d.activeCount).toBe(1)
  })
})
