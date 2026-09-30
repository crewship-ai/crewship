import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { RestrictedFiles } from "../files/restricted-files"

const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))

describe("classified output files", () => {
  beforeEach(() => fetchMock.mockReset())
  it("requests the exact conversation and displays metadata without fetching bytes", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ files: [{ id: "v1", name: "reports/result.txt", size_bytes: 4, sha256: "hash", created_at: "2026-09-30" }] })))
    render(<RestrictedFiles chatId="c/a" workspaceId="w/a" refreshKey={0} />)
    await screen.findByText("reports/result.txt")
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/chats/c%2Fa/restricted-files?workspace_id=w%2Fa")
    expect(screen.getByRole("button", { name: "Download reports/result.txt" })).toBeEnabled()
  })
  it("clears stale filenames when refreshed authority is denied", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ files: [{ id: "v1", name: "PRIVATE_CANARY", size_bytes: 4, sha256: "hash", created_at: "now" }] })))
    fetchMock.mockResolvedValueOnce(new Response("{}", { status: 404 }))
    render(<RestrictedFiles chatId="c1" workspaceId="w1" refreshKey={0} />)
    await screen.findByText("PRIVATE_CANARY")
    fireEvent.click(screen.getByRole("button", { name: "Refresh files" }))
    await screen.findByRole("alert")
    expect(screen.queryByText("PRIVATE_CANARY")).not.toBeInTheDocument()
  })
  it("does not turn a denied download into a cached or legacy filesystem request", async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ files: [{ id: "v1", name: "PRIVATE_CANARY", size_bytes: 4, sha256: "hash", created_at: "now" }] })))
    fetchMock.mockResolvedValueOnce(new Response("{}", { status: 404 }))
    render(<RestrictedFiles chatId="c1" workspaceId="w1" refreshKey={0} />)
    fireEvent.click(await screen.findByRole("button", { name: "Download PRIVATE_CANARY" }))
    await screen.findByRole("alert")
    expect(screen.queryByText("PRIVATE_CANARY")).not.toBeInTheDocument()
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(fetchMock.mock.calls[1][0]).toBe("/api/v1/chats/c1/restricted-files/v1/download?workspace_id=w1")
  })
})
