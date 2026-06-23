import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { fetchWithRetry } from "@/lib/fetch-with-retry"
import { _resetRefreshInflightForTesting } from "@/lib/api-fetch"

// Coverage companion for fetch-with-retry.test.ts — that file covers the
// bodyIsReplayable/inputIsReplayable helpers; this one drives the main
// fetchWithRetry loop: retry-on-gateway-flap, 429 passthrough, Retry-After
// honoring, error/abort semantics and the normalizeRetries clamp.

// Minimal Response shape with headers (fetchWithRetry reads Retry-After)
// and clone/json (apiFetch peeks the body on 401 — we avoid 401s here,
// but clone() keeps the mock honest if a future path touches it).
function res(status: number, headers: Record<string, string> = {}): Response {
  const r = {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers(headers),
    json: async () => ({}),
    clone: () => r,
  }
  return r as unknown as Response
}

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal("fetch", fetchMock)
  // Deterministic backoff: kill the jitter so sleeps are exactly
  // baseDelayMs * 2^attempt (we pass baseDelayMs: 1 → 1–4 ms real waits).
  vi.spyOn(Math, "random").mockReturnValue(0)
  _resetRefreshInflightForTesting()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("fetchWithRetry — success and routing", () => {
  it("returns the first OK response without retrying", async () => {
    fetchMock.mockResolvedValueOnce(res(200))
    const r = await fetchWithRetry("/api/v1/crews/c1")
    expect(r.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("routes replayable requests through apiFetch (credentials: include added)", async () => {
    fetchMock.mockResolvedValueOnce(res(200))
    await fetchWithRetry("/api/v1/crews/c1", { method: "GET" })
    // apiFetch always forces cookie auth — proof the call went through it
    // rather than plain fetch.
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: "include" })
  })

  it("uses plain fetch (no credentials injection) for ReadableStream bodies", async () => {
    fetchMock.mockResolvedValueOnce(res(200))
    const stream = new ReadableStream({
      start(c) {
        c.enqueue(new TextEncoder().encode("x"))
        c.close()
      },
    })
    const init = { method: "POST", body: stream, duplex: "half" } as RequestInit
    await fetchWithRetry("/api/v1/upload", init)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    // Plain fetch hands init through untouched — apiFetch would have
    // cloned it and added credentials: "include".
    expect(fetchMock.mock.calls[0][1]).toBe(init)
    expect((fetchMock.mock.calls[0][1] as RequestInit).credentials).toBeUndefined()
  })
})

describe("fetchWithRetry — gateway flap retries", () => {
  it("retries 502/503/504 and returns the eventual success", async () => {
    fetchMock
      .mockResolvedValueOnce(res(503))
      .mockResolvedValueOnce(res(502))
      .mockResolvedValueOnce(res(200))
    const r = await fetchWithRetry("/api/v1/crews/c1", { baseDelayMs: 1 })
    expect(r.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it("returns the last 5xx response after exhausting retries", async () => {
    fetchMock.mockResolvedValue(res(504))
    const r = await fetchWithRetry("/api/v1/crews/c1", { retries: 2, baseDelayMs: 1 })
    expect(r.status).toBe(504)
    // retries=2 → 3 total attempts.
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it("does NOT retry 429 — rate limiting means stop calling", async () => {
    fetchMock.mockResolvedValue(res(429))
    const r = await fetchWithRetry("/api/v1/crews/c1", { baseDelayMs: 1 })
    expect(r.status).toBe(429)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("does NOT retry plain client errors (404)", async () => {
    fetchMock.mockResolvedValue(res(404))
    const r = await fetchWithRetry("/api/v1/crews/missing", { baseDelayMs: 1 })
    expect(r.status).toBe(404)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("does NOT retry non-replayable (stream body) requests even on 503", async () => {
    fetchMock.mockResolvedValue(res(503))
    const stream = new ReadableStream({
      start(c) {
        c.close()
      },
    })
    const r = await fetchWithRetry("/api/v1/upload", {
      method: "POST",
      body: stream,
      retries: 3,
      baseDelayMs: 1,
    } as RequestInit & { retries?: number; baseDelayMs?: number })
    expect(r.status).toBe(503)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("does NOT retry when input is a Request carrying a body", async () => {
    fetchMock.mockResolvedValue(res(503))
    const req = new Request("http://localhost:3000/api/v1/x", { method: "POST", body: "p" })
    const r = await fetchWithRetry(req, { retries: 3, baseDelayMs: 1 })
    expect(r.status).toBe(503)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("honors Retry-After header (seconds → ms) before retrying", async () => {
    vi.useFakeTimers()
    fetchMock
      .mockResolvedValueOnce(res(503, { "Retry-After": "2" }))
      .mockResolvedValueOnce(res(200))
    const p = fetchWithRetry("/api/v1/crews/c1", { baseDelayMs: 1 })
    // The second attempt must wait the full 2000ms from the header, not
    // the 1ms exponential base.
    await vi.advanceTimersByTimeAsync(1999)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    const r = await p
    expect(r.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("caps Retry-After at 8000ms", async () => {
    vi.useFakeTimers()
    fetchMock
      .mockResolvedValueOnce(res(503, { "Retry-After": "60" }))
      .mockResolvedValueOnce(res(200))
    const p = fetchWithRetry("/api/v1/crews/c1", { baseDelayMs: 1 })
    // 60s requested, but the cap means the retry fires at 8s.
    await vi.advanceTimersByTimeAsync(8000)
    const r = await p
    expect(r.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})

describe("fetchWithRetry — thrown errors", () => {
  it("retries a network error and returns the recovery response", async () => {
    fetchMock
      .mockRejectedValueOnce(new TypeError("network down"))
      .mockResolvedValueOnce(res(200))
    const r = await fetchWithRetry("/api/v1/crews/c1", { baseDelayMs: 1 })
    expect(r.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("rethrows the last error after exhausting retries", async () => {
    fetchMock.mockRejectedValue(new TypeError("still down"))
    await expect(
      fetchWithRetry("/api/v1/crews/c1", { retries: 1, baseDelayMs: 1 }),
    ).rejects.toThrow("still down")
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it("rethrows AbortError immediately without retrying", async () => {
    const abort = new DOMException("The operation was aborted.", "AbortError")
    fetchMock.mockRejectedValue(abort)
    await expect(
      fetchWithRetry("/api/v1/crews/c1", { retries: 3, baseDelayMs: 1 }),
    ).rejects.toMatchObject({ name: "AbortError" })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})

describe("fetchWithRetry — normalizeRetries clamp", () => {
  it("falls back to 2 retries when retries is NaN", async () => {
    fetchMock.mockResolvedValue(res(503))
    await fetchWithRetry("/api/v1/x", { retries: Number.NaN, baseDelayMs: 1 })
    expect(fetchMock).toHaveBeenCalledTimes(3) // fallback 2 retries → 3 attempts
  })

  it("clamps negative retries to 0 (single attempt, no pre-fetch throw)", async () => {
    fetchMock.mockResolvedValue(res(503))
    const r = await fetchWithRetry("/api/v1/x", { retries: -5, baseDelayMs: 1 })
    expect(r.status).toBe(503)
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it("truncates fractional retries", async () => {
    fetchMock.mockResolvedValue(res(503))
    await fetchWithRetry("/api/v1/x", { retries: 1.9, baseDelayMs: 1 })
    expect(fetchMock).toHaveBeenCalledTimes(2) // trunc(1.9)=1 retry → 2 attempts
  })
})
