import { act, renderHook, waitFor } from "@testing-library/react"
import { beforeEach, describe, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import { usePageArtifact } from "@/hooks/use-page-artifact"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
const fetchMock = vi.mocked(apiFetch)
const original = { page_id: "original", html: "private source", head_rev: 1, revision: 1, can_write: true, revisions: [] }
const response = (data: unknown, status = 200) => new Response(JSON.stringify(data), { status })
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done }); return { promise, resolve } }
beforeEach(() => fetchMock.mockReset())
describe("identity-bound artifact transport", () => {
  it("cannot repopulate private source from a delayed GET after POST authority denial", async () => {
    const late = deferred<Response>()
    fetchMock.mockResolvedValueOnce(response(original)).mockReturnValueOnce(late.promise).mockResolvedValueOnce(response({ error: "revoked" }, 403))
    const { result } = renderHook(() => usePageArtifact("ws", "report", "original"))
    await waitFor(() => expect(result.current.data?.html).toBe("private source"))
    let reading!: Promise<void>
    act(() => { reading = result.current.refresh() })
    await act(async () => { await expect(result.current.mutate({ page_id: "original", base_rev: 1, mutation_id: "same-attempt", html: "new" }, false)).rejects.toThrow("revoked") })
    expect(result.current.data).toBeNull()
    await act(async () => { late.resolve(response(original)); await reading })
    expect(result.current.data).toBeNull()
  })
  it("refuses a different Page identity before sending any mutation", async () => {
    fetchMock.mockResolvedValueOnce(response(original))
    const { result } = renderHook(() => usePageArtifact("ws", "report", "original"))
    await waitFor(() => expect(result.current.data).not.toBeNull())
    await expect(result.current.mutate({ page_id: "replacement", base_rev: 0, mutation_id: "stale", html: "new" }, false)).rejects.toThrow("identity changed")
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
