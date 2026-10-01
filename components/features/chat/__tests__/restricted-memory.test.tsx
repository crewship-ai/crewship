import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { RestrictedMemory } from "../restricted-memory"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
const note = { id: "note1", kind: "memory", content: "H1_MEMORY_CANARY", created_at: "2026-09-30", created_by: "h1" }

describe("conversation memory", () => {
  beforeEach(() => fetchMock.mockReset())
  it("submits a human note without client-selected provenance", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ entries: [] })))
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(note), { status: 201 }))
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ entries: [note] })))
    render(<RestrictedMemory chatId="c1" workspaceId="w1" userId="h1" refreshKey={0} />)
    fireEvent.change(screen.getByLabelText("New conversation memory"), { target: { value: "H1_MEMORY_CANARY" } })
    fireEvent.click(screen.getByRole("button", { name: "Save note" }))
    await screen.findByText("H1_MEMORY_CANARY")
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/chats/c1/restricted-memory?workspace_id=w1")
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({ content: "H1_MEMORY_CANARY" })
    expect(screen.getByLabelText("New conversation memory")).toHaveValue("")
  })
  it("does not offer removal of another participant's note", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ entries: [note] })))
    render(<RestrictedMemory chatId="group" workspaceId="w1" userId="h2" refreshKey={0} />)
    await screen.findByText("H1_MEMORY_CANARY")
    expect(screen.queryByRole("button", { name: "Remove note" })).not.toBeInTheDocument()
  })
  it("reauthorizes an export and clears stale notes when access is revoked", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ entries: [note] })))
    fetchMock.mockResolvedValueOnce(new Response("{}", { status: 404 }))
    render(<RestrictedMemory chatId="c1" workspaceId="w1" userId="h1" refreshKey={0} />)
    await screen.findByText("H1_MEMORY_CANARY")
    fireEvent.click(screen.getByRole("button", { name: "Export context versions" }))
    await screen.findByRole("alert")
    expect(screen.queryByText("H1_MEMORY_CANARY")).not.toBeInTheDocument()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/chats/c1/restricted-context?workspace_id=w1")
  })
})
