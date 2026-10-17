import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { InboxItem } from "@/hooks/use-inbox"

// INB-03 (#3485, inbox.md INB-8): every decision endpoint the legacy
// /inbox list (inbox-list.tsx + inbox-list-panel.tsx) was tested through,
// driven through the page that replaced it — InboxV2 with its real detail
// (InboxV2Detail → InboxDetail → KindActions). Ported from
// components/features/inbox/__tests__/inbox-actions.test.tsx,
// inbox-list.test.tsx and inbox-panel.test.tsx; the old→new map is in the PR.
//
// Every action is a call to a specific endpoint, and the ones that go wrong go
// wrong QUIETLY: the server does the work, the row leaves the list, and the
// pane keeps a stale copy so the click looks like it did nothing. So each
// branch names the call it must make and asserts the pane moved on.

const m = vi.hoisted(() => ({
  role: "OWNER",
  items: [] as InboxItem[],
  loading: false,
  error: null as string | null,
  params: new URLSearchParams(),
}))
const patch = vi.fn(async () => {})
const refresh = vi.fn(async () => {})
const apiFetch = vi.fn()
const waitpointDecide = vi.fn()
const escalationResolve = vi.fn()
const inboxBulk = vi.fn()

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => m.params,
  usePathname: () => "/inbox",
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", role: m.role }),
  useCurrentWorkspaceId: () => "ws-test",
}))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: "me" } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeStatusSafe: () => "connected" }))
vi.mock("@/hooks/use-inbox", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-inbox")>()),
  useInbox: () => ({
    items: m.items, loading: m.loading, error: m.error, unreadCount: 0,
    patch, refresh, actOnInboxItem: vi.fn(async () => ({ ok: true })),
  }),
  useInboxItem: () => ({ data: undefined, isFetching: false }),
}))
vi.mock("@/hooks/use-approvals", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-approvals")>()),
  useApprovals: () => ({ rows: [], loading: false, error: null, notConfigured: false, refresh: vi.fn(async () => {}), patchRow: vi.fn() }),
}))
// The pane names crews and agents through this lookup; the network it
// would use is not this suite's concern.
vi.mock("@/components/features/inbox/use-inbox-lookup", () => ({
  useInboxLookup: () => ({ crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: true }),
}))
// Partial: use-websocket imports broadcastSessionExpired from the same module.
vi.mock("@/lib/api-fetch", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-fetch")>()),
  apiFetch: (...a: unknown[]) => apiFetch(...a),
}))
// Partial: kind-actions also imports isAlreadyDecidedError, and the REAL
// predicate is what reads a 409 as somebody else finishing.
vi.mock("@/lib/api/waitpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/waitpoints")>()),
  waitpointDecide: (...a: unknown[]) => waitpointDecide(...a),
}))
vi.mock("@/lib/api/escalations", () => ({ escalationResolve: (...a: unknown[]) => escalationResolve(...a) }))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: (...a: unknown[]) => inboxBulk(...a) }))
vi.mock("@/components/features/inbox/waitpoint-run-detail", () => ({ WaitpointRunDetail: () => null }))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

import { InboxV2 } from "../inbox-v2"

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

const ITEMS: InboxItem[] = [
  item({ id: "msg", kind: "message", title: "Atlas replied", sender_type: "agent", sender_name: "atlas" }),
  item({
    id: "wp", kind: "waitpoint", title: "Approve promote", blocking: true, source_id: "tok-1",
    sender_type: "pipeline", sender_name: "docs-publish",
    created_at: new Date(now - 2 * 60_000).toISOString(),
    // 30 s of slack so the label cannot flip to 10m on a slow machine.
    payload: { timeout_at: new Date(now + 11 * 60_000 + 30_000).toISOString(), api_key: "sk-supersecret", run_id: "r1" },
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
    body_md: "Parses nginx access logs into fields.", payload: { kind: "skill_proposal", crew_id: "c1", file_name: "f.md", slug: "log-parser" },
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

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><InboxV2 /></QueryClientProvider>)
}

const panel = () => within(document.querySelector("[data-slot=secondary-panel]") as HTMLElement)
const pane = () => within(screen.getByRole("main", { name: "Inbox detail" }))
const card = () => within(screen.getByTestId("decision-card"))

/** Open a row from the list, in whichever space it lives. */
function open(title: string) {
  const inList = () => panel().queryByText(title)
  for (const space of [/To handle/, /Updates/, /History/]) {
    if (inList()) break
    fireEvent.click(panel().getByRole("tab", { name: space }))
  }
  fireEvent.click(panel().getByText(title))
}

/** The one apiFetch call that hit `path` — the page fetches missions, routines and issues too. */
function callTo(path: string) {
  const call = apiFetch.mock.calls.find(([url]) => typeof url === "string" && url.includes(path))
  if (!call) throw new Error(`no call to ${path}; saw ${apiFetch.mock.calls.map(([u]) => u).join(", ")}`)
  return call as [string, RequestInit | undefined]
}

function openMenu() {
  fireEvent.keyDown(screen.getByRole("button", { name: "Message options" }), { key: "ArrowDown" })
}

beforeEach(() => {
  vi.clearAllMocks()
  m.role = "OWNER"
  m.items = ITEMS
  m.loading = false
  m.error = null
  m.params = new URLSearchParams()
  // GETs (missions, routines, issues) answer a list; decisions an object.
  apiFetch.mockImplementation(async (_url: string, init?: RequestInit) =>
    new Response(!init?.method || init.method === "GET" ? "[]" : "{}", { status: 200, headers: { "Content-Type": "application/json" } }))
  waitpointDecide.mockResolvedValue({ ok: true })
  escalationResolve.mockResolvedValue({ ok: true })
  inboxBulk.mockResolvedValue({ ok: true, result: { updated: 1, skipped: 0, not_found: 0 } })
})
afterEach(cleanup)

describe("dismiss and archive", () => {
  it("dismisses a message through the inbox PATCH", async () => {
    show()
    open("Atlas replied")
    fireEvent.click(pane().getByRole("button", { name: /^Dismiss$/ }))
    await waitFor(() => expect(patch).toHaveBeenCalledWith("msg", "resolved", "dismissed"))
  })

  it("moves the pane on after a decision instead of pinning the stale copy", async () => {
    show()
    open("Scheduled routine failed")
    expect(pane().getByRole("button", { name: /^Retry$/ })).toBeInTheDocument()
    fireEvent.click(pane().getByRole("button", { name: /^Cancel$/ }))
    await waitFor(() => expect(patch).toHaveBeenCalledWith("run", "resolved", "cancelled"))
    // The receipt replaces the item — buttons and all. Holding the resolved
    // copy is what made the click look like a no-op.
    await waitFor(() => expect(screen.getByTestId("inbox-receipt")).toHaveTextContent("Cancelled"))
    expect(pane().queryByRole("button", { name: /^Retry$/ })).toBeNull()
    expect(refresh).toHaveBeenCalled()
  })

  it("archives with its own resolved_action so the audit trail can tell them apart", async () => {
    show()
    open("Atlas replied")
    openMenu()
    fireEvent.click(screen.getByRole("menuitem", { name: /^Archive$/ }))
    await waitFor(() => expect(patch).toHaveBeenCalledWith("msg", "resolved", "archived"))
  })

  it("marks an item unread", async () => {
    show()
    open("Atlas replied")
    openMenu()
    fireEvent.click(screen.getByRole("menuitem", { name: /Mark unread/ }))
    await waitFor(() => expect(patch).toHaveBeenCalledWith("msg", "unread"))
  })

  it("does not offer Archive on a blocking decision", () => {
    show()
    open("Approve promote")
    // Source-managed: the inbox PATCH 409s on anything but "read" while the
    // source is live, so an Archive could only ever fail.
    expect(screen.getByRole("button", { name: "Archive (E)" })).toBeDisabled()
    openMenu()
    expect(screen.queryByRole("menuitem", { name: /^Archive$/ })).toBeNull()
  })
})

describe("each kind reaches its own endpoint", () => {
  it("approves a waitpoint through the waitpoint endpoint, with approved=true", async () => {
    show()
    open("Approve promote")
    fireEvent.click(card().getByRole("button", { name: /^Approve$/ }))
    // The boolean is what disambiguates approve from deny — an empty body
    // decoded to approved=false in Go and silently denied.
    await waitFor(() => expect(waitpointDecide).toHaveBeenCalledWith("ws-test", "tok-1", true))
    // The server cascades the inbox row itself, so the UI must NOT patch it.
    expect(patch).not.toHaveBeenCalledWith("wp", "resolved", expect.anything())
  })

  it("denies a waitpoint with approved=false", async () => {
    show()
    open("Approve promote")
    fireEvent.click(card().getByRole("button", { name: /^Deny$/ }))
    await waitFor(() => expect(waitpointDecide).toHaveBeenCalledWith("ws-test", "tok-1", false))
  })

  it("resolves an escalation through the escalation lifecycle", async () => {
    show()
    open("Agent escalation")
    fireEvent.click(card().getByRole("button", { name: /^Approve$/ }))
    await waitFor(() => expect(escalationResolve).toHaveBeenCalledWith("esc-1", "approve", expect.any(String), "ws-test"))
  })

  it("approves a skill proposal through the proposed-skills endpoint", async () => {
    show()
    open("Skill review")
    fireEvent.click(card().getByRole("button", { name: /^Approve$/ }))
    await waitFor(() => callTo("/api/v1/skills/proposed/approve"))
    expect(JSON.parse(callTo("/api/v1/skills/proposed/approve")[1]!.body as string)).toEqual({ crew_id: "c1", file_name: "f.md" })
  })

  it("accepts a memory consolidation through its proposal endpoint", async () => {
    show()
    open("Memory consolidation")
    fireEvent.click(card().getByRole("button", { name: /^Accept$/ }))
    await waitFor(() => callTo("/api/v1/consolidate/proposed/prop-1/approve"))
  })

  it("retries a failed run against the routine, replaying its inputs", async () => {
    show()
    open("Scheduled routine failed")
    fireEvent.click(pane().getByRole("button", { name: /^Retry$/ }))
    await waitFor(() => callTo("/pipelines/nightly/run"))
    const [, init] = callTo("/pipelines/nightly/run")
    expect(JSON.parse(init!.body as string)).toEqual({ inputs: { a: 1 }, triggered_via: "manual" })
    expect(new Headers(init!.headers).get("Prefer")).toBe("respond-async")
  })

  it("re-enables a tripped breaker through the schedules endpoint", async () => {
    show()
    open("Routine paused")
    fireEvent.click(card().getByRole("button", { name: /Re-enable schedule/ }))
    await waitFor(() => expect(callTo("/api/v1/workspaces/ws-test/pipeline-schedules/sch-1")[1]).toEqual(expect.objectContaining({ method: "PATCH" })))
  })
})

describe("bulk", () => {
  function pick(title: string) {
    fireEvent.click(panel().getByText(title))
  }

  it("archives the picked rows through the bulk endpoint, leaving open decisions alone", async () => {
    show()
    fireEvent.click(panel().getByRole("tab", { name: /Updates/ }))
    fireEvent.click(screen.getByRole("button", { name: "Select several" }))
    pick("Atlas replied")
    await act(async () => { fireEvent.click(within(screen.getByTestId("inbox-bulk")).getByRole("button", { name: /Archive/ })) })
    expect(inboxBulk).toHaveBeenCalledWith("ws-test", ["msg"], "resolved", "archived")
  })

  it("marks the picked rows read", async () => {
    show()
    fireEvent.click(screen.getByRole("button", { name: "Select several" }))
    pick("Approve promote")
    await act(async () => { fireEvent.click(within(screen.getByTestId("inbox-bulk")).getByRole("button", { name: /Mark read/ })) })
    expect(inboxBulk).toHaveBeenCalledWith("ws-test", ["wp"], "read")
  })
})

describe("kinds, deadlines and who decides", () => {
  it("gives a tripped circuit breaker its own card instead of a generic notification", () => {
    show()
    open("Routine paused")
    expect(card().getByText(/Routine is disabled/i)).toBeInTheDocument()
    expect(card().getByText(/5 failures in a row/i)).toBeInTheDocument()
  })

  it("counts down a waitpoint instead of showing how old it is", () => {
    show()
    open("Approve promote")
    expect(card().getByText(/in 11m/)).toBeInTheDocument()
  })

  it("lets an OWNER decide a skill proposal", () => {
    show()
    open("Skill review")
    expect(card().getByRole("button", { name: /^Approve$/ })).toBeEnabled()
  })

  it("withholds it from a MANAGER and names who decides", () => {
    m.role = "MANAGER"
    show()
    open("Skill review")
    // /skills/proposed/approve is roleManage while the row is addressed to MANAGER.
    expect(card().getByRole("button", { name: /^Approve$/ })).toBeDisabled()
    expect(card().getByText(/owner or admin decides this/i)).toBeInTheDocument()
  })

  it("keeps the waitpoint decidable by a MANAGER", () => {
    m.role = "MANAGER"
    show()
    open("Approve promote")
    expect(card().getByRole("button", { name: /^Approve$/ })).toBeEnabled()
  })
})

describe("search and context", () => {
  it("searches the body, not only the title", () => {
    show()
    fireEvent.change(screen.getByRole("textbox", { name: "Search inbox…" }), { target: { value: "nginx" } })
    expect(panel().getByText("Skill review")).toBeInTheDocument()
    expect(panel().queryByText("Approve promote")).toBeNull()
  })

  it("masks a credential-looking payload value until it is asked for", () => {
    show()
    open("Approve promote")
    fireEvent.click(pane().getByText("Additional context"))
    expect(pane().getByText("••••••••")).toBeInTheDocument()
    expect(pane().queryByText("sk-supersecret")).toBeNull()
    fireEvent.click(pane().getByRole("button", { name: /Reveal value/ }))
    expect(pane().getByText("sk-supersecret")).toBeInTheDocument()
    fireEvent.click(pane().getByRole("button", { name: /Hide value/ }))
    expect(pane().queryByText("sk-supersecret")).toBeNull()
  })

  it("leaves a plain identifier alone — a run id is not a secret, and it is a link", () => {
    show()
    open("Approve promote")
    expect(pane().getByRole("link", { name: /Open run/ }).getAttribute("href")).toContain("r1")
  })
})

describe("history and the states nobody looks at until they happen", () => {
  it("shows the outcome of a decided item", () => {
    m.items = [item({
      id: "a1", kind: "escalation", title: "casey asked for GH_TOKEN", state: "resolved",
      sender_type: "agent", sender_name: "casey", resolved_at: new Date(now - 20 * 60_000).toISOString(),
      resolved_action: "approved", resolved_by_user_id: "pavel",
    })]
    show()
    open("casey asked for GH_TOKEN")
    expect(screen.getByTestId("history-receipt")).toHaveTextContent("Approved")
  })

  it("says so while loading", () => {
    m.items = []
    m.loading = true
    show()
    expect(panel().getByRole("status", { name: "Loading inbox" })).toBeInTheDocument()
  })

  it("shows the failure rather than an empty inbox", async () => {
    m.items = []
    m.error = "boom"
    show()
    expect(await screen.findByText("The inbox could not load")).toBeInTheDocument()
    expect(screen.queryByRole("heading", { name: "Nothing waits for you" })).toBeNull()
  })

  it("distinguishes an empty inbox from an empty history", async () => {
    m.items = []
    show()
    expect(await screen.findByRole("heading", { name: "Nothing waits for you" })).toBeInTheDocument()
    fireEvent.click(panel().getByRole("tab", { name: /History/ }))
    expect(panel().getByText(/No decisions recorded yet/)).toBeInTheDocument()
  })
})
