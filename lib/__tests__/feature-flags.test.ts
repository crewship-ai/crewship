import { describe, it, expect, vi, afterEach } from "vitest"
import { readFileSync } from "node:fs"
import { parseEnv } from "node:util"
import { ALWAYS_ON_FLAGS, buildProfile, previewProfile, refTargetsMain, RELEASE_FLAGS, UNACCEPTED_FLAGS, type BuildProfile } from "../../e2e/build-profile"
import { artifactDesignSystem, artifactSlides, artifactsLibrary, artifactsServerCatalog, chatArtifactProjects, chatCraftLease, crewsUnifiedUI, legacyMcpIntegrations } from "@/lib/feature-flags"

// The value a flag must have in the checked-in profile: on in release for release
// flags, off on main, and exactly the declared value in a preview (#5261).
function expectedOn(profile: BuildProfile, flag: string, onInRelease: boolean): boolean {
  if (profile.name === "preview") return previewProfile(profile.preview!)[flag] === "true"
  return profile.name === "release" && onInRelease
}

describe.each([
  ["NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS", chatArtifactProjects],
  ["NEXT_PUBLIC_CHAT_CRAFT_LEASE", chatCraftLease],
] as const)("production %s", (name, enabled) => {
  afterEach(() => vi.unstubAllEnvs())

  it("follows the checked-in build profile", () => {
    const profile = buildProfile()
    vi.stubEnv(name, profile.env[name])
    expect(enabled()).toBe(expectedOn(profile, name, true))
  })

  it("allows an explicit build-time opt-in", () => {
    vi.stubEnv(name, "true")
    expect(enabled()).toBe(true)
  })
})

// The owner's decision (8 Nov 2026): Pages does not exist for users, on main
// as on a release. The library is on in the checked-in profile, whichever it is.
describe("production NEXT_PUBLIC_ARTIFACTS_LIBRARY", () => {
  afterEach(() => vi.unstubAllEnvs())

  it("is on in the checked-in build profile, main or release", () => {
    const profile = buildProfile()
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", profile.env.NEXT_PUBLIC_ARTIFACTS_LIBRARY)
    expect(artifactsLibrary()).toBe(true)
  })

  it("is an always-on flag, never a release flag", () => {
    expect(ALWAYS_ON_FLAGS).toContain("NEXT_PUBLIC_ARTIFACTS_LIBRARY")
    expect(RELEASE_FLAGS as readonly string[]).not.toContain("NEXT_PUBLIC_ARTIFACTS_LIBRARY")
  })

  it("still reads only a literal true", () => {
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_LIBRARY", "1")
    expect(artifactsLibrary()).toBe(false)
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
  it("stays off in the checked-in build profile until milestone D is accepted, unless a declared preview turns it on", () => {
    const profile = buildProfile()
    expect(profile.env.NEXT_PUBLIC_ARTIFACT_SLIDES === "true").toBe(expectedOn(profile, "NEXT_PUBLIC_ARTIFACT_SLIDES", false))
  })
})

describe("build profile", () => {
  it("is main for anything bound for main", () => {
    if (refTargetsMain()) expect(buildProfile().name).toBe("main")
  })
  it("recognises pushes and merge-queue refs of main only", () => {
    expect(refTargetsMain("refs/heads/main", "")).toBe(true)
    expect(refTargetsMain("refs/heads/gh-readonly-queue/main/pr-4484-abc", "")).toBe(true)
    expect(refTargetsMain("refs/pull/4484/merge", "main")).toBe(true)
    expect(refTargetsMain("refs/heads/demo/release-dev-20261029-r2", "")).toBe(false)
  })
  it("main keeps every release flag off; release turns each on without the sharing mock", () => {
    const profile = buildProfile()
    for (const flag of RELEASE_FLAGS) expect(profile.env[flag] === "true", flag).toBe(expectedOn(profile, flag, true))
    expect(profile.env.NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK).not.toBe("true")
  })
  it("turns every always-on flag on in both profiles", () => {
    const profile = buildProfile()
    for (const flag of ALWAYS_ON_FLAGS) expect(profile.env[flag], flag).toBe("true")
    expect(ALWAYS_ON_FLAGS.filter(flag => (RELEASE_FLAGS as readonly string[]).includes(flag) || (UNACCEPTED_FLAGS as readonly string[]).includes(flag))).toEqual([])
  })
  it("a release commit switches only the profile and the release flags, so the library stays on", () => {
    const file = readFileSync(".env.production", "utf8")
    const release = parseEnv(file.replace(/^CREWSHIP_BUILD_PROFILE=.*$/m, "CREWSHIP_BUILD_PROFILE=release")
      + RELEASE_FLAGS.map(flag => `\n${flag}=true`).join(""))
    for (const flag of ALWAYS_ON_FLAGS) expect(release[flag], flag).toBe("true")
  })
  it("keeps unaccepted milestones off in main and release; only a declared preview may turn one on", () => {
    const profile = buildProfile()
    for (const flag of UNACCEPTED_FLAGS) expect(profile.env[flag] === "true", flag).toBe(expectedOn(profile, flag, false))
    expect(RELEASE_FLAGS.filter(flag => (UNACCEPTED_FLAGS as readonly string[]).includes(flag))).toEqual([])
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
  it("keeps the tracked production build configuration default-off, unless a declared preview turns it on", () => {
    const production = readFileSync(".env.production", "utf8")
    const on = production.match(/^NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM\s*=\s*(.*?)\s*$/m)?.[1] === "true"
    expect(on).toBe(expectedOn(buildProfile(), "NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM", false))
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
