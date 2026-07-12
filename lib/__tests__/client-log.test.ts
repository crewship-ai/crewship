import { afterEach, describe, expect, it, vi } from "vitest"

// devWarn is the sanctioned home for "operator debugging" logs that used
// to be raw console.warn calls in production components (#1000): it must
// forward to console.warn in development and stay silent in production.

afterEach(() => {
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
  vi.resetModules()
})

describe("devWarn", () => {
  it("forwards to console.warn outside production", async () => {
    vi.stubEnv("NODE_ENV", "development")
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {})
    const { devWarn } = await import("../client-log")
    devWarn("[capability PATCH] server error:", "boom")
    expect(spy).toHaveBeenCalledWith("[capability PATCH] server error:", "boom")
  })

  it("is silent in production", async () => {
    vi.stubEnv("NODE_ENV", "production")
    const spy = vi.spyOn(console, "warn").mockImplementation(() => {})
    const { devWarn } = await import("../client-log")
    devWarn("should not appear")
    expect(spy).not.toHaveBeenCalled()
  })
})
