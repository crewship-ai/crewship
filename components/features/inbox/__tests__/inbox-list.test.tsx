import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

const patch = vi.fn().mockResolvedValue(undefined)
const refresh = vi.fn().mockResolvedValue(undefined)

let role = "OWNER"

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role }),
}))
vi.mock("@/hooks/use-dashboard-data", () => ({
  useAgentSummaries: () => ({
    data: [
      { id: "a1", name: "casey", slug: "casey" },
      { id: "a2", name: "harper", slug: "harper" },
    ],
  }),
}))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [{ id: "p1", name: "docs-publish", slug: "docs-publish" }], refresh: vi.fn() }),
}))

// One row per shape the surface has to render: a waitpoint with a deadline, a
// keeper escalation whose approve endpoint needs OWNER/ADMIN, a chat reply, and
// a circuit breaker — the kind the previous UI could not draw at all.
const now = Date.now()
const iso = (msAgo: number) => new Date(now - msAgo).toISOString()

function base(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  return {
    workspace_id: "ws-test",
    source_id: `src-${over.id}`,
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: iso(60_000),
    updated_at: iso(60_000),
    ...over,
  } as InboxItem
}

const ITEMS: InboxItem[] = [
  base({
    id: "wp1",
    kind: "waitpoint",
    title: "Approve step promote in docs-publish",
    sender_type: "pipeline",
    sender_name: "docs-publish",
    blocking: true,
    priority: "high",
    created_at: iso(20 * 60_000),
    payload: {
      pipeline_run_id: "run1",
      step_id: "promote",
      timeout_at: new Date(now + 11 * 60_000).toISOString(),
    },
  }),
  base({
    id: "esc1",
    kind: "escalation",
    title: "Skill log-parser proposed for review",
    sender_type: "agent",
    sender_name: "casey",
    state: "read",
    blocking: true,
    payload: { kind: "skill_proposal", crew_id: "c1", file_name: "f.md", slug: "log-parser", scan_status: "clean" },
  }),
  base({
    id: "msg1",
    kind: "message",
    title: "Atlas replied in migration v167",
    sender_type: "agent",
    sender_name: "atlas",
    payload: { chat_url: "/chat/atlas", agent_slug: "atlas" },
  }),
  base({
    id: "brk1",
    kind: "schedule_circuit_breaker_tripped",
    title: "Routine docs-publish paused after 5 straight failures",
    sender_type: "pipeline",
    sender_name: "docs-publish",
    payload: { schedule_id: "sch1", consecutive_failures: 5 },
  }),
]

vi.mock("@/hooks/use-inbox", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-inbox")>()
  return {
    ...actual,
    useInbox: () => ({ items: ITEMS, unreadCount: 3, loading: false, error: null, patch, refresh }),
  }
})

import { InboxList } from "../inbox-list"

beforeEach(() => { role = "OWNER" })
afterEach(cleanup)

/** The row title also appears in the reading pane, so list assertions scope. */
function list() {
  return within(screen.getByTestId("inbox-list"))
}

function openFilter() {
  fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
}

describe("InboxList — kinds the previous surface could not draw", () => {
  it("gives a tripped circuit breaker its own card instead of a generic notification", () => {
    render(<InboxList />)

    fireEvent.click(list().getByText("Routine docs-publish paused after 5 straight failures"))

    const card = screen.getByTestId("decision-card")
    expect(within(card).getByText(/Routine is disabled/i)).toBeInTheDocument()
    // The payload the writer sends, promoted out of the Context dump.
    expect(within(card).getByText(/5 failures in a row/i)).toBeInTheDocument()
  })
})

describe("InboxList — the deadline nobody rendered", () => {
  it("counts down a waitpoint instead of showing how old it is", () => {
    render(<InboxList />)
    fireEvent.click(list().getByText("Approve step promote in docs-publish"))

    const card = screen.getByTestId("decision-card")
    expect(within(card).getByText(/in 11 min/)).toBeInTheDocument()
  })
})

describe("InboxList — RBAC", () => {
  it("lets an OWNER decide a skill proposal", () => {
    render(<InboxList />)
    fireEvent.click(list().getByText("Skill log-parser proposed for review"))

    const card = screen.getByTestId("decision-card")
    expect(within(card).getByRole("button", { name: /Approve/ })).toBeEnabled()
  })

  it("withholds it from a MANAGER and names who decides", () => {
    role = "MANAGER"
    render(<InboxList />)
    fireEvent.click(list().getByText("Skill log-parser proposed for review"))

    const card = screen.getByTestId("decision-card")
    // /skills/proposed/approve is roleManage — OWNER/ADMIN only — while the row
    // is addressed to MANAGER. The button used to be offered and answer 403.
    expect(within(card).getByRole("button", { name: /Approve/ })).toBeDisabled()
    expect(within(card).getByText(/OWNER or ADMIN decides this/i)).toBeInTheDocument()
  })

  it("keeps the waitpoint decidable by a MANAGER", () => {
    role = "MANAGER"
    render(<InboxList />)
    fireEvent.click(list().getByText("Approve step promote in docs-publish"))

    const card = screen.getByTestId("decision-card")
    expect(within(card).getByRole("button", { name: /Approve/ })).toBeEnabled()
  })
})

describe("InboxList — search and filter", () => {
  it("searches the body, not only the title", () => {
    render(<InboxList />)

    fireEvent.change(screen.getByPlaceholderText(/Search inbox/i), { target: { value: "log-parser" } })

    expect(list().getByText("Skill log-parser proposed for review")).toBeInTheDocument()
    expect(list().queryByText("Atlas replied in migration v167")).not.toBeInTheDocument()
  })

  it("narrows to a bucket from the Filter menu", () => {
    render(<InboxList />)
    openFilter()

    fireEvent.click(screen.getByTestId("facet-bucket-replies"))

    expect(list().getByText("Atlas replied in migration v167")).toBeInTheDocument()
    expect(list().queryByText("Approve step promote in docs-publish")).not.toBeInTheDocument()
  })

  it("finds a roster agent that has no items in the loaded window", () => {
    render(<InboxList />)
    openFilter()

    expect(screen.queryByTestId("subject-harper")).not.toBeInTheDocument()
    fireEvent.change(screen.getByTestId("subject-search"), { target: { value: "harp" } })
    expect(screen.getByTestId("subject-harper")).toBeInTheDocument()
  })
})

describe("InboxList — selection", () => {
  function enterSelectMode() {
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))
  }

  it("ticks a row and takes a range on shift-click", () => {
    render(<InboxList />)
    enterSelectMode()

    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)
    expect(screen.getByText("1 selected")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("check-2").parentElement as HTMLElement, { shiftKey: true })
    expect(screen.getByText("3 selected")).toBeInTheDocument()
  })

  it("warns that decisions are never closed in bulk", () => {
    render(<InboxList />)
    enterSelectMode()
    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)

    expect(screen.getByText(/waiting on/i)).toBeInTheDocument()
  })
})
