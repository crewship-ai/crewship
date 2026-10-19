import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { SavedDocumentPanel, type SavedDocumentPanelProps } from "./saved-document-panel"

const events = vi.hoisted(() => new Map<string, () => void>())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, callback: () => void) => events.set(event, callback) }))
const content = { type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text: "Private saved body" }] }] }
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const props = (): SavedDocumentPanelProps => ({ workspaceId: "ws /&", chatId: "chat1", currentUserId: "human1", activationKey: "open1", enabled: true,
  target: { workspaceId: "ws /&", pageId: "page /&", type: "document", mode: "latest" }, layout: "side", expanded: false, onClose: vi.fn(), onExpandedChange: vi.fn() })
beforeEach(() => {
  events.clear()
  vi.mocked(apiFetch).mockReset().mockImplementation(async url => response(String(url).includes("/document?")
    ? { page_id: "page /&", schema: 3, rev: 4, content }
    : { id: "page /&", name: "Private saved title", has_document: true }))
})
afterEach(cleanup)
it("opens the full editor by stable Page identity only after current metadata and body authorize", async () => {
  let resolve!: (value: Response) => void
  vi.mocked(apiFetch).mockReturnValueOnce(new Promise(done => { resolve = done }))
  render(<SavedDocumentPanel {...props()} />)
  expect(screen.queryByRole("link", { name: "Open full editor" })).toBeNull()
  await act(async () => resolve(response({ id: "page /&", name: "Private saved title", has_document: true })))
  const link = await screen.findByRole("link", { name: "Open full editor" })
  expect(link).toHaveAttribute("href", "/pages?ref=%40page+%2F%26&workspace_id=ws+%2F%26")
  expect(link).toHaveAttribute("target", "_blank")
  expect(link).toHaveAttribute("rel", "noopener noreferrer")
  expect(screen.getByText("Private saved body")).toBeInTheDocument()
  expect(screen.getByText("Current saved document")).toBeInTheDocument()
})
it.each([401, 403, 404, 503])("removes private content and the editor action after refresh returns %s", async status => {
  render(<SavedDocumentPanel {...props()} />)
  await screen.findByRole("link", { name: "Open full editor" })
  vi.mocked(apiFetch).mockResolvedValue(response({ error: "Private error" }, status))
  act(() => events.get("page.updated")?.())
  expect(screen.queryByText("Private saved title")).toBeNull()
  expect(screen.queryByText("Private saved body")).toBeNull()
  expect(screen.queryByRole("link", { name: "Open full editor" })).toBeNull()
  await waitFor(() => expect(screen.getByRole(status === 503 ? "status" : "alert")).toHaveTextContent(status === 503 ? "Preview unavailable" : "Document access is unavailable"))
  expect(screen.queryByText("Private error")).toBeNull()
})
it.each(["human", "workspace", "chat", "activation", "closed", "disabled"])("hides the editor and body immediately when %s changes", async kind => {
  const p = props()
  const view = render(<SavedDocumentPanel {...p} />)
  await screen.findByRole("link", { name: "Open full editor" })
  vi.mocked(apiFetch).mockReturnValue(new Promise(() => {}))
  const next = { ...p }
  if (kind === "human") next.currentUserId = "human2"
  if (kind === "workspace") next.workspaceId = "other"
  if (kind === "chat") next.chatId = "chat2"
  if (kind === "activation") next.activationKey = "open2"
  if (kind === "closed") next.target = null
  if (kind === "disabled") next.enabled = false
  view.rerender(<SavedDocumentPanel {...next} />)
  expect(screen.queryByText("Private saved title")).toBeNull()
  expect(screen.queryByText("Private saved body")).toBeNull()
  expect(screen.queryByRole("link", { name: "Open full editor" })).toBeNull()
})
it("does not restore a late saved body after the human changes", async () => {
  let resolve!: (value: Response) => void
  vi.mocked(apiFetch).mockResolvedValueOnce(response({ id: "page /&", name: "Private saved title", has_document: true }))
    .mockReturnValueOnce(new Promise(done => { resolve = done }))
  const p = props()
  const view = render(<SavedDocumentPanel {...p} />)
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  vi.mocked(apiFetch).mockResolvedValue(response({}, 403))
  view.rerender(<SavedDocumentPanel {...p} currentUserId="human2" />)
  await act(async () => resolve(response({ page_id: "page /&", schema: 3, rev: 4, content })))
  expect(screen.queryByRole("link", { name: "Open full editor" })).toBeNull()
  expect(screen.queryByText("Private saved title")).toBeNull()
  expect(screen.queryByText("Private saved body")).toBeNull()
})
it("does not offer an editor link when the saved body is malformed", async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce(response({ id: "page /&", name: "Private saved title", has_document: true }))
    .mockResolvedValueOnce(response({ page_id: "page /&", schema: 3, rev: 4, content: { type: "unknown" } }))
  render(<SavedDocumentPanel {...props()} />)
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Preview unavailable"))
  expect(screen.queryByRole("link", { name: "Open full editor" })).toBeNull()
  expect(screen.queryByText("Private saved title")).toBeNull()
})
