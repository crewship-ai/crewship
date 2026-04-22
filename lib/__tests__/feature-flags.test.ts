import { describe, it, expect, vi, afterEach } from "vitest"
import { crewsUnifiedUI } from "@/lib/feature-flags"

describe("crewsUnifiedUI", () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it("returns false when env var is unset", () => {
    vi.stubEnv("NEXT_PUBLIC_CREWS_UNIFIED_UI", "")
    expect(crewsUnifiedUI()).toBe(false)
  })

  it("returns true when env var is exactly 'true'", () => {
    vi.stubEnv("NEXT_PUBLIC_CREWS_UNIFIED_UI", "true")
    expect(crewsUnifiedUI()).toBe(true)
  })

  it("returns false for other truthy-looking values", () => {
    vi.stubEnv("NEXT_PUBLIC_CREWS_UNIFIED_UI", "1")
    expect(crewsUnifiedUI()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_CREWS_UNIFIED_UI", "yes")
    expect(crewsUnifiedUI()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_CREWS_UNIFIED_UI", "TRUE")
    expect(crewsUnifiedUI()).toBe(false)
  })

  it("returns false for 'false'", () => {
    vi.stubEnv("NEXT_PUBLIC_CREWS_UNIFIED_UI", "false")
    expect(crewsUnifiedUI()).toBe(false)
  })
})
