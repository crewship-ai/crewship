import { beforeEach, afterEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen } from "@testing-library/react"

const mocks = vi.hoisted(() => ({
  push: vi.fn(), replace: vi.fn(), work: vi.fn(), deliveries: vi.fn(), mobile: false,
}))
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mocks.push, replace: mocks.replace }),
  useSearchParams: () => new URLSearchParams(window.location.search),
}))
vi.mock("../activity-stream-view", () => ({ ActivityStreamView: () => <p>Execution overview</p> }))
vi.mock("@/hooks/use-mobile", () => ({ useIsMobile: () => mocks.mobile }))
vi.mock("@/hooks/use-work-items", async (original) => ({
  ...await original<typeof import("@/hooks/use-work-items")>(), useWorkItems: mocks.work,
}))
vi.mock("@/hooks/use-webhook-deliveries", () => ({ useWebhookDeliveries: mocks.deliveries }))
vi.mock("@/components/features/work/work-item-detail", () => ({
  WorkItemDetail: ({ workItemId }: { workItemId: string }) => <p>Detail {workItemId}</p>,
}))
import { ActivityWorkspace } from "../activity-workspace"
import WorkPage from "@/app/(dashboard)/work/page"

beforeEach(() => {
  vi.clearAllMocks()
  mocks.mobile = false
  window.history.replaceState(null, "", "/activity")
  mocks.push.mockImplementation((url: string) => window.history.pushState(null, "", url))
  mocks.work.mockReturnValue({ items: [], loading: false, error: null, refetch: vi.fn() })
  mocks.deliveries.mockReturnValue({ deliveries: [], loading: false, error: null, refetch: vi.fn() })
})
afterEach(cleanup)

it("keeps the overview default and loads the ledger only when requested", () => {
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByText("Execution overview")).toBeVisible()
  expect(mocks.work).not.toHaveBeenCalled()
  fireEvent.mouseDown(screen.getByRole("tab", { name: "Work", exact: true }), { button: 0, ctrlKey: false })
  expect(mocks.push).toHaveBeenCalledWith("/activity?section=work", { scroll: false })
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByText("No work in the ledger")).toBeVisible()
  expect(screen.queryByText("Execution overview")).toBeNull()
  expect(mocks.work).toHaveBeenCalledWith("ws-a", { state: null })
})

it("restores a linked section and keeps legacy run parameters when switching", () => {
  window.history.replaceState(null, "", "/activity?run=run-7&section=work")
  render(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("tab", { name: "Work", exact: true })).toHaveAttribute("aria-selected", "true")
  fireEvent.mouseDown(screen.getByRole("tab", { name: "Overview" }), { button: 0, ctrlKey: false })
  expect(mocks.push).toHaveBeenCalledWith("/activity?run=run-7", { scroll: false })
})

it("opens an accepted delivery's work and preserves its detail across the section change", () => {
  window.history.replaceState(null, "", "/activity?section=deliveries")
  mocks.deliveries.mockReturnValue({ deliveries: [{
    id: "delivery-1", event_type: "push", filter_decision: "accepted", work_id: "work-1",
    received_at: new Date().toISOString(), raw_body_available: true,
  }], loading: false, error: null, refetch: vi.fn() })
  const view = render(<ActivityWorkspace workspaceId="ws-a" />)
  fireEvent.click(screen.getByRole("button", { name: /work work-1/ }))
  view.rerender(<ActivityWorkspace workspaceId="ws-a" />)
  expect(screen.getByRole("tab", { name: "Work", exact: true })).toHaveAttribute("aria-selected", "true")
  expect(screen.getByText("Detail work-1")).toBeVisible()
})

it("resets ledger selection and filters when the page changes workspace", () => {
  window.history.replaceState(null, "", "/activity?section=work")
  const view = render(<ActivityWorkspace key="ws-a" workspaceId="ws-a" />)
  fireEvent.click(screen.getByRole("button", { name: "Running", exact: true }))
  expect(mocks.work).toHaveBeenLastCalledWith("ws-a", { state: "running" })
  view.rerender(<ActivityWorkspace key="ws-b" workspaceId="ws-b" />)
  expect(mocks.work).toHaveBeenLastCalledWith("ws-b", { state: null })
})

it("uses a replacement redirect for old Work bookmarks", () => {
  window.history.replaceState(null, "", "/work?section=deliveries")
  render(<WorkPage />)
  expect(mocks.replace).toHaveBeenCalledWith("/activity?section=deliveries", { scroll: false })
})
