import { act, cleanup, renderHook, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

const h = vi.hoisted(() => ({ fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.fetch }))
import { useRunExecutions, type RunExecution } from "../use-run-executions"

afterEach(() => {
  cleanup()
  h.fetch.mockReset()
})
const row = (i: number): RunExecution => ({
  id: `e${i}`,
  parent_execution_id: "",
  step_id: `step${i % 100}`,
  execution_path: "",
  attempt: Math.floor(i / 100) + 1,
  kind: "step",
  status: "completed",
  agent_slug: "",
  model: "",
  started_at: "2026-09-09T08:00:00Z",
  ended_at: "2026-09-09T08:00:01Z",
  error: "",
  output_bytes: 1000,
})

describe("run detail execution paging", () => {
  it("N13 retains paging during refresh and aborts refresh on unmount", async () => {
    h.fetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ rows: [row(0)], next_cursor: "more" }),
    })
    const { result, unmount } = renderHook(() => useRunExecutions("ws", "run", false))
    await waitFor(() => expect(result.current.loadMore).toBeDefined())
    let signal!: AbortSignal
    h.fetch.mockImplementation((_url: string, options: { signal: AbortSignal }) => {
      signal = options.signal
      return new Promise(() => {})
    })
    act(() => result.current.refresh())
    expect(result.current.loadMore).toBeDefined()
    unmount()
    expect(signal.aborted).toBe(true)
  })
  it("loads one page initially for 100 steps / 1000 attempts and more only on demand", async () => {
    const rows = Array.from({ length: 1000 }, (_, i) => row(i))
    let bytes = 0
    h.fetch.mockImplementation(async (url: string) => {
      const start = Number(new URL(url, "http://test").searchParams.get("after") ?? 0)
      const page = {
        rows: rows.slice(start, start + 100),
        next_cursor: start + 100 < rows.length ? String(start + 100) : null,
      }
      bytes += JSON.stringify(page).length
      return { ok: true, json: async () => page }
    })
    const { result } = renderHook(() => useRunExecutions("ws", "large-run", false))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(h.fetch).toHaveBeenCalledTimes(1)
    expect(result.current.rows).toHaveLength(100)
    expect(result.current.byStep.size).toBe(100)
    expect(result.current.truncated).toBe(true)
    const firstPageBytes = bytes
    const previousInitialBytes = Array.from(
      { length: 5 },
      (_, i) =>
        JSON.stringify({
          rows: rows.slice(i * 100, (i + 1) * 100),
          next_cursor: String((i + 1) * 100),
        }).length,
    ).reduce((a, b) => a + b, 0)
    expect(firstPageBytes).toBeLessThan(previousInitialBytes / 4)
    act(() => {
      result.current.loadMore?.()
      result.current.loadMore?.()
    })
    await waitFor(() => expect(result.current.rows).toHaveLength(200))
    expect(h.fetch).toHaveBeenCalledTimes(3) // refresh the two visible pages, once
    expect(result.current.byStep.get("step0")?.attempts).toBe(2)
    expect(result.current.truncated).toBe(true)
  })

  it("ignores a late response from the previous run", async () => {
    let resolveOld!: (value: unknown) => void
    h.fetch.mockImplementation((url: string) =>
      url.includes("/old/")
        ? new Promise((resolve) => {
            resolveOld = resolve
          })
        : Promise.resolve({ ok: true, json: async () => ({ rows: [row(42)], next_cursor: null }) }),
    )
    const { result, rerender } = renderHook(({ run }) => useRunExecutions("ws", run, false), {
      initialProps: { run: "old" },
    })
    rerender({ run: "new" })
    await waitFor(() => expect(result.current.rows?.[0].id).toBe("e42"))
    await act(async () => {
      resolveOld({ ok: true, json: async () => ({ rows: [row(1)], next_cursor: null }) })
    })
    expect(result.current.rows?.[0].id).toBe("e42")
  })
})
