import { describe, it, expect, vi, afterEach } from "vitest"
import { crewsUnifiedUI, legacyMcpIntegrations } from "@/lib/feature-flags"

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

describe("legacyMcpIntegrations", () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it("returns false when env var is unset (legacy UI hidden by default)", () => {
    vi.stubEnv("NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS", "")
    expect(legacyMcpIntegrations()).toBe(false)
  })

  it("returns true when env var is exactly 'true'", () => {
    vi.stubEnv("NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS", "true")
    expect(legacyMcpIntegrations()).toBe(true)
  })

  it("returns false for other truthy-looking values", () => {
    vi.stubEnv("NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS", "1")
    expect(legacyMcpIntegrations()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS", "yes")
    expect(legacyMcpIntegrations()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS", "TRUE")
    expect(legacyMcpIntegrations()).toBe(false)
  })

  it("returns false for 'false'", () => {
    vi.stubEnv("NEXT_PUBLIC_LEGACY_MCP_INTEGRATIONS", "false")
    expect(legacyMcpIntegrations()).toBe(false)
  })
})
