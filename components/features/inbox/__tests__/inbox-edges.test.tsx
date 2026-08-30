import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// The paths that only run when something goes wrong, or when a rarely-used
// control is used twice. They are the ones that rot unnoticed, because nobody
// clicks Reject on a legacy credential escalation until the day they must.

const apiFetch = vi.fn()
const waitpointDecide = vi.fn()
const escalationResolve = vi.fn()
const toastError = vi.fn()
const inboxBulk = vi.fn()
const patch = vi.fn().mockResolvedValue(undefined)
const refresh = vi.fn().mockResolvedValue(undefined)
let ITEMS: InboxItem[] = []

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws", role: "OWNER" }),
  useCurrentWorkspaceId: () => "ws",
}))
vi.mock("@/hooks/use-dashboard-data", () => ({
  useAgentSummaries: () => ({
    data: [
      { id: "1", name: "casey" }, { id: "2", name: "carol" }, { id: "3", name: "carter" },
      { id: "4", name: "cara" }, { id: "5", name: "carmen" }, { id: "6", name: "carlos" },
      { id: "7", name: "carla" }, { id: "8", name: "carson" },
    ],
  }),
}))
vi.mock("@/hooks/use-pipelines", () => ({
  usePipelines: () => ({ pipelines: [{ id: "p", name: "car-wash", slug: "car-wash" }], refresh: vi.fn() }),
}))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: (...a: unknown[]) => inboxBulk(...a) }))
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetch(...a),
}))
// Partial: kind-actions also imports isAlreadyDecidedError from here, and the
// tests want the REAL predicate — it is the thing under test when a 409 has to
// read as somebody else finishing rather than as a failure.
vi.mock("@/lib/api/waitpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/waitpoints")>()),
  waitpointDecide: (...a: unknown[]) => waitpointDecide(...a),
}))
vi.mock("@/lib/api/escalations", () => ({ escalationResolve: (...a: unknown[]) => escalationResolve(...a) }))
vi.mock("sonner", () => ({
  toast: { error: (...a: unknown[]) => toastError(...a), success: vi.fn(), info: vi.fn() },
}))
vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
vi.mock("@/hooks/use-inbox", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-inbox")>()
  return { ...actual, useInbox: () => ({ items: ITEMS, unreadCount: 1, loading: false, error: null, patch, refresh }) }
})

import { KindActions } from "../kind-actions"
import { InboxDetail } from "../inbox-detail"
import { InboxList } from "../inbox-list"

function item(over: Partial<InboxItem> & Pick<InboxItem, "kind">): InboxItem {
  return {
    id: "i", workspace_id: "ws", source_id: "src", title: "t",
    state: "unread", priority: "medium", blocking: false,
    created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    ...over,
  } as InboxItem
}

const onResolve = vi.fn()
const onRefresh = vi.fn()
const mount = (i: InboxItem) =>
  render(<KindActions item={i} onResolve={onResolve} onRefresh={onRefresh} disabled={false} />)

beforeEach(() => {
  vi.clearAllMocks()
  apiFetch.mockResolvedValue({ ok: true, json: async () => ({}) })
  waitpointDecide.mockResolvedValue({ ok: true })
  escalationResolve.mockResolvedValue({ ok: true })
  inboxBulk.mockResolvedValue({ ok: true, result: { updated: 1, skipped: 0, not_found: 0 } })
  ITEMS = []
})
afterEach(cleanup)

describe("failure paths that would otherwise pass silently", () => {
  it("reports a waitpoint approval the server genuinely refused", async () => {
    waitpointDecide.mockResolvedValueOnce({ ok: false, status: 503, error: "waitpoint store unavailable" })
    mount(item({ kind: "waitpoint", source_id: "tok" }))
    fireEvent.click(screen.getByRole("button", { name: /Approve/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("waitpoint store unavailable"))
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("reports a network failure on a skill decision", async () => {
    apiFetch.mockRejectedValueOnce(new Error("dns"))
    mount(item({ kind: "escalation", payload: { kind: "skill_proposal", crew_id: "c", file_name: "f" } }))
    fireEvent.click(screen.getByRole("button", { name: /Approve/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/dns/)))
  })

  it("reports a network failure on a retry", async () => {
    apiFetch.mockRejectedValueOnce(new Error("offline"))
    mount(item({ kind: "failed_run", payload: { pipeline_slug: "nightly" } }))
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/offline/)))
  })

  it("reports a network failure on a consolidation decision", async () => {
    apiFetch.mockRejectedValueOnce(new Error("gone"))
    mount(item({ kind: "memory_consolidation", payload: { proposal_id: "p1" } }))
    fireEvent.click(screen.getByRole("button", { name: /Accept/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/gone/)))
  })

  it("falls back to a status code when the server sends no message", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 500, json: async () => { throw new Error("not json") } })
    mount(item({ kind: "waitpoint", source_id: "a1", payload: { kind: "hire" } }))
    fireEvent.click(screen.getByRole("button", { name: /Approve hire/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/500/)))
  })
})

describe("the schedule calls fail loudly too", () => {
  it("reports a network failure on re-enable", async () => {
    apiFetch.mockRejectedValueOnce(new Error("offline"))
    mount(item({ kind: "schedule_circuit_breaker_tripped", payload: { schedule_id: "s1" } }))
    fireEvent.click(screen.getByRole("button", { name: /Re-enable schedule/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/offline/)))
    expect(onResolve).not.toHaveBeenCalled()
  })

  it("reports a network failure on run-now", async () => {
    apiFetch.mockRejectedValueOnce(new Error("dns"))
    mount(item({ kind: "schedule_missed", payload: { schedule_id: "s2" } }))
    fireEvent.click(screen.getByRole("button", { name: /Run now/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/dns/)))
  })

  it("reports a refused run-now", async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 503, json: async () => ({ error: "runner not wired" }) })
    mount(item({ kind: "schedule_missed", payload: { schedule_id: "s2" } }))
    fireEvent.click(screen.getByRole("button", { name: /Run now/ }))

    await waitFor(() => expect(toastError).toHaveBeenCalledWith("runner not wired"))
  })

  it("offers only Dismiss when a missed notice carries no schedule id", () => {
    mount(item({ kind: "schedule_missed", payload: {} }))
    expect(screen.queryByRole("button", { name: /Run now/ })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: /Dismiss/ })).toBeInTheDocument()
  })
})

describe("the reject half of every escalation shape", () => {
  it("rejects a credential escalation that already has a proposed value", async () => {
    mount(item({ kind: "escalation", source_id: "e1", payload: { escalation_type: "CREDENTIAL", has_pending_credential: true } }))
    fireEvent.click(screen.getByRole("button", { name: /Reject/ }))
    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("e1", "reject", expect.any(String), "ws"))
  })

  it("rejects a legacy credential escalation, where approving is not offered at all", async () => {
    mount(item({ kind: "escalation", source_id: "e2", payload: { escalation_type: "CREDENTIAL" } }))
    fireEvent.click(screen.getByRole("button", { name: /Reject/ }))
    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("e2", "reject", expect.any(String), "ws"))
  })

  it("rejects a general escalation", async () => {
    mount(item({ kind: "escalation", source_id: "e3", payload: { escalation_type: "GENERAL" } }))
    fireEvent.click(screen.getByRole("button", { name: /Reject/ }))
    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("e3", "reject", expect.any(String), "ws"))
  })
})

describe("a kind this build has never heard of", () => {
  it("still offers a way out rather than rendering nothing", async () => {
    // The DB CHECK is widened by migration before the UI learns the kind, so
    // "unknown" is a state that reaches production by design.
    mount(item({ kind: "some_future_kind" as InboxItem["kind"] }))
    fireEvent.click(screen.getByRole("button", { name: /Dismiss/ }))

    await waitFor(() => expect(onResolve).toHaveBeenCalledWith("dismissed"))
  })
})

describe("the detail's own edges", () => {
  const noop = vi.fn()

  it("renders every chip the decision payloads can carry", () => {
    render(
      <InboxDetail
        item={item({
          kind: "escalation",
          payload: {
            credential_name: "GH_TOKEN", security_level: "HIGH", risk_score: 62, scan_status: "warn",
            consecutive_failures: 5, missed_count: 3, catchup_policy: "skip", rules_count: 7,
            entries_scanned: 340, escalation_type: "GENERAL", intent: "publish docs",
            risk_reasons: ["runs a shell"],
          },
        })}
        role="OWNER"
        onResolve={noop} onArchive={noop} onMarkUnread={noop} onRefresh={noop}
      />,
    )

    // Scoped to the decision card: several of these also appear in Context,
    // and the point is that they were promoted OUT of it.
    const card = within(screen.getByTestId("decision-card"))
    for (const text of [/GH_TOKEN/, /HIGH/, /risk 62/, /scan: warn/, /5 failures in a row/, /3 missed runs/,
      /catchup: skip/, /7 rules/, /340 entries scanned/, /publish docs/, /runs a shell/]) {
      expect(card.getByText(text)).toBeInTheDocument()
    }
  })

  it("renders no Context card when the payload is only plumbing", () => {
    render(
      <InboxDetail
        item={item({ kind: "message", payload: { reason: "hidden", source: "hidden", step_id: "hidden" } })}
        role="OWNER"
        onResolve={noop} onArchive={noop} onMarkUnread={noop} onRefresh={noop}
      />,
    )
    // Every key in that payload is on the hide-list, so a Context card would be
    // an empty box with a heading.
    expect(screen.queryByText("Context")).not.toBeInTheDocument()
  })

  it("offers Restore on a resolved item instead of Archive", () => {
    render(
      <InboxDetail
        item={item({ kind: "message", state: "resolved", resolved_action: "archived", resolved_at: new Date().toISOString() })}
        role="OWNER"
        onResolve={noop} onArchive={noop} onMarkUnread={noop} onRefresh={noop}
      />,
    )
    expect(screen.getByRole("button", { name: /Restore/ })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /^Archive$/ })).not.toBeInTheDocument()
  })
})

describe("the subject picker at scale", () => {
  beforeEach(() => {
    ITEMS = [item({ id: "m", kind: "message", title: "hello", sender_type: "agent", sender_name: "casey" })]
  })

  it("truncates a long group and asks for more typing rather than growing a scrollbar", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    fireEvent.change(screen.getByTestId("subject-search"), { target: { value: "car" } })

    // Seven agents match; a filter that needs scrolling has stopped narrowing.
    expect(screen.getByText(/keep typing/)).toBeInTheDocument()
  })

  it("clears the query from the field", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    fireEvent.change(screen.getByTestId("subject-search"), { target: { value: "car" } })

    fireEvent.click(screen.getByRole("button", { name: /Clear subject search/ }))

    expect(screen.getByTestId("subject-search")).toHaveValue("")
  })

  it("deselects the subject by picking it again", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    fireEvent.click(screen.getByTestId("subject-casey"))
    expect(screen.getByRole("button", { name: /Remove filter/ })).toBeInTheDocument()

    // The selected subject is pinned to the top of the picker; clicking it
    // there is how you undo it without hunting through the list.
    fireEvent.click(screen.getAllByTestId("subject-casey")[0])
    expect(screen.queryByRole("button", { name: /Remove filter/ })).not.toBeInTheDocument()
  })
})

describe("selection, used twice", () => {
  beforeEach(() => {
    ITEMS = [
      item({ id: "a", kind: "message", title: "one" }),
      item({ id: "b", kind: "message", title: "two" }),
    ]
  })

  it("unticks a row that was ticked", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))
    const box = () => screen.getByTestId("check-0").parentElement as HTMLElement

    fireEvent.click(box())
    expect(screen.getByText("1 selected")).toBeInTheDocument()

    fireEvent.click(box())
    expect(screen.queryByText("1 selected")).not.toBeInTheDocument()
  })

  it("unticks a whole group from its header", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))
    const header = () => within(screen.getByTestId("inbox-list")).getByLabelText(/Select all in/)

    fireEvent.click(header())
    expect(screen.getByText("2 selected")).toBeInTheDocument()

    fireEvent.click(header())
    expect(screen.queryByText("2 selected")).not.toBeInTheDocument()
  })

  it("drops the selection when select mode is left", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))
    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)

    fireEvent.click(screen.getByRole("button", { name: /Done selecting/i }))

    expect(screen.queryByText("1 selected")).not.toBeInTheDocument()
    expect(screen.queryByTestId("check-0")).not.toBeInTheDocument()
  })

  it("re-expands a collapsed group", () => {
    render(<InboxList />)
    const list = within(screen.getByTestId("inbox-list"))
    const header = () => list.getByRole("button", { name: /Everything else/ })

    fireEvent.click(header())
    expect(list.queryByText("one")).not.toBeInTheDocument()

    fireEvent.click(header())
    expect(list.getByText("one")).toBeInTheDocument()
  })
})

describe("dropdowns close on the backdrop", () => {
  beforeEach(() => { ITEMS = [item({ id: "a", kind: "message", title: "one" })] })

  it("closes Filter when the page behind it is clicked", async () => {
    const { container } = render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    expect(screen.getByTestId("facet-bucket-decisions")).toBeInTheDocument()

    fireEvent.click(container.querySelector(".fixed.inset-0") as HTMLElement)

    await waitFor(() => expect(screen.queryByTestId("facet-bucket-decisions")).not.toBeInTheDocument())
  })

  it("closes Display when the page behind it is clicked", async () => {
    const { container } = render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Display/ }))
    expect(screen.getByRole("button", { name: /Smart buckets/ })).toBeInTheDocument()

    fireEvent.click(container.querySelector(".fixed.inset-0") as HTMLElement)

    await waitFor(() => expect(screen.queryByRole("button", { name: /Smart buckets/ })).not.toBeInTheDocument())
  })

  it("resets the bucket filter to all", () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Filter/ }))
    fireEvent.click(screen.getByTestId("facet-bucket-decisions"))
    expect(screen.getByRole("button", { name: /Remove filter/ })).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /All buckets/ }))
    expect(screen.queryByRole("button", { name: /Remove filter/ })).not.toBeInTheDocument()
  })
})
