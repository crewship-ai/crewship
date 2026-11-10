import { describe, it, expect } from "vitest"
import { readdirSync, readFileSync } from "node:fs"
import { parseEnv } from "node:util"
import { ALWAYS_ON_FLAGS, buildProfile, checkProfile, PREVIEW_PROFILE_DIR, previewProfile, refTargetsMain, RELEASE_FLAGS } from "../../e2e/build-profile"

// Gating policy (#5261): the checked-in .env.production is main, release, or a
// declared preview of a shared dev slot. Anything bound for main is main.
const env = (lines: Record<string, string>) => ({ ...lines })
const mainEnv = env({ CREWSHIP_BUILD_PROFILE: "main", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true", NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS: "false", NEXT_PUBLIC_CHAT_CRAFT_LEASE: "false" })
const releaseEnv = env({ CREWSHIP_BUILD_PROFILE: "release", NEXT_PUBLIC_ARTIFACTS_LIBRARY: "true", ...Object.fromEntries(RELEASE_FLAGS.map(f => [f, "true"])) })

describe("refTargetsMain fails closed", () => {
  it.each([
    ["", "", true],
    ["refs/heads/main", "", true],
    ["refs/heads/gh-readonly-queue/main/pr-4484-abc", "", true],
    ["refs/pull/4484/merge", "main", true],
    ["refs/pull/4484/merge", "", true],
    ["refs/tags/v1.0.0", "", true],
    ["refs/pull/4484/merge", "release-1", false],
    ["refs/heads/demo/qualified-integration-20261109", "", false],
    ["refs/heads/feature/x", "", false],
  ])("ref %j base %j targets main: %s", (ref, base, want) => {
    expect(refTargetsMain(ref, base)).toBe(want)
  })
})

describe("checkProfile", () => {
  it("accepts main with every release and unaccepted flag off, and refuses one on", () => {
    expect(checkProfile({ name: "main", env: mainEnv }, { targetsMain: true })).toEqual([])
    expect(checkProfile({ name: "main", env: { ...mainEnv, NEXT_PUBLIC_ARTIFACT_SLIDES: "true" } }, { targetsMain: false })).not.toEqual([])
    expect(checkProfile({ name: "main", env: { ...mainEnv, NEXT_PUBLIC_ARTIFACTS_DESIGN: "true" } }, { targetsMain: false })).not.toEqual([])
  })
  it("accepts release with every release flag on and no unaccepted flag, never for main", () => {
    expect(checkProfile({ name: "release", env: releaseEnv }, { targetsMain: false })).toEqual([])
    expect(checkProfile({ name: "release", env: { ...releaseEnv, NEXT_PUBLIC_ARTIFACT_DASHBOARDS: "true" } }, { targetsMain: false })).not.toEqual([])
    expect(checkProfile({ name: "release", env: releaseEnv }, { targetsMain: true })).not.toEqual([])
  })
  it("refuses a preview bound for main, even when it matches its declaration", () => {
    const declared = previewProfile("dev2")
    expect(checkProfile({ name: "preview", preview: "dev2", env: declared }, { targetsMain: false })).toEqual([])
    expect(checkProfile({ name: "preview", preview: "dev2", env: declared }, { targetsMain: true })).not.toEqual([])
  })
  it("refuses a preview that differs from its declaration by one key or value", () => {
    const declared = previewProfile("dev2")
    const [key] = Object.keys(declared).filter(k => k.startsWith("NEXT_PUBLIC_") && declared[k] === "true")
    expect(checkProfile({ name: "preview", preview: "dev2", env: { ...declared, [key]: "false" } }, { targetsMain: false })).not.toEqual([])
    expect(checkProfile({ name: "preview", preview: "dev2", env: { ...declared, NEXT_PUBLIC_STATIC_APP_PREVIEW: "true" } }, { targetsMain: false })).not.toEqual([])
    const missing = { ...declared }; delete missing[key]
    expect(checkProfile({ name: "preview", preview: "dev2", env: missing }, { targetsMain: false })).not.toEqual([])
  })
  it("refuses an undeclared preview", () => {
    expect(checkProfile({ name: "preview", preview: "nowhere", env: mainEnv }, { targetsMain: false })).not.toEqual([])
  })
  it("never lets the sharing mock or a missing always-on flag through", () => {
    expect(checkProfile({ name: "main", env: { ...mainEnv, NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK: "true" } }, { targetsMain: false })).not.toEqual([])
    const noLibrary = { ...mainEnv }; delete noLibrary.NEXT_PUBLIC_ARTIFACTS_LIBRARY
    expect(checkProfile({ name: "main", env: noLibrary }, { targetsMain: false })).not.toEqual([])
  })
})

describe("declared preview profiles", () => {
  const files = readdirSync(PREVIEW_PROFILE_DIR).filter(f => f.endsWith(".env"))
  it("exist and each is a valid preview of its own name", () => {
    expect(files.length).toBeGreaterThan(0)
    for (const file of files) {
      const name = file.replace(/\.env$/, "")
      const declared = parseEnv(readFileSync(`${PREVIEW_PROFILE_DIR}/${file}`, "utf8"))
      expect(declared.CREWSHIP_BUILD_PROFILE, file).toBe(`preview:${name}`)
      expect(checkProfile({ name: "preview", preview: name, env: declared }, { targetsMain: false }), file).toEqual([])
      expect(declared.NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK, file).not.toBe("true")
      for (const flag of ALWAYS_ON_FLAGS) expect(declared[flag], `${file} ${flag}`).toBe("true")
    }
  })
})

describe("the checked-in .env.production", () => {
  it("passes its profile's rules for the ref being built", () => {
    expect(checkProfile(buildProfile(), { targetsMain: refTargetsMain() })).toEqual([])
  })
  it("is the main profile on anything bound for main, so a preview cannot land through the queue", () => {
    if (refTargetsMain()) expect(buildProfile().name).toBe("main")
  })
})
