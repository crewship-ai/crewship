import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { RoutineRunsTab } from "../routine-runs-tab"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"

// Hoisted holder so the vi.mock factories (which are hoisted above the
// imports) can read per-test state without a TDZ crash.
const h = vi.hoisted(() => ({
  records: [] as unknown[],
  refreshRecords: vi.fn(),
  refreshRuns: vi.fn(),
}))

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(),
}))

vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
}))

vi.mock("@/hooks/use-pipeline-run-records", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-pipeline-run-records")>()),
  usePipelineRunRecords: () => ({
    records: h.records,
    legacy: false,
    loading: false,
    error: null,
    refresh: h.refreshRecords,
  }),
}))

// Journal-backed hook (waterfall source). Empty is fine — the list
// renders from records when legacy=false.
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelineRuns: () => ({ runs: [], loading: false, error: null, refresh: h.refreshRuns }),
}))

const NOW = new Date().toISOString()

function record(overrides: Partial<PipelineRunRecord>): PipelineRunRecord {
  return {
    id: "run-x",
    pipeline_id: "pipe-1",
    pipeline_slug: "daily-report",
    status: "completed",
    mode: "run",
    started_at: NOW,
    cost_usd: 0,
    duration_ms: 0,
    triggered_via: "manual",
    ...overrides,
  }
}

const RUNNING = record({ id: "run-active-1", status: "running", triggered_via: "manual" })
const COMPLETED = record({
  id: "run-done-2",
  status: "completed",
  triggered_via: "schedule",
  duration_ms: 2500,
  cost_usd: 0.1234,
})
const FAILED = record({
  id: "run-fail-3",
  status: "failed",
  triggered_via: "webhook",
  duration_ms: 61000,
  cost_usd: 0.05,
  error_message: "step exploded: connection refused",
})

describe("<RoutineRunsTab> — enriched rows + cancel", () => {
  beforeEach(() => {
    h.records = [RUNNING, COMPLETED, FAILED]
    vi.mocked(apiFetch).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ run_id: "run-active-1", cancel_requested: true }),
      text: async () => "",
    } as unknown as Response)
  })

  it("renders trigger badge, duration and cost on run rows", () => {
    render(<RoutineRunsTab workspaceId="ws-1" slug="daily-report" />)
    // triggered_via badges
    expect(screen.getByText("schedule")).toBeInTheDocument()
    expect(screen.getByText("webhook")).toBeInTheDocument()
    // duration_ms humanized (2500ms → 2.50s) + cost_usd 4dp like LastRunCard
    expect(screen.getByText("2.50s")).toBeInTheDocument()
    expect(screen.getByText("$0.1234")).toBeInTheDocument()
  })

  it("shows the error message for failed runs with the full text as title", () => {
    render(<RoutineRunsTab workspaceId="ws-1" slug="daily-report" />)
    const err = screen.getByText("step exploded: connection refused")
    expect(err).toBeInTheDocument()
    expect(err).toHaveAttribute("title", "step exploded: connection refused")
  })

  it("shows a cancel affordance only for active runs", () => {
    render(<RoutineRunsTab workspaceId="ws-1" slug="daily-report" />)
    // One running record → exactly one cancel button.
    expect(screen.getAllByLabelText("Cancel run")).toHaveLength(1)
  })

  it("shows no cancel affordance when nothing is active", () => {
    h.records = [COMPLETED, FAILED]
    render(<RoutineRunsTab workspaceId="ws-1" slug="daily-report" />)
    expect(screen.queryByLabelText("Cancel run")).not.toBeInTheDocument()
  })

  it("POSTs the cancel endpoint, toasts and refreshes on click", async () => {
    render(<RoutineRunsTab workspaceId="ws-1" slug="daily-report" />)
    fireEvent.click(screen.getByLabelText("Cancel run"))
    await waitFor(() => {
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws-1/pipelines/runs/run-active-1/cancel",
        expect.objectContaining({ method: "POST" }),
      )
      expect(toast.success).toHaveBeenCalled()
      expect(h.refreshRecords).toHaveBeenCalled()
      expect(h.refreshRuns).toHaveBeenCalled()
    })
  })

  it("surfaces a permission toast when the server returns 403", async () => {
    vi.mocked(apiFetch).mockResolvedValue({
      ok: false,
      status: 403,
      statusText: "Forbidden",
      json: async () => ({}),
      text: async () => "Forbidden",
    } as unknown as Response)
    render(<RoutineRunsTab workspaceId="ws-1" slug="daily-report" />)
    fireEvent.click(screen.getByLabelText("Cancel run"))
    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith(
        "Cancel failed",
        expect.objectContaining({ description: expect.stringMatching(/permission/i) }),
      )
    })
  })
})
