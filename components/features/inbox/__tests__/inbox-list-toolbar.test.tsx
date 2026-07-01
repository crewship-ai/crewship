import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// Radix DropdownMenu relies on pointer-capture + scrollIntoView, which the
// test DOM doesn't implement. Polyfill them so the Display menu can open.
beforeEach(() => {
  if (!Element.prototype.hasPointerCapture) {
    Element.prototype.hasPointerCapture = () => false
  }
  if (!Element.prototype.releasePointerCapture) {
    Element.prototype.releasePointerCapture = () => {}
  }
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = () => {}
  }
})

vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-dashboard-data", () => ({ useCrewSummaries: () => ({ data: [] }) }))

// Everything the factory needs is declared INSIDE it — vi.mock is hoisted
// above the imports, so closing over module-scope locals would hit the TDZ.
vi.mock("@/hooks/use-inbox", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-inbox")>()
  const items: InboxItem[] = [
    {
      id: "ibx_escalation_1",
      workspace_id: "ws-test",
      kind: "escalation",
      source_id: "esc-1",
      title: "Credential approval: AWS-API-Key",
      sender_type: "system",
      sender_name: "Alice",
      state: "unread",
      priority: "high",
      blocking: true,
      payload: { escalation_type: "GENERAL" },
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
    {
      id: "ibx_failed_run_1",
      workspace_id: "ws-test",
      kind: "failed_run",
      source_id: "run-1",
      title: "Scheduled routine failed: nightly-backup",
      sender_type: "pipeline",
      sender_name: "nightly-backup",
      state: "read",
      priority: "medium",
      blocking: false,
      payload: { pipeline_slug: "nightly-backup" },
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
  ]
  const patch = vi.fn().mockResolvedValue(undefined)
  const refresh = vi.fn().mockResolvedValue(undefined)
  return {
    ...actual,
    useInbox: () => ({ items, unreadCount: 1, loading: false, error: null, patch, refresh }),
  }
})

import { InboxList } from "../inbox-list"

describe("InboxList — search-led toolbar", () => {
  beforeEach(() => cleanup())

  it("grouping lives inside a single Display menu, not a standing button row", async () => {
    render(<InboxList />)
    const trigger = screen.getByRole("button", { name: /display/i })
    fireEvent.pointerDown(trigger, { button: 0 })
    // The six dimensions are options in the Display popover now.
    expect(await screen.findByRole("menuitemradio", { name: /type/i })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: /sender/i })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: /routine/i })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: /crew/i })).toBeInTheDocument()
    // The old always-visible "Group:" pill is gone.
    expect(screen.queryByText(/^Group:/)).not.toBeInTheDocument()
  })

  it("a search field filters the list by item title", () => {
    render(<InboxList />)
    // Both items visible up front.
    expect(screen.getByText(/Credential approval/)).toBeInTheDocument()
    expect(screen.getByText(/Scheduled routine failed/)).toBeInTheDocument()

    const search = screen.getByPlaceholderText(/search inbox/i)
    fireEvent.change(search, { target: { value: "credential" } })

    // Only the matching row survives.
    expect(screen.getByText(/Credential approval/)).toBeInTheDocument()
    expect(screen.queryByText(/Scheduled routine failed/)).not.toBeInTheDocument()
  })

  it("checkboxes are hidden until Select mode is turned on", async () => {
    render(<InboxList />)
    expect(screen.queryByLabelText(/^Select /)).not.toBeInTheDocument()

    const selectBtn = screen.getByRole("button", { name: /^select$/i })
    fireEvent.click(selectBtn)

    expect(await screen.findByLabelText(/Credential approval/)).toBeInTheDocument()
  })

  it("surfaces the item priority as a pill on the row", () => {
    render(<InboxList />)
    expect(screen.getAllByText(/high/i).length).toBeGreaterThan(0)
  })

  it("drops a selection once a search hides it, so bulk never acts on unseen rows", async () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /^select$/i }))

    // Select the failed-run row, then narrow the search so it's hidden.
    const cb = await screen.findByLabelText(/Scheduled routine failed/)
    fireEvent.click(cb)
    expect(screen.getByText(/1 selected/)).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText(/search inbox/i), {
      target: { value: "credential" },
    })

    // The hidden row leaves the selection — the bulk bar count follows.
    expect(screen.queryByText(/1 selected/)).not.toBeInTheDocument()
  })
})
