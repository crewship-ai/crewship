import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// The bell is the only inbox surface most people look at all day, so the parts
// that matter are the ones that decide what it shows: which list it reads,
// what the badge counts, and whether "mark all read" does anything.

const push = vi.fn()
const refresh = vi.fn().mockResolvedValue(undefined)
const inboxBulk = vi.fn()
let stateFilterSeen: string | undefined
let ITEMS: InboxItem[] = []

vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws", role: "OWNER" }),
  useCurrentWorkspaceId: () => "ws",
}))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: (...a: unknown[]) => inboxBulk(...a) }))
vi.mock("@/hooks/use-inbox", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-inbox")>()
  return {
    ...actual,
    useInbox: (_ws: string, state?: string) => {
      stateFilterSeen = state
      return { items: ITEMS, unreadCount: 0, loading: false, error: null, patch: vi.fn(), refresh }
    },
  }
})

import { InboxBell } from "../inbox-bell"

const now = Date.now()

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  return {
    workspace_id: "ws", source_id: `s-${over.id}`, state: "unread", priority: "medium", blocking: false,
    created_at: new Date(now - 60_000).toISOString(), updated_at: new Date(now - 60_000).toISOString(),
    ...over,
  } as InboxItem
}

beforeEach(() => {
  vi.clearAllMocks()
  inboxBulk.mockResolvedValue({ ok: true, result: { updated: 2, skipped: 0, not_found: 0 } })
  ITEMS = [
    item({ id: "wp", kind: "waitpoint", title: "Approve promote", blocking: true, sender_type: "pipeline", sender_name: "nightly" }),
    item({ id: "esc", kind: "escalation", title: "Skill review", blocking: true, state: "read", sender_type: "agent", sender_name: "casey" }),
    item({ id: "msg", kind: "message", title: "Atlas replied", sender_type: "agent", sender_name: "atlas" }),
  ]
})
afterEach(cleanup)

describe("which list the bell reads", () => {
  it("reads the ACTIVE list, so a blocking item that was opened stays visible", () => {
    render(<InboxBell />)

    // state=unread was the old choice, and it dropped a read-but-blocking
    // escalation off the bell while the agent kept waiting on it.
    expect(stateFilterSeen).toBe("active")
    fireEvent.click(screen.getByTestId("bell-trigger"))
    expect(screen.getByTestId("bell-row-esc")).toBeInTheDocument()
  })
})

describe("the badge", () => {
  it("counts decisions when there are any", () => {
    render(<InboxBell />)
    expect(screen.getByTestId("bell-badge")).toHaveTextContent("2")
  })

  it("falls back to unread when nothing is waiting on a decision", () => {
    ITEMS = [item({ id: "m1", kind: "message", title: "one" }), item({ id: "m2", kind: "message", title: "two" })]
    render(<InboxBell />)
    expect(screen.getByTestId("bell-badge")).toHaveTextContent("2")
  })

  it("disappears when there is nothing at all", () => {
    ITEMS = []
    render(<InboxBell />)
    expect(screen.queryByTestId("bell-badge")).not.toBeInTheDocument()
  })

  it("caps at 99+ rather than widening the toolbar", () => {
    ITEMS = Array.from({ length: 120 }, (_, i) =>
      item({ id: `m${i}`, kind: "waitpoint", title: `w${i}`, blocking: true }))
    render(<InboxBell />)
    expect(screen.getByTestId("bell-badge")).toHaveTextContent("99+")
  })
})

describe("the popover", () => {
  it("says how many it is holding back rather than pretending it is all of them", () => {
    ITEMS = Array.from({ length: 9 }, (_, i) =>
      item({ id: `w${i}`, kind: "waitpoint", title: `Approve ${i}`, blocking: true }))
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    expect(screen.getByText(/\+5 more in the inbox/)).toBeInTheDocument()
  })

  it("shows an empty state when the queue is clear", () => {
    ITEMS = []
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    expect(screen.getByText(/All caught up/)).toBeInTheDocument()
  })

  it("opens the inbox from the footer and closes itself", async () => {
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    fireEvent.click(screen.getByRole("button", { name: /Open inbox/ }))

    expect(push).toHaveBeenCalledWith("/inbox")
    // AnimatePresence keeps the node mounted through its exit transition.
    await waitFor(() => expect(screen.queryByTestId("bell-popover")).not.toBeInTheDocument())
  })

  it("navigates when a row is picked", () => {
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    fireEvent.click(screen.getByTestId("bell-row-msg"))

    // Deep-link, not just "/inbox": acting on what the popover showed must not
    // start with finding it again.
    expect(push).toHaveBeenCalledWith("/inbox?item=msg")
  })

  it("closes when the trigger is clicked again", async () => {
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))
    expect(screen.getByTestId("bell-popover")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("bell-trigger"))
    await waitFor(() => expect(screen.queryByTestId("bell-popover")).not.toBeInTheDocument())
  })
})

describe("mark all read", () => {
  it("sends exactly the unread ids to the bulk endpoint, then refreshes", async () => {
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    fireEvent.click(screen.getByRole("button", { name: /Mark 2 read/ }))

    // "esc" is already read; sending it would be a no-op the server has to
    // absorb, and the count on the button would be a lie.
    await waitFor(() => expect(inboxBulk).toHaveBeenCalledWith("ws", ["wp", "msg"], "read"))
    await waitFor(() => expect(refresh).toHaveBeenCalled())
  })

  it("stops on a failure instead of claiming success", async () => {
    inboxBulk.mockResolvedValueOnce({ ok: false, error: "nope" })
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    fireEvent.click(screen.getByRole("button", { name: /Mark 2 read/ }))

    await waitFor(() => expect(inboxBulk).toHaveBeenCalled())
    expect(refresh).not.toHaveBeenCalled()
  })

  it("is not offered when everything has been read", () => {
    ITEMS = ITEMS.map((i) => ({ ...i, state: "read" as const }))
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    expect(screen.queryByRole("button", { name: /Mark .* read/ })).not.toBeInTheDocument()
  })
})

describe("sections", () => {
  it("keeps decisions above recent, whatever arrived last", () => {
    render(<InboxBell />)
    fireEvent.click(screen.getByTestId("bell-trigger"))

    const popover = within(screen.getByTestId("bell-popover"))
    expect(popover.getByText("Needs a decision")).toBeInTheDocument()
    expect(popover.getByText("Recent")).toBeInTheDocument()

    const rows = popover.getAllByRole("button").filter((b) => b.dataset.testid?.startsWith("bell-row-"))
    expect(rows.map((r) => r.dataset.testid)).toEqual(["bell-row-wp", "bell-row-esc", "bell-row-msg"])
  })
})
