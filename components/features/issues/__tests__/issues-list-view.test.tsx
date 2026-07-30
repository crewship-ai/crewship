import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react"

import { IssuesListView } from "../issues-list-view"
import type { Mission } from "@/lib/types/mission"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => toastSuccess(...args),
    error: (...args: unknown[]) => toastError(...args),
  },
}))

// The sort preference syncs itself over the same apiFetch mock. Pinning it to
// plain state keeps /api/v1/me/preferences out of the call log this test reads.
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
    lead_agent_name: "Ada",
    lead_agent_slug: "ada",
    trace_id: "t1",
    title: `Issue ${n}`,
    description: null,
    status: "TODO",
    plan: null,
    workflow_template: null,
    total_token_count: null,
    total_estimated_cost: null,
    created_at: "2026-07-01T00:00:00Z",
    updated_at: "2026-07-01T00:00:00Z",
    completed_at: null,
    task_stats: null,
    tasks: [],
    total_token_budget: null,
    complexity: null,
    pattern: null,
    number: n,
    identifier: `CRE-${n}`,
    priority: "medium",
  }
}

const issues = [issue("i1", 1), issue("i2", 2)]

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

/** Select both rows and ask for a bulk status change to Done. */
function bulkSetDone() {
  const boxes = screen.getAllByRole("checkbox")
  // boxes[0] is the header select-all.
  fireEvent.click(boxes[0])
  expect(screen.getByText("2 selected")).toBeTruthy()
  fireEvent.click(screen.getByRole("button", { name: "Status" }))
  fireEvent.click(screen.getByRole("button", { name: "Done" }))
}

describe("IssuesListView bulk update", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    toastSuccess.mockReset()
    toastError.mockReset()
  })

  it("PATCHes the selected ids and clears the selection when the server accepts", async () => {
    apiFetch.mockResolvedValue(json(200, { updated: 2 }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

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
    // A landed write is the only thing allowed to drop the selection.
    await waitFor(() => expect(screen.queryByText("2 selected")).toBeNull())
    expect(toastError).not.toHaveBeenCalled()
  })

  // The defect: apiFetch resolves on 5xx, the result was discarded inside a
  // `catch { /* silent */ }`, and clearSelection() ran either way — so a
  // refused bulk edit looked exactly like an applied one AND took away the
  // selection you would have retried with.
  it("keeps the selection and says why when the bulk PATCH is refused", async () => {
    apiFetch.mockResolvedValue(json(500, { error: "3 of 2 issues are locked by a running mission" }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()

    await waitFor(() => expect(apiFetch).toHaveBeenCalled())

    // 1. The failure is stated, in the server's words.
    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith("3 of 2 issues are locked by a running mission"),
    )
    expect(toastSuccess).not.toHaveBeenCalled()

    // 2. It stays readable next to the controls you retry with, not only in a
    //    toast that has already faded.
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toContain(
        "3 of 2 issues are locked by a running mission",
      ),
    )

    // 3. The selection survives — retrying is one click, not a re-selection.
    expect(screen.getByText("2 selected")).toBeTruthy()

    // 4. …and the dropdown that fired the request is closed, so the bar is
    //    not covering its own error message.
    expect(screen.queryByRole("button", { name: "Done" })).toBeNull()
  })

  it("keeps the selection when the request never reaches the server", async () => {
    apiFetch.mockRejectedValue(new Error("Failed to fetch"))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("Failed to fetch"))
    expect(screen.getByText("2 selected")).toBeTruthy()
  })

  it("names the status when the refusal carries no readable body", async () => {
    apiFetch.mockResolvedValue(new Response("<html>502 Bad Gateway</html>", { status: 502 }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringContaining("502")))
    expect(screen.getByText("2 selected")).toBeTruthy()
  })

  it("clears a stale error once a fresh attempt succeeds", async () => {
    apiFetch.mockResolvedValueOnce(json(409, { error: "conflict" }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy())

    apiFetch.mockResolvedValueOnce(json(200, { updated: 2 }))
    fireEvent.click(screen.getByRole("button", { name: "Status" }))
    fireEvent.click(screen.getByRole("button", { name: "Done" }))

    await waitFor(() => expect(screen.queryByText("2 selected")).toBeNull())
    expect(screen.queryByRole("alert")).toBeNull()
  })
})
