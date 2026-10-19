import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, renderHook, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { useState } from "react"

import type { InboxItem } from "@/hooks/use-inbox"
import type { ApprovalRow } from "@/lib/types/approvals"

import { approvalEntry, EMPTY_INBOX_V2_FILTERS, inboxEntry, type InboxV2Filters } from "../inbox-v2-derive"
import { filterInboxEntries } from "../inbox-entry-identity"
import { InboxV2Explorer } from "../inbox-v2-explorer"
import { useInboxV2DeepLink } from "../inbox-v2-deeplink"
import type { InboxAgentRef, InboxLookup } from "../inbox-v2-types"

// INB-01 (#3485, approvals X03): the Agent facet. Old `/approvals?agent_id=`
// links (escalation notifications) and the agent pages' "Review in Inbox"
// (`/inbox?agent=<slug>`) both name ONE agent; the Inbox shows that agent's
// rows under an "Agent: {name}" chip. GET /api/v1/inbox has no agent filter
// (#3455), so the facet narrows and counts over the loaded feed, like Crew.

const m = vi.hoisted(() => ({
  params: new URLSearchParams(),
  push: vi.fn(),
  items: [] as InboxItem[],
  approvals: [] as ApprovalRow[],
  lookup: null as InboxLookup | null,
}))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: vi.fn(async () => ({ ok: true, result: { updated: 0 } })) }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: m.push, replace: vi.fn() }),
  useSearchParams: () => m.params,
  usePathname: () => "/inbox",
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => new Response("[]")) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER" }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeStatusSafe: () => "connected" }))
vi.mock("@/hooks/use-inbox", () => ({
  useInbox: () => ({
    items: m.items, loading: false, error: null, unreadCount: 0,
    refresh: vi.fn(async () => {}), patch: vi.fn(async () => {}), actOnInboxItem: vi.fn(async () => {}),
  }),
  useInboxItem: () => ({ data: undefined, isFetching: false }),
  useInboxCounts: () => ({ data: null }),
}))
vi.mock("@/hooks/use-approvals", () => ({
  useApprovals: () => ({ rows: m.approvals, loading: false, error: null, notConfigured: false, refresh: vi.fn(async () => {}), patchRow: vi.fn() }),
  decideApproval: vi.fn(),
}))
vi.mock("@/components/features/inbox/use-inbox-lookup", () => ({ useInboxLookup: () => m.lookup }))
vi.mock("../inbox-v2-detail", () => ({
  InboxV2Detail: ({ entry }: { entry: { title?: string } | null }) => <p>Detail {entry?.title ?? "none"}</p>,
}))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

import { InboxV2 } from "../inbox-v2"

const SCOUT: InboxAgentRef = { id: "ag_scout", slug: "scout", name: "Scout" }
const RILEY: InboxAgentRef = { id: "ag_riley", slug: "riley", name: "Riley" }

function lookupOf(agents: InboxAgentRef[], ready = true): InboxLookup {
  return {
    crewById: new Map(),
    agentBySlug: new Map(agents.map((a) => [a.slug, a])),
    agentById: new Map(agents.map((a) => [a.id, a])),
    ready,
  }
}

function item(o: Partial<InboxItem>): InboxItem {
  return {
    id: "i1", workspace_id: "ws", kind: "escalation", source_id: "s", title: "An ask",
    state: "unread", priority: "medium", blocking: true,
    created_at: "2026-10-17T10:00:00Z", updated_at: "2026-10-17T10:00:00Z", ...o,
  }
}

function approval(o: Partial<ApprovalRow>): ApprovalRow {
  return {
    id: "ap1", workspace_id: "ws", kind: "tool_call", status: "pending", reason: "Merge PR #812",
    requested_by: "", created_at: "2026-10-17T10:00:00Z", payload: {}, ...o,
  } as ApprovalRow
}

// Scout asks twice (an escalation by slug, an approval by id), Riley once.
const SCOUT_ESC = item({ id: "e1", title: "Scout needs a key", sender_type: "agent", sender_name: "scout", payload: { escalation_type: "GENERAL" } })
const RILEY_ESC = item({ id: "e2", title: "Riley needs a key", sender_type: "agent", sender_name: "riley", payload: { escalation_type: "GENERAL" } })
const SCOUT_APPROVAL = approval({ id: "ap1", agent_id: "ag_scout", payload: { tool: "github.merge_pr" } })

describe("filterInboxEntries — the agent facet", () => {
  const feed = [inboxEntry(SCOUT_ESC), inboxEntry(RILEY_ESC), approvalEntry(SCOUT_APPROVAL)]
  const titles = (filters: Partial<InboxV2Filters>, lookup = lookupOf([SCOUT, RILEY])) =>
    filterInboxEntries(feed, { ...EMPTY_INBOX_V2_FILTERS, ...filters }, lookup).map((e) => e.key).sort()

  it("keeps the rows of the agent named by id, whichever key the source uses", () => {
    // The approvals queue names the agent by id, an escalation by slug.
    expect(titles({ agent: "ag_scout" })).toEqual(["approval:ap1", "inbox:e1"])
  })

  it("accepts the agent's slug, as /inbox?agent=<slug> sends it", () => {
    expect(titles({ agent: "riley" })).toEqual(["inbox:e2"])
  })

  it("matches a sender the roster does not know by the slug the row carries", () => {
    expect(titles({ agent: "scout" }, lookupOf([]))).toEqual(["inbox:e1"])
  })

  it("leaves nothing for an agent nobody in the feed is", () => {
    expect(titles({ agent: "ag_1" })).toEqual([])
  })
})

describe("the Agent facet in the panel", () => {
  const feed = [inboxEntry(SCOUT_ESC), inboxEntry(RILEY_ESC), approvalEntry(SCOUT_APPROVAL)]
  function show(filters: Partial<InboxV2Filters>, over: Partial<React.ComponentProps<typeof InboxV2Explorer>> = {}) {
    const onFilters = vi.fn()
    render(
      <InboxV2Explorer
        view="action" onView={vi.fn()} viewCounts={{ action: 3, updates: 0, history: 0 }}
        entries={feed} visible={feed} filters={{ ...EMPTY_INBOX_V2_FILTERS, ...filters }} onFilters={onFilters}
        selectedKey={null} onOpen={vi.fn()} lookup={lookupOf([SCOUT, RILEY])} {...over}
      />,
    )
    return onFilters
  }
  afterEach(cleanup)

  it("names the chip by the roster: Agent: Scout, and its × says which filter it removes", () => {
    const onFilters = show({ agent: "ag_scout" })
    expect(screen.getByText("Agent: Scout")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Remove filter Agent: Scout" }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ agent: null }))
  })

  it("says Agent: unknown for an id the roster does not have, and the list says nothing matches", () => {
    const onFilters = show({ agent: "ag_1" }, { visible: [] })
    expect(screen.getByRole("button", { name: "Remove filter Agent: unknown" })).toBeInTheDocument()
    expect(screen.getByText("Nothing matches these filters.")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }))
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ agent: null }))
  })

  it("lists the agents of the feed with counts over the whole space, and picks one by id", () => {
    const onFilters = show({}, { visible: [feed[1]] })
    fireEvent.click(screen.getByRole("button", { name: /^Filter/ }))
    const block = screen.getByRole("group", { name: "Filter inbox" })
    fireEvent.click(within(block).getByRole("button", { name: /^Agent/ }))
    const scout = within(block).getByRole("button", { name: /Scout/ })
    // Two rows from Scout in the space, although only Riley's is on screen.
    expect(scout).toHaveTextContent("2")
    fireEvent.click(scout)
    expect(onFilters).toHaveBeenCalledWith(expect.objectContaining({ agent: "ag_scout" }))
  })
})

describe("the URL names the agent", () => {
  function harness(initial: { agent: string | null }) {
    return renderHook(
      ({ agent }: { agent: string | null }) => {
        const [, setSelectedKey] = useState<string | null>(null)
        const [filters, setFilters] = useState<InboxV2Filters>({ ...EMPTY_INBOX_V2_FILTERS, agent })
        useInboxV2DeepLink(null, "", setSelectedKey, setFilters, agent)
        return { filters, setFilters }
      },
      { initialProps: initial },
    )
  }

  it("follows a same-route navigation to another agent, and drops it when the link names none", () => {
    const { result, rerender } = harness({ agent: "ag_scout" })
    rerender({ agent: "riley" })
    expect(result.current.filters.agent).toBe("riley")
    rerender({ agent: null })
    expect(result.current.filters.agent).toBeNull()
  })

  it("does not clobber an agent the user picked while the URL stayed put", () => {
    const { result, rerender } = harness({ agent: "ag_scout" })
    act(() => result.current.setFilters((f) => ({ ...f, agent: "ag_riley" })))
    rerender({ agent: "ag_scout" })
    expect(result.current.filters.agent).toBe("ag_riley")
  })
})

describe("/inbox?attention=approvals&agent_id= — where an old /approvals?agent_id= link lands", () => {
  function page(query: string) {
    m.params = new URLSearchParams(query)
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    return render(<QueryClientProvider client={client}><InboxV2 /></QueryClientProvider>)
  }
  const panel = () => document.querySelector("[data-slot=secondary-panel]") as HTMLElement

  beforeEach(() => {
    m.items = [SCOUT_ESC, RILEY_ESC]
    m.approvals = [SCOUT_APPROVAL, approval({ id: "ap2", agent_id: "ag_riley", payload: { tool: "deploy.production" } })]
    m.lookup = lookupOf([SCOUT, RILEY])
    m.push.mockClear()
  })
  afterEach(cleanup)

  it("shows only that agent's decisions under an Agent chip", () => {
    page("attention=approvals&agent_id=ag_scout")
    expect(within(panel()).getByText("Agent: Scout")).toBeInTheDocument()
    expect(within(panel()).getByText("Approve github.merge_pr")).toBeInTheDocument()
    expect(within(panel()).queryByText("Approve deploy.production")).toBeNull()
    expect(within(panel()).queryByText("Riley needs a key")).toBeNull()
  })

  it("reads ?agent=<slug> the same way", () => {
    page("agent=riley")
    expect(within(panel()).getByText("Agent: Riley")).toBeInTheDocument()
    expect(within(panel()).getByText("Riley needs a key")).toBeInTheDocument()
    expect(within(panel()).queryByText("Scout needs a key")).toBeNull()
  })

  it("an unknown agent id says so and offers Clear filters, which also leaves the link", () => {
    page("attention=approvals&agent_id=ag_1")
    expect(within(panel()).getByRole("button", { name: "Remove filter Agent: unknown" })).toBeInTheDocument()
    expect(within(panel()).getByText("Nothing matches these filters.")).toBeInTheDocument()
    expect(within(panel()).queryAllByTestId("inbox-row")).toHaveLength(0)
    fireEvent.click(within(panel()).getByRole("button", { name: "Remove filter Agent: unknown" }))
    // The chip is gone and the URL no longer names the agent, so a reload
    // does not bring the filter back.
    expect(within(panel()).queryByText(/^Agent:/)).toBeNull()
    expect(m.push).toHaveBeenLastCalledWith("/inbox?attention=approvals")
  })
})
