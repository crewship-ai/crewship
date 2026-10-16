import React from "react"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { PageArtifactView } from "../page-artifact"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/hooks/use-static-artifact-document", () => ({ useStaticArtifactDocument: (html?: string) => html ? "<p>safe preview</p>" : "" }))
const fetchMock = vi.mocked(apiFetch)
const saved = { page_id: "original", html: "saved source", revision: 1, head_rev: 1, can_write: true, revisions: [{ revision: 1, created_at: "2026-10-16T00:00:00Z" }] }
const response = (data: unknown) => new Response(JSON.stringify(data))
beforeEach(() => fetchMock.mockReset())
async function edit() {
  await screen.findByText("Revision 1 · latest 1")
  fireEvent.click(screen.getByRole("button", { name: "Source" }))
  fireEvent.click(screen.getByRole("button", { name: "Edit as new revision" }))
  fireEvent.change(screen.getByLabelText(/HTML for the next revision/), { target: { value: "new draft" } })
}
describe("human static artifact pane", () => {
  it("retains the exact request after lost ACK; newer head does not rebase the retry", async () => {
    fetchMock.mockImplementation(async (_url, init) => {
      if (init?.method === "POST") throw new TypeError("connection lost")
      return response(saved)
    })
    render(<PageArtifactView workspaceId="ws" slug="report" pageId="original" onDirtyChange={vi.fn()} />)
    await edit()
    fireEvent.click(screen.getByRole("button", { name: "Save revision" }))
    await screen.findByText("connection lost")
    const first = fetchMock.mock.calls.find(call => call[1]?.method === "POST")![1]!.body
    fireEvent.click(screen.getByRole("button", { name: "Retry exact save" }))
    await waitFor(() => expect(fetchMock.mock.calls.filter(call => call[1]?.method === "POST")).toHaveLength(2))
    expect(fetchMock.mock.calls.filter(call => call[1]?.method === "POST")[1][1]!.body).toBe(first)
    expect(JSON.parse(String(first))).toMatchObject({ page_id: "original", base_rev: 1, html: "new draft" })
  })
  it("does not save a retained draft into a recreated slug", async () => {
    fetchMock.mockResolvedValue(response(saved))
    const dirty = vi.fn()
    const { rerender } = render(<PageArtifactView workspaceId="ws" slug="report" pageId="original" onDirtyChange={dirty} />)
    await edit()
    rerender(<PageArtifactView workspaceId="ws" slug="report" pageId="replacement" onDirtyChange={dirty} />)
    expect(screen.getByRole("button", { name: "Save revision" })).toBeDisabled()
    expect(screen.getByText(/This slug now identifies another Page/)).toBeVisible()
    expect(screen.getByLabelText(/HTML for the next revision/)).toHaveValue("new draft")
    expect(fetchMock.mock.calls.filter(call => call[1]?.method === "POST")).toHaveLength(0)
  })
  it("shows saved history to a reader without offering authoring controls", async () => {
    fetchMock.mockResolvedValue(response({ ...saved, can_write: false }))
    render(<PageArtifactView workspaceId="ws" slug="report" pageId="original" onDirtyChange={vi.fn()} />)
    await screen.findByText(/read only/)
    fireEvent.click(screen.getByRole("button", { name: "History" }))
    expect(screen.getByRole("button", { name: "View revision 1" })).toBeVisible()
    expect(screen.queryByRole("button", { name: "Restore revision 1" })).toBeNull()
    expect(screen.queryByLabelText("Upload HTML")).toBeNull()
  })
})
