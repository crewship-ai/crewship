import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, cleanup, within } from "@testing-library/react"

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

/** Tick both rows (there is no header row: the bar offers "Select all"). */
function selectBoth() {
  const boxes = screen.getAllByRole("checkbox")
  fireEvent.click(boxes[0])
  fireEvent.click(boxes[1])
}

/** Open one of the bulk bar's menus and pick an item. */
function pick(menu: string, item: string) {
  fireEvent.pointerDown(within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByRole("button", { name: menu }), { button: 0, ctrlKey: false })
  fireEvent.click(screen.getByRole("menuitem", { name: item }))
}

/** Select both rows and ask for a bulk status change to Done. */
function bulkSetDone() {
  selectBoth()
  expect(screen.getByText("2 selected")).toBeTruthy()
  pick("Status", "Done")
  confirmBulk()
}

/** Accept the confirmation a bulk change now asks for. */
function confirmBulk() {
  fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: /^Set to/ }))
}

// A bulk status change can set off automations on every selected issue, so it
// asks first; priority asks the same way, so the two menus behave alike.
describe("IssuesListView bulk changes ask first", () => {
  beforeEach(() => {
    cleanup()
    apiFetch.mockReset()
    apiFetch.mockResolvedValue(json(200, { updated: 2 }))
  })

  it("names the change and the issues, and sends nothing until confirmed", async () => {
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)
    selectBoth()
    pick("Status", "Done")
    const dialog = screen.getByRole("alertdialog")
    expect(dialog).toHaveTextContent("Set 2 issues to Done?")
    expect(dialog).toHaveTextContent(/automations/i)
    expect(apiFetch.mock.calls.filter((c) => String(c[0]).includes("/issues/bulk"))).toHaveLength(0)
  })

  it("Cancel leaves the issues and the selection alone", async () => {
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)
    selectBoth()
    pick("Status", "Done")
    fireEvent.click(within(screen.getByRole("alertdialog")).getByRole("button", { name: "Cancel" }))
    expect(apiFetch.mock.calls.filter((c) => String(c[0]).includes("/issues/bulk"))).toHaveLength(0)
    expect(screen.getByText("2 selected")).toBeTruthy()
  })

  it("asks before a bulk priority change too", async () => {
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)
    selectBoth()
    pick("Priority", "Urgent")
    expect(screen.getByRole("alertdialog")).toHaveTextContent("Set 2 issues to Urgent?")
    confirmBulk()
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/issues/bulk?workspace_id=ws-1",
        expect.objectContaining({ body: JSON.stringify({ ids: ["i1", "i2"], updates: { priority: "urgent" } }) }),
      ),
    )
  })
})

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
          body: JSON.stringify({ ids: ["i1", "i2"], updates: { status: "DONE" } }),
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
      expect(toastError).toHaveBeenCalledWith("3 of 2 issues are locked by a running mission", { duration: Infinity }),
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

    // 4. …and the menu that fired the request is closed, so the bar is
    //    not covering its own error message.
    expect(screen.queryByRole("menuitem", { name: "Done" })).toBeNull()
  })

  it("keeps the selection when the request never reaches the server", async () => {
    apiFetch.mockRejectedValue(new Error("Failed to fetch"))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("Failed to fetch", { duration: Infinity }))
    expect(screen.getByText("2 selected")).toBeTruthy()
  })

  it("names the status when the refusal carries no readable body", async () => {
    apiFetch.mockResolvedValue(new Response("<html>502 Bad Gateway</html>", { status: 502 }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringContaining("502"), { duration: Infinity }))
    expect(screen.getByText("2 selected")).toBeTruthy()
  })

  // The refusal names the rows it refused. Once the selection changes it is
  // describing something else, so it must not outlive the selection it was
  // about.
  it("drops the error when the selection it described is changed", async () => {
    apiFetch.mockResolvedValue(json(409, { error: "2 issues are locked by a running mission" }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy())

    // Deselect one row: the message no longer applies to what is selected.
    fireEvent.click(screen.getAllByRole("checkbox")[1])

    expect(screen.getByText("1 selected")).toBeTruthy()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("does not bring a stale error back with the next select-all", async () => {
    apiFetch.mockResolvedValue(json(409, { error: "2 issues are locked by a running mission" }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy())

    fireEvent.click(screen.getAllByRole("checkbox")[0]) // deselect one
    fireEvent.click(screen.getByRole("button", { name: "Select all 2" })) // and select all again

    expect(screen.getByText("2 selected")).toBeTruthy()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("clears a stale error once a fresh attempt succeeds", async () => {
    apiFetch.mockResolvedValueOnce(json(409, { error: "conflict" }))
    render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" />)

    bulkSetDone()
    await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy())

    apiFetch.mockResolvedValueOnce(json(200, { updated: 2 }))
    pick("Status", "Done")
    confirmBulk()

    await waitFor(() => expect(screen.queryByText("2 selected")).toBeNull())
    expect(screen.queryByRole("alert")).toBeNull()
  })
})

it("shows the human currently doing the work in list view", () => {
  render(<IssuesListView issues={[{ ...issue("held", 1), assignee_id: "agent", assignee_name: "Jordan", work_mode: "human", worker_name: "Petra", worker_user_id: "person" }]} onIssueClick={() => {}} />)
  expect(screen.getByTitle("Petra holds the work")).toBeInTheDocument()
  expect(screen.queryByTitle(/Jordan/)).not.toBeInTheDocument()
})

it("keeps the selection and says so when the server changed only some of them", async () => {
  cleanup()
  apiFetch.mockReset()
  toastError.mockReset()
  apiFetch.mockResolvedValue(json(200, { updated: 1 }))
  const applied = vi.fn()
  render(<IssuesListView issues={issues} onIssueClick={vi.fn()} workspaceId="ws-1" onBulkApplied={applied} />)
  bulkSetDone()
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("1 of 2 issues were not changed"))
  expect(screen.getByText("2 selected")).toBeTruthy()
  // What did change is refreshed.
  expect(applied).toHaveBeenCalled()
})

describe("IssuesListView groups", () => {
  beforeEach(() => cleanup())

  it("puts the work in motion first and counts each group", () => {
    const rows = [
      { ...issue("a", 1), status: "BACKLOG" as const },
      { ...issue("b", 2), status: "IN_PROGRESS" as const },
      { ...issue("c", 3), status: "IN_PROGRESS" as const },
    ]
    render(<IssuesListView issues={rows} onIssueClick={() => {}} />)
    const groups = screen.getAllByRole("region")
    expect(groups.map((g) => g.getAttribute("aria-label"))).toEqual(["In progress: 2 issues", "Backlog: 1 issue"])
  })

  it("a group's + opens New issue with that group's status", () => {
    const onCreate = vi.fn()
    render(<IssuesListView issues={issues} onIssueClick={() => {}} onCreate={onCreate} />)
    fireEvent.click(screen.getByRole("button", { name: "New issue in Todo" }))
    expect(onCreate).toHaveBeenCalledWith("TODO")
  })

  it("a Done group has no +, since a new issue cannot start there", () => {
    const rows = [{ ...issues[0], status: "DONE" as const }]
    render(<IssuesListView issues={rows} onIssueClick={() => {}} onCreate={vi.fn()} />)
    expect(screen.queryByRole("button", { name: "New issue in Done" })).toBeNull()
  })

  it("a row opens the issue", () => {
    const onOpen = vi.fn()
    render(<IssuesListView issues={issues} onIssueClick={onOpen} />)
    fireEvent.click(screen.getByText("Issue 2"))
    expect(onOpen).toHaveBeenCalledWith(expect.objectContaining({ id: "i2" }))
  })
})
