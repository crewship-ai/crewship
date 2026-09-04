import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, within, waitFor } from "@testing-library/react"

import type { InboxItem } from "@/hooks/use-inbox"

// Every action in this surface is a call to a specific endpoint, and the ones
// that go wrong go wrong QUIETLY: the server does the work, the row leaves the
// list, and the pane keeps rendering a stale copy so it looks like the click
// did nothing. So each branch gets a test that names the call it must make and
// asserts the pane moved on afterwards.

const patch = vi.fn().mockResolvedValue(undefined)
const refresh = vi.fn().mockResolvedValue(undefined)
const apiFetch = vi.fn()
const waitpointDecide = vi.fn()
const escalationResolve = vi.fn()
const inboxBulk = vi.fn()

// The pane names crews and agents through this lookup; the network it
// would use is not this suite's concern.
vi.mock("../use-inbox-lookup", () => ({
  useInboxLookup: () => ({ crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: true }),
}))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: "OWNER" }),
  useCurrentWorkspaceId: () => "ws-test",
}))
vi.mock("@/hooks/use-dashboard-data", () => ({ useAgentSummaries: () => ({ data: [] }) }))
vi.mock("@/hooks/use-pipelines", () => ({ usePipelines: () => ({ pipelines: [], refresh: vi.fn() }) }))
// Partial: use-websocket imports broadcastSessionExpired from the same module,
// so replacing the whole thing breaks the realtime provider on import.
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
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: (...a: unknown[]) => inboxBulk(...a) }))
vi.mock("../waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))

const now = Date.now()

function item(over: Partial<InboxItem> & Pick<InboxItem, "id" | "kind" | "title">): InboxItem {
  return {
    workspace_id: "ws-test",
    source_id: `src-${over.id}`,
    state: "unread",
    priority: "medium",
    blocking: false,
    created_at: new Date(now - 60_000).toISOString(),
    updated_at: new Date(now - 60_000).toISOString(),
    ...over,
  } as InboxItem
}

// Newest first is the default sort, so the ORDER here decides which row the
// pane opens on. Each test clicks the row it means.
const ITEMS: InboxItem[] = [
  item({ id: "msg", kind: "message", title: "Atlas replied", sender_type: "agent", sender_name: "atlas" }),
  item({
    id: "wp", kind: "waitpoint", title: "Approve promote", blocking: true, source_id: "tok-1",
    sender_type: "pipeline", sender_name: "docs-publish",
    created_at: new Date(now - 2 * 60_000).toISOString(),
  }),
  item({
    id: "esc", kind: "escalation", title: "Agent escalation", blocking: true, source_id: "esc-1",
    sender_type: "agent", sender_name: "casey",
    created_at: new Date(now - 3 * 60_000).toISOString(),
    payload: { escalation_type: "GENERAL", reason: "needs a key" },
  }),
  item({
    id: "skill", kind: "escalation", title: "Skill review", blocking: true,
    sender_type: "agent", sender_name: "casey",
    created_at: new Date(now - 4 * 60_000).toISOString(),
    payload: { kind: "skill_proposal", crew_id: "c1", file_name: "f.md", slug: "log-parser" },
  }),
  item({
    id: "mem", kind: "memory_consolidation", title: "Memory consolidation",
    sender_type: "system", sender_name: "consolidator",
    created_at: new Date(now - 5 * 60_000).toISOString(),
    payload: { proposal_id: "prop-1", rules_count: 7 },
  }),
  item({
    id: "run", kind: "failed_run", title: "Scheduled routine failed",
    sender_type: "pipeline", sender_name: "nightly",
    created_at: new Date(now - 6 * 60_000).toISOString(),
    payload: { pipeline_slug: "nightly", inputs: { a: 1 } },
  }),
  item({
    id: "brk", kind: "schedule_circuit_breaker_tripped", title: "Routine paused",
    sender_type: "pipeline", sender_name: "nightly",
    created_at: new Date(now - 7 * 60_000).toISOString(),
    payload: { schedule_id: "sch-1", consecutive_failures: 5 },
  }),
]

vi.mock("@/hooks/use-inbox", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/use-inbox")>()
  return {
    ...actual,
    useInbox: () => ({ items: ITEMS, unreadCount: 7, loading: false, error: null, patch, refresh }),
  }
})

import { InboxList } from "../inbox-list"

beforeEach(() => {
  vi.clearAllMocks()
  apiFetch.mockResolvedValue({ ok: true, json: async () => ({}) })
  waitpointDecide.mockResolvedValue({ ok: true })
  escalationResolve.mockResolvedValue({ ok: true })
  inboxBulk.mockResolvedValue({ ok: true, result: { updated: 1, skipped: 0, not_found: 0 } })
})
afterEach(cleanup)

function open(title: string) {
  fireEvent.click(within(screen.getByTestId("inbox-list")).getByText(title))
}

function card() {
  return within(screen.getByTestId("decision-card"))
}

describe("dismiss and archive", () => {
  it("dismisses a message through the inbox PATCH", async () => {
    render(<InboxList />)
    open("Atlas replied")

    fireEvent.click(screen.getByRole("button", { name: /^Dismiss$/ }))

    await waitFor(() => expect(patch).toHaveBeenCalledWith("msg", "resolved", "dismissed"))
  })

  it("moves the pane on after a decision instead of pinning the stale copy", async () => {
    render(<InboxList />)
    // Deliberately not the first row: the pane falls back to the top of the
    // list once the selection is released, so a mid-list item makes the move
    // observable.
    open("Scheduled routine failed")
    const pane = () => screen.getByTestId("reading-pane")
    expect(within(pane()).getByText("Scheduled routine failed")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /^Cancel$/ }))

    // Holding the resolved item — buttons and all — is what made the click
    // look like a no-op while the server had already done the work.
    await waitFor(() => expect(refresh).toHaveBeenCalled())
    await waitFor(() =>
      expect(within(pane()).queryByText("Scheduled routine failed")).not.toBeInTheDocument(),
    )
    expect(within(pane()).getByText("Atlas replied")).toBeInTheDocument()
  })

  it("archives with its own resolved_action so the audit trail can tell them apart", async () => {
    render(<InboxList />)
    open("Atlas replied")

    fireEvent.click(screen.getByRole("button", { name: /^Archive$/ }))

    await waitFor(() => expect(patch).toHaveBeenCalledWith("msg", "resolved", "archived"))
  })

  it("marks an item unread", async () => {
    render(<InboxList />)
    open("Atlas replied")

    fireEvent.click(screen.getByRole("button", { name: /Mark unread/ }))

    await waitFor(() => expect(patch).toHaveBeenCalledWith("msg", "unread"))
  })

  it("does not offer Archive on a blocking decision", () => {
    render(<InboxList />)
    open("Approve promote")

    // A waitpoint whose source is still live is source-managed: the inbox
    // PATCH 409s on anything but "read", so an Archive button could only ever
    // fail. The server says so on the detail read via source_missing, which is
    // absent here.
    expect(screen.queryByRole("button", { name: /^Archive$/ })).not.toBeInTheDocument()
  })
})

describe("each kind reaches its own endpoint", () => {
  it("approves a waitpoint through the waitpoint endpoint, with approved=true", async () => {
    render(<InboxList />)
    open("Approve promote")

    fireEvent.click(card().getByRole("button", { name: /Approve/ }))

    // The boolean is what disambiguates approve from deny — an empty body
    // decoded to approved=false in Go and silently denied.
    await waitFor(() => expect(waitpointDecide).toHaveBeenCalledWith("ws-test", "tok-1", true))
    // The server cascades the inbox row itself, so the UI must NOT patch it.
    expect(patch).not.toHaveBeenCalledWith("wp", "resolved", expect.anything())
  })

  it("denies a waitpoint with approved=false", async () => {
    render(<InboxList />)
    open("Approve promote")

    fireEvent.click(card().getByRole("button", { name: /Deny/ }))

    await waitFor(() => expect(waitpointDecide).toHaveBeenCalledWith("ws-test", "tok-1", false))
  })

  it("resolves an escalation through the escalation lifecycle", async () => {
    render(<InboxList />)
    open("Agent escalation")

    fireEvent.click(card().getByRole("button", { name: /Approve/ }))

    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("esc-1", "approve", expect.any(String), "ws-test"))
  })

  it("approves a skill proposal through the proposed-skills endpoint", async () => {
    render(<InboxList />)
    open("Skill review")

    fireEvent.click(card().getByRole("button", { name: /Approve/ }))

    await waitFor(() => {
      const url = apiFetch.mock.calls[0]?.[0] as string
      expect(url).toContain("/api/v1/skills/proposed/approve")
    })
    const body = JSON.parse((apiFetch.mock.calls[0]?.[1] as { body: string }).body)
    expect(body).toEqual({ crew_id: "c1", file_name: "f.md" })
  })

  it("accepts a memory consolidation through the endpoint that already existed", async () => {
    render(<InboxList />)
    open("Memory consolidation")

    fireEvent.click(card().getByRole("button", { name: /Accept/ }))

    await waitFor(() => {
      const url = apiFetch.mock.calls[0]?.[0] as string
      expect(url).toContain("/api/v1/consolidate/proposed/prop-1/approve")
    })
  })

  it("retries a failed run against the routine, replaying its inputs", async () => {
    render(<InboxList />)
    open("Scheduled routine failed")

    fireEvent.click(screen.getByRole("button", { name: /Retry/ }))

    await waitFor(() => {
      const url = apiFetch.mock.calls[0]?.[0] as string
      expect(url).toContain("/pipelines/nightly/run")
    })
    const body = JSON.parse((apiFetch.mock.calls[0]?.[1] as { body: string }).body)
    expect(body).toEqual({ inputs: { a: 1 }, triggered_via: "manual" })
  })

  it("re-enables a tripped breaker through the schedules endpoint", async () => {
    render(<InboxList />)
    open("Routine paused")

    fireEvent.click(card().getByRole("button", { name: /Re-enable schedule/ }))

    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        "/api/v1/workspaces/ws-test/pipeline-schedules/sch-1",
        expect.objectContaining({ method: "PATCH" }),
      ))
  })
})

describe("bulk", () => {
  it("resolves the ticked rows through the bulk endpoint", async () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))
    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)

    fireEvent.click(screen.getByRole("button", { name: /^Resolve$/ }))

    await waitFor(() => expect(inboxBulk).toHaveBeenCalledWith("ws-test", expect.any(Array), "resolved", "dismissed"))
  })

  it("marks the ticked rows read", async () => {
    render(<InboxList />)
    fireEvent.click(screen.getByRole("button", { name: /Select items/i }))
    fireEvent.click(screen.getByTestId("check-0").parentElement as HTMLElement)

    fireEvent.click(screen.getByRole("button", { name: /Mark read/ }))

    await waitFor(() => expect(inboxBulk).toHaveBeenCalledWith("ws-test", expect.any(Array), "read", undefined))
  })
})
