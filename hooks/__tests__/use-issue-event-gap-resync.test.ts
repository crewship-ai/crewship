import { describe, it, expect, vi, beforeEach } from "vitest"
import { renderHook } from "@testing-library/react"
import { useIssueEventGapResync } from "@/hooks/use-issue-event-gap-resync"

// F43 (PRD-ISSUES-AND-ROUTINES-2026.md §2.6/§14.2/§17, work package B11,
// #2368): ws.Hub.dispatch drops a frame silently under load — registering
// a type (F32) is not proof every frame of it arrived. These tests drive
// the hook with synthetic `issue.delivery.acked` frames (the one seq-
// carrying type today) and assert the ACTUAL resync fetch and its
// after_seq — not merely that the hook subscribed to something.

const realtime = vi.hoisted(() => ({
  subs: new Map<string, (event: unknown) => void>(),
}))
vi.mock("@/hooks/use-realtime", () => ({
  useRealtimeEvent: (type: string, cb: (event: unknown) => void) => {
    realtime.subs.set(type, cb)
  },
}))

const api = vi.hoisted(() => ({ apiFetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...args: unknown[]) => api.apiFetch(...args) }))

function emit(type: string, payload: Record<string, unknown>) {
  const cb = realtime.subs.get(type)
  if (!cb) throw new Error(`nothing is subscribed to "${type}"`)
  cb({ type, payload, timestamp: new Date() })
}

function jsonResponse(body: unknown): Response {
  return { ok: true, json: () => Promise.resolve(body) } as unknown as Response
}

describe("useIssueEventGapResync", () => {
  beforeEach(() => {
    realtime.subs.clear()
    api.apiFetch.mockReset()
  })

  it("subscribes to issue.delivery.acked", () => {
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync: vi.fn() }))
    expect(realtime.subs.has("issue.delivery.acked")).toBe(true)
  })

  it("does NOT resync on the first-ever seq observed for a mission", () => {
    const onResync = vi.fn()
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync }))

    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 5 })

    expect(api.apiFetch).not.toHaveBeenCalled()
    expect(onResync).not.toHaveBeenCalled()
  })

  it("does NOT resync on a consecutive seq", () => {
    const onResync = vi.fn()
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync }))

    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 5 })
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 6 })

    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it("a skipped seq calls the resync endpoint with the OLD lastSeq as after_seq", async () => {
    const onResync = vi.fn()
    api.apiFetch.mockResolvedValue(
      jsonResponse({ events: [{ seq: 6 }, { seq: 7 }], after_seq: 5, latest_seq: 7 }),
    )
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync }))

    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 5 })
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 8 }) // skipped 6, 7 — a gap

    expect(api.apiFetch).toHaveBeenCalledTimes(1)
    const url = api.apiFetch.mock.calls[0][0] as string
    expect(url).toBe("/api/v1/crews/crew-1/issues/ENG-1/events?after_seq=5")

    await new Promise((r) => setTimeout(r, 0))
    expect(onResync).toHaveBeenCalledWith("m1", "ENG-1", [{ seq: 6 }, { seq: 7 }])
  })

  it("does not resync when crewId is not yet known, but still tracks the seq", () => {
    const onResync = vi.fn()
    const { rerender } = renderHook(({ crewId }) => useIssueEventGapResync({ crewId, onResync }), {
      initialProps: { crewId: null as string | null },
    })

    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 5 })
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 8 }) // a gap, but no crewId yet

    expect(api.apiFetch).not.toHaveBeenCalled()

    // crewId shows up later; the NEXT consecutive frame (seq 9, following
    // the already-tracked 8) must not re-trigger a resync for the gap that
    // already passed.
    rerender({ crewId: "crew-1" })
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 9 })
    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it("tracks missions independently — a gap on one mission does not affect another", () => {
    const onResync = vi.fn()
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync }))

    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 5 })
    emit("issue.delivery.acked", { mission_id: "m2", identifier: "ENG-9", seq: 100 })
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 6 }) // consecutive for m1
    emit("issue.delivery.acked", { mission_id: "m2", identifier: "ENG-9", seq: 101 }) // consecutive for m2

    expect(api.apiFetch).not.toHaveBeenCalled()
  })

  it("ignores a payload with no mission_id or no seq", () => {
    const onResync = vi.fn()
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync }))

    emit("issue.delivery.acked", { identifier: "ENG-1" })
    emit("issue.delivery.acked", { mission_id: "m1" })

    expect(api.apiFetch).not.toHaveBeenCalled()
    expect(onResync).not.toHaveBeenCalled()
  })

  // Code review on #2377: a gap spanning more than one server page (the
  // endpoint caps a page at 500 rows) must not let the client mark itself
  // "caught up" at the server's true latest_seq while never having
  // fetched the middle chunk. This pins the fix: the resync PAGES until
  // it has actually seen every event up to latest_seq.
  it("pages until caught up when a gap spans more than one page", async () => {
    const onResync = vi.fn()
    api.apiFetch
      .mockResolvedValueOnce(jsonResponse({ events: [{ seq: 6 }, { seq: 7 }], after_seq: 5, latest_seq: 20 }))
      .mockResolvedValueOnce(jsonResponse({ events: [{ seq: 8 }, { seq: 9 }], after_seq: 7, latest_seq: 20 }))
      .mockResolvedValueOnce(jsonResponse({ events: [], after_seq: 9, latest_seq: 20 }))
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync }))

    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 5 })
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 9 })

    await new Promise((r) => setTimeout(r, 0))
    await new Promise((r) => setTimeout(r, 0))
    await new Promise((r) => setTimeout(r, 0))

    const urls = api.apiFetch.mock.calls.map((c) => c[0])
    expect(urls).toEqual([
      "/api/v1/crews/crew-1/issues/ENG-1/events?after_seq=5",
      "/api/v1/crews/crew-1/issues/ENG-1/events?after_seq=7",
      "/api/v1/crews/crew-1/issues/ENG-1/events?after_seq=9",
    ])
    expect(onResync).toHaveBeenCalledWith("m1", "ENG-1", [{ seq: 6 }, { seq: 7 }, { seq: 8 }, { seq: 9 }])

    // A SUBSEQUENT consecutive ack (seq 10, one past the true watermark
    // this resync converged on) must read as consecutive, not another gap.
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 10 })
    expect(api.apiFetch).toHaveBeenCalledTimes(3)
  })

  it("does not fan out a second concurrent resync for the same mission while one is in flight", async () => {
    const onResync = vi.fn()
    let resolveFirst: (v: Response) => void = () => {}
    api.apiFetch.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          resolveFirst = resolve
        }),
    )
    renderHook(() => useIssueEventGapResync({ crewId: "crew-1", onResync }))

    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 5 })
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 8 }) // triggers the pending fetch
    emit("issue.delivery.acked", { mission_id: "m1", identifier: "ENG-1", seq: 12 }) // another gap while in flight

    expect(api.apiFetch).toHaveBeenCalledTimes(1)

    resolveFirst(jsonResponse({ events: [{ seq: 6 }], after_seq: 5, latest_seq: 6 }))
    await new Promise((r) => setTimeout(r, 0))
    await new Promise((r) => setTimeout(r, 0))

    // Once the in-flight fetch clears, the mission is no longer guarded —
    // this only asserts no SECOND request fired while the first was
    // outstanding, not that a later, independent gap never resyncs again.
    expect(api.apiFetch).toHaveBeenCalledTimes(1)
  })
})
