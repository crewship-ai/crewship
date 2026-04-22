import { describe, it, expect, vi, afterEach } from "vitest"
import { cruiseUnifiedUI } from "@/lib/feature-flags"

describe("cruiseUnifiedUI", () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it("returns false when env var is unset", () => {
    vi.stubEnv("NEXT_PUBLIC_CRUISE_UNIFIED_UI", "")
    expect(cruiseUnifiedUI()).toBe(false)
  })

  it("returns true when env var is exactly 'true'", () => {
    vi.stubEnv("NEXT_PUBLIC_CRUISE_UNIFIED_UI", "true")
    expect(cruiseUnifiedUI()).toBe(true)
  })

  it("returns false for other truthy-looking values", () => {
    vi.stubEnv("NEXT_PUBLIC_CRUISE_UNIFIED_UI", "1")
    expect(cruiseUnifiedUI()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_CRUISE_UNIFIED_UI", "yes")
    expect(cruiseUnifiedUI()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_CRUISE_UNIFIED_UI", "TRUE")
    expect(cruiseUnifiedUI()).toBe(false)
  })

  it("returns false for 'false'", () => {
    vi.stubEnv("NEXT_PUBLIC_CRUISE_UNIFIED_UI", "false")
    expect(cruiseUnifiedUI()).toBe(false)
  })
})
