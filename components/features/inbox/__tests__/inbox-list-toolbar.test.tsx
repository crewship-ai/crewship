import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, cleanup } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// Radix DropdownMenu relies on pointer-capture + scrollIntoView, which the
// test DOM doesn't implement. Polyfill them so the Group menu can open.
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

const ITEMS: InboxItem[] = [
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

vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws-test" }) }))
vi.mock("@/hooks/use-dashboard-data", () => ({ useCrewSummaries: () => ({ data: [] }) }))

const patch = vi.fn().mockResolvedValue(undefined)
const refresh = vi.fn().mockResolvedValue(undefined)
vi.mock("@/hooks/use-inbox", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-inbox")>()
  return {
    ...actual,
    useInbox: () => ({
      items: ITEMS,
      unreadCount: 1,
      loading: false,
      error: null,
      patch,
      refresh,
    }),
  }
})

import { InboxList } from "../inbox-list"

describe("InboxList — refined toolbar", () => {
  beforeEach(() => cleanup())

  it("group-by is a single dropdown showing the current dimension, not a row of buttons", async () => {
    render(<InboxList />)
    // A single control that opens the grouping menu, labelled with the
    // active dimension (default: Smart) — replaces the old 6-button row.
    const trigger = screen.getByRole("button", { name: /group/i })
    expect(trigger).toHaveTextContent(/smart/i)

    fireEvent.pointerDown(trigger, { button: 0 })
    // The other dimensions live inside the menu now, not as always-visible buttons.
    expect(await screen.findByRole("menuitemradio", { name: /type/i })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: /sender/i })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: /routine/i })).toBeInTheDocument()
    expect(screen.getByRole("menuitemradio", { name: /crew/i })).toBeInTheDocument()
  })

  it("checkboxes are hidden until Select mode is turned on", async () => {
    render(<InboxList />)
    // Clean list by default — no per-row checkboxes.
    expect(screen.queryByLabelText(/^Select /)).not.toBeInTheDocument()

    const selectBtn = screen.getByRole("button", { name: /^select$/i })
    fireEvent.click(selectBtn)

    // Now the row checkboxes exist.
    expect(await screen.findByLabelText(/Credential approval/)).toBeInTheDocument()
  })

  it("surfaces the item priority as a pill on the row", () => {
    render(<InboxList />)
    // The 'high' priority escalation shows its priority in the list (the API
    // already returns it; the row now renders it).
    expect(screen.getAllByText(/high/i).length).toBeGreaterThan(0)
  })
})
