import { describe, it, expect, vi, afterEach } from "vitest"
import { readFileSync } from "node:fs"
import { parseEnv } from "node:util"
import { buildProfile, refTargetsMain, RELEASE_FLAGS } from "../../e2e/build-profile"
import { artifactDesignSystem, artifactSlides, artifactsLibrary, artifactsServerCatalog, chatArtifactProjects, chatCraftLease, crewsUnifiedUI, legacyMcpIntegrations, pagesOpenArtifacts } from "@/lib/feature-flags"

describe.each([
  ["NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", chatArtifactProjects],
  ["NEXT_PUBLIC_CHAT_CRAFT_LEASE", chatCraftLease],
  ["NEXT_PUBLIC_ARTIFACTS_LIBRARY", artifactsLibrary],
] as const)("production %s", (name, enabled) => {
  afterEach(() => vi.unstubAllEnvs())

  it("follows the checked-in build profile", () => {
    const profile = buildProfile()
    vi.stubEnv(name, profile.env[name])
    expect(enabled()).toBe(profile.name === "release")
  })

  it("allows an explicit build-time opt-in", () => {
    vi.stubEnv(name, "true")
    expect(enabled()).toBe(true)
  })
})

describe("pagesOpenArtifacts", () => {
  afterEach(() => vi.unstubAllEnvs())

  it("follows the checked-in build profile", () => {
    const profile = buildProfile()
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", profile.env.NEXT_PUBLIC_ARTIFACTS_LIBRARY)
    vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", profile.env.NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS)
    expect(pagesOpenArtifacts()).toBe(profile.name === "release")
  })

  it("needs the library itself as well as its own opt-in", () => {
    vi.stubEnv("NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS", "true")
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "false")
    expect(pagesOpenArtifacts()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
    expect(pagesOpenArtifacts()).toBe(true)
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
  it("follows the checked-in build profile", () => {
    const profile = buildProfile()
    expect(profile.env.NEXT_PUBLIC_ARTIFACT_SLIDES === "true").toBe(profile.name === "release")
  })
})

describe("build profile", () => {
  it("is main for anything bound for main", () => {
    if (refTargetsMain()) expect(buildProfile().name).toBe("main")
  })
  it("recognises pushes and merge-queue refs of main only", () => {
    expect(refTargetsMain("refs/heads/main")).toBe(true)
    expect(refTargetsMain("refs/heads/gh-readonly-queue/main/pr-4484-abc")).toBe(true)
    expect(refTargetsMain("refs/pull/4484/merge")).toBe(false)
    expect(refTargetsMain("refs/heads/demo/release-dev-20261029-r2")).toBe(false)
  })
  it("main keeps every release flag off; release turns each on without the sharing mock", () => {
    const profile = buildProfile()
    for (const flag of RELEASE_FLAGS) expect(profile.env[flag] === "true", flag).toBe(profile.name === "release")
    expect(profile.env.NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK).not.toBe("true")
  })
})

describe("artifactDesignSystem", () => {
  afterEach(() => vi.unstubAllEnvs())
  it.each(["", "false", "1", "TRUE", "yes"])("defaults off for %s", value => {
    vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", value)
    expect(artifactDesignSystem()).toBe(false)
  })
  it("requires literal true at build time", () => {
    vi.stubEnv("NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", "true")
    expect(artifactDesignSystem()).toBe(true)
  })
  it("keeps the tracked production build configuration default-off", () => {
    const production = readFileSync(".env.production", "utf8")
    expect(production.match(/^NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM\s*=\s*(.*?)\s*$/m)?.[1]).not.toBe("true")
  })
})

describe("artifactsServerCatalog", () => {
  afterEach(() => vi.unstubAllEnvs())
  it("defaults off and needs the library itself", () => {
    expect(artifactsServerCatalog()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_SERVER_CATALOG", "true")
    expect(artifactsServerCatalog()).toBe(false)
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "true")
    expect(artifactsServerCatalog()).toBe(true)
  })
})
