import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { LiveRoutinesChip } from "../live-routines-chip"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"
import { deriveActiveRoutineRuns } from "@/hooks/use-active-routine-runs"

// Hoisted holder so the vi.mock factories can read per-test state.
const h = vi.hoisted(() => ({
  runs: [] as unknown[],
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

// The chip consumes the shared hook — feed it the same derivation the
// provider would produce, over per-test rows.
vi.mock("@/hooks/use-active-routine-runs", async (importOriginal) => {
  const mod = await importOriginal<typeof import("@/hooks/use-active-routine-runs")>()
  return {
    ...mod,
    useActiveRoutineRuns: () => ({
      ...mod.deriveActiveRoutineRuns(h.runs as PipelineRun[]),
      loading: false,
      error: null,
      refresh: h.refresh,
    }),
  }
})

// next/link → plain anchor so href assertions are trivial in jsdom.
vi.mock("next/link", () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: ({ href, children, ...rest }: any) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
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

function openPopover() {
  fireEvent.click(screen.getByRole("button", { name: /routines? running/i }))
}

describe("<LiveRoutinesChip>", () => {
  beforeEach(() => {
    h.runs = []
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ cancel_requested: true }),
      text: async () => "",
    } as unknown as Response)
  })

  it("renders nothing when no routine runs are active", () => {
    const { container } = render(<LiveRoutinesChip />)
    expect(container).toBeEmptyDOMElement()
  })

  it("shows the active run count", () => {
    h.runs = [run({ id: "r1" }), run({ id: "r2", pipeline_slug: "digest" })]
    render(<LiveRoutinesChip />)
    expect(screen.getByText("2 routines running")).toBeInTheDocument()
  })

  it("uses the singular form for one run", () => {
    h.runs = [run({ id: "r1" })]
    render(<LiveRoutinesChip />)
    expect(screen.getByText("1 routine running")).toBeInTheDocument()
  })

  it("appends the awaiting-approval count when a run is parked", () => {
    h.runs = [run({ id: "r1" }), run({ id: "r2", status: "waiting" })]
    render(<LiveRoutinesChip />)
    expect(screen.getByText("2 routines running · 1 awaiting approval")).toBeInTheDocument()
  })

  it("shows run rows with current step, trace link and waiting hint in the popover", async () => {
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
    render(<LiveRoutinesChip />)
    openPopover()

    await waitFor(() => {
      expect(screen.getByText("Classify support ticket")).toBeInTheDocument()
    })
    // Current step id rendered for the running row.
    expect(screen.getByText(/ask-casey/)).toBeInTheDocument()
    // Waiting row gets the amber hint + Review link into the routine.
    // Exact string — the chip label also contains "awaiting approval"
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

  it("caps the popover at 6 rows and links 'View all N running →' when more are active", async () => {
    h.runs = Array.from({ length: 8 }, (_, i) =>
      run({
        id: `r${i}`,
        pipeline_slug: `routine-${i}`,
        pipeline_name: `Routine ${i}`,
        started_at: new Date(Date.now() - i * 1000).toISOString(),
      }),
    )
    render(<LiveRoutinesChip />)
    openPopover()

    await waitFor(() => {
      expect(screen.getAllByRole("link", { name: /open trace/i })).toHaveLength(6)
    })
    const viewAll = screen.getByRole("link", { name: "View all 8 running →" })
    expect(viewAll).toHaveAttribute("href", "/activity?status=active")
  })

  it("hides the view-all footer at 6 or fewer active runs", async () => {
    h.runs = [run({ id: "r1" })]
    render(<LiveRoutinesChip />)
    openPopover()
    await waitFor(() => {
      expect(screen.getAllByRole("link", { name: /open trace/i })).toHaveLength(1)
    })
    expect(screen.queryByRole("link", { name: /view all/i })).not.toBeInTheDocument()
  })

  it("POSTs the workspace-scoped cancel endpoint and refreshes", async () => {
    h.runs = [run({ id: "r-cancel-me" })]
    render(<LiveRoutinesChip />)
    openPopover()

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
    render(<LiveRoutinesChip />)
    openPopover()

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
