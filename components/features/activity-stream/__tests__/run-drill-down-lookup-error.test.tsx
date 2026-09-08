import * as React from "react"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
vi.mock("@/components/features/activity/run-activity-timeline", () => ({ RunActivityTimeline: () => <div data-testid="run-timeline" /> }))
vi.mock("@/components/features/activity/trace-canvas", () => ({ TraceCanvas: () => <div data-testid="historical-graph" /> }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: vi.fn() }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "OWNER" }) }))
vi.mock("@/hooks/use-pending-approval", () => ({ usePendingApproval: () => ({ waitpoint: null, error: null, refresh: vi.fn() }) }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
import { RunDrillDown } from "../drill-downs"
describe("Shared run detail", () => {
  it("loads the run directly and offers a retry when its endpoint fails", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 503 })
    render(<RunDrillDown workspaceId="ws_1" runID="old_run" />)
    await screen.findByText("Could not load this run.")
    expect(apiFetch.mock.calls.every(([url]) => String(url).includes("/pipeline-runs/old_run"))).toBe(true)
    const before = apiFetch.mock.calls.length
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    await waitFor(() => expect(apiFetch.mock.calls.length).toBe(before + 1))
  })
})
