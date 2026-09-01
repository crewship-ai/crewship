import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, waitFor } from "@testing-library/react"

// =============================================================================
// F33 (docs/prd/PRD-ISSUES-AND-ROUTINES-2026.md, A6): RunsView's header claims
// it spans "ALL runs in the workspace (routine + ad-hoc agent/chat/user)" but
// it only ever subscribed to run.* — never pipeline.run.*, unlike every other
// pipeline consumer (hooks/use-pipeline-runs.ts, use-active-runs.ts,
// use-trace.ts). A routine firing never repainted this view live. This test
// captures every event type useRealtimeEvent is called with and asserts the
// pipeline.run.* trio is among them.
// =============================================================================

const subscribedTypes: string[] = []
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (type: string) => {
    subscribedTypes.push(type)
  },
}))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))

import { apiFetch } from "@/lib/api-fetch"
import { RunsView } from "@/components/features/journal/runs-view"

beforeEach(() => {
  subscribedTypes.length = 0
  vi.mocked(apiFetch).mockReset()
  vi.mocked(apiFetch).mockImplementation(async (input) => {
    const url = String(input)
    const body = url.includes("/runs/insights")
      ? {
          window: "24h",
          totals: { total: 0, succeeded: 0, failed: 0, running: 0 },
          duration: { p50_ms: 0, p95_ms: 0 },
          by_trigger: [],
          by_model: [],
          by_crew: [],
        }
      : {
          data: [],
          stats: { running: 0, today: 0, failed: 0 },
          pagination: { page: 1, limit: 25, total: 0, total_pages: 1 },
        }
    return { ok: true, status: 200, json: async () => body } as unknown as Response
  })
})

describe("RunsView — realtime subscriptions", () => {
  it("subscribes to pipeline.run.* alongside run.*, matching every other pipeline consumer (F33)", async () => {
    render(<RunsView workspaceId="ws-1" workspaceLoading={false} page={1} onPageChange={vi.fn()} />)

    await waitFor(() => {
      expect(subscribedTypes.length).toBeGreaterThan(0)
    })

    expect(subscribedTypes).toEqual(
      expect.arrayContaining([
        "run.started",
        "run.completed",
        "run.failed",
        "pipeline.run.started",
        "pipeline.run.completed",
        "pipeline.run.failed",
      ]),
    )
  })
})
