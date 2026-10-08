import React from "react"
import { act, cleanup, render, renderHook, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { ActiveRoutineRunsProvider, useActiveRoutineRuns, deriveActiveRoutineRuns, deriveRecentTerminalRuns } from "@/hooks/use-active-routine-runs"
import type { PipelineRun } from "@/hooks/use-pipeline-runs"

const mocks = vi.hoisted(() => ({ feed: vi.fn(), workspace: "ws-one", event: vi.fn(), refresh: vi.fn() }))
vi.mock("@/hooks/use-workspace", () => ({ useWorkspace: () => ({ workspaceId: mocks.workspace }) }))
vi.mock("@/hooks/use-pipeline-runs", () => ({ usePipelineRuns: mocks.feed }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEvent: mocks.event }))
function run(id: string, status: string, started_at = "2026-01-01T12:00:00Z", pipeline_slug = "daily"): PipelineRun {
  return { id, status, started_at, pipeline_slug, ended_at: "" } as PipelineRun
}
beforeEach(() => { mocks.workspace = "ws-one"; mocks.feed.mockReset(); mocks.refresh.mockReset(); mocks.event.mockReset() })
afterEach(cleanup)

it("provides an inert fallback outside the dashboard", () => {
  const { result } = renderHook(useActiveRoutineRuns)
  expect(result.current).toMatchObject({ runs: [], activeCount: 0, awaitingApproval: 0, recentRuns: [], recentDashboardRuns: [], loading: false, error: null })
  act(() => result.current.refresh())
  expect(mocks.feed).not.toHaveBeenCalled()
})

it("shares one workspace feed across consumers and refreshes on step events", () => {
  const rows = [run("running", "running"), run("waiting", "waiting"), ...Array.from({ length: 15 }, (_, index) => run(`done-${index}`, "completed"))]
  mocks.feed.mockReturnValue({ runs: rows, loading: false, error: null, refresh: mocks.refresh })
  function Consumer({ label }: { label: string }) {
    const value = useActiveRoutineRuns()
    return <output aria-label={label}>{value.activeCount}/{value.awaitingApproval}/{value.recentRuns.length}/{value.recentDashboardRuns.length}</output>
  }
  render(<ActiveRoutineRunsProvider><Consumer label="header" /><Consumer label="sidebar" /></ActiveRoutineRunsProvider>)
  expect(screen.getByLabelText("header")).toHaveTextContent("2/1/3/12")
  expect(screen.getByLabelText("sidebar")).toHaveTextContent("2/1/3/12")
  // One active feed and one history feed for every consumer, not one per consumer.
  expect(mocks.feed).toHaveBeenCalledTimes(2)
  expect(mocks.feed).toHaveBeenCalledWith("ws-one", "active", 200)
  expect(mocks.feed).toHaveBeenCalledWith("ws-one", "all", 200, { poll: false })
  const [event, handler] = mocks.event.mock.calls[0]
  expect(event).toBe("pipeline.step.started")
  act(() => handler({ run_id: "running" }))
  expect(mocks.refresh).toHaveBeenCalledTimes(1)
})

it("updates derived data and exposes the new workspace loading/error state", () => {
  mocks.feed.mockReturnValue({ runs: [run("old", "running")], loading: false, error: null, refresh: mocks.refresh })
  const { result, rerender } = renderHook(useActiveRoutineRuns, { wrapper: ActiveRoutineRunsProvider })
  expect(result.current.bySlug.get("daily")?.id).toBe("old")
  mocks.workspace = "ws-two"
  mocks.feed.mockReturnValue({ runs: [], loading: true, error: null, refresh: mocks.refresh })
  rerender()
  expect(mocks.feed).toHaveBeenCalledWith("ws-two", "active", 200)
  expect(mocks.feed).toHaveBeenLastCalledWith("ws-two", "all", 200, { poll: false })
  expect(result.current.activeCount).toBe(0)
  expect(result.current.loading).toBe(true)
  mocks.feed.mockReturnValue({ runs: [], loading: false, error: "offline", refresh: mocks.refresh })
  rerender()
  expect(result.current.error).toBe("offline")
})

// The history feed is the 200 newest rows. A run that started before them and
// is still going is not in it; the active feed is where it comes from.
it("keeps an older active run visible behind 200 newer results", () => {
  const history = Array.from({ length: 200 }, (_, index) => run(`done-${index}`, "completed", `2026-01-02T12:${String(index % 60).padStart(2, "0")}:00Z`, "ingest"))
  mocks.feed.mockImplementation((_ws: string, filter: string) => ({
    runs: filter === "active" ? [run("long", "running", "2026-01-01T00:00:00Z", "monthly-close")] : history,
    loading: false,
    error: null,
    refresh: mocks.refresh,
  }))
  const { result } = renderHook(useActiveRoutineRuns, { wrapper: ActiveRoutineRunsProvider })
  expect(result.current.activeCount).toBe(1)
  expect(result.current.bySlug.get("monthly-close")?.id).toBe("long")
  expect(result.current.recentDashboardRuns).toHaveLength(12)
  expect(mocks.feed).toHaveBeenCalledWith("ws-one", "active", 200)
})

it("handles missing timestamps and slugs without inventing a current routine", () => {
  const rows = [run("missing", "running", "", ""), run("invalid", "running", "bad-date"), run("valid", "running")]
  const derived = deriveActiveRoutineRuns(rows)
  expect(derived.runs.map(row => row.id)).toEqual(["valid", "missing", "invalid"])
  expect(derived.bySlug.has("")).toBe(false)
  expect(derived.bySlug.get("daily")?.id).toBe("valid")
  expect(rows[0].id).toBe("missing")
  expect(deriveRecentTerminalRuns([run("unknown", "failed", ""), run("latest", "completed")]).map(row => row.id)).toEqual(["latest", "unknown"])
})
