import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { renderHook, waitFor } from "@testing-library/react"

import {
  useCrewSpend,
  useAgentSpend,
  useTopSpenders,
  useSubscriptionUsage,
} from "@/hooks/use-paymaster"

// Mock-fetch helper so each test can stub a single response. The hooks
// each fire one fetch on mount; bumping reloadKey or changing range
// fires another. Tests assert on the recorded URL + the resulting state.
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

beforeEach(() => {
  global.fetch = vi.fn()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe("useCrewSpend", () => {
  it("loads rows on mount and reports loading=false on success", async () => {
    const body = { rows: [{ crew_id: "c1", cost_usd: 1.5, call_count: 3, total_tokens: 100 }] }
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(okJSON(body))

    const { result } = renderHook(() => useCrewSpend("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBeNull()
    expect(result.current.notConfigured).toBe(false)
    expect(result.current.data?.rows).toHaveLength(1)
    expect(result.current.data?.rows[0].crew_id).toBe("c1")
  })

  it("surfaces 404 as notConfigured (paymaster not yet enabled)", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(notFound())

    const { result } = renderHook(() => useCrewSpend("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.notConfigured).toBe(true)
    expect(result.current.error).toBeNull()
  })

  it("surfaces 5xx as error", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(serverError(503))

    const { result } = renderHook(() => useCrewSpend("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("HTTP 503")
    expect(result.current.notConfigured).toBe(false)
  })

  it("surfaces network error from rejected fetch", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new Error("offline"))

    const { result } = renderHook(() => useCrewSpend("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBe("Network error")
  })

  it("falls back to empty rows when schema validation fails (graceful degrade)", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      okJSON({ rows: "definitely not an array" }),
    )

    const { result } = renderHook(() => useCrewSpend("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    // Schema mismatch returns empty rows + no error so the panel renders
    // "no data" rather than a hard failure.
    expect(result.current.data?.rows).toEqual([])
    expect(result.current.error).toBeNull()
  })

  it("does not fetch when enabled=false", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const { result } = renderHook(() => useCrewSpend("24h", false))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(fetchMock).not.toHaveBeenCalled()
  })

  it("refetches when range changes", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValue(okJSON({ rows: [] }))

    const { result, rerender } = renderHook(({ range }: { range: "24h" | "7d" }) => useCrewSpend(range), {
      initialProps: { range: "24h" },
    })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(fetchMock).toHaveBeenCalledTimes(1)

    rerender({ range: "7d" })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(fetchMock.mock.calls[1][0]).toContain("range=7d")
  })

  it("refetches when reloadKey bumps", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValue(okJSON({ rows: [] }))

    const { rerender } = renderHook(
      ({ reload }: { reload: number }) => useCrewSpend("24h", true, reload),
      { initialProps: { reload: 0 } },
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))

    rerender({ reload: 1 })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
  })
})

describe("useAgentSpend", () => {
  it("is disabled when crewId is null (no fetch fired)", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    const { result } = renderHook(() => useAgentSpend(null, "24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(fetchMock).not.toHaveBeenCalled()
    expect(result.current.data).toBeNull()
  })

  it("encodes crewId into the URL", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [] }))

    renderHook(() => useAgentSpend("crew with spaces/and-slashes", "24h"))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())

    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain("crew%20with%20spaces%2Fand-slashes")
  })
})

describe("useTopSpenders", () => {
  it("includes limit query parameter", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [] }))

    renderHook(() => useTopSpenders("24h", 25))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())

    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain("limit=25")
    expect(url).toContain("range=24h")
  })

  it("defaults limit to 10 when not provided", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [] }))

    renderHook(() => useTopSpenders("24h"))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())

    expect((fetchMock.mock.calls[0][0] as string)).toContain("limit=10")
  })
})

describe("useSubscriptionUsage", () => {
  it("hits the subscriptions endpoint with range", async () => {
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    fetchMock.mockResolvedValueOnce(okJSON({ rows: [] }))

    renderHook(() => useSubscriptionUsage("7d"))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())

    expect(fetchMock.mock.calls[0][0]).toBe("/api/v1/paymaster/subscriptions?range=7d")
  })

  it("404 → notConfigured (subscription billing not enabled)", async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(notFound())

    const { result } = renderHook(() => useSubscriptionUsage("24h"))
    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.notConfigured).toBe(true)
  })
})
