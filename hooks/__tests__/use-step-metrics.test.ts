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
import { useStepMetrics } from "../use-step-metrics"

const response = (rows: unknown) => ({ ok: true, json: async () => rows })
const entry = (payload: unknown, type = "pipeline.step.completed") => ({ id: "entry", ts: "2026-10-02T00:00:00Z", entry_type: type, payload })
async function emit(payload: Record<string, unknown> | null) {
  await act(async () => {
    h.listeners.get("pipeline.step.completed")?.({ type: "pipeline.step.completed", payload, timestamp: new Date() } as RealtimeEvent)
  })
}
afterEach(() => {
  cleanup()
  h.fetch.mockReset()
  h.listeners.clear()
})

describe("step metrics", () => {
  it("loads the encoded pipeline journal, scopes completed steps to the run, and preserves latest retry metrics", async () => {
    h.fetch.mockResolvedValue(response([
      // ListRuns orders ts DESC, so the latest retry arrives first.
      { ...entry({ run_id: "run", step_id: "one", duration_ms: 5, cost_usd: 6 }), id: "latest-retry", ts: "2026-10-02T00:00:02Z" },
      { ...entry({ run_id: "run", step_id: "one", duration_ms: 1, cost_usd: 2 }), id: "older-retry", ts: "2026-10-02T00:00:01Z" },
      entry({ pipeline_run_id: "run", step_id: "two" }),
      entry({ run_id: "other", step_id: "foreign", duration_ms: 100 }),
      entry({ run_id: "run", step_id: "unfinished" }, "pipeline.step.started"),
      entry(null),
      entry({ run_id: "run" }),
    ]))
    const { result, unmount } = renderHook(() => useStepMetrics("workspace /", "pipeline /", "run"))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(h.fetch).toHaveBeenCalledExactlyOnceWith("/api/v1/workspaces/workspace%20%2F/pipelines/pipeline%20%2F/runs?limit=200&include_steps=1")
    expect([...result.current.metrics]).toEqual([
      ["one", { durationMs: 5, costUsd: 6 }], ["two", { durationMs: 0, costUsd: 0 }],
    ])
    expect([...h.listeners.keys()]).toEqual(["pipeline.step.completed"])
    unmount()
    expect(h.listeners.size).toBe(0)
  })

  it("merges live completions immutably and ignores unrelated or incomplete events", async () => {
    h.fetch.mockResolvedValue(response([entry({ run_id: "run", step_id: "one", duration_ms: 5, cost_usd: 6 })]))
    const { result } = renderHook(() => useStepMetrics("ws", "pipe", "run"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    const initial = result.current.metrics
    await emit({ run_id: "other", step_id: "foreign" })
    await emit({ run_id: "run" })
    await emit({})
    await emit(null)
    expect(result.current.metrics).toBe(initial)
    await emit({ pipeline_run_id: "run", step_id: "two", duration_ms: "invalid", cost_usd: 3 })
    expect(result.current.metrics.get("two")).toEqual({ durationMs: 0, costUsd: 3 })
    expect(initial.has("two")).toBe(false)
    await emit({ run_id: "run", step_id: "one", duration_ms: 9, cost_usd: "invalid" })
    expect(result.current.metrics.get("one")).toEqual({ durationMs: 9, costUsd: 0 })
    expect(initial.get("one")).toEqual({ durationMs: 5, costUsd: 6 })
    expect(h.fetch).toHaveBeenCalledTimes(1)
  })

  it.each(["response", "network-error", "non-ok"])("retains live completions while initial history finishes with %s", async (completion) => {
    let resolveHistory!: (value: unknown) => void
    let rejectHistory!: (error: Error) => void
    h.fetch.mockImplementationOnce(() => new Promise((resolve, reject) => { resolveHistory = resolve; rejectHistory = reject }))
    const { result } = renderHook(() => useStepMetrics("ws", "pipe", "run"))
    await emit({ run_id: "run", step_id: "retry", duration_ms: 9, cost_usd: 8 })
    await emit({ run_id: "run", step_id: "live-only", duration_ms: 7, cost_usd: 6 })
    await act(async () => {
      if (completion === "network-error") rejectHistory(new Error("history unavailable"))
      else if (completion === "non-ok") resolveHistory({ ok: false })
      else resolveHistory(response([
        entry({ run_id: "run", step_id: "retry", duration_ms: 1, cost_usd: 2 }),
        entry({ run_id: "run", step_id: "history-only", duration_ms: 3, cost_usd: 4 }),
      ]))
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.metrics.get("retry")).toEqual({ durationMs: 9, costUsd: 8 })
    expect(result.current.metrics.get("live-only")).toEqual({ durationMs: 7, costUsd: 6 })
    if (completion === "response") expect(result.current.metrics.get("history-only")).toEqual({ durationMs: 3, costUsd: 4 })
  })

  it("clears the old run heatmap while the new run history is pending", async () => {
    h.fetch.mockResolvedValueOnce(response([entry({ run_id: "old", step_id: "old-step", duration_ms: 1, cost_usd: 2 })]))
    const { result, rerender } = renderHook(({ run }) => useStepMetrics("ws", "pipe", run), { initialProps: { run: "old" } })
    await waitFor(() => expect(result.current.metrics.has("old-step")).toBe(true))
    let resolveNew!: (value: unknown) => void
    h.fetch.mockImplementationOnce(() => new Promise(resolve => { resolveNew = resolve }))
    rerender({ run: "new" })
    expect(result.current.loading).toBe(true)
    expect(result.current.metrics.size).toBe(0)
    await emit({ run_id: "old", step_id: "old-step", duration_ms: 5 })
    expect(result.current.metrics.size).toBe(0)
    await act(async () => { resolveNew(response([entry({ run_id: "new", step_id: "new-step", duration_ms: 7, cost_usd: 8 })])) })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect([...result.current.metrics]).toEqual([["new-step", { durationMs: 7, costUsd: 8 }]])
  })

  it.each(["non-ok", "network", "malformed-json"])("treats %s as nonfatal empty heatmap", async (failure) => {
    if (failure === "network") h.fetch.mockRejectedValue(new Error("offline"))
    else if (failure === "non-ok") h.fetch.mockResolvedValue({ ok: false })
    else h.fetch.mockResolvedValue({ ok: true, json: async () => { throw new Error("invalid JSON") } })
    const { result } = renderHook(() => useStepMetrics("ws", "pipe", "run"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.metrics.size).toBe(0)
  })

  it.each(["response", "error"])("ignores a previous run's late %s", async (completion) => {
    let resolveOld!: (value: unknown) => void
    let rejectOld!: (error: Error) => void
    h.fetch.mockImplementationOnce(() => new Promise((resolve, reject) => { resolveOld = resolve; rejectOld = reject }))
    h.fetch.mockResolvedValue(response([entry({ run_id: "new", step_id: "new-step", duration_ms: 7, cost_usd: 8 })]))
    const { result, rerender } = renderHook(({ run }) => useStepMetrics("ws", "pipe", run), { initialProps: { run: "old" } })
    rerender({ run: "new" })
    await waitFor(() => expect(result.current.loading).toBe(false))
    const current = result.current.metrics
    await act(async () => {
      if (completion === "response") resolveOld(response([entry({ run_id: "old", step_id: "old-step" })]))
      else rejectOld(new Error("old request failed"))
    })
    expect(result.current.metrics).toBe(current)
    expect(result.current.metrics.get("new-step")).toEqual({ durationMs: 7, costUsd: 8 })
    await emit({ run_id: "old", step_id: "old-step" })
    expect(result.current.metrics).toBe(current)
  })

  it("clears a pending selection and loading without allowing a late request to restore data", async () => {
    let resolveOld!: (value: unknown) => void
    h.fetch.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const { result, rerender } = renderHook(({ run }: { run: string | null }) => useStepMetrics("ws", "pipe", run), { initialProps: { run: "old" as string | null } })
    expect(result.current.loading).toBe(true)
    rerender({ run: null })
    expect(result.current.loading).toBe(false)
    await act(async () => { resolveOld(response([entry({ run_id: "old", step_id: "old-step" })])) })
    await emit({ run_id: "old", step_id: "old-step" })
    expect(result.current.metrics.size).toBe(0)
  })

  it.each([
    [null, "pipe", "run"], ["ws", null, "run"], ["ws", "pipe", null],
  ])("does not fetch or accept realtime data for incomplete scope %s/%s/%s", async (ws, pipe, run) => {
    const { result, unmount } = renderHook(() => useStepMetrics(ws, pipe, run))
    expect(h.fetch).not.toHaveBeenCalled()
    expect(result.current.loading).toBe(false)
    expect(result.current.metrics.size).toBe(0)
    await emit({ run_id: "run", step_id: "unscoped", duration_ms: 9, cost_usd: 8 })
    expect(result.current.metrics.size).toBe(0)
    unmount()
    expect(h.listeners.size).toBe(0)
  })
})
