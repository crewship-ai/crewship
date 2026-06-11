import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { useFeedbackStore } from "@/stores/feedback-store"

// res.ok / res.status are the only fields the store reads.
const ok = () => ({ ok: true, status: 200 })
const fail = (status: number) => ({ ok: false, status })

// Flush enough microtask turns for chained ops (prev.then(op).then(cleanup))
// to start/finish without relying on timers.
const flush = () => new Promise<void>((r) => setTimeout(r, 0))

let mockFetch: ReturnType<typeof vi.fn>
let warnSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  mockFetch = vi.fn()
  vi.stubGlobal("fetch", mockFetch)
  warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {})
  useFeedbackStore.setState({ userId: null, byTurn: {} })
})

afterEach(() => {
  warnSpy.mockRestore()
  vi.unstubAllGlobals()
})

describe("setUser", () => {
  it("binds the user and clears byTurn on user switch", () => {
    useFeedbackStore.setState({ userId: "u_old", byTurn: { t1: { helpful: true } } })
    useFeedbackStore.getState().setUser("u_new")
    expect(useFeedbackStore.getState().userId).toBe("u_new")
    expect(useFeedbackStore.getState().byTurn).toEqual({})
  })

  it("clears byTurn even on null → user transition (pre-auth state)", () => {
    useFeedbackStore.setState({ userId: null, byTurn: { t1: { helpful: true } } })
    useFeedbackStore.getState().setUser("u1")
    expect(useFeedbackStore.getState().byTurn).toEqual({})
  })

  it("is a no-op when the same user is set again (votes preserved)", () => {
    useFeedbackStore.setState({ userId: "u1", byTurn: { t1: { helpful: true } } })
    useFeedbackStore.getState().setUser("u1")
    expect(useFeedbackStore.getState().byTurn).toEqual({ t1: { helpful: true } })
  })
})

describe("submit", () => {
  it("refuses and warns when no user is bound", async () => {
    await useFeedbackStore.getState().submit("t1", "helpful")
    expect(mockFetch).not.toHaveBeenCalled()
    expect(useFeedbackStore.getState().byTurn).toEqual({})
    expect(warnSpy).toHaveBeenCalledWith(
      "[feedback] submit called before setUser; ignoring",
    )
  })

  it("POSTs to /api/v1/feedback with the full body and keeps the optimistic flip on success", async () => {
    mockFetch.mockResolvedValue(ok())
    useFeedbackStore.getState().setUser("u1")
    await useFeedbackStore.getState().submit("t1", "helpful", {
      chatId: "c9",
      traceId: "tr3",
      reason: "great answer",
    })

    expect(mockFetch).toHaveBeenCalledTimes(1)
    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe("/api/v1/feedback")
    expect(init.method).toBe("POST")
    expect(init.credentials).toBe("include")
    expect(init.headers).toEqual({ "Content-Type": "application/json" })
    expect(JSON.parse(init.body)).toEqual({
      message_id: "t1",
      chat_id: "c9",
      trace_id: "tr3",
      signal: "helpful",
      reason: "great answer",
    })
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({ helpful: true })
  })

  it("omits optional fields from the body when opts not given", async () => {
    mockFetch.mockResolvedValue(ok())
    useFeedbackStore.getState().setUser("u1")
    await useFeedbackStore.getState().submit("t1", "regenerate")
    const body = JSON.parse(mockFetch.mock.calls[0][1].body)
    expect(body).toEqual({ message_id: "t1", signal: "regenerate" })
  })

  it("rolls back the optimistic flip on a non-2xx response", async () => {
    mockFetch.mockResolvedValue(fail(400))
    useFeedbackStore.getState().setUser("u1")
    await useFeedbackStore.getState().submit("t1", "inaccurate")
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({})
    expect(warnSpy).toHaveBeenCalledWith(
      "[feedback] submit returned 400; rolling back",
    )
  })

  it("rolls back on a network/transport rejection", async () => {
    mockFetch.mockRejectedValue(new TypeError("network down"))
    useFeedbackStore.getState().setUser("u1")
    await useFeedbackStore.getState().submit("t1", "unsafe")
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({})
  })

  it("rollback only removes the failed signal, keeping sibling signals", async () => {
    useFeedbackStore.setState({ userId: "u1", byTurn: { t1: { helpful: true } } })
    mockFetch.mockResolvedValue(fail(500))
    await useFeedbackStore.getState().submit("t1", "edit")
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({ helpful: true })
  })

  it("does not write or fetch when the user switched between click and execution", async () => {
    useFeedbackStore.getState().setUser("u1")
    const p = useFeedbackStore.getState().submit("t1", "helpful")
    // Synchronous switch before the chained op's microtask runs.
    useFeedbackStore.getState().setUser("u2")
    await p
    expect(mockFetch).not.toHaveBeenCalled()
    expect(useFeedbackStore.getState().byTurn).toEqual({})
  })
})

describe("reset", () => {
  it("is a no-op without a bound user", async () => {
    await useFeedbackStore.getState().reset("t1", "helpful")
    expect(mockFetch).not.toHaveBeenCalled()
  })

  it("DELETEs with encoded query params and clears local state on success", async () => {
    mockFetch.mockResolvedValue(ok())
    useFeedbackStore.setState({
      userId: "u1",
      byTurn: { "t 1/x": { helpful: true, edit: true } },
    })
    await useFeedbackStore.getState().reset("t 1/x", "helpful")

    const [url, init] = mockFetch.mock.calls[0]
    expect(url).toBe(
      `/api/v1/feedback?message_id=${encodeURIComponent("t 1/x")}&signal=helpful`,
    )
    expect(init).toEqual({ method: "DELETE", credentials: "include" })
    // helpful cleared, sibling edit kept
    expect(useFeedbackStore.getState().byTurn["t 1/x"]).toEqual({ edit: true })
  })

  it("keeps local state when the DELETE returns non-2xx", async () => {
    mockFetch.mockResolvedValue(fail(404))
    useFeedbackStore.setState({ userId: "u1", byTurn: { t1: { helpful: true } } })
    await useFeedbackStore.getState().reset("t1", "helpful")
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({ helpful: true })
    expect(warnSpy).toHaveBeenCalledWith(
      "[feedback] reset returned 404; keeping local state",
    )
  })

  it("keeps local state on a network rejection", async () => {
    mockFetch.mockRejectedValue(new TypeError("offline"))
    useFeedbackStore.setState({ userId: "u1", byTurn: { t1: { helpful: true } } })
    await useFeedbackStore.getState().reset("t1", "helpful")
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({ helpful: true })
  })

  it("skips the DELETE when the user switched before execution", async () => {
    useFeedbackStore.setState({ userId: "u1", byTurn: { t1: { helpful: true } } })
    const p = useFeedbackStore.getState().reset("t1", "helpful")
    useFeedbackStore.getState().setUser("u2")
    await p
    expect(mockFetch).not.toHaveBeenCalled()
  })
})

describe("per-(turn, signal) sequencing", () => {
  it("holds a reset's DELETE until the in-flight POST for the same key resolves", async () => {
    useFeedbackStore.getState().setUser("u1")
    const methods: string[] = []
    let resolvePost!: (v: unknown) => void
    mockFetch.mockImplementation((_url: string, init?: RequestInit) => {
      methods.push(init?.method ?? "GET")
      if (init?.method === "POST") {
        return new Promise((res) => {
          resolvePost = res
        })
      }
      return Promise.resolve(ok())
    })

    const p1 = useFeedbackStore.getState().submit("t1", "helpful")
    const p2 = useFeedbackStore.getState().reset("t1", "helpful")

    await flush()
    // POST is in flight; the DELETE must not have been issued yet.
    expect(methods).toEqual(["POST"])
    // Optimistic flip already applied.
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({ helpful: true })

    resolvePost(ok())
    await p1
    await p2
    expect(methods).toEqual(["POST", "DELETE"])
    // reset succeeded → cleared.
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({})
  })

  it("does not block requests for a different signal on the same turn", async () => {
    useFeedbackStore.getState().setUser("u1")
    const bodies: string[] = []
    let resolveFirst!: (v: unknown) => void
    mockFetch.mockImplementation((_url: string, init?: RequestInit) => {
      const signal = JSON.parse(String(init?.body)).signal
      bodies.push(signal)
      if (signal === "helpful") {
        return new Promise((res) => {
          resolveFirst = res
        })
      }
      return Promise.resolve(ok())
    })

    const p1 = useFeedbackStore.getState().submit("t1", "helpful")
    const p2 = useFeedbackStore.getState().submit("t1", "not_helpful")

    await flush()
    // Both POSTs issued — different (turn, signal) keys are independent.
    expect(bodies.sort()).toEqual(["helpful", "not_helpful"])

    resolveFirst(ok())
    await p1
    await p2
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({
      helpful: true,
      not_helpful: true,
    })
  })

  it("a queued submit still runs after a prior op for the same key rejects upstream", async () => {
    useFeedbackStore.getState().setUser("u1")
    mockFetch
      .mockResolvedValueOnce(fail(500))
      .mockResolvedValueOnce(ok())

    await useFeedbackStore.getState().submit("t1", "helpful") // rolled back
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({})
    await useFeedbackStore.getState().submit("t1", "helpful") // retried, ok
    expect(useFeedbackStore.getState().byTurn.t1).toEqual({ helpful: true })
    expect(mockFetch).toHaveBeenCalledTimes(2)
  })
})
