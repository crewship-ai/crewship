import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"
import { documentSchema } from "@/lib/documents/schema"
import { DocumentHistory } from "./document-history"
import type { DocumentHistoryReview } from "./types"

const para = (id: string, text: string) => ({ type: "paragraph", attrs: { id }, content: [{ type: "text", text }] })
const doc = (...content: unknown[]) => ({ type: "doc", content })
const old = doc(para("a", "first"), para("b", "second"))
const head = doc(para("a", "first edited"), para("c", "third"))
documentSchema.validateJSON(old, 3); documentSchema.validateJSON(head, 3)

function view(review: Partial<DocumentHistoryReview>) {
  const props = {
    pageId: "p", scopeKey: "s", selectedId: "c1", onSelect: vi.fn(), onClose: vi.fn(),
    list: { status: "ready" as const, scopeKey: "s", data: { checkpoints: [{ id: "c1", rev: 2, schema: 3, actor: "Ada", created_at: "2026-10-19T10:00:00Z" }] } },
    snapshot: { status: "ready" as const, scopeKey: "s", data: { page_id: "p", id: "c1", rev: 2, schema: 3, content: old } },
    review: { currentContent: head, headRev: 5, hasLocalDraft: false, onDownloadDraft: vi.fn(), onActionDone: vi.fn(), ...review },
  }
  return { ...render(<DocumentHistory {...props} />), props }
}
const done = { status: "done" as const, rev: 6 }
const revert = (n = 1) => screen.getAllByRole("button", { name: "Revert this change" })[n]

describe("history compare and revert", () => {
  it("compares the selected revision with the saved version using words, not colour", async () => {
    view({})
    fireEvent.click(screen.getByRole("button", { name: "Compare with saved version" }))
    expect(screen.getByText(/1 added, 1 removed, 1 changed, 0 moved/)).toBeInTheDocument()
    expect(screen.getByText(/Added · paragraph/)).toBeInTheDocument()
    expect(screen.getByText(/Removed · paragraph/)).toBeInTheDocument()
  })
  it("labels the comparison as the saved version and says unsent edits are not included", () => {
    view({ hasLocalDraft: true })
    fireEvent.click(screen.getByRole("button", { name: "Compare with saved version" }))
    expect(screen.getByRole("region", { name: "Compare revision 2 with the saved version" })).toBeInTheDocument()
    expect(screen.getByText(/Unsent edits are not included/)).toBeInTheDocument()
  })
  it("has no draft note when nothing is unsent", () => {
    view({})
    fireEvent.click(screen.getByRole("button", { name: "Compare with saved version" }))
    expect(screen.queryByText(/Unsent edits are not included/)).toBeNull()
  })
  it("never compares against a document that has not loaded", () => {
    view({ currentContent: undefined })
    expect(screen.getByRole("button", { name: "Compare with saved version" })).toBeDisabled()
    expect(screen.getByText("Loading current document…")).toBeInTheDocument()
    expect(screen.queryByText(/Removed · paragraph/)).toBeNull()
  })
  it("offers no write action without an actions client", () => {
    view({})
    expect(screen.queryByRole("button", { name: "Revert this change" })).toBeNull()
  })
  it("reverts with the head the user reviewed and a request ID", async () => {
    const actions = { revertChange: vi.fn().mockResolvedValue(done) }
    const { props } = view({ actions })
    fireEvent.click(screen.getByRole("button", { name: "Revert this change" }))
    expect(screen.getByText(/Undo only the change recorded at this checkpoint \(revision 2\)/)).toBeInTheDocument()
    fireEvent.click(revert())
    await waitFor(() => expect(actions.revertChange).toHaveBeenCalledWith({ checkpointId: "c1", expectedRev: 5, requestId: expect.any(String) }))
    expect(props.review.onActionDone).toHaveBeenCalledWith(6)
  })
  it("will not revert over unsent edits; offers a download instead", () => {
    const actions = { revertChange: vi.fn() }
    const { props } = view({ actions, hasLocalDraft: true })
    fireEvent.click(screen.getByRole("button", { name: "Revert this change" }))
    expect(revert()).toBeDisabled()
    fireEvent.click(screen.getByRole("button", { name: "Download unsent edits" }))
    expect(props.review.onDownloadDraft).toHaveBeenCalledTimes(1)
    expect(actions.revertChange).not.toHaveBeenCalled()
  })
  it.each([
    ["conflict", { status: "conflict", blocks: ["a", "b"] }, /Later edits touch the same blocks.*\(2 blocks\)/],
    ["stale", { status: "stale" }, /moved on/],
    ["retention", { status: "retention" }, /no longer retained/],
    ["denied", { status: "denied" }, /no longer have access/],
    ["unknown", { status: "unknown" }, /result is unknown.*Check the document history/],
  ])("shows %s as an explicit result", async (_n, result, text) => {
    const actions = { revertChange: vi.fn().mockResolvedValue(result) }
    const { props } = view({ actions })
    fireEvent.click(screen.getByRole("button", { name: "Revert this change" }))
    fireEvent.click(revert())
    expect(await screen.findByRole("alert")).toHaveTextContent(text)
    expect(props.review.onActionDone).not.toHaveBeenCalled()
  })
  it("an unknown outcome retries with the SAME request ID", async () => {
    const actions = { revertChange: vi.fn().mockResolvedValueOnce({ status: "unknown" }).mockResolvedValueOnce(done) }
    view({ actions })
    fireEvent.click(screen.getByRole("button", { name: "Revert this change" }))
    fireEvent.click(revert())
    fireEvent.click(await screen.findByRole("button", { name: "Try again (same request)" }))
    await waitFor(() => expect(actions.revertChange).toHaveBeenCalledTimes(2))
    expect(actions.revertChange.mock.calls[1][0].requestId).toBe(actions.revertChange.mock.calls[0][0].requestId)
  })
  it("never reuses a request ID for a different expected revision", async () => {
    const actions = { revertChange: vi.fn().mockResolvedValueOnce({ status: "unknown" }).mockResolvedValueOnce(done) }
    const { props, rerender } = view({ actions })
    fireEvent.click(screen.getByRole("button", { name: "Revert this change" }))
    fireEvent.click(revert())
    await screen.findByRole("button", { name: "Try again (same request)" })
    rerender(<DocumentHistory {...props} review={{ ...props.review, headRev: 6 }} />)
    fireEvent.click(revert(0)) // reopen: now reviewing revision 6
    fireEvent.click(revert())
    await waitFor(() => expect(actions.revertChange).toHaveBeenCalledTimes(2))
    const [first, second] = actions.revertChange.mock.calls.map(call => call[0])
    expect([first.expectedRev, second.expectedRev]).toEqual([5, 6])
    expect(second.requestId).not.toBe(first.requestId)
  })
  it("sends the revision the user reviewed even if the head moves while confirming", async () => {
    const actions = { revertChange: vi.fn().mockResolvedValue(done) }
    const { props, rerender } = view({ actions })
    fireEvent.click(screen.getByRole("button", { name: "Revert this change" }))
    rerender(<DocumentHistory {...props} review={{ ...props.review, headRev: 6 }} />)
    expect(screen.getByText(/changed after you opened this confirmation/)).toBeInTheDocument()
    fireEvent.click(revert())
    await waitFor(() => expect(actions.revertChange).toHaveBeenCalledWith(expect.objectContaining({ expectedRev: 5 })))
  })
  it("Escape cancels a pending confirmation", () => {
    view({ actions: { revertChange: vi.fn() } })
    fireEvent.click(screen.getByRole("button", { name: "Revert this change" }))
    fireEvent.keyDown(screen.getByRole("group", { name: "Confirm revert" }), { key: "Escape" })
    expect(screen.queryByRole("group", { name: "Confirm revert" })).toBeNull()
  })
})
