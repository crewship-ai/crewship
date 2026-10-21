import { useEffect } from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { SavedDocumentPanel, type SavedDocumentPanelProps } from "./saved-document-panel"

// Chat → result card → this panel → edit in place → the saved view re-reads.
const events = vi.hoisted(() => new Map<string, () => void>())
const editor = vi.hoisted(() => ({ dirty: false, mounts: 0 }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, callback: () => void) => events.set(event, callback) }))
vi.mock("@/components/features/pages/page-document", () => ({
  PageDocument: (p: { workspaceId: string; slug: string; pageId: string; name?: string; onDirtyChange?: (dirty: boolean) => void }) => {
    useEffect(() => { editor.mounts++ }, [])
    useEffect(() => { p.onDirtyChange?.(editor.dirty) })
    return <div data-testid="page-document" data-workspace={p.workspaceId} data-slug={p.slug} data-page={p.pageId} data-name={p.name} />
  },
}))
const content = (text: string) => ({ type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text }] }] })
const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
let body = "Private saved body"
const props = (): SavedDocumentPanelProps => ({ workspaceId: "ws1", chatId: "chat1", currentUserId: "human1", activationKey: "open1", enabled: true,
  target: { workspaceId: "ws1", pageId: "page1", type: "document", mode: "latest" }, layout: "side", expanded: false, onClose: vi.fn(), onExpandedChange: vi.fn() })
beforeEach(() => {
  events.clear(); editor.dirty = false; editor.mounts = 0; body = "Private saved body"
  vi.mocked(apiFetch).mockReset().mockImplementation(async url => response(String(url).includes("/document?")
    ? { page_id: "page1", schema: 3, rev: 4, content: content(body) }
    : { id: "page1", name: "Launch plan", has_document: true }))
})
afterEach(cleanup)

it("edits the saved document in place, by its stable Page identity", async () => {
  render(<SavedDocumentPanel {...props()} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  const doc = screen.getByTestId("page-document")
  expect(doc).toHaveAttribute("data-workspace", "ws1")
  expect(doc).toHaveAttribute("data-slug", "@page1")
  expect(doc).toHaveAttribute("data-page", "page1")
  expect(doc).toHaveAttribute("data-name", "Launch plan")
  expect(screen.queryByText("Private saved body")).toBeNull()
})
it("keeps the editor mounted while its own saves announce page updates", async () => {
  render(<SavedDocumentPanel {...props()} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  const reads = vi.mocked(apiFetch).mock.calls.length
  act(() => events.get("page.updated")?.())
  act(() => events.get("page.updated")?.())
  expect(screen.getByTestId("page-document")).toBeInTheDocument()
  expect(editor.mounts).toBe(1)
  expect(vi.mocked(apiFetch).mock.calls.length).toBe(reads)
})
it("Done re-reads the saved document and shows the edit", async () => {
  render(<SavedDocumentPanel {...props()} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  body = "Edited in chat"
  fireEvent.click(screen.getByRole("button", { name: "Done editing" }))
  expect(await screen.findByText("Edited in chat")).toBeInTheDocument()
  expect(screen.queryByTestId("page-document")).toBeNull()
})
it("asks before closing while edits are unsent", async () => {
  editor.dirty = true
  const p = props()
  render(<SavedDocumentPanel {...p} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  await screen.findByText("Edits not saved yet")
  fireEvent.click(screen.getByRole("button", { name: "Close document preview" }))
  expect(p.onClose).not.toHaveBeenCalled()
  const ask = screen.getByRole("alertdialog", { name: "Close with unsent edits?" })
  expect(screen.getByRole("button", { name: "Keep editing" })).toHaveFocus()
  fireEvent.click(screen.getByRole("button", { name: "Keep editing" }))
  expect(ask).not.toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Done editing" })).toHaveFocus()
  fireEvent.click(screen.getByRole("button", { name: "Close document preview" }))
  fireEvent.click(screen.getByRole("button", { name: "Close anyway" }))
  expect(p.onClose).toHaveBeenCalledTimes(1)
})
it("asks on Escape too, and Escape in the question keeps editing", async () => {
  editor.dirty = true
  const p = props()
  render(<SavedDocumentPanel {...p} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  await screen.findByText("Edits not saved yet")
  fireEvent.keyDown(screen.getByTestId("page-document"), { key: "Escape" })
  const ask = screen.getByRole("alertdialog", { name: "Close with unsent edits?" })
  fireEvent.keyDown(ask, { key: "Escape" })
  expect(ask).not.toBeInTheDocument()
  expect(p.onClose).not.toHaveBeenCalled()
  expect(screen.getByTestId("page-document")).toBeInTheDocument()
})
it("never locks Done: with edits that cannot drain it asks, then leaves editing", async () => {
  editor.dirty = true
  render(<SavedDocumentPanel {...props()} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  await screen.findByText("Edits not saved yet")
  const done = screen.getByRole("button", { name: "Done editing" })
  expect(done).toBeEnabled()
  fireEvent.click(done)
  fireEvent.click(screen.getByRole("button", { name: "Stop editing" }))
  await waitFor(() => expect(screen.queryByTestId("page-document")).toBeNull())
  expect(await screen.findByText("Private saved body")).toBeInTheDocument()
})
it("does not reopen the editor when an earlier identity returns", async () => {
  const p = props()
  const view = render(<SavedDocumentPanel {...p} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  view.rerender(<SavedDocumentPanel {...p} enabled={false} />)
  view.rerender(<SavedDocumentPanel {...p} />)
  await screen.findByRole("button", { name: "Edit here" })
  expect(screen.queryByTestId("page-document")).toBeNull()
})
it("closes at once when nothing is unsent", async () => {
  const p = props()
  render(<SavedDocumentPanel {...p} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  fireEvent.click(screen.getByRole("button", { name: "Close document preview" }))
  expect(p.onClose).toHaveBeenCalledTimes(1)
})
it.each(["human", "workspace", "chat", "activation", "disabled"])("removes the editor immediately when %s changes", async kind => {
  const p = props()
  const view = render(<SavedDocumentPanel {...p} />)
  fireEvent.click(await screen.findByRole("button", { name: "Edit here" }))
  vi.mocked(apiFetch).mockReturnValue(new Promise(() => {}))
  const next = { ...p }
  if (kind === "human") next.currentUserId = "human2"
  if (kind === "workspace") next.workspaceId = "other"
  if (kind === "chat") next.chatId = "chat2"
  if (kind === "activation") next.activationKey = "open2"
  if (kind === "disabled") next.enabled = false
  view.rerender(<SavedDocumentPanel {...next} />)
  expect(screen.queryByTestId("page-document")).toBeNull()
})
