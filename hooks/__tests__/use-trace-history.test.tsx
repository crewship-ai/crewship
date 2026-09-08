import { renderHook, waitFor } from "@testing-library/react"
import { describe, it, expect, vi, beforeEach } from "vitest"
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: vi.fn() }))
const fetchMock = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: fetchMock }))
import { useTrace } from "../use-trace"
describe("historical run definition", () => {
  beforeEach(() => fetchMock.mockReset())
  it("uses the immutable definition on the run without resolving current HEAD", async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ id: "old-run", pipeline_slug: "changed-recipe", status: "completed", outcome: "FAILED", definition: { steps: [{ id: "old-step", type: "transform" }] } }) })
    const { result } = renderHook(() => useTrace("ws", "old-run"))
    await waitFor(() => expect(result.current.dsl?.steps?.[0].id).toBe("old-step"))
    expect(result.current.run?.outcome).toBe("FAILED")
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/workspaces/ws/pipeline-runs/old-run")
  })
  it("does not substitute the previous graph when a run has no archived definition", async () => {
    fetchMock.mockResolvedValueOnce({ ok: true, json: async () => ({ id: "first", status: "completed", definition: { steps: [{ id: "first-step" }] } }) })
    const { result, rerender } = renderHook(({ run }) => useTrace("ws", run), { initialProps: { run: "first" } })
    await waitFor(() => expect(result.current.dsl).not.toBeNull())
    fetchMock.mockResolvedValueOnce({ ok: true, json: async () => ({ id: "second", status: "completed", definition: null, definition_status: "unavailable" }) })
    rerender({ run: "second" })
    await waitFor(() => expect(result.current.run?.id).toBe("second"))
    expect(result.current.dsl).toBeNull()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})
