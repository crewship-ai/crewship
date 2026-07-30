import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// The chrome around the rows: grouping, sorting, the facet chips, the archive's
// own questions, and the states nobody looks at until they happen (loading,
// error, empty). Plus the two display concerns that carry meaning rather than
// decoration — a person is a circle, a machine is a square, and a payload value
// that looks like a secret stays masked until it is asked for.

const patch = vi.fn().mockResolvedValue(undefined)
const refresh = vi.fn().mockResolvedValue(undefined)
const inboxBulk = vi.fn()
let useInboxState: {
  items: InboxItem[]
  unreadCount: number
  loading: boolean
  error: string | null
}

vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER" }) }))
vi.mock("@/hooks/use-dashboard-data", () => ({
  useAgentSummaries: () => ({ data: [{ id: "a1", name: "casey" }, { id: "a2", name: "harper" }] }),
}))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [{ id: "p", name: "nightly", slug: "nightly" }], refresh: vi.fn() }),
}))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: (...a: unknown[]) => inboxBulk(...a) }))
vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
vi.mock("@/hooks/use-inbox", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-inbox")>()
  return { ...actual, useInbox: () => ({ ...useInboxState, patch, refresh }) }
})

import { InboxList } from "../inbox-list"

const now = Date.now()

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  return {
    workspace_id: "ws", source_id: `s-${over.id}`, state: "unread", priority: "medium", blocking: false,
    created_at: new Date(now - 60_000).toISOString(), updated_at: new Date(now - 60_000).toISOString(),
    ...over,
  } as InboxItem
}

const LIVE: InboxItem[] = [
  item({
    id: "wp", kind: "waitpoint", title: "Approve promote", blocking: true,
    sender_type: "pipeline", sender_name: "nightly",
    created_at: new Date(now - 30 * 60_000).toISOString(),
    payload: { timeout_at: new Date(now + 5 * 60_000).toISOString(), api_key: "sk-supersecret", run_id: "r1" },
  }),
  item({
    id: "msg", kind: "message", title: "Atlas replied", sender_type: "agent", sender_name: "casey",
    payload: { chat_url: "/chat/casey", agent_name: "casey" },
  }),
  item({
    id: "brk", kind: "schedule_circuit_breaker_tripped", title: "Routine paused",
    sender_type: "pipeline", sender_name: "nightly",
    created_at: new Date(now - 10 * 60_000).toISOString(),
    payload: { schedule_id: "sch" },
  }),
]

const ARCHIVE: InboxItem[] = [
  item({
    id: "a1", kind: "escalation", title: "casey asked for GH_TOKEN", state: "resolved",
    sender_type: "agent", sender_name: "casey",
    resolved_at: new Date(now - 20 * 60_000).toISOString(), resolved_action: "approved", resolved_by_user_id: "pavel",
  }),
  item({
    id: "a2", kind: "waitpoint", title: "Approve release", state: "resolved",
    sender_type: "pipeline", sender_name: "nightly",
    resolved_at: new Date(now - 30 * 60_000).toISOString(), resolved_action: "expired",
  }),
]

beforeEach(() => {
  vi.clearAllMocks()
  inboxBulk.mockResolvedValue({ ok: true, result: { updated: 1, skipped: 0, not_found: 0 } })
  useInboxState = { items: LIVE, unreadCount: 3, loading: false, error: null }
})
afterEach(cleanup)

const list = () => within(screen.getByTestId("inbox-list"))
const openDisplay = () => fireEvent.click(screen.getByRole("button", { name: /Display/ }))
const openFilter = () => fireEvent.click(screen.getByRole("button", { name: /Filter/ }))

describe("grouping", () => {
  it("puts decisions above everything else regardless of what arrived last", () => {
    render(<InboxList />)
    const headers = list().getAllByRole("button", { expanded: true }).map((b) => b.textContent)
    expect(headers[0]).toMatch(/Decisions needed/)
  })

  it("regroups by category", () => {
    render(<InboxList />)
    openDisplay()
    fireEvent.click(screen.getByRole("button", { name: /^Category$/ }))

    // The category also appears on each row's meta line, so assert on the
    // group headers rather than on the first match.
    const headers = list().getAllByRole("button", { expanded: true }).map((b) => b.textContent)
    expect(headers.join(" ")).toMatch(/agents\.approval/)
    expect(headers.join(" ")).toMatch(/routines\.missed/)
  })

  it("regroups by subject, which is the agent the row is about", () => {
    render(<InboxList />)
    openDisplay()
    fireEvent.click(screen.getByRole("button", { name: /^Subject$/ }))

    // The chat reply is from casey; grouping by subject files it under them.
    expect(list().getAllByText("casey").length).toBeGreaterThan(0)
  })

  it("drops the headers entirely when grouping is off", () => {
    render(<InboxList />)
    openDisplay()
    fireEvent.click(screen.getByRole("button", { name: /^Nothing$/ }))

    expect(list().queryByText("Decisions needed")).not.toBeInTheDocument()
    expect(list().getByText("Approve promote")).toBeInTheDocument()
  })

  it("collapses a group without losing its count", () => {
    render(<InboxList />)
    const header = list().getAllByRole("button", { expanded: true })[0]

    fireEvent.click(header)

    expect(list().queryByText("Approve promote")).not.toBeInTheDocument()
    expect(list().getAllByRole("button", { expanded: false }).length).toBeGreaterThan(0)
  })
})

describe("sorting", () => {
  it("puts the deadline first when asked, ahead of anything newer", () => {
    render(<InboxList />)
    openDisplay()
    fireEvent.click(screen.getByRole("button", { name: /Expiring first/ }))
    openDisplay()
    fireEvent.click(screen.getByRole("button", { name: /^Nothing$/ }))

    const rows = list().getAllByTestId(/^row-/)
    expect(rows[0]).toHaveAttribute("data-testid", "row-wp")
  })

  it("reverses to oldest first", () => {
    render(<InboxList />)
    openDisplay()
    fireEvent.click(screen.getByRole("button", { name: /Oldest first/ }))
    openDisplay()
    fireEvent.click(screen.getByRole("button", { name: /^Nothing$/ }))

    const rows = list().getAllByTestId(/^row-/)
    expect(rows[0]).toHaveAttribute("data-testid", "row-wp")
    expect(rows[rows.length - 1]).toHaveAttribute("data-testid", "row-msg")
  })
})

describe("active filters stay visible", () => {
  it("shows the applied bucket as a chip and removes it again", () => {
    render(<InboxList />)
    openFilter()
    fireEvent.click(screen.getByTestId("facet-bucket-replies"))

    // A filter hidden inside a dropdown is a filter people forget is on, so it
    // surfaces as a removable chip in the toolbar.
    expect(screen.getByRole("button", { name: /Remove filter/ })).toBeInTheDocument()
    expect(list().queryByText("Approve promote")).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /Remove filter/ }))
    expect(list().getByText("Approve promote")).toBeInTheDocument()
  })

  it("filters by subject picked from the roster", () => {
    render(<InboxList />)
    openFilter()
    fireEvent.click(screen.getByTestId("subject-casey"))

    expect(list().getByText("Atlas replied")).toBeInTheDocument()
    expect(list().queryByText("Routine paused")).not.toBeInTheDocument()
  })
})

describe("the archive asks different questions", () => {
  beforeEach(() => { useInboxState = { items: ARCHIVE, unreadCount: 0, loading: false, error: null } })

  it("offers outcome and decider instead of live buckets", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("tab", { name: /Archived/ }))
    openFilter()

    expect(screen.queryByTestId("facet-bucket-decisions")).not.toBeInTheDocument()
    expect(screen.getByTestId("outcome-approved")).toBeInTheDocument()
    expect(screen.getByText("Decided by")).toBeInTheDocument()
  })

  it("narrows by who decided it", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("tab", { name: /Archived/ }))
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /pavel/ }))

    expect(list().getByText("casey asked for GH_TOKEN")).toBeInTheDocument()
    expect(list().queryByText("Approve release")).not.toBeInTheDocument()
  })

  it("narrows by period", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("tab", { name: /Archived/ }))
    openFilter()
    fireEvent.click(screen.getByRole("button", { name: /Last 7 days/ }))

    expect(screen.getByText("Last 7 days", { selector: "span" })).toBeInTheDocument()
  })

  it("shows the outcome and the human who reached it", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("tab", { name: /Archived/ }))

    expect(screen.getByTestId("reading-pane")).toHaveTextContent(/approved/)
  })
})

describe("states nobody looks at until they happen", () => {
  it("says so while loading", () => {
    useInboxState = { items: [], unreadCount: 0, loading: true, error: null }
    render(<InboxList />)
    expect(list().getByText(/Loading/)).toBeInTheDocument()
  })

  it("shows the failure rather than an empty inbox", () => {
    useInboxState = { items: [], unreadCount: 0, loading: false, error: "boom" }
    render(<InboxList />)
    expect(list().getByText(/Inbox unavailable: boom/)).toBeInTheDocument()
  })

  it("distinguishes an empty inbox from an empty archive", () => {
    useInboxState = { items: [], unreadCount: 0, loading: false, error: null }
    render(<InboxList />)
    expect(list().getByText(/Nothing waiting on you/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole("tab", { name: /Archived/ }))
    expect(list().getByText(/Nothing archived yet/)).toBeInTheDocument()
  })
})

describe("selection", () => {
  it("ticks a whole group from its header, and clears again", async () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))

    fireEvent.click(list().getByLabelText(/Select all in Decisions needed/))
    expect(screen.getByText("1 selected")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /^Clear$/ }))
    await waitFor(() => expect(screen.queryByText("1 selected")).not.toBeInTheDocument())
  })
})

describe("context", () => {
  it("masks a credential-looking payload value until it is asked for", () => {
    render(<InboxList />)
    fireEvent.click(list().getByText("Approve promote"))
    // The waitpoint carries api_key. The backend redacts body_md, but a payload
    // an agent wrote can still hold something that should not sit on screen.
    const pane = within(screen.getByTestId("reading-pane"))
    expect(pane.getByText("••••••••")).toBeInTheDocument()
    expect(pane.queryByText("sk-supersecret")).not.toBeInTheDocument()

    fireEvent.click(pane.getByRole("button", { name: /Reveal value/ }))
    expect(pane.getByText("sk-supersecret")).toBeInTheDocument()

    fireEvent.click(pane.getByRole("button", { name: /Hide value/ }))
    expect(pane.queryByText("sk-supersecret")).not.toBeInTheDocument()
  })

  it("leaves a plain identifier alone — a run id is not a secret", () => {
    render(<InboxList />)
    fireEvent.click(list().getByText("Approve promote"))
    expect(within(screen.getByTestId("reading-pane")).getByText("r1")).toBeInTheDocument()
  })
})
