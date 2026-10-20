import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

import type { InboxItem } from "@/hooks/use-inbox"

// INB-02 (#3485, inbox.md INB-7): on a wide desktop (≥ 1536 px, ODCH-07) the
// right rail carries what v67 marks `data-rail` — "Where this came from" and
// the expandable run context ("How the run got here", "Additional context",
// opened) — beside the sender card and "Earlier from". The middle column keeps
// only the message and the ask. Below 1536 nothing moves.

const m = vi.hoisted(() => ({ wide: true, params: new URLSearchParams("item=wp1"), items: [] as InboxItem[] }))
vi.mock("@/lib/api/inbox", () => ({ inboxBulk: vi.fn() }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => false }))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useSearchParams: () => m.params,
  usePathname: () => "/inbox",
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn(async () => new Response("[]")) }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: "ws", role: "OWNER" }) }))
vi.mock("@/hooks/use-auth", () => ({ useSession: () => ({ data: { user: { id: "u1" } } }) }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: () => {}, useRealtimeStatusSafe: () => "connected" }))
vi.mock("@/hooks/use-inbox", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/use-inbox")>()),
  useInbox: () => ({
    items: m.items, loading: false, error: null, unreadCount: 0,
    refresh: vi.fn(async () => {}), patch: vi.fn(async () => {}), actOnInboxItem: vi.fn(async () => {}),
  }),
  useInboxItem: () => ({ data: undefined, isFetching: false }),
}))
vi.mock("@/hooks/use-approvals", () => ({
  useApprovals: () => ({ rows: [], loading: false, error: null, notConfigured: false, refresh: vi.fn(async () => {}), patchRow: vi.fn() }),
  decideApproval: vi.fn(),
}))
vi.mock("@/components/features/inbox/use-inbox-lookup", () => ({
  useInboxLookup: () => ({ crewById: new Map(), agentBySlug: new Map(), agentById: new Map(), ready: true }),
}))
vi.mock("@/components/features/inbox/waitpoint-run-detail", () => ({ WaitpointRunDetail: () => <p>Step 3 of 5 · waiting on you</p> }))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span /> }))

import { InboxV2 } from "../inbox-v2"

function item(o: Partial<InboxItem>): InboxItem {
  return {
    id: "wp1", workspace_id: "ws", kind: "waitpoint", source_id: "tok-1", title: "Approve publishing the report",
    state: "read", priority: "medium", blocking: true, sender_type: "pipeline", sender_name: "weekly-scan",
    created_at: "2026-10-17T10:00:00Z", updated_at: "2026-10-17T10:00:00Z",
    payload: { pipeline_run_id: "run_8f2c1a", pipeline_slug: "weekly-scan", sites: 14 },
    ...o,
  }
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}><InboxV2 /></QueryClientProvider>)
}

beforeEach(() => {
  m.items = [item({}), item({ id: "wp0", title: "Approve last week's report", created_at: "2026-10-10T10:00:00Z" })]
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes("min-width: 1536px") ? m.wide : false,
    media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
  }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); m.wide = true })

describe("the wide-desktop rail (INB-02)", () => {
  it("moves Where this came from and the run context into the rail, opened, beside the sender", () => {
    show()
    const rail = screen.getByTestId("inbox-rail")
    const frame = screen.getByTestId("inbox-detail-frame")
    // Where this came from — the chips are in the rail, not in the column.
    expect(within(rail).getByRole("navigation", { name: "Where this came from" })).toBeInTheDocument()
    expect(within(rail).getByRole("link", { name: /Open run/ })).toBeInTheDocument()
    expect(within(frame).queryByRole("navigation", { name: "Where this came from" })).toBeNull()
    // How the run got here — the steps, opened in the rail (v67 opens every
    // moved <details>).
    const steps = within(rail).getByText("How the run got here").closest("details")!
    expect(steps).toHaveAttribute("open")
    expect(within(steps).getByText("Step 3 of 5 · waiting on you")).toBeInTheDocument()
    expect(within(frame).queryByText("How the run got here")).toBeNull()
    expect(within(rail).getByText("Additional context").closest("details")).toHaveAttribute("open")
    // Sender card first, Earlier from last — v67 `ib-rail` order.
    const order = [...rail.children].map((el) => el.getAttribute("data-rail-part") ?? el.tagName.toLowerCase())
    expect(order[0]).toBe("sender")
    expect(order[order.length - 1]).toBe("earlier")
    // The decision itself never moves.
    expect(within(frame).getByTestId("decision-card")).toBeInTheDocument()
  })

  it("leaves everything in the column below 1536 px", () => {
    m.wide = false
    show()
    const frame = screen.getByTestId("inbox-detail-frame")
    expect(within(frame).getByRole("navigation", { name: "Where this came from" })).toBeInTheDocument()
    expect(within(frame).getByText("How the run got here")).toBeInTheDocument()
    expect(within(screen.getByTestId("inbox-rail")).queryByText("How the run got here")).toBeNull()
  })
})
