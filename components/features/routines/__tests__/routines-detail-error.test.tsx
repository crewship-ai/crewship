import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
  useRealtime: () => ({ connected: true }),
}))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: "ADMIN" }) }))
vi.mock("@/hooks/use-pending-approval", () => ({
  usePendingApproval: () => ({ waitpoint: null, deciding: false, decide: vi.fn() }),
}))
vi.mock("@/hooks/use-pipeline-run-records", () => ({
  usePipelineRunRecords: () => ({ records: [], refresh: vi.fn() }),
  isActiveRunStatus: () => false,
}))
vi.mock("../routine-card-detail", () => ({ RoutineCardDetail: () => <div data-testid="card" /> }))

import { RoutinesDetailPanel } from "../routines-detail-panel"

// The fetch-error banner was unreachable. fetchRoutine sets the error
// and then clears `routine`; the banner sat inside the `{routine && …}`
// guard, so the one state it exists for is the one state it could not
// render in. Loading is false by then too, so a failed fetch showed an
// empty surface and no explanation at all.
//
// Found by CodeRabbit on the PR.

describe("<RoutinesDetailPanel> when the fetch fails", () => {
  beforeEach(() => apiFetch.mockReset())

  it("says so instead of showing an empty panel", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 500, json: async () => ({}) })
    render(
      <RoutinesDetailPanel workspaceId="ws-1" slug="nightly" onClose={vi.fn()} onChanged={vi.fn()} />,
    )
    await waitFor(() => expect(screen.getByText(/fetch routine: 500/i)).toBeInTheDocument())
  })

  it("does not render the card it could not load", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 404, json: async () => ({}) })
    render(
      <RoutinesDetailPanel workspaceId="ws-1" slug="gone" onClose={vi.fn()} onChanged={vi.fn()} />,
    )
    await waitFor(() => expect(screen.getByText(/fetch routine: 404/i)).toBeInTheDocument())
    expect(screen.queryByTestId("card")).not.toBeInTheDocument()
  })
})
