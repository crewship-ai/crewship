import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => undefined }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))

import { apiFetch } from "@/lib/api-fetch"
import { RunsView } from "@/components/features/journal/runs-view"

// The Runs tab lists routine runs next to agent runs (#2284), but its KPI and
// breakdown cards read /runs/insights, which counts agent runs only. On a
// workspace whose work is routines that drew "Runs 0" over a table of runs,
// three "No data yet" cards, and every routine row named "Unknown" with the
// same "#run_cmul" id.

function row(id: string, over: Record<string, unknown> = {}) {
  return {
    id,
    agent_id: "",
    status: "COMPLETED",
    trigger_type: "WEBHOOK",
    kind: "pipeline",
    started_at: "2026-08-01T10:00:00Z",
    finished_at: "2026-08-01T10:00:05Z",
    error_message: null,
    exit_code: 0,
    created_at: "2026-08-01T10:00:00Z",
    triggerer: null,
    ...over,
  }
}

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response
}

function mockFeeds(rows: unknown[], insightsTotal: number, live: unknown[] = []) {
  vi.mocked(apiFetch).mockImplementation((input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes("/runs/insights")) {
      return Promise.resolve(
        okJSON({
          totals: { total: insightsTotal, succeeded: insightsTotal, failed: 0, running: 0 },
          duration: { p50_ms: 0, p95_ms: 0 },
          window: "24h",
          by_trigger: [],
          by_crew: [],
          by_model: [],
          top_agents: [],
          truncated: false,
        }),
      )
    }
    if (url.includes("status=RUNNING")) return Promise.resolve(okJSON({ data: live }))
    return Promise.resolve(
      okJSON({
        data: rows,
        stats: { running: 0, today: 0, failed: 0 },
        pagination: { page: 1, limit: 25, total: rows.length, total_pages: 1 },
      }),
    )
  })
}

describe("RunsView presentation", () => {
  beforeEach(() => vi.mocked(apiFetch).mockReset())

  it("says in one line that the figures count agent runs when there are none, instead of zero tiles", async () => {
    mockFeeds([row("run_cmulpp3pt0001c39add54")], 0)
    render(<RunsView workspaceId="ws" workspaceLoading={false} />)
    const line = await screen.findByTestId("runs-no-agent-runs")
    expect(line).toHaveTextContent(/No agent runs in the last 24h/i)
    expect(screen.queryByText("No data yet")).toBeNull()
    expect(screen.queryByText("Median duration")).toBeNull()
  })

  it("names a routine row as a routine run, with a short id that differs per row", async () => {
    mockFeeds([row("run_cmulpp3pt0001c39add54"), row("run_cmulq28h8000320b937e3")], 0)
    render(<RunsView workspaceId="ws" workspaceLoading={false} />)
    await waitFor(() => expect(screen.getAllByText("Routine run")).toHaveLength(2))
    expect(screen.queryByText("Unknown")).toBeNull()
    expect(screen.getByText("#add54")).toBeInTheDocument()
    expect(screen.getByText("#937e3")).toBeInTheDocument()
  })

  it("counts the live strip by the rows it lists, not by the agent-only insights", async () => {
    // insights.totals.running is agent runs only; the strip lists routine runs too.
    mockFeeds([], 0, [row("run_a1", { status: "RUNNING", finished_at: null }), row("run_b2", { status: "RUNNING", finished_at: null })])
    render(<RunsView workspaceId="ws" workspaceLoading={false} />)
    expect(await screen.findByText("2 live executions")).toBeInTheDocument()
  })
})
