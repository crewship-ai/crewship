import { afterEach, expect, it, vi } from "vitest"

// Separate module instances model separate browser realms (refreshInflight is
// realm-local). The lock scheduler is a deterministic Web Locks test double;
// cookie rotation/replay behavior is qualified separately by refresh_test.go.
function sharedLocks() {
  let tail: Promise<unknown> = Promise.resolve()
  return {
    request: vi.fn((_name: string, options: { signal: AbortSignal }, callback: () => Promise<unknown>) => {
      let onAbort!: () => void
      const aborted = new Promise<never>((_, reject) => {
        onAbort = () => reject(new DOMException("Aborted", "AbortError"))
        options.signal.addEventListener("abort", onAbort, { once: true })
      })
      const running = tail.then(() => {
        if (options.signal.aborted) throw new DOMException("Aborted", "AbortError")
        return callback()
      })
      tail = running.catch(() => {})
      return Promise.race([running, aborted]).finally(() => options.signal.removeEventListener("abort", onAbort))
    }),
  }
}

afterEach(() => { vi.unstubAllGlobals(); vi.resetModules(); vi.useRealTimers() })

it("serializes refreshes from separate tabs before either snapshots the rotation cookie", async () => {
  const locks = sharedLocks()
  vi.stubGlobal("navigator", { locks })
  let release!: () => void
  const firstResponse = new Promise<void>(resolve => { release = resolve })
  let refreshActive = false, revoked = false
  const fetch = vi.fn(async () => {
    if (refreshActive) {
      // Both requests snapshotted the same old cookie; real RefreshToken CAS
      // treats this as replay and revokes the SQL session.
      revoked = true
      return new Response('{"error":"session_revoked"}', { status: 401 })
    }
    refreshActive = true
    await firstResponse
    refreshActive = false
    return new Response(null, { status: revoked ? 401 : 200 })
  })
  vi.stubGlobal("fetch", fetch)
  vi.resetModules()
  const tabA = await import("../api-fetch")
  vi.resetModules()
  const tabB = await import("../api-fetch")
  const a = tabA.tryRefresh(), b = tabB.tryRefresh()
  // A lock must cover the cookie-carrying fetch itself, not just JSON parsing.
  try {
    await vi.waitFor(() => expect(fetch).toHaveBeenCalledTimes(1))
  } finally { release() }
  expect(await Promise.all([a, b])).toEqual(["ok", "ok"])
  expect(revoked).toBe(false)
  expect(fetch).toHaveBeenCalledTimes(2)
  expect(locks.request).toHaveBeenCalledTimes(2)
})

it("bounds a refresh waiting for another tab without sending after timeout", async () => {
  const locks = sharedLocks()
  vi.stubGlobal("navigator", { locks })
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 200 }))
  vi.stubGlobal("fetch", fetch)
  vi.resetModules()
  const tab = await import("../api-fetch")
  let release!: () => void
  const held = locks.request("crewship.auth.refresh", { signal: new AbortController().signal }, () => new Promise<void>(resolve => { release = resolve }))
  await Promise.resolve()
  vi.useFakeTimers()
  const attempt = tab.tryRefresh()
  try {
    await vi.advanceTimersByTimeAsync(10_000)
    expect(await attempt).toBe("retryable_failed")
    expect(fetch).not.toHaveBeenCalled()
  } finally { release(); await held }
  await vi.advanceTimersByTimeAsync(1)
  expect(fetch).not.toHaveBeenCalled()
})

it("releases the cross-tab lock when the refresh fetch fails", async () => {
  const locks = sharedLocks()
  vi.stubGlobal("navigator", { locks })
  const fetch = vi.fn().mockRejectedValueOnce(new Error("network"))
    .mockResolvedValue(new Response(null, { status: 200 }))
  vi.stubGlobal("fetch", fetch)
  vi.resetModules()
  const tabA = await import("../api-fetch")
  vi.resetModules()
  const tabB = await import("../api-fetch")
  expect(await Promise.all([tabA.tryRefresh(), tabB.tryRefresh()])).toEqual(["retryable_failed", "ok"])
  expect(locks.request).toHaveBeenCalledTimes(2)
  expect(fetch).toHaveBeenCalledTimes(2)
})

it("skips coordinated proactive rotation where Web Locks are unavailable", async () => {
  vi.stubGlobal("navigator", {})
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 200 }))
  vi.stubGlobal("fetch", fetch)
  vi.resetModules()
  const tab = await import("../api-fetch")
  expect(await tab.tryRefresh({ requireCrossTabLock: true })).toBe("retryable_failed")
  expect(fetch).not.toHaveBeenCalled()
  // Preserve the ordinary legacy refresh behavior; no multi-tab guarantee.
  expect(await tab.tryRefresh()).toBe("ok")
  expect(fetch).toHaveBeenCalledTimes(1)
})
