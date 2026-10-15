import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { Editor, Extension } from "@tiptap/core"
import StarterKit from "@tiptap/starter-kit"
import { DocumentAgentWork } from "../document-agent-work"
import { captureDocumentSelection } from "../document-selection"
import type { DocumentState } from "@/hooks/use-page-document"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
afterEach(() => { cleanup(); request.mockReset() })
function fixture() {
  const ids = Extension.create({ name: "testIDs", addGlobalAttributes: () => [{ types: ["paragraph", "heading", "bulletList", "listItem"], attributes: { id: { default: null } } }] })
  const editor = new Editor({ extensions: [StarterKit, ids], content: { type: "doc", content: [{ type: "paragraph", attrs: { id: "paragraph" }, content: [{ type: "text", text: "Alpha 😀 Beta" }] }] } })
  const confirmed: DocumentState = { page_id: "real-page-id", schema: 2, rev: 7, epoch: "epoch", snapshot: "", can_write: true, content: editor.getJSON(), outline: [{ id: "paragraph", type: "paragraph", text: "Alpha 😀 Beta", hash: "a".repeat(64) }] }
  request.mockResolvedValue({ ok: true, json: async () => ({ requests: [], can_dispatch: true, eligible_chats: [{ chat_id: "chat", agent_name: "Agent" }] }) })
  return { editor, confirmed }
}
it("captures confirmed Page ID and UTF16 selected range, then freezes exact submission across selection changes and retries", async () => {
  const { editor, confirmed } = fixture()
  const submissions: unknown[] = []
  request.mockImplementation(async (_url: string, init?: RequestInit) => {
    if (init?.method === "POST") { submissions.push(JSON.parse(init.body as string)); return { ok: submissions.length > 1, status: 503, json: async () => ({ error: "Reply lost" }) } }
    return { ok: true, json: async () => ({ requests: [], can_dispatch: true, eligible_chats: [{ chat_id: "chat", agent_name: "Agent" }] }) }
  })
  const { rerender } = render(<DocumentAgentWork workspaceId="w" slug="different-slug" pageId="real-page-id" confirmed={confirmed} editor={editor} ready disabled={false} />)
  act(() => { editor.commands.setTextSelection({ from: 10, to: 14 }) })
  fireEvent.click(await screen.findByRole("button", { name: "Use selected text" }))
  expect(screen.getByLabelText("Selected text for agent")).toHaveTextContent("Beta")
  act(() => { editor.commands.setTextSelection({ from: 1, to: 6 }) })
  fireEvent.change(screen.getByLabelText("Private agent chat"), { target: { value: "chat" } })
  fireEvent.change(screen.getByLabelText("Agent instructions"), { target: { value: "Rewrite this selected text." } })
  fireEvent.click(screen.getByRole("button", { name: "Send to agent" }))
  await screen.findByText("Reply lost")
  expect(submissions[0]).toMatchObject({ page_id: "real-page-id", base_rev: 7, targets: [{ id: "paragraph", hash: "a".repeat(64), from: 9, to: 13, quote: "Beta" }], chat_id: "chat", instruction: "Rewrite this selected text." })
  rerender(<DocumentAgentWork workspaceId="w" slug="different-slug" pageId="real-page-id" confirmed={{ ...confirmed, rev: 8 }} editor={editor} ready disabled={false} />)
  fireEvent.click(screen.getByRole("button", { name: "Retry send" }))
  await waitFor(() => expect(submissions).toHaveLength(2))
  expect(submissions[1]).toEqual(submissions[0])
  await waitFor(() => expect(screen.queryByLabelText("Selected text for agent")).toBeNull())
  editor.destroy()
})
it("does not dispatch without selection and requires explicit discard before replacing a stale captured source", async () => {
  const { editor, confirmed } = fixture()
  const { rerender } = render(<DocumentAgentWork workspaceId="w" slug="doc" confirmed={confirmed} editor={editor} ready disabled={false} />)
  fireEvent.change(await screen.findByLabelText("Private agent chat"), { target: { value: "chat" } })
  fireEvent.change(screen.getByLabelText("Agent instructions"), { target: { value: "Rewrite." } })
  expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Use selected text" }))
  expect(screen.getByText(/Select text in a paragraph/)).toBeInTheDocument()
  act(() => { editor.commands.setTextSelection({ from: 1, to: 6 }) })
  fireEvent.click(screen.getByRole("button", { name: "Use selected text" }))
  rerender(<DocumentAgentWork workspaceId="w" slug="doc" confirmed={{ ...confirmed, rev: 8 }} editor={editor} ready disabled={false} />)
  expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled()
  expect(screen.getByText(/Discard this draft and select/)).toBeInTheDocument()
  expect(screen.getByRole("button", { name: "Use selected text" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Discard draft" }))
  expect(screen.queryByLabelText("Selected text for agent")).toBeNull()
  expect(request.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  editor.destroy()
})
it("rejects pending state, unconfirmed edits, missing hashes, split characters, and multi-block selection", () => {
  const { editor, confirmed } = fixture()
  editor.commands.setTextSelection({ from: 1, to: 6 })
  expect(() => captureDocumentSelection(editor, confirmed, undefined, false)).toThrow(/saved/)
  expect(() => captureDocumentSelection(editor, { ...confirmed, outline: [] }, undefined, true)).toThrow(/confirmed/)
  editor.commands.insertContentAt(1, "Drift ")
  editor.commands.setTextSelection({ from: 1, to: 6 })
  expect(() => captureDocumentSelection(editor, confirmed, undefined, true)).toThrow(/confirmed/)
  editor.commands.setContent(confirmed.content as Parameters<typeof editor.commands.setContent>[0])
  editor.commands.setTextSelection({ from: 8, to: 9 })
  expect(() => captureDocumentSelection(editor, confirmed, undefined, true)).toThrow(/whole characters/)
  editor.commands.setContent({ type: "doc", content: [{ type: "paragraph", attrs: { id: "one" }, content: [{ type: "text", text: "One" }] }, { type: "paragraph", attrs: { id: "two" }, content: [{ type: "text", text: "Two" }] }] })
  editor.commands.setTextSelection({ from: 1, to: 9 })
  const multi = { ...confirmed, content: editor.getJSON(), outline: [{ id: "one", type: "paragraph", text: "One", hash: "a".repeat(64) }, { id: "two", type: "paragraph", text: "Two", hash: "b".repeat(64) }] }
  expect(() => captureDocumentSelection(editor, multi, undefined, true)).toThrow(/within one paragraph/)
  editor.destroy()
})

it("captures a selected list paragraph without including its list ancestors", () => {
  const { editor, confirmed } = fixture()
  editor.commands.setContent({ type: "doc", content: [{ type: "bulletList", attrs: { id: "list" }, content: [{ type: "listItem", attrs: { id: "item" }, content: [{ type: "paragraph", attrs: { id: "nested" }, content: [{ type: "text", text: "Nested text" }] }] }] }] })
  editor.commands.setTextSelection({ from: 3, to: 9 })
  const state = { ...confirmed, content: editor.getJSON(), outline: [{ id: "list", type: "bulletList", text: "Nested text", hash: "b".repeat(64) }, { id: "item", type: "listItem", text: "Nested text", hash: "c".repeat(64) }, { id: "nested", type: "paragraph", text: "Nested text", hash: "d".repeat(64) }] }
  expect(captureDocumentSelection(editor, state, undefined, true).targets).toEqual([{ id: "nested", hash: "d".repeat(64), from: 0, to: 6, quote: "Nested" }])
  editor.destroy()
})

it("rejects unconfirmed changes elsewhere in the document before capturing an unchanged selected paragraph", () => {
  const { editor, confirmed } = fixture()
  editor.commands.setContent({ type: "doc", content: [{ type: "paragraph", attrs: { id: "one" }, content: [{ type: "text", text: "One" }] }, { type: "paragraph", attrs: { id: "two" }, content: [{ type: "text", text: "Two" }] }] })
  const state = { ...confirmed, content: editor.getJSON(), outline: [{ id: "one", type: "paragraph", text: "One", hash: "a".repeat(64) }, { id: "two", type: "paragraph", text: "Two", hash: "b".repeat(64) }] }
  editor.commands.insertContentAt(6, "Unconfirmed ")
  editor.commands.setTextSelection({ from: 1, to: 4 })
  expect(() => captureDocumentSelection(editor, state, undefined, true)).toThrow(/editor differs from the confirmed/)
  editor.destroy()
})

it("accepts equivalent confirmed empty text blocks without treating projection defaults as unsaved edits", () => {
  const { editor, confirmed } = fixture()
  editor.commands.insertContentAt(editor.state.doc.content.size, { type: "paragraph", attrs: { id: "empty" } })
  editor.commands.setTextSelection({ from: 1, to: 6 })
  const content = editor.getJSON()
  content.content![1].content = []
  const state = { ...confirmed, content }
  expect(captureDocumentSelection(editor, state, undefined, true).targets[0].quote).toBe("Alpha")
  editor.destroy()
})
