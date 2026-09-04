import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook } from "@testing-library/react"
import { useIssueBoardRealtime } from "@/hooks/use-issue-board-realtime"

// #2257 (PR #2310) registered the issues board's issue.* realtime
// subscriptions for the first time, but wired the debounced refetch to
// `onRefresh` — OrchestrationPageShell's missions/crews/agents/connections
// fetch — and never to the board's own separate `issues` state
// (useIssuesList's `fetchIssues`/refetch). So a live issue.created moved
// Graph/Timeline data but never actually repainted the issues board. These
// tests pin the fix: an issue.* event (and a reconnect) must reach
// `fetchIssues`, and `onRefresh` must keep firing too — dropping it would
// regress the mission-scoped views (Graph/Timeline/Activity) #2257 already
// covered.

const realtime = vi.hoisted(() => ({
  subs: new Map<string, (event: unknown) => void>(),
}))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (type: string, cb: (event: unknown) => void) => {
    realtime.subs.set(type, cb)
  },
}))

function emit(type: string, payload: Record<string, unknown>) {
  const cb = realtime.subs.get(type)
  if (!cb) throw new Error(`nothing is subscribed to "${type}"`)
  cb({ type, payload, timestamp: new Date() })
}

describe("useIssueBoardRealtime", () => {
  beforeEach(() => {
    realtime.subs.clear()
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it("an issue.created event for the visible crew triggers an issues fetch", () => {
    const fetchIssues = vi.fn()
    const onRefresh = vi.fn()
    renderHook(() => useIssueBoardRealtime({ filterCrewId: "crew-1", fetchIssues, onRefresh }))

    emit("issue.created", { id: "m1", crew_id: "crew-1" })
    vi.advanceTimersByTime(200)

    expect(fetchIssues).toHaveBeenCalledTimes(1)
  })

  it("still calls onRefresh too — Graph/Timeline/Activity must not regress", () => {
    const fetchIssues = vi.fn()
    const onRefresh = vi.fn()
    renderHook(() => useIssueBoardRealtime({ filterCrewId: null, fetchIssues, onRefresh }))

    emit("issue.status_changed", { id: "m1", crew_id: "crew-1" })
    vi.advanceTimersByTime(200)

    expect(fetchIssues).toHaveBeenCalledTimes(1)
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it("skips a refetch the active crew filter can prove is off-screen", () => {
    const fetchIssues = vi.fn()
    const onRefresh = vi.fn()
    renderHook(() => useIssueBoardRealtime({ filterCrewId: "crew-1", fetchIssues, onRefresh }))

    emit("issue.created", { id: "m1", crew_id: "crew-2" })
    vi.advanceTimersByTime(200)

    expect(fetchIssues).not.toHaveBeenCalled()
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("debounces a burst of events into one pair of requests", () => {
    const fetchIssues = vi.fn()
    const onRefresh = vi.fn()
    renderHook(() => useIssueBoardRealtime({ filterCrewId: null, fetchIssues, onRefresh }))

    for (let i = 0; i < 5; i++) {
      emit("issue.created", { id: `m${i}`, crew_id: "crew-1" })
    }
    vi.advanceTimersByTime(200)

    expect(fetchIssues).toHaveBeenCalledTimes(1)
    expect(onRefresh).toHaveBeenCalledTimes(1)
  })

  it("a dropped-socket reconnect refreshes issues, not debounced", () => {
    const fetchIssues = vi.fn()
    const onRefresh = vi.fn()
    renderHook(() => useIssueBoardRealtime({ filterCrewId: null, fetchIssues, onRefresh }))

    emit("realtime.reconnected", {})

    expect(fetchIssues).toHaveBeenCalledTimes(1)
    // Mission-scoped data has its own realtime.reconnected subscriber at
    // the shell level (OrchestrationPageShell) — this hook's job is only
    // the board's separate `issues` state.
    expect(onRefresh).not.toHaveBeenCalled()
  })

  it("ignores event types the issue board doesn't care about", () => {
    const fetchIssues = vi.fn()
    const onRefresh = vi.fn()
    renderHook(() => useIssueBoardRealtime({ filterCrewId: null, fetchIssues, onRefresh }))

    expect(realtime.subs.has("mission.updated")).toBe(false)
    expect(realtime.subs.has("task.updated")).toBe(false)
  })
})
