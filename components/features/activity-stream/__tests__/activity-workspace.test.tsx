import { beforeEach, afterEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react"

const mocks = vi.hoisted(() => ({
  push: vi.fn(), replace: vi.fn(), work: vi.fn(), deliveries: vi.fn(), mobile: false, role: "OWNER" as string | null, roleLoading: false,
}))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mocks.push, replace: mocks.replace }),
  useSearchParams: () => new URLSearchParams(window.location.search),
  usePathname: () => window.location.pathname,
}))
vi.mock("../activity-stream-view", () => ({
  ActivityStreamView: ({ onOpenSection, journalLedgers, modeSegment }: { onOpenSection: (s: string) => void; journalLedgers?: boolean; modeSegment?: React.ReactNode }) => (
    <>
      <p>Execution overview</p>
      <button onClick={() => onOpenSection("work")}>Rail: Work queue</button>
      {journalLedgers && <button onClick={() => onOpenSection("log")}>Rail: Technical log</button>}
      {modeSegment}
    </>
  ),
}))
vi.mock("@/components/features/journal/journal-ledger", () => ({
  JournalLedger: ({ view, actions }: { view: string; actions?: React.ReactNode }) => <><p>Journal ledger {view}</p>{actions}</>,
}))
vi.mock("@/components/features/journal/journal-spend-view", () => ({
  JournalSpendView: () => <p>Spend ledger</p>,
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mocks.mobile }))
// The panel reads its remembered collapse from the user's preferences.
vi.mock("@/lib/api-fetch", async (original) => ({
  ...await original<typeof import("@/lib/api-fetch")>(),
  apiFetch: vi.fn(async () => new Response("{}")),
}))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: mocks.role, loading: mocks.roleLoading }) }))
vi.mock("@/hooks/use-work-items", async (original) => ({
  ...await original<typeof import("@/hooks/use-work-items")>(),
  useLedgerWork: mocks.work,
  useResolveWorkItem: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useReplayWorkItem: () => ({ mutateAsync: vi.fn(), isPending: false }),
}))
vi.mock("@/hooks/use-webhook-deliveries", () => ({
  useLedgerDeliveries: mocks.deliveries,
  useWebhookDelivery: () => ({ delivery: null, loading: false, notFound: false }),
}))
vi.mock("@/components/features/work/work-item-detail", () => ({
  WorkItemDetail: ({ workItemId }: { workItemId: string }) => <p>Detail {workItemId}</p>,
}))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span data-testid="avatar" /> }))
import { ActivityWorkspace } from "../activity-workspace"
import WorkPage from "@/app/(dashboard)/work/page"
import { useAppStore } from "@/lib/store"

const casey = { id: "a-casey", name: "Casey", slug: "casey", avatar_seed: "c", avatar_style: "" }
const robot = { id: "a-robot", name: "Lab Robot 2", slug: "lab-robot-2", avatar_seed: "r", avatar_style: "" }
const ago = (min: number) => new Date(Date.now() - min * 60_000).toISOString()
const work = (id: string, state: string, agent: typeof casey, event: string, min: number, over: object = {}) => ({
  id, state, agent, agent_id: agent.id, crew: null, event_type: event, source: "webhook", state_reason: "",
  attempt_count: 1, generation: 1, created_at: ago(min), duration_ms: 18_000, ...over,
})

beforeEach(() => {
  vi.clearAllMocks()
  mocks.mobile = false
  mocks.role = "OWNER"
  mocks.roleLoading = false
  try { window.localStorage.removeItem("crewship.pref.panel.activity.collapsed") } catch { /* no storage */ }
  useAppStore.getState().setSecondaryPanelOpen(false)
  window.history.replaceState(null, "", "/activity")
  mocks.push.mockImplementation((url: string) => window.history.pushState(null, "", url))
  mocks.work.mockReturnValue({ items: [], capped: false, loading: false, error: null, refetch: vi.fn() })
  mocks.deliveries.mockReturnValue({ deliveries: [], capped: false, loading: false, error: null, refetch: vi.fn() })
})
afterEach(cleanup)

it("keeps the overview default and loads the ledger only when requested", () => {
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByText("Execution overview")).toBeVisible()
  expect(mocks.work).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Rail: Work queue" }))
  expect(mocks.push).toHaveBeenCalledWith("/activity?section=work", { scroll: false })
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("heading", { name: "Work queue" })).toBeInTheDocument()
  expect(screen.queryByText("Execution overview")).toBeNull()
  expect(mocks.work).toHaveBeenCalledWith("ws-a", expect.stringMatching(/^\d{4}-\d\d-\d\dT\d\d:00:00\.000Z$/))
})

it("switches the rail to the ledger's rows and leaves to all activity with legacy parameters kept (#3012)", () => {
  window.history.replaceState(null, "", "/activity?run=run-7&section=work")
  render(<ActivityWorkspace workspaceId="ws-a" />)
  const ledgers = screen.getByRole("navigation", { name: "Ledger" })
  expect(within(ledgers).getByRole("button", { name: "Work queue" })).toHaveAttribute("aria-current", "page")
  expect(screen.getByRole("region", { name: "Status" })).toHaveTextContent("Needs you")
  expect(screen.getByRole("region", { name: "Status" })).not.toHaveTextContent("Expired")
  fireEvent.click(screen.getByRole("button", { name: "All activity" }))
  expect(mocks.push).toHaveBeenCalledWith("/activity?run=run-7", { scroll: false })
})

it("switches between the two ledgers from the rail", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  render(<ActivityWorkspace workspaceId="ws-a" />)
  fireEvent.click(within(screen.getByRole("navigation", { name: "Ledger" })).getByRole("button", { name: "Deliveries" }))
  expect(mocks.push).toHaveBeenCalledWith("/activity?section=deliveries", { scroll: false })
})

it("names the agent and the event, and narrows every card to an agent picked in the rail", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  mocks.work.mockReturnValue({
    items: [
      work("w1", "succeeded", casey, "invoice.disputed", 2),
      work("w2", "failed", robot, "order.shipped", 5, { state_reason: "sidecar did not start" }),
    ],
    capped: false, loading: false, error: null, refetch: vi.fn(),
  })
  render(<ActivityWorkspace workspaceId="ws-a" />)
  const latest = screen.getByRole("region", { name: "Latest work" })
  expect(latest).toHaveTextContent("invoice.disputed")
  expect(latest).toHaveTextContent("Casey")
  expect(latest).toHaveTextContent("Sidecar did not start")
  fireEvent.click(screen.getByLabelText(/^Casey,/))
  expect(screen.getByRole("heading", { name: /Work queue · Casey/ })).toBeInTheDocument()
  expect(screen.getByRole("region", { name: "Latest work" })).not.toHaveTextContent("order.shipped")
})

it("offers to settle work whose outcome is unclear, only to a manager", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  mocks.work.mockReturnValue({
    items: [
      work("w1", "needs_reconciliation", robot, "invoice.export", 6, { state_reason: "The provider rejected the API key" }),
      work("w2", "queued", robot, "invoice.export", 3),
    ],
    capped: false, loading: false, error: null, refetch: vi.fn(),
  })
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  const needs = screen.getByRole("region", { name: "Needs you" })
  expect(needs).toHaveTextContent("1 more item waits behind it")
  expect(within(needs).getByRole("button", { name: "Mark as failed" })).toBeInTheDocument()
  mocks.role = "MEMBER"
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(within(screen.getByRole("region", { name: "Needs you" })).queryByRole("button", { name: "Mark as failed" })).toBeNull()
})

it("opens an accepted delivery's work in the Work queue", () => {
  window.history.replaceState(null, "", "/activity?section=deliveries")
  mocks.deliveries.mockReturnValue({ deliveries: [{
    id: "delivery-1", endpoint_id: casey.id, endpoint_kind: "agent", agent: casey, event_type: "push",
    filter_decision: "accepted", work_id: "work-1", work_state: "succeeded", profile: "crewship-hmac",
    received_at: new Date().toISOString(), raw_body_available: true, body_bytes: 72, body_sha256: "abc",
  }], capped: false, loading: false, error: null, refetch: vi.fn() })
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  fireEvent.click(within(screen.getByRole("region", { name: "Latest deliveries" })).getByRole("button", { name: /push/ }))
  fireEvent.click(screen.getByRole("button", { name: /^work · done/ }))
  expect(mocks.push).toHaveBeenCalledWith("/activity?section=work", { scroll: false })
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByText("Detail work-1")).toBeVisible()
})

it("keeps an agent picked on the Work queue removable on Deliveries, where it has no row (#3017)", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  mocks.work.mockReturnValue({
    items: [work("w1", "succeeded", casey, "invoice.disputed", 2)],
    capped: false, loading: false, error: null, refetch: vi.fn(),
  })
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  fireEvent.click(screen.getByLabelText(/^Casey,/))
  fireEvent.click(within(screen.getByRole("navigation", { name: "Ledger" })).getByRole("button", { name: "Deliveries" }))
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("heading", { name: /Webhook deliveries · Casey/ })).toBeInTheDocument()
  expect(screen.getByLabelText(/^Casey, none here/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole("button", { name: /^Remove filter/ }))
  expect(screen.getByRole("heading", { name: "Webhook deliveries" })).toBeInTheDocument()
})

it("says the ledger did not load, and loads it again on request", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  const refetch = vi.fn()
  mocks.work.mockReturnValue({ items: [], capped: false, loading: false, error: new Error("server unavailable"), refetch })
  render(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("alert")).toHaveTextContent("Could not load the work queue: server unavailable")
  fireEvent.click(within(screen.getByRole("alert")).getByRole("button", { name: "Try again" }))
  expect(refetch).toHaveBeenCalled()
})

it("opens on a phone with the rail in the closed panel sheet, which a pick closes again", () => {
  mocks.mobile = true
  window.history.replaceState(null, "", "/activity?section=work")
  mocks.work.mockReturnValue({
    items: [work("w1", "succeeded", casey, "invoice.disputed", 2)],
    capped: false, loading: false, error: null, refetch: vi.fn(),
  })
  render(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("heading", { name: "Work queue" })).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "All activity" })).toBeNull()
  expect(document.querySelector("[data-slot=secondary-panel]")).toBeNull()
  act(() => useAppStore.getState().setSecondaryPanelOpen(true))
  const sheet = screen.getByRole("dialog", { name: "Activity views" })
  expect(within(sheet).getByRole("button", { name: "All activity" })).toBeInTheDocument()
  fireEvent.click(within(sheet).getByLabelText(/^Casey,/))
  expect(useAppStore.getState().secondaryPanelOpen).toBe(false)
})

// Harbor 2 M3 (#3350): the ledger rail is Activity's SecondaryPanel.
it("puts the ledger rail in the activity SecondaryPanel with Back in its toolbar and one collapse", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  render(<ActivityWorkspace workspaceId="ws-a" />)
  const panel = document.querySelector("[data-slot=secondary-panel]") as HTMLElement
  expect(panel).toHaveAttribute("data-panel-id", "activity")
  const toolbar = panel.querySelector(".secondary-panel-toolbar") as HTMLElement
  expect(within(toolbar).getByRole("button", { name: "All activity" })).toBeInTheDocument()
  expect(screen.getAllByRole("button", { name: "Hide Activity views" })).toHaveLength(1)
  fireEvent.click(screen.getByRole("button", { name: "Hide Activity views" }))
  expect(panel).toHaveAttribute("data-collapsed", "true")
  expect(screen.queryByRole("navigation", { name: "Ledger" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Show Activity views" }))
  expect(screen.getByRole("navigation", { name: "Ledger" })).toBeInTheDocument()
})

it("keeps work stuck for longer than the window in Needs you (#3017)", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  mocks.work.mockReturnValue({
    items: [work("w-old", "needs_reconciliation", robot, "invoice.export", 40 * 60, { state_reason: "The provider rejected the API key" })],
    capped: false, loading: false, error: null, refetch: vi.fn(),
  })
  render(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("region", { name: "Needs you" })).toHaveTextContent("invoice.export")
})

it("names a routine's endpoint as such, never as a deleted agent", () => {
  window.history.replaceState(null, "", "/activity?section=deliveries")
  mocks.deliveries.mockReturnValue({ deliveries: [{
    id: "d-rt", endpoint_id: "rt-1", endpoint_kind: "routine", agent: null, event_type: "push",
    filter_decision: "accepted", work_id: null, work_state: null, profile: "github",
    received_at: new Date().toISOString(), raw_body_available: true, body_bytes: 10, body_sha256: "abc",
  }], capped: false, loading: false, error: null, refetch: vi.fn() })
  render(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("region", { name: "Latest deliveries" })).toHaveTextContent("Routine endpoint")
  expect(screen.queryByText("Deleted agent")).toBeNull()
})

it("closes the open work when Back leaves the Work queue for Deliveries", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  mocks.work.mockReturnValue({
    items: [work("w1", "succeeded", casey, "invoice.disputed", 2)],
    capped: false, loading: false, error: null, refetch: vi.fn(),
  })
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  fireEvent.click(within(screen.getByRole("region", { name: "Latest work" })).getAllByRole("button")[0])
  expect(screen.getByText("Detail w1")).toBeInTheDocument()
  window.history.replaceState(null, "", "/activity?section=deliveries")
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  window.history.replaceState(null, "", "/activity?section=work")
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.queryByText("Detail w1")).toBeNull()
})

it("uses a replacement redirect for old Work bookmarks", () => {
  window.history.replaceState(null, "", "/work?section=deliveries")
  render(<WorkPage />)
  expect(mocks.replace).toHaveBeenCalledWith("/activity?section=deliveries", { scroll: false })
})

// F-02: the Journal's views live in Activity, gated exactly as before.
describe("the Journal inside Activity (F-02)", () => {
  it.each([
    ["section=log", "Journal ledger log"],
    ["section=runs", "Journal ledger runs"],
    ["mode=spend", "Spend ledger"],
  ])("shows ?%s to OWNER and ADMIN", (query, text) => {
    for (const role of ["OWNER", "ADMIN"]) {
      mocks.role = role
      window.history.replaceState(null, "", `/activity?${query}`)
      const view = render(<ActivityWorkspace workspaceId="ws-a" />)
      expect(screen.getByText(text)).toBeInTheDocument()
      expect(mocks.replace).not.toHaveBeenCalled()
      view.unmount()
    }
  })

  it.each([
    ["MANAGER", "section=log&trace_id=run-1&severity=error", "/activity?run=run-1"],
    ["MEMBER", "section=log&crew_id=c1&time=30d", "/activity?range=30d&crew=c1"],
    ["MEMBER", "section=runs&run_status=FAILED", "/activity?what=work&range=24h&view=failed"],
    ["VIEWER", "section=runs", "/activity?what=work&range=24h"],
    ["MEMBER", "mode=spend", "/activity"],
    ["MANAGER", "mode=spend", "/activity"],
  ])("sends a %s on ?%s to the list (%s), never drawing the admin view", (role, query, target) => {
    mocks.role = role
    window.history.replaceState(null, "", `/activity?${query}`)
    const view = render(<ActivityWorkspace workspaceId="ws-a" />)
    expect(mocks.replace).toHaveBeenCalledWith(target, { scroll: false })
    expect(view.container.textContent).toBe("")
  })

  it("draws nothing for an admin-only view while the role is loading, and does not redirect", () => {
    mocks.role = null
    mocks.roleLoading = true
    window.history.replaceState(null, "", "/activity?mode=spend")
    const view = render(<ActivityWorkspace workspaceId="ws-a" />)
    expect(view.container.textContent).toBe("")
    expect(mocks.replace).not.toHaveBeenCalled()
  })

  it("offers the Activity / Spend segment and the Journal ledgers only to OWNER / ADMIN", () => {
    const view = render(<ActivityWorkspace workspaceId="ws-a" />)
    const seg = screen.getByRole("navigation", { name: "Activity or spend" })
    expect(within(seg).getByRole("link", { name: "Activity" })).toHaveAttribute("aria-current", "page")
    expect(within(seg).getByRole("link", { name: "Spend" })).toHaveAttribute("href", "/activity?mode=spend")
    expect(screen.getByRole("button", { name: "Rail: Technical log" })).toBeInTheDocument()
    view.unmount()
    for (const role of ["MANAGER", "MEMBER", "VIEWER"]) {
      mocks.role = role
      const v = render(<ActivityWorkspace workspaceId="ws-a" />)
      expect(screen.queryByRole("navigation", { name: "Activity or spend" })).toBeNull()
      expect(screen.queryByRole("link", { name: "Spend" })).toBeNull()
      expect(screen.queryByRole("button", { name: "Rail: Technical log" })).toBeNull()
      v.unmount()
    }
  })

  it("marks Spend as the current side on the Spend view", () => {
    window.history.replaceState(null, "", "/activity?mode=spend")
    render(<ActivityWorkspace workspaceId="ws-a" />)
    expect(screen.getByRole("link", { name: "Spend" })).toHaveAttribute("aria-current", "page")
  })

  it("opens the technical log clean, without the list's filters", () => {
    window.history.replaceState(null, "", "/activity?view=failed&q=boom")
    render(<ActivityWorkspace workspaceId="ws-a" />)
    fireEvent.click(screen.getByRole("button", { name: "Rail: Technical log" }))
    expect(mocks.push).toHaveBeenLastCalledWith("/activity?section=log", { scroll: false })
  })
})
