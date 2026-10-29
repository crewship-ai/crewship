import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { act, cleanup, render, screen } from "@testing-library/react"
import { ChatDocumentAccess, NativeDocumentProvenance } from "../right-panel-tabs/chat-document-access"

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

const edit = { page_id: "edited-page", title: "Recorded native Doc", application_id: "wordcraft", provenance: "agent_edited", chat_id: "source-chat", message_id: "original-message", agent_slug: "native-agent", last_revision: "12", edited_at: "2026-10-25T00:00:00Z" }
const editResponse = () => ({ ok: true, json: async () => ({ documents: [], artifacts: [edit], artifacts_partial: true }) })

it("shows recorded edits separately from standing grants and never calls them created", async () => {
  apiFetch.mockResolvedValue(editResponse())
  render(<ChatDocumentAccess {...props} />)
  await settle()
  expect(screen.getByRole("link", { name: "Recorded native Doc" })).toHaveAttribute("href", "/pages/%40edited-page")
  expect(screen.getByText("Edited by agent in this chat")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Open source message" })).toHaveAttribute("href", "/chat/native-agent?session=source-chat&message=original-message")
  expect(screen.getByText("No document grants visible to you.")).toBeInTheDocument()
  expect(screen.queryByText(/Created by agent/)).toBeNull()
})

it("removes all recorded titles and backlinks on an authoritative refresh denial", async () => {
  apiFetch.mockResolvedValueOnce(editResponse()).mockResolvedValue({ ok: false, status: 403 })
  render(<ChatDocumentAccess {...props} />)
  await settle()
  expect(screen.getByText("Recorded native Doc")).toBeInTheDocument()
  await act(async () => { await vi.advanceTimersByTimeAsync(1501) })
  expect(screen.queryByText("Recorded native Doc")).toBeNull()
  expect(screen.queryByRole("link", { name: "Open source message" })).toBeNull()
})

it("drops provenance immediately while hidden and refuses an old viewer response", async () => {
  apiFetch.mockResolvedValueOnce(editResponse()).mockImplementation(() => new Promise(() => {}))
  const view = render(<ChatDocumentAccess {...props} />)
  await settle()
  Object.defineProperty(document, "visibilityState", { value: "hidden", configurable: true })
  act(() => { document.dispatchEvent(new Event("visibilitychange")) })
  expect(screen.queryByText("Recorded native Doc")).toBeNull()
  Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true })
  view.rerender(<ChatDocumentAccess {...props} viewerId="different-viewer" />)
  expect(screen.queryByRole("link", { name: "Open source message" })).toBeNull()
})


it("reads a native Page backlink through its own current-ACL projection and expires it", async () => {
  apiFetch.mockResolvedValueOnce(editResponse()).mockImplementation(() => new Promise(() => {}))
  render(<NativeDocumentProvenance workspaceId="workspace" pageId="edited-page" viewerId="owner" />)
  await settle()
  expect(apiFetch.mock.calls[0][0]).toContain("/api/v1/pages/%40edited-page/mooring/provenance")
  expect(screen.getByRole("link", { name: "Open source message" })).toBeInTheDocument()
  await act(async () => { await vi.advanceTimersByTimeAsync(3001) })
  expect(screen.queryByRole("link", { name: "Open source message" })).toBeNull()
})
