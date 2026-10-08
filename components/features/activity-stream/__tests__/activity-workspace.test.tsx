import { beforeEach, afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"

const mocks = vi.hoisted(() => ({
  push: vi.fn(), replace: vi.fn(), work: vi.fn(), deliveries: vi.fn(), mobile: false, role: "OWNER",
}))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mocks.push, replace: mocks.replace }),
  useSearchParams: () => new URLSearchParams(window.location.search),
}))
vi.mock("../activity-stream-view", () => ({
  ActivityStreamView: ({ onOpenSection }: { onOpenSection: (s: "work" | "deliveries") => void }) => (
    <><p>Execution overview</p><button onClick={() => onOpenSection("work")}>Rail: Work queue</button></>
  ),
}))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mocks.mobile }))
vi.mock("@/hooks/use-abilities", () => ({ useAbilities: () => ({ role: mocks.role }) }))
vi.mock("@/hooks/use-work-items", async (original) => ({
  ...await original<typeof import("@/hooks/use-work-items")>(),
  useWorkItems: mocks.work,
  useResolveWorkItem: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useReplayWorkItem: () => ({ mutateAsync: vi.fn(), isPending: false }),
}))
vi.mock("@/hooks/use-webhook-deliveries", () => ({ useWebhookDeliveries: mocks.deliveries }))
vi.mock("@/components/features/work/work-item-detail", () => ({
  WorkItemDetail: ({ workItemId }: { workItemId: string }) => <p>Detail {workItemId}</p>,
}))
vi.mock("@/components/ui/agent-avatar", () => ({ AgentAvatar: () => <span data-testid="avatar" /> }))
import { ActivityWorkspace } from "../activity-workspace"
import WorkPage from "@/app/(dashboard)/work/page"

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
  window.history.replaceState(null, "", "/activity")
  mocks.push.mockImplementation((url: string) => window.history.pushState(null, "", url))
  mocks.work.mockReturnValue({ items: [], nextCursor: null, loading: false, error: null, refetch: vi.fn() })
  mocks.deliveries.mockReturnValue({ deliveries: [], nextCursor: null, loading: false, error: null, refetch: vi.fn() })
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
  expect(mocks.work).toHaveBeenCalledWith("ws-a")
})

it("switches the rail to the ledger's rows and leaves to all activity with legacy parameters kept (#3012)", () => {
  window.history.replaceState(null, "", "/activity?run=run-7&section=work")
  render(<ActivityWorkspace workspaceId="ws-a" />)
  const tabs = screen.getByRole("tablist", { name: "Ledger" })
  expect(within(tabs).getByRole("tab", { name: "Work queue" })).toHaveAttribute("aria-selected", "true")
  expect(screen.getByRole("region", { name: "Status" })).toHaveTextContent("Needs you")
  expect(screen.getByRole("region", { name: "Status" })).not.toHaveTextContent("Expired")
  fireEvent.click(screen.getByRole("button", { name: "All activity" }))
  expect(mocks.push).toHaveBeenCalledWith("/activity?run=run-7", { scroll: false })
})

it("switches between the two ledgers from the rail", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  render(<ActivityWorkspace workspaceId="ws-a" />)
  fireEvent.click(screen.getByRole("tab", { name: "Deliveries" }))
  expect(mocks.push).toHaveBeenCalledWith("/activity?section=deliveries", { scroll: false })
})

it("names the agent and the event, and narrows every card to an agent picked in the rail", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  mocks.work.mockReturnValue({
    items: [
      work("w1", "succeeded", casey, "invoice.disputed", 2),
      work("w2", "failed", robot, "order.shipped", 5, { state_reason: "sidecar did not start" }),
    ],
    nextCursor: null, loading: false, error: null, refetch: vi.fn(),
  })
  render(<ActivityWorkspace workspaceId="ws-a" />)
  const latest = screen.getByRole("region", { name: "Latest work" })
  expect(latest).toHaveTextContent("invoice.disputed")
  expect(latest).toHaveTextContent("Casey")
  expect(latest).toHaveTextContent("Sidecar did not start")
  fireEvent.click(screen.getByLabelText("Casey"))
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
    nextCursor: null, loading: false, error: null, refetch: vi.fn(),
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
  }], nextCursor: null, loading: false, error: null, refetch: vi.fn() })
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  fireEvent.click(within(screen.getByRole("region", { name: "Latest deliveries" })).getByRole("button", { name: /push/ }))
  fireEvent.click(screen.getByRole("button", { name: /^work · done/ }))
  expect(mocks.push).toHaveBeenCalledWith("/activity?section=work", { scroll: false })
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByText("Detail work-1")).toBeVisible()
})

it("uses a replacement redirect for old Work bookmarks", () => {
  window.history.replaceState(null, "", "/work?section=deliveries")
  render(<WorkPage />)
  expect(mocks.replace).toHaveBeenCalledWith("/activity?section=deliveries", { scroll: false })
})
