import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"
import type { PaymasterRange } from "@/lib/types/paymaster"

import {
  useAgentSpend,
  useTopSpenders,
  useSubscriptionUsage,
} from "@/hooks/use-paymaster"

// Coverage companion for use-paymaster.test.ts — the base file covers
// useCrewSpend end-to-end plus the happy paths of the other three hooks.
// This file drives the 404 / 5xx / network-error / schema-mismatch arms
// of useAgentSpend, useTopSpenders and useSubscriptionUsage.

function okJSON(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: async () => body,
  } as unknown as Response
}

function notFound(): Response {
  return {
    ok: false,
    status: 404,
    json: async () => ({}),
  } as unknown as Response
}

function serverError(status = 500): Response {
  return {
    ok: false,
    status,
    json: async () => ({}),
  } as unknown as Response
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  global.fetch = fetchMock as unknown as typeof fetch
})

afterEach(() => {
  vi.restoreAllMocks()
})

// Stale-response guards: every hook stamps each request with a reqId and
// discards responses that land after the effect re-ran (range change).
// Two windows exist — after the fetch resolves, and after the JSON body
// parses — covered separately with hand-controlled promises.

async function flushMicrotasks() {
  for (let i = 0; i < 5; i++) await Promise.resolve()
}

/** Build a 200 response whose json() promise is resolved manually.
 *  `resolveJson` is assigned lazily when the hook actually calls json(),
 *  so access it via the returned object (not destructuring). */
function okDeferredJSON(): { response: Response; resolveJson: (body: unknown) => void } {
  const d = {
    resolveJson: (() => {
      throw new Error("json() was not called yet")
    }) as (body: unknown) => void,
    response: undefined as unknown as Response,
  }
  d.response = {
    ok: true,
    status: 200,
    json: () => new Promise<unknown>((res) => { d.resolveJson = res }),
  } as unknown as Response
  return d
}

type HookUnderTest = (range: PaymasterRange) => { loading: boolean; data: { rows: unknown[] } | null }

const staleGuardHooks: Array<[string, HookUnderTest]> = [
  ["useCrewSpend", (range) => useCrewSpend(range)],
  ["useAgentSpend", (range) => useAgentSpend("crew-1", range)],
  ["useTopSpenders", (range) => useTopSpenders(range)],
  ["useSubscriptionUsage", (range) => useSubscriptionUsage(range)],
]

// useCrewSpend is part of the guard matrix below but only its happy paths
// live in the base test file — import it here for the table.
import { useCrewSpend } from "@/hooks/use-paymaster"

describe.each(staleGuardHooks)("%s — stale response guards", (_name, useHook) => {
  it("discards a fetch response that lands after the range changed", async () => {
    const resolvers: Array<(r: Response) => void> = []
    fetchMock.mockImplementation(() => new Promise<Response>((res) => { resolvers.push(res) }))

    const { result, rerender } = renderHook(
      ({ range }: { range: PaymasterRange }) => useHook(range),
      { initialProps: { range: "24h" as PaymasterRange } },
    )
    rerender({ range: "7d" })
    await waitFor(() => expect(resolvers).toHaveLength(2))

    // The 24h response arrives late — the hook must throw it away and
    // keep waiting for the in-flight 7d request.
    resolvers[0](okJSON({ rows: [{ poisoned: true }] }))
    await flushMicrotasks()
    expect(result.current.loading).toBe(true)
    expect(result.current.data).toBeNull()

    resolvers[1](okJSON({ rows: [] }))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows).toEqual([])
  })

  it("discards a JSON body that finishes parsing after the range changed", async () => {
    const stale = okDeferredJSON()
    fetchMock
      .mockResolvedValueOnce(stale.response) // 24h — body parse will straggle
      .mockResolvedValueOnce(okJSON({ rows: [] })) // 7d — completes normally

    const { result, rerender } = renderHook(
      ({ range }: { range: PaymasterRange }) => useHook(range),
      { initialProps: { range: "24h" as PaymasterRange } },
    )
    // Let the 24h request pass its post-fetch guard and start awaiting json().
    await flushMicrotasks()

    rerender({ range: "7d" })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows).toEqual([])

    // The stale body finally parses — must NOT clobber the 7d state.
    stale.resolveJson({ rows: [{ poisoned: true }] })
    await flushMicrotasks()
    expect(result.current.data?.rows).toEqual([])
  })
})

describe("useAgentSpend — error arms", () => {
  it("404 → notConfigured", async () => {
    fetchMock.mockResolvedValueOnce(notFound())
    const { result } = renderHook(() => useAgentSpend("crew-1", "24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.notConfigured).toBe(true)
    expect(result.current.error).toBeNull()
    expect(result.current.data).toBeNull()
  })

  it("5xx → error string", async () => {
    fetchMock.mockResolvedValueOnce(serverError(502))
    const { result } = renderHook(() => useAgentSpend("crew-1", "24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("HTTP 502")
    expect(result.current.notConfigured).toBe(false)
  })

  it("rejected fetch → 'Network error'", async () => {
    fetchMock.mockRejectedValueOnce(new Error("offline"))
    const { result } = renderHook(() => useAgentSpend("crew-1", "24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("Network error")
  })

  it("schema mismatch degrades to empty rows without error", async () => {
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [{ agent_id: 42 }] }))
    const { result } = renderHook(() => useAgentSpend("crew-1", "24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows).toEqual([])
    expect(result.current.error).toBeNull()
  })

  it("parses a valid agent-spend payload", async () => {
    fetchMock.mockResolvedValueOnce(
      okJSON({
        rows: [{ agent_id: "a1", agent_name: "Viktor", cost_usd: 2.5, call_count: 7, total_tokens: 900 }],
        crew_id: "crew-1",
      }),
    )
    const { result } = renderHook(() => useAgentSpend("crew-1", "7d"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows[0].agent_id).toBe("a1")
    expect(result.current.data?.crew_id).toBe("crew-1")
  })

  it("refetches when reloadKey bumps", async () => {
    fetchMock.mockResolvedValue(okJSON({ rows: [] }))
    const { rerender } = renderHook(
      ({ reload }: { reload: number }) => useAgentSpend("crew-1", "24h", reload),
      { initialProps: { reload: 0 } },
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    rerender({ reload: 1 })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })
})

describe("useTopSpenders — error arms", () => {
  it("404 → notConfigured", async () => {
    fetchMock.mockResolvedValueOnce(notFound())
    const { result } = renderHook(() => useTopSpenders("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.notConfigured).toBe(true)
    expect(result.current.error).toBeNull()
  })

  it("5xx → error string", async () => {
    fetchMock.mockResolvedValueOnce(serverError(500))
    const { result } = renderHook(() => useTopSpenders("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("HTTP 500")
  })

  it("rejected fetch → 'Network error'", async () => {
    fetchMock.mockRejectedValueOnce(new Error("offline"))
    const { result } = renderHook(() => useTopSpenders("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("Network error")
  })

  it("schema mismatch degrades to empty rows without error", async () => {
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [{ cost_usd: "lots" }] }))
    const { result } = renderHook(() => useTopSpenders("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows).toEqual([])
    expect(result.current.error).toBeNull()
  })

  it("parses valid rows and surfaces them", async () => {
    fetchMock.mockResolvedValueOnce(
      okJSON({
        rows: [{ scope: "crew", cost_usd: 9.99, call_count: 12, total_tokens: 4321, crew_id: "c1" }],
      }),
    )
    const { result } = renderHook(() => useTopSpenders("7d", 5))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows).toHaveLength(1)
    expect(result.current.data?.rows[0].cost_usd).toBe(9.99)
  })
})

describe("useSubscriptionUsage — error arms", () => {
  it("5xx → error string", async () => {
    fetchMock.mockResolvedValueOnce(serverError(503))
    const { result } = renderHook(() => useSubscriptionUsage("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("HTTP 503")
    expect(result.current.notConfigured).toBe(false)
  })

  it("rejected fetch → 'Network error'", async () => {
    fetchMock.mockRejectedValueOnce(new Error("offline"))
    const { result } = renderHook(() => useSubscriptionUsage("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("Network error")
  })

  it("schema mismatch degrades to empty rows without error", async () => {
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [{ subscription_plan: 1 }] }))
    const { result } = renderHook(() => useSubscriptionUsage("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows).toEqual([])
    expect(result.current.error).toBeNull()
  })

  it("parses valid subscription rows", async () => {
    fetchMock.mockResolvedValueOnce(
      okJSON({
        rows: [
          {
            subscription_plan: "Anthropic Max",
            provider: "ANTHROPIC",
            call_count: 47,
            input_tokens: 1000,
            output_tokens: 2000,
            last_ts: "2026-06-11T10:00:00Z",
          },
        ],
      }),
    )
    const { result } = renderHook(() => useSubscriptionUsage("30d"))
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.data?.rows[0].subscription_plan).toBe("Anthropic Max")
  })

  it("refetches when reloadKey bumps", async () => {
    fetchMock.mockResolvedValue(okJSON({ rows: [] }))
    const { rerender } = renderHook(
      ({ reload }: { reload: number }) => useSubscriptionUsage("24h", reload),
      { initialProps: { reload: 0 } },
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    rerender({ reload: 1 })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })
})
