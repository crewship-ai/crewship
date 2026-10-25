import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"
import { ChatDocumentAccess } from "../right-panel-tabs/chat-document-access"

const apiFetch = vi.fn()
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
const realtimeHandlers = new Map<string, () => void>()
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEventSafe: (event: string, callback: () => void) => { realtimeHandlers.set(event, callback) },
}))

const doc = {
  page_id: "stable-page-id", title: "Shared budget", document_type: "spreadsheet",
  document_grant: "write", policy_allows: true, task_status: "no_active_task",
  tasks: [], provenance: "unknown",
}
const response = (documents = [doc]) => ({ ok: true, json: async () => ({ documents }) })
const props = { workspaceId: "workspace", chatId: "chat", viewerId: "owner" }

beforeEach(() => {
  vi.useFakeTimers()
  apiFetch.mockReset()
  realtimeHandlers.clear()
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true })
})
afterEach(() => { cleanup(); vi.useRealTimers() })

async function settle() { await act(async () => { await vi.advanceTimersByTimeAsync(0) }) }

it("opens the existing document and distinguishes its grant from a task and origin", async () => {
  apiFetch.mockResolvedValue(response())
  render(<ChatDocumentAccess {...props} />)
  await settle()
  expect(screen.getByRole("link", { name: "Shared budget" })).toHaveAttribute("href", "/pages/%40stable-page-id")
  expect(screen.getByText("Document grant: read and write")).toBeInTheDocument()
  expect(screen.getByText("No verified active task in this chat.")).toBeInTheDocument()
  expect(screen.getByText("Origin: unknown")).toBeInTheDocument()
})

it("removes document names and rights when refresh is denied", async () => {
  apiFetch.mockResolvedValueOnce(response()).mockResolvedValue({ ok: false, status: 403 })
  render(<ChatDocumentAccess {...props} />)
  await settle()
  expect(screen.getByText("Shared budget")).toBeInTheDocument()
  await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
  expect(apiFetch).toHaveBeenCalledTimes(2)
  // Deliver React Query's batched observer notification, before the 3 s expiry.
  await act(async () => { await vi.advanceTimersByTimeAsync(1) })
  expect(screen.queryByText("Shared budget")).toBeNull()
  expect(screen.getByText("Current access could not be verified.")).toBeInTheDocument()
})

it("rechecks current task metadata after an existing run event", async () => {
  apiFetch.mockResolvedValueOnce(response()).mockResolvedValue(response([]))
  render(<ChatDocumentAccess {...props} />)
  await settle()
  expect(screen.getByText("Shared budget")).toBeInTheDocument()
  await act(async () => { realtimeHandlers.get("run.completed")?.(); await vi.advanceTimersByTimeAsync(0) })
  expect(screen.queryByText("Shared budget")).toBeNull()
})

it("expires previous metadata while the next request hangs", async () => {
  apiFetch.mockResolvedValueOnce(response()).mockImplementation(() => new Promise(() => {}))
  render(<ChatDocumentAccess {...props} />)
  await settle()
  await act(async () => { await vi.advanceTimersByTimeAsync(3001) })
  expect(screen.queryByText("Shared budget")).toBeNull()
  expect(screen.queryByText("Document grant: read and write")).toBeNull()
})

it("counts freshness from sending the request and discards a delayed reply", async () => {
  let complete!: (value: ReturnType<typeof response>) => void
  apiFetch.mockImplementation(() => new Promise((resolve) => { complete = resolve }))
  render(<ChatDocumentAccess {...props} />)
  await act(async () => { await vi.advanceTimersByTimeAsync(3001) })
  await act(async () => { complete(response()); await Promise.resolve() })
  expect(screen.queryByText("Shared budget")).toBeNull()
})

it("does not show another viewer's snapshot or accept its delayed response", async () => {
  let oldReply!: (value: ReturnType<typeof response>) => void
  apiFetch.mockResolvedValueOnce(response()).mockImplementationOnce(() => new Promise((resolve) => { oldReply = resolve })).mockResolvedValue(response([]))
  const view = render(<ChatDocumentAccess {...props} />)
  await settle()
  expect(screen.getByText("Shared budget")).toBeInTheDocument()
  await act(async () => { await vi.advanceTimersByTimeAsync(1500) })
  view.rerender(<ChatDocumentAccess {...props} viewerId="different-viewer" />)
  expect(screen.queryByText("Shared budget")).toBeNull()
  await act(async () => { oldReply(response()); await Promise.resolve() })
  expect(screen.queryByText("Shared budget")).toBeNull()
})
