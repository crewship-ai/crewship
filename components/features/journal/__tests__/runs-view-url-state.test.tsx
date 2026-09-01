import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

// =============================================================================
// The Runs tab's window / status / trigger / page were component state, so a
// Runs view could not be sent to anyone — "the CRON runs that failed this
// week" was a list of buttons to press, not a link. /journal drives all four
// from the URL now; RunsView takes them as controlled props, and keeps its own
// state when nobody passes them (the LogsPanel severity/muted/live idiom).
//
// The page reset moved out of an effect on [statusFilter, triggerFilter] and
// into the setters: with a controlled parent, an effect would have written the
// filter and the reset as two navigations, so Back would land on page 3 of the
// new filter — a view the user never saw.
// =============================================================================

vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => undefined }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { RunsView } from "@/components/features/journal/runs-view"

function run(overrides: Record<string, unknown> = {}) {
  return {
    id: "run_aabbccdd-1122",
    agent_id: "ag-1",
    status: "FAILED",
    trigger_type: "CRON",
    started_at: "2026-08-01T10:00:00Z",
    finished_at: "2026-08-01T10:00:30Z",
    error_message: null,
    exit_code: 1,
    created_at: "2026-08-01T10:00:00Z",
    agent_name: "Casey",
    agent_slug: "casey",
    crew_name: "Quality",
    triggerer: null,
    ...overrides,
  }
}

const INSIGHTS = {
  window: "24h",
  totals: { total: 3, succeeded: 1, failed: 2, running: 0 },
  duration: { p50_ms: 1000, p95_ms: 2000 },
  by_trigger: [],
  by_model: [],
  by_crew: [],
}

/** Every /api/v1/runs URL the component asked for (not insights, not live). */
function tableRequests(): string[] {
  return vi
    .mocked(apiFetch)
    .mock.calls.map((c) => String(c[0]))
    .filter((u) => u.includes("/api/v1/runs?") && !u.includes("status=RUNNING"))
}

beforeEach(() => {
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async (input) => {
    const url = String(input)
    const body = url.includes("/runs/insights")
      ? INSIGHTS
      : {
          data: [run()],
          stats: { running: 0, today: 1, failed: 1 },
          pagination: { page: 1, limit: 25, total: 30, total_pages: 2 },
        }
    return { ok: true, status: 200, json: async () => body } as unknown as Response
  })
})

describe("RunsView — controlled filter state", () => {
  it("fetches what the controlled props name, not its own defaults", async () => {
    render(
      <RunsView
        workspaceId="ws-1"
        workspaceLoading={false}
        window="7d"
        onWindowChange={vi.fn()}
        statusFilter="FAILED"
        onStatusFilterChange={vi.fn()}
        triggerFilter="CRON"
        onTriggerFilterChange={vi.fn()}
        page={2}
        onPageChange={vi.fn()}
      />,
    )

    await waitFor(() => {
      expect(tableRequests().length).toBeGreaterThan(0)
    })
    const url = tableRequests()[0]
    expect(url).toContain("status=FAILED")
    expect(url).toContain("trigger=CRON")
    expect(url).toContain("page=2")
    await waitFor(() => {
      expect(
        vi.mocked(apiFetch).mock.calls.some((c) => String(c[0]).includes("window=7d")),
      ).toBe(true)
    })
  })

  it("reports a status change upward instead of keeping it", async () => {
    const onStatusFilterChange = vi.fn()
    render(
      <RunsView
        workspaceId="ws-1"
        workspaceLoading={false}
        statusFilter="all"
        onStatusFilterChange={onStatusFilterChange}
        page={3}
        onPageChange={vi.fn()}
      />,
    )

    fireEvent.click(await screen.findByRole("button", { name: "Failed" }))
    expect(onStatusFilterChange).toHaveBeenCalledWith("FAILED")
    // The parent owns the page reset; RunsView must not also fire a page
    // change, or a controlled parent gets two navigations for one click.
    expect(tableRequests().every((u) => u.includes("page=3"))).toBe(true)
  })

  it("reports a window change upward", async () => {
    const onWindowChange = vi.fn()
    render(
      <RunsView
        workspaceId="ws-1"
        workspaceLoading={false}
        window="24h"
        onWindowChange={onWindowChange}
      />,
    )

    fireEvent.click(await screen.findByRole("button", { name: "7d" }))
    expect(onWindowChange).toHaveBeenCalledWith("7d")
  })
})

describe("RunsView — uncontrolled fallback", () => {
  it("still resets its own page when a filter changes", async () => {
    render(<RunsView workspaceId="ws-1" workspaceLoading={false} />)

    await screen.findByText("Next")
    fireEvent.click(screen.getByText("Next"))
    await waitFor(() => {
      expect(tableRequests().some((u) => u.includes("page=2"))).toBe(true)
    })

    fireEvent.click(screen.getByRole("button", { name: "Failed" }))
    await waitFor(() => {
      const last = tableRequests()[tableRequests().length - 1]
      expect(last).toContain("status=FAILED")
      expect(last).toContain("page=1")
    })
  })
})
