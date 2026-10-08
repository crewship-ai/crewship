import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

const mocks = vi.hoisted(() => ({
  resolve: vi.fn(),
  replay: vi.fn(),
  delivery: { delivery: null as unknown, loading: false, notFound: false },
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}))
vi.mock("sonner", () => ({ toast: { success: mocks.toastSuccess, error: mocks.toastError } }))
vi.mock("@/hooks/use-work-items", async (original) => ({
  ...(await original<typeof import("@/hooks/use-work-items")>()),
  useResolveWorkItem: () => ({ mutateAsync: mocks.resolve, isPending: false }),
  useReplayWorkItem: () => ({ mutateAsync: mocks.replay, isPending: false }),
}))
vi.mock("@/hooks/use-webhook-deliveries", () => ({ useWebhookDelivery: () => mocks.delivery }))

import { ResolveWorkDialog } from "../resolve-work-dialog"

const item = {
  id: "w1", state: "needs_reconciliation", source: "webhook", source_ref: "dlv-1", domain_kind: "",
  generation: 3, event_type: "invoice.export", agent: { id: "a1", name: "Lab Robot 2" },
} as never

beforeEach(() => {
  vi.clearAllMocks()
  mocks.delivery = { delivery: { raw_body_available: true }, loading: false, notFound: false }
  mocks.resolve.mockResolvedValue({})
  mocks.replay.mockResolvedValue({})
})
afterEach(cleanup)

function fill() {
  fireEvent.change(screen.getByLabelText("What you checked"), { target: { value: "not in the ERP" } })
  fireEvent.click(screen.getByRole("checkbox"))
}

it("does not offer a retry whose payload is gone, and says why (#3017)", () => {
  mocks.delivery = { delivery: { raw_body_available: false, raw_body_expires_at: null }, loading: false, notFound: false }
  render(<ResolveWorkDialog workspaceId="ws" item={item} mode="retry" onOpenChange={vi.fn()} />)
  fill()
  expect(screen.getByText(/passed its retention window/)).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Retry" })).toBeDisabled()
})

it("says the work was marked failed when running it again is refused", async () => {
  mocks.replay.mockRejectedValue(new Error("replay refused"))
  const onOpenChange = vi.fn()
  render(<ResolveWorkDialog workspaceId="ws" item={item} mode="retry" onOpenChange={onOpenChange} />)
  fill()
  fireEvent.click(screen.getByRole("button", { name: "Retry" }))
  await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
  expect(mocks.resolve).toHaveBeenCalledWith(expect.objectContaining({ state: "failed", generation: 3 }))
  expect(mocks.toastError).toHaveBeenCalledWith(expect.stringMatching(/marked as failed.*replay refused/i))
})
