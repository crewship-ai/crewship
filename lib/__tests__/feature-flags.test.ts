import { describe, it, expect, vi, afterEach } from "vitest"
import { fleetUnifiedUI } from "@/lib/feature-flags"

describe("fleetUnifiedUI", () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it("returns false when env var is unset", () => {
    vi.stubEnv("NEXT_PUBLIC_FLEET_UNIFIED_UI", "")
    expect(fleetUnifiedUI()).toBe(false)
  })

  it("returns true when env var is exactly 'true'", () => {
    vi.stubEnv("NEXT_PUBLIC_FLEET_UNIFIED_UI", "true")
    expect(fleetUnifiedUI()).toBe(true)
  })

  it("returns false for other truthy-looking values", () => {
    vi.stubEnv("NEXT_PUBLIC_FLEET_UNIFIED_UI", "1")
    expect(fleetUnifiedUI()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_FLEET_UNIFIED_UI", "yes")
    expect(fleetUnifiedUI()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_FLEET_UNIFIED_UI", "TRUE")
    expect(fleetUnifiedUI()).toBe(false)
  })

  it("returns false for 'false'", () => {
    vi.stubEnv("NEXT_PUBLIC_FLEET_UNIFIED_UI", "false")
    expect(fleetUnifiedUI()).toBe(false)
  })
})
