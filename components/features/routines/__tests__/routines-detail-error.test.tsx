import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, waitFor, fireEvent } from "@testing-library/react"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn(), replace: vi.fn() }), useSearchParams: () => new URLSearchParams() }))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: () => {},
  useRealtimeEventSafe: () => {},
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
vi.mock("../routine-page", () => ({ RoutinePage: () => <div data-testid="page" /> }))

import { RoutinesDetailPanel } from "../routines-detail-panel"

// R12: a routine that cannot be shown says why, in the shared page states —
// gone (with the way back), not yours to open, or a failure with Retry. It
// never renders the page it could not load.

describe("<RoutinesDetailPanel> when the fetch fails", () => {
  beforeEach(() => apiFetch.mockReset())

  it("5xx shows Retry and the request that failed", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 500, json: async () => ({}) })
    render(<RoutinesDetailPanel workspaceId="ws-1" slug="nightly" onClose={vi.fn()} onChanged={vi.fn()} />)
    expect(await screen.findByText("This routine could not load")).toBeInTheDocument()
    expect(screen.getByText(/fetch routine: 500/i)).toBeInTheDocument()
    const before = apiFetch.mock.calls.length
    fireEvent.click(screen.getByRole("button", { name: "Retry" }))
    await waitFor(() => expect(apiFetch.mock.calls.length).toBeGreaterThan(before))
    expect(screen.queryByTestId("page")).not.toBeInTheDocument()
  })

  it("404 shows no-longer-exists with Back, and not the page", async () => {
    apiFetch.mockImplementation(async (url: string) =>
      String(url).endsWith("/draft")
        ? { ok: false, status: 404, json: async () => ({ error: "not found" }) }
        : { ok: false, status: 404, json: async () => ({}) },
    )
    const onClose = vi.fn()
    render(<RoutinesDetailPanel workspaceId="ws-1" slug="gone" onClose={onClose} onChanged={vi.fn()} />)
    expect(await screen.findByText("This routine doesn’t exist")).toBeInTheDocument()
    expect(screen.getByText(/Runs it already made stay readable in Activity/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Back to routines" }))
    expect(onClose).toHaveBeenCalled()
    expect(screen.queryByTestId("page")).not.toBeInTheDocument()
  })

  it("403 says it cannot be opened", async () => {
    apiFetch.mockResolvedValue({ ok: false, status: 403, json: async () => ({}) })
    render(<RoutinesDetailPanel workspaceId="ws-1" slug="secret" onClose={vi.fn()} onChanged={vi.fn()} />)
    expect(await screen.findByText(/You can’t open this routine/)).toBeInTheDocument()
  })
})
