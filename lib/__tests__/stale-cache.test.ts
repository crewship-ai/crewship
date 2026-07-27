import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { clearStaleCache, invalidate, readThrough } from "../stale-cache"

beforeEach(() => {
  clearStaleCache()
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe("readThrough", () => {
  it("fetches on a miss and reports that it did not come from cache", async () => {
    const fetcher = vi.fn().mockResolvedValue("a")
    const r = readThrough("k", fetcher)
    expect(r.value).toBeUndefined()
    expect(r.fromCache).toBe(false)
    await expect(r.fresh).resolves.toBe("a")
    expect(fetcher).toHaveBeenCalledOnce()
  })

  it("serves the cached value without refetching inside the TTL", async () => {
    const fetcher = vi.fn().mockResolvedValue("a")
    await readThrough("k", fetcher, 1000).fresh

    const second = readThrough("k", fetcher, 1000)
    expect(second.value).toBe("a")
    expect(second.fromCache).toBe(true)
    await expect(second.fresh).resolves.toBe("a")
    // The point of the cache: a repeat visit costs nothing.
    expect(fetcher).toHaveBeenCalledOnce()
  })

  it("serves the stale value immediately while refreshing past the TTL", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce("old").mockResolvedValueOnce("new")
    await readThrough("k", fetcher, 1000).fresh

    vi.advanceTimersByTime(1500)
    const stale = readThrough("k", fetcher, 1000)
    // This is the whole behaviour: a first paint from cache, not a skeleton.
    expect(stale.value).toBe("old")
    expect(stale.fromCache).toBe(true)
    await expect(stale.fresh).resolves.toBe("new")
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it("collapses concurrent readers onto one request", async () => {
    let resolve!: (v: string) => void
    const fetcher = vi.fn().mockImplementation(() => new Promise<string>((r) => (resolve = r)))

    const a = readThrough("k", fetcher)
    const b = readThrough("k", fetcher)
    resolve("a")
    await Promise.all([a.fresh, b.fresh])
    // Two sections mounting in the same tick must not double the load.
    expect(fetcher).toHaveBeenCalledOnce()
  })

  it("keeps serving the last good value when a refresh fails", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce("good")
      .mockRejectedValueOnce(new Error("network"))
      .mockResolvedValue("recovered")
    await readThrough("k", fetcher, 1000).fresh

    vi.advanceTimersByTime(1500)
    const during = readThrough("k", fetcher, 1000)
    expect(during.value).toBe("good")
    await expect(during.fresh).rejects.toThrow("network")

    // A failed refresh must not poison the cache — the old value survives and
    // the next read is free to try again.
    const after = readThrough("k", fetcher, 1000)
    expect(after.value).toBe("good")
  })

  it("does not cache a first fetch that failed", async () => {
    const fetcher = vi.fn().mockRejectedValueOnce(new Error("boom")).mockResolvedValueOnce("ok")
    await expect(readThrough("k", fetcher).fresh).rejects.toThrow("boom")

    const retry = readThrough("k", fetcher)
    expect(retry.value).toBeUndefined()
    await expect(retry.fresh).resolves.toBe("ok")
  })

  it("keys entries separately", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce("a").mockResolvedValueOnce("b")
    await readThrough("one", fetcher).fresh
    await readThrough("two", fetcher).fresh
    expect(readThrough("one", fetcher).value).toBe("a")
    expect(readThrough("two", fetcher).value).toBe("b")
  })
})

describe("invalidate", () => {
  it("drops matching entries so the next read refetches", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce("a").mockResolvedValueOnce("b")
    await readThrough("composio:ws1:inventory", fetcher).fresh

    // After connecting an account, serving the stale list would show the
    // user's own change missing — which reads as the connect having failed.
    invalidate("composio:ws1:")

    const after = readThrough("composio:ws1:inventory", fetcher)
    expect(after.value).toBeUndefined()
    await expect(after.fresh).resolves.toBe("b")
  })

  it("leaves other prefixes alone", async () => {
    const fetcher = vi.fn().mockResolvedValue("a")
    await readThrough("composio:ws1:inventory", fetcher).fresh
    await readThrough("composio:ws2:inventory", fetcher).fresh

    invalidate("composio:ws1:")

    expect(readThrough("composio:ws1:inventory", fetcher).value).toBeUndefined()
    expect(readThrough("composio:ws2:inventory", fetcher).value).toBe("a")
  })
})
