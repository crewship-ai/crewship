import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// "Try again" on the topology card called setChain(null), which was not in
// the fetch effect's dependencies: the button cleared the graph and fetched
// nothing, so the same error panel came straight back. A retry has to
// re-run the request.

const calls = vi.hoisted(() => ({ n: 0, failFirst: true }))
vi.mock("@/lib/api-fetch", () => ({
  apiFetch: vi.fn(() => {
    calls.n += 1
    if (calls.failFirst && calls.n === 1) return Promise.reject(new Error("HTTP 500"))
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ nodes: [{ id: "run:r1", kind: "run", ref: "r1", label: "r1" }], edges: [], truncated: false }),
    })
  }),
}))
vi.mock("@/components/features/activity-stream/chain-canvas", () => ({
  ChainCanvas: () => <div data-testid="chain-canvas" />,
}))

import { TopologyCard } from "../topology-card"

beforeEach(() => {
  calls.n = 0
  calls.failFirst = true
})

describe("TopologyCard retry", () => {
  it("re-requests the chain when Try again is pressed", async () => {
    render(<TopologyCard workspaceId="ws-1" anchor="run:r1" anchorLabel="r1" onOpenNode={() => {}} />)
    await screen.findByText(/could not load the chain/i)
    expect(calls.n).toBe(1)
    fireEvent.click(screen.getByRole("button", { name: /try again/i }))
    await waitFor(() => expect(calls.n).toBe(2))
    await waitFor(() => expect(screen.queryByText(/could not load the chain/i)).toBeNull())
  })
})
