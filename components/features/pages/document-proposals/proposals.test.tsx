import { fireEvent, render, screen, waitFor } from "@testing-library/react"
const userEvent = { click: async (el: Element) => { fireEvent.click(el) } }
import { describe, expect, it, vi } from "vitest"
import type { DocumentProposalClient, DocumentProposalDetail } from "@/lib/documents/proposals"
import { DocumentProposals } from "./document-proposals"

const para = (id: string, text: string) => ({ type: "paragraph", attrs: { id }, content: [{ type: "text", text }] })
const detail = (over: Partial<DocumentProposalDetail> = {}): DocumentProposalDetail => ({
  id: "pr1", author: "Research agent", target: { kind: "blocks", blockIds: ["a"] }, baseRev: 4, status: "open", createdAt: "2026-10-19T10:00:00Z",
  baseContent: { type: "doc", content: [para("a", "old text")] }, proposedContent: { type: "doc", content: [para("a", "new text")] }, ...over,
})
function client(over: Partial<DocumentProposalClient> = {}): DocumentProposalClient {
  return { list: async () => [detail()], read: async () => detail(), accept: vi.fn().mockResolvedValue({ status: "done", rev: 6 }), reject: vi.fn().mockResolvedValue({ status: "done" }), ...over }
}
async function open(c: DocumentProposalClient, canDecide = true, headRev = 4, hasLocalDraft = false) {
  const onAccepted = vi.fn()
  render(<DocumentProposals client={c} headRev={headRev} canDecide={canDecide} hasLocalDraft={hasLocalDraft} onDownloadDraft={vi.fn()} onAccepted={onAccepted} />)
  await userEvent.click(await screen.findByRole("button", { name: /Research agent · open/ }))
  await screen.findByText(/Proposal from Research agent/)
  return onAccepted
}

describe("proposal review", () => {
  it("shows the diff before anything is applied", async () => {
    await open(client())
    expect(screen.getByText("old text")).toBeInTheDocument()
    expect(screen.getByText("new text")).toBeInTheDocument()
    expect(screen.getByText(/changes 1 block · based on revision 4/)).toBeInTheDocument()
  })
  it("accepts against the confirmed head revision", async () => {
    const c = client(); const onAccepted = await open(c)
    await userEvent.click(screen.getByRole("button", { name: "Accept proposal" }))
    await waitFor(() => expect(c.accept).toHaveBeenCalledWith("pr1", 4, expect.any(String)))
    expect(await screen.findByText("Applied as revision 6.")).toBeInTheDocument()
    expect(onAccepted).toHaveBeenCalledWith(6)
  })
  it("reject needs a second explicit step", async () => {
    const c = client(); await open(c)
    await userEvent.click(screen.getByRole("button", { name: "Reject proposal" }))
    expect(c.reject).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole("button", { name: "Confirm reject" }))
    await waitFor(() => expect(c.reject).toHaveBeenCalledWith("pr1", expect.any(String)))
  })
  it("a conflict refuses the whole proposal and says nothing was applied", async () => {
    const c = client({ accept: vi.fn().mockResolvedValue({ status: "conflict" }) }); const onAccepted = await open(c, true, 7)
    expect(screen.getByText(/moved on since revision 4/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole("button", { name: "Accept proposal" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("Nothing was applied")
    expect(onAccepted).not.toHaveBeenCalled()
  })
  it("a reader can compare but not decide", async () => {
    await open(client(), false)
    expect(screen.queryByRole("button", { name: "Accept proposal" })).toBeNull()
    expect(screen.getByText(/compare this proposal but not decide/)).toBeInTheDocument()
  })
  it("shows load failures and an empty list explicitly", async () => {
    const { unmount } = render(<DocumentProposals client={client({ list: async () => [] })} headRev={1} canDecide hasLocalDraft={false} onDownloadDraft={vi.fn()} />)
    expect(await screen.findByText("No proposals.")).toBeInTheDocument(); unmount()
    render(<DocumentProposals client={client({ list: async () => { throw new Error("x") } })} headRev={1} canDecide hasLocalDraft={false} onDownloadDraft={vi.fn()} />)
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded")
  })
  it("accept is blocked while edits are unsent, with a download instead", async () => {
    const c = client()
    await open(c, true, 4, true)
    expect(screen.getByRole("button", { name: "Accept proposal" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Download unsent edits" })).toBeInTheDocument()
    expect(c.accept).not.toHaveBeenCalled()
  })
  it("an unknown outcome says so and retries with the same request ID", async () => {
    const accept = vi.fn().mockResolvedValueOnce({ status: "unknown" }).mockResolvedValueOnce({ status: "done", rev: 6 })
    await open(client({ accept }))
    await userEvent.click(screen.getByRole("button", { name: "Accept proposal" }))
    expect(await screen.findByRole("alert")).toHaveTextContent("result is unknown")
    await userEvent.click(screen.getByRole("button", { name: "Try again (same request)" }))
    await waitFor(() => expect(accept).toHaveBeenCalledTimes(2))
    expect(accept.mock.calls[1][2]).toBe(accept.mock.calls[0][2])
  })
  it("sends the revision that was on screen when the proposal was opened", async () => {
    const c = client()
    const onAccepted = vi.fn()
    const view = render(<DocumentProposals client={c} headRev={4} canDecide hasLocalDraft={false} onDownloadDraft={vi.fn()} onAccepted={onAccepted} />)
    await userEvent.click(await screen.findByRole("button", { name: /Research agent · open/ }))
    await screen.findByText(/Proposal from Research agent/)
    view.rerender(<DocumentProposals client={c} headRev={9} canDecide hasLocalDraft={false} onDownloadDraft={vi.fn()} onAccepted={onAccepted} />)
    await userEvent.click(screen.getByRole("button", { name: "Accept proposal" }))
    await waitFor(() => expect(c.accept).toHaveBeenCalledWith("pr1", 4, expect.any(String)))
  })
  it("a slow answer for an earlier click never replaces the proposal the user chose last", async () => {
    const slow: { resolve?: (d: DocumentProposalDetail) => void } = {}
    const c = client({
      list: async () => [detail({ id: "A", author: "Agent A" }), detail({ id: "B", author: "Agent B" })],
      read: id => id === "A" ? new Promise<DocumentProposalDetail>(resolve => { slow.resolve = resolve }) : Promise.resolve(detail({ id: "B", author: "Agent B" })),
    })
    render(<DocumentProposals client={c} headRev={4} canDecide hasLocalDraft={false} onDownloadDraft={vi.fn()} />)
    await userEvent.click(await screen.findByRole("button", { name: /Agent A/ }))
    await userEvent.click(screen.getByRole("button", { name: /Agent B/ }))
    await screen.findByText("Proposal from Agent B")
    slow.resolve!(detail({ id: "A", author: "Agent A" }))
    await new Promise(resolve => setTimeout(resolve, 10))
    expect(screen.queryByText("Proposal from Agent A")).toBeNull()
  })
  it("refreshes the list after a decision", async () => {
    const list = vi.fn().mockResolvedValue([detail()])
    await open(client({ list }))
    await userEvent.click(screen.getByRole("button", { name: "Accept proposal" }))
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2))
  })
})
