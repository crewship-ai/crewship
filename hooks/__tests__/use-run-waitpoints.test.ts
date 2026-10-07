import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"
import type { RealtimeEvent } from "@/hooks/use-realtime"

const h = vi.hoisted(() => ({
  fetch: vi.fn(),
  listeners: new Map<string, (event: RealtimeEvent) => unknown>(),
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
vi.mock("@/hooks/use-realtime", async () => {
  const { useEffect } = await import("react")
  return {
    useRealtimeEvent(type: string, callback: (event: RealtimeEvent) => unknown) {
      useEffect(() => {
        h.listeners.set(type, callback)
        return () => { h.listeners.delete(type) }
      }, [type, callback])
    },
  }
})
import { useRunWaitpoints, useWorkspaceWaitpoints } from "../use-run-waitpoints"

const row = (run: string) => ({
  token: `token-${run}`, pipeline_run_id: run, step_id: "approval",
  kind: "approval", prompt: "Review", timeout_at: "2026-10-03T00:00:00Z",
  created_at: "2026-10-02T00:00:00Z",
})
const response = (rows: unknown) => ({ ok: true, json: async () => rows })
const events = ["pipeline.waitpoint.created", "pipeline.run.completed", "pipeline.run.failed", "pipeline.step.started"]
async function emit(type: string, payload: Record<string, unknown>) {
  await act(async () => { await h.listeners.get(type)?.({ type: type as RealtimeEvent["type"], payload, timestamp: new Date() }) })
}

afterEach(() => {
  cleanup()
  h.fetch.mockReset()
  h.listeners.clear()
})

describe("run waitpoint audience", () => {
  it("fetches the encoded workspace endpoint, filters the run, and releases subscriptions", async () => {
    h.fetch.mockResolvedValue(response([row("run"), row("other")]))
    const { result, unmount } = renderHook(() => useRunWaitpoints("workspace /", "run"))
    await waitFor(() => expect(result.current.waitpoints).toEqual([row("run")]))
    expect(h.fetch).toHaveBeenCalledExactlyOnceWith("/api/v1/workspaces/workspace%20%2F/pipelines/waitpoints")
    expect([...h.listeners.keys()]).toEqual(events)
    unmount()
    expect(h.listeners.size).toBe(0)
  })

  it("refreshes only matching run events, including the alternate run field and external decisions", async () => {
    h.fetch.mockResolvedValue(response([row("run")]))
    const { result } = renderHook(() => useRunWaitpoints("ws", "run"))
    await waitFor(() => expect(result.current.waitpoints).toHaveLength(1))
    for (const event of events) {
      await emit(event, { run_id: "other" })
      await emit(event, {})
    }
    expect(h.fetch).toHaveBeenCalledTimes(1)
    h.fetch.mockResolvedValue(response([]))
    for (const [index, event] of events.entries()) {
      await emit(event, index % 2 ? { pipeline_run_id: "run" } : { run_id: "run" })
    }
    expect(h.fetch).toHaveBeenCalledTimes(5)
    expect(result.current.waitpoints).toEqual([])
  })

  it("ignores slower previous-run and previous-refresh responses", async () => {
    let resolveOld!: (value: unknown) => void
    h.fetch.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    h.fetch.mockResolvedValue(response([row("new")]))
    const { result, rerender } = renderHook(({ run }) => useRunWaitpoints("ws", run), { initialProps: { run: "old" } })
    rerender({ run: "new" })
    await waitFor(() => expect(result.current.waitpoints).toEqual([row("new")]))
    await act(async () => { resolveOld(response([row("old")])) })
    expect(result.current.waitpoints).toEqual([row("new")])
    h.fetch.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    let pending!: Promise<void>
    act(() => { pending = result.current.refresh() })
    h.fetch.mockResolvedValue(response([]))
    await act(async () => { await result.current.refresh() })
    await act(async () => { resolveOld(response([row("new")])); await pending })
    expect(result.current.waitpoints).toEqual([])
  })

  it("does not restore approval capabilities after the run selection is cleared", async () => {
    let resolveOld!: (value: unknown) => void
    h.fetch.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const { result, rerender } = renderHook(({ run }: { run: string | null }) => useRunWaitpoints("ws", run), { initialProps: { run: "old" as string | null } })
    rerender({ run: null })
    await act(async () => { resolveOld(response([row("old")])) })
    expect(result.current.waitpoints).toEqual([])
    await emit("pipeline.waitpoint.created", { run_id: "old" })
    expect(h.fetch).toHaveBeenCalledTimes(1)
  })

  it("keeps missing scopes empty and clears waitpoints on an unavailable response", async () => {
    const { result, rerender } = renderHook(({ ws, run }: { ws: string | null, run: string | null }) => useRunWaitpoints(ws, run), { initialProps: { ws: null as string | null, run: "run" } })
    expect(h.fetch).not.toHaveBeenCalled()
    h.fetch.mockResolvedValue(response([row("run")]))
    rerender({ ws: "ws", run: "run" })
    await waitFor(() => expect(result.current.waitpoints).toHaveLength(1))
    h.fetch.mockResolvedValue({ ok: false })
    await act(async () => { await result.current.refresh() })
    expect(result.current.waitpoints).toEqual([])
  })
})

describe("workspace waitpoint audience", () => {
  it("includes every run, refreshes workspace events, and drops the previous workspace response", async () => {
    h.fetch.mockResolvedValue(response([row("one"), row("two")]))
    const { result, rerender, unmount } = renderHook(({ ws }) => useWorkspaceWaitpoints(ws), { initialProps: { ws: "old" } })
    await waitFor(() => expect(result.current.waitpoints).toHaveLength(2))
    for (const event of events) await emit(event, { run_id: "any" })
    expect(h.fetch).toHaveBeenCalledTimes(5)
    let resolveOld!: (value: unknown) => void
    h.fetch.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    let pending!: Promise<void>
    act(() => { pending = result.current.refresh() })
    h.fetch.mockResolvedValue(response([row("new")]))
    rerender({ ws: "new" })
    await waitFor(() => expect(result.current.waitpoints).toEqual([row("new")]))
    await act(async () => { resolveOld(response([row("old")])); await pending })
    expect(result.current.waitpoints).toEqual([row("new")])
    unmount()
    expect(h.listeners.size).toBe(0)
  })

  it("does not restore old workspace capabilities after deselection", async () => {
    let resolveOld!: (value: unknown) => void
    h.fetch.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const { result, rerender } = renderHook(({ ws }: { ws: string | null }) => useWorkspaceWaitpoints(ws), { initialProps: { ws: "old" as string | null } })
    rerender({ ws: null })
    await act(async () => { resolveOld(response([row("old")])) })
    expect(result.current.waitpoints).toEqual([])
    await emit("pipeline.waitpoint.created", {})
    expect(h.fetch).toHaveBeenCalledTimes(1)
  })
})
