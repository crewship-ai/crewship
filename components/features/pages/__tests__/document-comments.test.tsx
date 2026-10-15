import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { Editor, Extension } from "@tiptap/core"
import StarterKit from "@tiptap/starter-kit"
import { DocumentComments } from "../document-comments"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
afterEach(() => { cleanup(); request.mockReset() })
it("requires reselection when a remote edit shifts selected text before comment submission", async () => {
  const ids = Extension.create({ name: "testIDs", addGlobalAttributes: () => [{ types: ["paragraph"], attributes: { id: { default: "block" } } }] })
  const editor = new Editor({ extensions: [StarterKit, ids], content: { type: "doc", content: [{ type: "paragraph", attrs: { id: "block" }, content: [{ type: "text", text: "Alpha Beta" }] }] } })
  request.mockResolvedValue({ ok: true, json: async () => ({ comments: [], can_comment: true, can_resolve: false }) })
  const { rerender } = render(<DocumentComments workspaceId="w" slug="notes" rev={1} epoch="epoch" editor={editor} disabled={false} />)
  act(() => { editor.commands.setTextSelection({ from: 7, to: 11 }) })
  fireEvent.click(await screen.findByRole("button", { name: "Comment on selected text" }))
  fireEvent.change(screen.getByLabelText("Comment text"), { target: { value: "Discuss Beta" } })
  expect(screen.getByRole("button", { name: "Add comment" })).toBeEnabled()
  act(() => { editor.commands.insertContentAt(1, "Remote ") })
  rerender(<DocumentComments workspaceId="w" slug="notes" rev={2} epoch="epoch" editor={editor} disabled={false} />)
  expect(screen.getByRole("button", { name: "Add comment" })).toBeDisabled()
  expect(screen.getByText(/Reselect the text/)).toBeInTheDocument()
  expect(request.mock.calls.some(([, init]) => init?.method === "POST")).toBe(false)
  act(() => { editor.commands.setTextSelection({ from: 14, to: 18 }) })
  fireEvent.click(screen.getByRole("button", { name: "Comment on selected text" }))
  fireEvent.click(screen.getByRole("button", { name: "Add comment" }))
  await waitFor(() => expect(request.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true))
  const [, submission] = request.mock.calls.find(([, init]) => init?.method === "POST")!
  expect(JSON.parse(submission.body)).toMatchObject({ ifRev: 2, epoch: "epoch", from: 13, to: 17, text: "Discuss Beta" })
  editor.destroy()
})

it("clears an acknowledged comment draft even when its subsequent list refresh fails", async () => {
  const ids = Extension.create({ name: "testIDs", addGlobalAttributes: () => [{ types: ["paragraph"], attributes: { id: { default: "block" } } }] })
  const editor = new Editor({ extensions: [StarterKit, ids], content: { type: "doc", content: [{ type: "paragraph", content: [{ type: "text", text: "Alpha Beta" }] }] } })
  let commits = 0
  request.mockImplementation(async (_url: string, init?: RequestInit) => {
    if (init?.method === "POST") { commits++; return { ok: true, json: async () => ({}) } }
    if (commits) return { ok: false, status: 503, json: async () => ({ error: "Read unavailable" }) }
    return { ok: true, json: async () => ({ comments: [], can_comment: true, can_resolve: false }) }
  })
  render(<DocumentComments workspaceId="w" slug="notes" rev={1} epoch="epoch" editor={editor} disabled={false} />)
  act(() => { editor.commands.setTextSelection({ from: 7, to: 11 }) })
  fireEvent.click(await screen.findByRole("button", { name: "Comment on selected text" }))
  fireEvent.change(screen.getByLabelText("Comment text"), { target: { value: "Discuss Beta" } })
  fireEvent.click(screen.getByRole("button", { name: "Add comment" }))
  expect(await screen.findByText(/Comment saved, but comments could not be refreshed/)).toBeInTheDocument()
  expect(screen.getByLabelText("Comment text")).toHaveValue("")
  expect(screen.getByRole("button", { name: "Add comment" })).toBeDisabled()
  fireEvent.click(screen.getByRole("button", { name: "Add comment" }))
  expect(commits).toBe(1)
  editor.destroy()
})

it("gives a reselected source revision a new identity after an ambiguous earlier comment", async () => {
  const ids = Extension.create({ name: "testIDs", addGlobalAttributes: () => [{ types: ["paragraph"], attributes: { id: { default: "block" } } }] })
  const editor = new Editor({ extensions: [StarterKit, ids], content: { type: "doc", content: [{ type: "paragraph", content: [{ type: "text", text: "Alpha Beta" }] }] } })
  const submissions: Record<string, unknown>[] = []
  request.mockImplementation(async (_url: string, init?: RequestInit) => {
    if (init?.method === "POST") { submissions.push(JSON.parse(init.body as string)); return { ok: false, status: 503, json: async () => ({ error: "Reply lost" }) } }
    return { ok: true, json: async () => ({ comments: [], can_comment: true, can_resolve: false }) }
  })
  const { rerender } = render(<DocumentComments workspaceId="w" slug="notes" rev={1} epoch="epoch" editor={editor} disabled={false} />)
  act(() => { editor.commands.setTextSelection({ from: 7, to: 11 }) })
  fireEvent.click(await screen.findByRole("button", { name: "Comment on selected text" }))
  fireEvent.change(screen.getByLabelText("Comment text"), { target: { value: "Discuss this" } })
  fireEvent.click(screen.getByRole("button", { name: "Add comment" }))
  await screen.findByText("Reply lost")
  fireEvent.click(screen.getByRole("button", { name: "Add comment" }))
  await waitFor(() => expect(submissions).toHaveLength(2))
  expect(submissions[1]).toEqual(submissions[0])
  await waitFor(() => expect(screen.getByRole("button", { name: "Add comment" })).toBeEnabled())
  act(() => { editor.commands.insertContentAt({ from: 7, to: 11 }, "Next") })
  rerender(<DocumentComments workspaceId="w" slug="notes" rev={2} epoch="epoch" editor={editor} disabled={false} />)
  act(() => { editor.commands.setTextSelection({ from: 7, to: 11 }) })
  fireEvent.click(screen.getByRole("button", { name: "Comment on selected text" }))
  fireEvent.click(screen.getByRole("button", { name: "Add comment" }))
  await waitFor(() => expect(submissions).toHaveLength(3))
  expect(submissions[2].ifRev).toBe(2)
  expect(submissions[2].id).not.toBe(submissions[0].id)
  editor.destroy()
})
