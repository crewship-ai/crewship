import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { IssuesListInline } from "../issues-inline"
import type { Mission } from "@/lib/types/mission"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: (...a: unknown[]) => toastError(...a) },
}))

vi.mock("@/hooks/use-user-preference", async () => {
  const { useState } = await import("react")
  return {
    useUserPreference: <T,>(_key: string, initial: T) => {
      const [v, setV] = useState<T>(initial)
      return [v, setV, { ready: true }]
    },
  }
})

function issue(id: string, n: number): Mission {
  return {
    id,
    workspace_id: "ws-1",
    crew_id: "crew-1",
    lead_agent_id: "a1",
    trace_id: "t1",
    title: `Issue ${n}`,
    status: "TODO",
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    number: n,
    identifier: `CRE-${n}`,
    priority: "medium",
  } as Mission
}

const issues = [issue("i1", 1), issue("i2", 2)]

function bulkSetDone() {
  fireEvent.click(screen.getAllByRole("checkbox")[0]) // select-all
  expect(screen.getByText("2 selected")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Status" }))
  fireEvent.click(screen.getByRole("button", { name: "Done" }))
}

// IssuesListInline is what /issues actually renders in list mode. It used to
// drop `workspaceId` on the floor, so `handleBulkUpdate` returned at
// `if (!workspaceId) return`: rows selected, status picked, nothing sent, no
// error. The IssuesListView tests never saw it because they pass the prop
// themselves. This suite is the level that was missing.
describe("IssuesListInline — bulk edit reaches the server", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastError.mockReset()
  })

  it("PATCHes the workspace-scoped bulk endpoint", async () => {
    apiFetch.mockResolvedValue(
      new Response(JSON.stringify({ updated: 2 }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    )
    render(<IssuesListInline issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()

    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/issues/bulk?workspace_id=ws-1",
        expect.objectContaining({
          method: "PATCH",
          body: JSON.stringify({ ids: ["i1", "i2"], updates: { status: "COMPLETED" } }),
        }),
      ),
    )
    await waitFor(() => expect(screen.queryByText("2 selected")).toBeNull())
  })

  // #1563 is only worth anything if it is reachable from the real UI: the
  // refusal has to survive the trip through the wrapper too.
  it("surfaces a refusal instead of silently doing nothing", async () => {
    apiFetch.mockResolvedValue(
      new Response(JSON.stringify({ error: "2 issues are locked by a running mission" }), {
        status: 409,
        headers: { "Content-Type": "application/json" },
      }),
    )
    render(<IssuesListInline issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()

    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain("locked by a running mission"),
    )
    expect(screen.getByText("2 selected")).toBeTruthy()
  })

  // A caller that wants to own the write (optimistic update, its own refresh)
  // still gets first refusal — the wrapper must not swallow that either.
  it("prefers onBulkAction over the built-in PATCH when one is given", async () => {
    const onBulkAction = vi.fn()
    render(
      <IssuesListInline
        issues={issues}
        onIssueClick={vi.fn()}
        workspaceId="ws-1"
        onBulkAction={onBulkAction}
      />,
    )

    bulkSetDone()

    expect(onBulkAction).toHaveBeenCalledWith(["i1", "i2"], { status: "COMPLETED" })
    expect(apiFetch).not.toHaveBeenCalled()
  })
})
