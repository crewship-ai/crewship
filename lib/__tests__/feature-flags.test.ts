import { describe, it, expect, vi, afterEach } from "vitest"
import { readFileSync } from "node:fs"
import { parseEnv } from "node:util"
import { artifactSlides, artifactsLibrary, chatArtifactProjects, chatCraftLease, crewsUnifiedUI, legacyMcpIntegrations } from "@/lib/feature-flags"

describe.each([
  ["NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", chatArtifactProjects],
  ["NEXT_PUBLIC_CHAT_CRAFT_LEASE", chatCraftLease],
  ["NEXT_PUBLIC_ARTIFACTS_LIBRARY", artifactsLibrary],
] as const)("production %s", (name, enabled) => {
  afterEach(() => vi.unstubAllEnvs())

  it("stays off with the checked-in production build configuration", () => {
    const defaults = parseEnv(readFileSync(".env.production", "utf8"))
    vi.stubEnv(name, defaults[name])
    expect(enabled()).toBe(false)
  })

  it("allows an explicit build-time opt-in", () => {
    vi.stubEnv(name, "true")
    expect(enabled()).toBe(true)
  })
})

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

describe("artifactSlides", () => {
  afterEach(() => vi.unstubAllEnvs())
  it.each(["", "false", "1", "TRUE", "yes"])("defaults off for %s", value => {
    vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", value)
    expect(artifactSlides()).toBe(false)
  })
  it("requires literal true at build time", () => {
    vi.stubEnv("NEXT_PUBLIC_ARTIFACT_SLIDES", "true")
    expect(artifactSlides()).toBe(true)
  })
  it("keeps the tracked production build configuration default-off", () => {
    const production = readFileSync(".env.production", "utf8")
    expect(production.match(/^NEXT_PUBLIC_ARTIFACT_SLIDES\s*=\s*(.*?)\s*$/m)?.[1]).not.toBe("true")
  })
})
