/**
 * The work ledger's data layer
 * (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
 *
 * The load-bearing assertions here are the ones about identity and about
 * evidence. A run stream keyed on anything but run_id merges two concurrent
 * runs into one — the bug the existing agentSlug-filtered log panels have, and
 * the one §9 names in a single sentence. And a view that trusts a push as the
 * last word walks backwards the first time one arrives twice, late, or not at
 * all; these tests pin that a duplicate changes nothing, that an out-of-order
 * snapshot cannot un-finish finished work, and that a reconnect goes and asks.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import React from "react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { renderHook, waitFor, act } from "@testing-library/react"

const h = vi.hoisted(() => ({
  apiFetch: vi.fn(),
  subs: new Map<string, Array<(event: unknown) => void>>(),
}))

vi.mock("@/lib/api-fetch", () => ({ apiFetch: h.apiFetch }))
vi.mock("@/hooks/use-realtime", () => ({
  // Last subscriber per type wins. The real hook is called with a fresh
  // closure on every render, so keeping them all would fire one emit several
  // times and hide a missing subscription behind an accidental double call.
  useRealtimeEventSafe: (type: string, cb: (event: unknown) => void) => {
    h.subs.set(type, [cb])
  },
}))

import {
  WORK_POLL_MS,
  attemptCount,
  isNewerWorkRow,
  ledgerEvents,
  mergeWorkAttempts,
  mergeWorkDetail,
  mergeWorkEvents,
  queuedReason,
  replayAvailability,
  totalCostUSD,
  useWorkItem,
  useWorkItems,
  workItemKeys,
  workOutcome,
  workQueryParams,
  workRunStreams,
  type WorkAttempt,
  type WorkEvent,
  type WorkItem,
  type WorkItemDetail,
} from "@/hooks/use-work-items"

// ── fixtures ────────────────────────────────────────────────────────────────

function workItem(over: Partial<WorkItem> = {}): WorkItem {
  return {
    id: "cwork0000000000000001",
    workspace_id: "ws-1",
    source: "webhook",
    source_ref: "cdlv0000000000000001",
    domain_kind: "pipeline",
    domain_id: "pipe-1",
    agent_id: "agent-jamie",
    crew_id: "crew-1",
    session_id: "",
    class: "background",
    authorized_by_user_id: "user-1",
    input_sha256: "a".repeat(64),
    target_revision: "rev-7",
    state: "succeeded",
    state_reason: "",
    generation: 2,
    attempts: 2,
    priority: 0,
    eligible_at: "2026-09-10T09:00:00.000000000Z",
    deadline_at: null,
    replay_of: null,
    replay_reason: "",
    created_at: "2026-09-10T09:00:00.000000000Z",
    updated_at: "2026-09-10T09:05:00.000000000Z",
    terminal_at: "2026-09-10T09:05:00.000000000Z",
    ...over,
  }
}

function attempt(over: Partial<WorkAttempt> = {}): WorkAttempt {
  return {
    run_id: "run-1",
    attempt: 1,
    generation: 1,
    lease_owner: "worker-a",
    lease_expires_at: "2026-09-10T09:01:00.000000000Z",
    heartbeat_at: "2026-09-10T09:00:50.000000000Z",
    runtime_locator: "container://abc",
    started_at: "2026-09-10T09:00:10.000000000Z",
    start_reason: "claim",
    ended_at: "2026-09-10T09:01:00.000000000Z",
    end_reason: "exit",
    exit_evidence: "exit=1",
    cost_usd: 0.25,
    ...over,
  }
}

function event(over: Partial<WorkEvent> = {}): WorkEvent {
  return {
    seq: 1,
    at: "2026-09-10T09:00:00.000000000Z",
    from_state: "queued",
    to_state: "starting",
    run_id: "run-1",
    generation: 1,
    reason: "",
    ...over,
  }
}

function detail(over: Partial<WorkItemDetail> = {}): WorkItemDetail {
  const { attempts, events, ...rest } = over
  const base = workItem(rest as Partial<WorkItem>)
  const { attempts: _count, ...scalars } = base
  return { ...scalars, attempts: attempts ?? [attempt()], events: events ?? [event()] }
}

function okJSON(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body, text: async () => JSON.stringify(body) } as unknown as Response
}

function newQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } },
  })
}

function wrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  }
}

function emit(type: string) {
  const subs = h.subs.get(type)
  if (!subs || subs.length === 0) throw new Error(`nothing is subscribed to "${type}"`)
  for (const cb of subs) cb({ type, payload: {}, timestamp: new Date() })
}

function urlsFetched(): string[] {
  return h.apiFetch.mock.calls.map((c) => String(c[0]))
}

// ── query keys and cache separation ─────────────────────────────────────────

describe("query keys", () => {
  it("uses [resource, workspaceId, params] and puts the workspace IN the key", () => {
    expect(workItemKeys.all("ws-1")).toEqual(["work-items", "ws-1"])
    expect(workItemKeys.list("ws-1")).toEqual(["work-items", "ws-1", { view: "list" }])
    expect(workItemKeys.detail("ws-1", "w-9")).toEqual(["work-items", "ws-1", { id: "w-9" }])
  })

  it("keys two workspaces apart, so one can never serve the other's ledger", () => {
    expect(workItemKeys.list("ws-1", { state: "running" }))
      .not.toEqual(workItemKeys.list("ws-2", { state: "running" }))
    expect(workItemKeys.detail("ws-1", "w-9")).not.toEqual(workItemKeys.detail("ws-2", "w-9"))
  })

  it("omits an absent filter instead of sending it empty, so {} and {state:null} share one entry", () => {
    expect(workQueryParams({})).toEqual({})
    expect(workQueryParams({ state: null, class: null, source: null, agentId: null })).toEqual({})
    expect(workItemKeys.list("ws-1", {})).toEqual(workItemKeys.list("ws-1", { state: null }))
    expect(workQueryParams({ state: "queued", agentId: "a-1" })).toEqual({ state: "queued", agent_id: "a-1" })
  })
})

describe("the list hook", () => {
  let qc: QueryClient

  beforeEach(() => {
    h.apiFetch.mockReset()
    h.subs.clear()
    qc = newQueryClient()
  })
  afterEach(() => qc.clear())

  it("fires nothing without a workspace", () => {
    renderHook(() => useWorkItems(null), { wrapper: wrapper(qc) })
    expect(h.apiFetch).not.toHaveBeenCalled()
  })

  it("scopes the request and the cache to one workspace", async () => {
    h.apiFetch.mockImplementation((url: string) =>
      Promise.resolve(okJSON({
        items: [workItem({ id: url.includes("ws-2") ? "w-two" : "w-one", workspace_id: url.includes("ws-2") ? "ws-2" : "ws-1" })],
        next_cursor: null,
      })),
    )

    const one = renderHook(() => useWorkItems("ws-1"), { wrapper: wrapper(qc) })
    const two = renderHook(() => useWorkItems("ws-2"), { wrapper: wrapper(qc) })

    await waitFor(() => expect(one.result.current.items).toHaveLength(1))
    await waitFor(() => expect(two.result.current.items).toHaveLength(1))

    expect(urlsFetched()).toEqual([
      "/api/v1/workspaces/ws-1/work-items",
      "/api/v1/workspaces/ws-2/work-items",
    ])
    expect(one.result.current.items[0].id).toBe("w-one")
    expect(two.result.current.items[0].id).toBe("w-two")
    // Two entries, not one shared one.
    expect(qc.getQueryData(workItemKeys.list("ws-1"))).not.toBe(qc.getQueryData(workItemKeys.list("ws-2")))
  })

  it("puts a filter on the URL and gives it its own cache entry", async () => {
    h.apiFetch.mockResolvedValue(okJSON({ items: [], next_cursor: null }))
    renderHook(() => useWorkItems("ws-1", { state: "needs_reconciliation" }), { wrapper: wrapper(qc) })
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalled())
    expect(urlsFetched()[0]).toBe("/api/v1/workspaces/ws-1/work-items?state=needs_reconciliation")
  })

  it("refetches the authoritative snapshot when the socket comes back", async () => {
    // §9: after a reconnect the client fetches an authoritative snapshot. Every
    // push during the gap is gone, so "we heard nothing" is not evidence that
    // nothing happened.
    h.apiFetch.mockResolvedValue(okJSON({ items: [workItem()], next_cursor: null }))
    const { result } = renderHook(() => useWorkItems("ws-1"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))
    expect(h.apiFetch).toHaveBeenCalledTimes(1)

    await act(async () => { emit("realtime.reconnected") })

    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledTimes(2))
  })

  it("treats a run push as a hint to re-read, never as the answer itself", async () => {
    h.apiFetch.mockResolvedValue(okJSON({ items: [workItem()], next_cursor: null }))
    const { result } = renderHook(() => useWorkItems("ws-1"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.items).toHaveLength(1))

    await act(async () => { emit("run.completed") })

    // The server row is what changed the view — the push only asked for it.
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledTimes(2))
    expect(urlsFetched()[1]).toBe("/api/v1/workspaces/ws-1/work-items")
  })

  it("keeps a poll backstop, because push is not the only evidence of completion", () => {
    expect(WORK_POLL_MS).toBeGreaterThan(0)
  })
})

describe("the detail hook", () => {
  let qc: QueryClient

  beforeEach(() => {
    h.apiFetch.mockReset()
    h.subs.clear()
    qc = newQueryClient()
  })
  afterEach(() => qc.clear())

  it("reads one item and keys it by workspace and id", async () => {
    h.apiFetch.mockResolvedValue(okJSON(detail()))
    const { result } = renderHook(() => useWorkItem("ws-1", "cwork0000000000000001"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.item).toBeTruthy())
    expect(urlsFetched()[0]).toBe("/api/v1/workspaces/ws-1/work-items/cwork0000000000000001")
    expect(qc.getQueryData(workItemKeys.detail("ws-1", "cwork0000000000000001"))).toBeTruthy()
    expect(qc.getQueryData(workItemKeys.detail("ws-2", "cwork0000000000000001"))).toBeUndefined()
  })

  it("separates a 404 from a failure — the ledger not having it is an answer", async () => {
    h.apiFetch.mockResolvedValue({
      ok: false, status: 404, json: async () => ({ error: "Work item not found" }),
    } as unknown as Response)
    const { result } = renderHook(() => useWorkItem("ws-1", "missing"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.notFound).toBe(true))
    expect(result.current.error).toBeNull()
  })

  it("a second read that lost a race does not walk a finished item backwards", async () => {
    const finished = detail({ state: "succeeded", generation: 3, events: [event({ seq: 1 }), event({ seq: 2, to_state: "succeeded" })] })
    const stale = detail({ state: "running", generation: 2, events: [event({ seq: 1 })] })
    h.apiFetch
      .mockResolvedValueOnce(okJSON(finished))
      .mockResolvedValueOnce(okJSON(stale))

    const { result } = renderHook(() => useWorkItem("ws-1", "cwork0000000000000001"), { wrapper: wrapper(qc) })
    await waitFor(() => expect(result.current.item?.state).toBe("succeeded"))

    await act(async () => { emit("realtime.reconnected") })
    await waitFor(() => expect(h.apiFetch).toHaveBeenCalledTimes(2))

    // The stale snapshot contributed nothing it should not have.
    expect(result.current.item?.state).toBe("succeeded")
    expect(result.current.item?.generation).toBe(3)
    expect(result.current.item?.events.map((e) => e.seq)).toEqual([1, 2])
  })
})

// ── duplicate and out-of-order tolerance ────────────────────────────────────

describe("merging what arrives", () => {
  it("keeps one row per seq no matter how many times it arrives", () => {
    const merged = mergeWorkEvents(
      [event({ seq: 1 }), event({ seq: 2 })],
      [event({ seq: 2 }), event({ seq: 2 }), event({ seq: 1 })],
    )
    expect(merged.map((e) => e.seq)).toEqual([1, 2])
  })

  it("orders by seq whatever order it was handed", () => {
    const merged = mergeWorkEvents([], [event({ seq: 5 }), event({ seq: 1 }), event({ seq: 3 })])
    expect(merged.map((e) => e.seq)).toEqual([1, 3, 5])
  })

  it("a late snapshot contributes its events without owning the item row", () => {
    const held = detail({ state: "succeeded", generation: 4, events: [event({ seq: 2 })] })
    const late = detail({ state: "running", generation: 3, events: [event({ seq: 1 })] })
    const merged = mergeWorkDetail(held, late)
    expect(merged.state).toBe("succeeded")
    expect(merged.generation).toBe(4)
    expect(merged.events.map((e) => e.seq)).toEqual([1, 2])
  })

  it("a genuinely newer snapshot does replace the row", () => {
    const held = detail({ state: "running", generation: 3, events: [event({ seq: 1 })] })
    const fresh = detail({ state: "failed", generation: 4, events: [event({ seq: 2, to_state: "failed" })] })
    const merged = mergeWorkDetail(held, fresh)
    expect(merged.state).toBe("failed")
    expect(merged.events.map((e) => e.seq)).toEqual([1, 2])
  })

  it("a duplicate of what we already hold changes nothing observable", () => {
    const held = detail()
    const merged = mergeWorkDetail(held, detail())
    expect(merged.state).toBe(held.state)
    expect(merged.events).toEqual(held.events)
    expect(merged.attempts).toEqual(held.attempts)
  })

  it("never merges two work items, however alike", () => {
    const a = detail({ id: "w-a" })
    const b = detail({ id: "w-b", events: [event({ seq: 9 })] })
    expect(mergeWorkDetail(a, b).id).toBe("w-b")
    expect(mergeWorkDetail(a, b).events.map((e) => e.seq)).toEqual([9])
  })

  it("prefers a terminal row at equal generation — terminal history is not rewritten", () => {
    expect(isNewerWorkRow(
      { generation: 2, updated_at: "2026-09-10T09:00:00Z", state: "succeeded" },
      { generation: 2, updated_at: "2026-09-10T09:09:00Z", state: "running" },
    )).toBe(true)
  })

  it("merges attempts by run_id, the newer generation winning", () => {
    const merged = mergeWorkAttempts(
      [attempt({ run_id: "run-1", generation: 1, ended_at: null })],
      [attempt({ run_id: "run-1", generation: 2 }), attempt({ run_id: "run-2", attempt: 2 })],
    )
    expect(merged).toHaveLength(2)
    expect(merged.find((a) => a.run_id === "run-1")?.generation).toBe(2)
  })
})

// ── the §9 rule about run streams ───────────────────────────────────────────

describe("run streams", () => {
  it("does not merge two runs that share an agent and a session", () => {
    // This is the exact shape the rule exists for: one agent, one conversation,
    // two attempts. Anything keyed on agent slug or ChatID sees one stream.
    const d = detail({
      agent_id: "agent-jamie",
      session_id: "chat-42",
      attempts: [
        attempt({ run_id: "run-1", attempt: 1, exit_evidence: "exit=1" }),
        attempt({ run_id: "run-2", attempt: 2, exit_evidence: "exit=0" }),
      ],
      events: [
        event({ seq: 1, run_id: "run-1" }),
        event({ seq: 2, run_id: "run-2" }),
        event({ seq: 3, run_id: "run-1", to_state: "retry_wait" }),
      ],
    })
    const streams = workRunStreams(d)
    expect(streams.map((s) => s.runId)).toEqual(["run-1", "run-2"])
    expect(streams[0].events.map((e) => e.seq)).toEqual([1, 3])
    expect(streams[1].events.map((e) => e.seq)).toEqual([2])
  })

  it("gives an event whose run has no attempt row its own stream rather than folding it into another", () => {
    const d = detail({
      attempts: [attempt({ run_id: "run-1" })],
      events: [event({ seq: 1, run_id: "run-1" }), event({ seq: 2, run_id: "run-orphan" })],
    })
    expect(workRunStreams(d).map((s) => s.runId)).toEqual(["run-1", "run-orphan"])
  })

  it("keeps item-level events out of every run stream", () => {
    const d = detail({
      attempts: [attempt({ run_id: "run-1" })],
      events: [event({ seq: 1, run_id: "" , reason: "accepted" }), event({ seq: 2, run_id: "run-1" })],
    })
    expect(workRunStreams(d).flatMap((s) => s.events).map((e) => e.seq)).toEqual([2])
    expect(ledgerEvents(d).map((e) => e.seq)).toEqual([1])
  })
})

// ── state semantics ─────────────────────────────────────────────────────────

describe("state semantics", () => {
  it("needs_reconciliation is neither a success nor a failure", () => {
    expect(workOutcome("needs_reconciliation")).toBe("unresolved")
    expect(workOutcome("succeeded")).toBe("succeeded")
    expect(workOutcome("failed")).toBe("failed")
    expect(workOutcome("expired")).toBe("failed")
    expect(workOutcome("cancelled")).toBe("cancelled")
  })

  it("says why a queued item is not running", () => {
    expect(queuedReason({ state: "queued", state_reason: "", eligible_at: "2026-09-10T08:00:00Z" }, Date.parse("2026-09-10T09:00:00Z")))
      .toBe("Waiting for capacity.")
    expect(queuedReason({ state: "retry_wait", state_reason: "", eligible_at: "2026-09-10T10:00:00Z" }, Date.parse("2026-09-10T09:00:00Z")))
      .toBe("Backing off after a repeatable failure.")
    expect(queuedReason({ state: "queued", state_reason: "chat reservation exhausted", eligible_at: "2026-09-10T08:00:00Z" }))
      .toBe("chat reservation exhausted")
  })

  it("prefers the server's attempt counter over the length of the array", () => {
    // The summary carries `attempt_count`; the detail carries both that and the
    // `attempts` array. They are two names on purpose — an earlier shape used
    // one key for both, and encoding/json resolved the collision in favour of
    // the shallower field, so a client expecting a number silently got an array.
    expect(attemptCount(workItem({ attempt_count: 3 }))).toBe(3)

    // The array is the attempt ROWS, which can lag the counter while a claim is
    // in flight, so the counter wins when both are present.
    expect(
      attemptCount(
        detail({
          attempt_count: 3,
          attempts: [attempt({ run_id: "a" }), attempt({ run_id: "b" })],
        }),
      ),
    ).toBe(3)
  })

  it("sums cost across attempts, because a retry costs again", () => {
    expect(totalCostUSD([attempt({ cost_usd: 0.25 }), attempt({ run_id: "r2", cost_usd: 0.5 })])).toBeCloseTo(0.75)
  })
})

// ── replay availability ─────────────────────────────────────────────────────

describe("replay availability", () => {
  const noDelivery = { delivery: null, loading: false, notFound: false }

  it("is unavailable while the original is still running, and says so", () => {
    const a = replayAvailability(workItem({ state: "running" }), noDelivery)
    expect(a.state).toBe("unavailable")
    expect(a.reason).toContain("only available once the original has finished")
  })

  it("is available for a finished non-webhook item with nothing to retain", () => {
    expect(replayAvailability(workItem({ source: "chat", source_ref: "" }), noDelivery).state).toBe("available")
  })

  it("is unavailable with a stated reason once the payload has passed retention", () => {
    const a = replayAvailability(workItem(), {
      loading: false,
      notFound: false,
      delivery: {
        id: "cdlv0000000000000001", workspace_id: "ws-1", endpoint_id: "e-1", endpoint_kind: "routine",
        profile: "github", source_delivery_id: "gh-1", event_type: "push", event_action: "",
        signing_key_id: "", body_sha256: "b".repeat(64), body_bytes: 120,
        filter_decision: "accepted", filter_reason: "", target_revision: "rev-7", work_id: "w-1",
        received_at: "2026-08-01T09:00:00Z", dedup_expires_at: "2026-08-31T09:00:00Z",
        raw_body_available: false, raw_body_expires_at: "2026-09-01T09:00:00Z",
      },
    })
    expect(a.state).toBe("unavailable")
    expect(a.reason).toContain("passed its retention window")
    expect(a.reason).toContain("2026-09-01T09:00:00Z")
    expect(a.reason).toContain("Ask the sender to deliver it again.")
  })

  it("distinguishes a dropped payload from a delivery that is not in the ledger at all", () => {
    const a = replayAvailability(workItem(), { delivery: null, loading: false, notFound: true })
    expect(a.state).toBe("unavailable")
    expect(a.reason).toContain("no longer in the ledger")
  })

  it("does not offer the button while it still does not know", () => {
    expect(replayAvailability(workItem(), { delivery: null, loading: true, notFound: false }).state).toBe("checking")
  })
})
