import { readFileSync } from "node:fs"
import { parseEnv } from "node:util"

// The checked-in .env.production is the single build profile. main ships the
// "main" profile (every experimental surface off, Artifacts nav on /pages); a
// shared dev release commit switches it to "release" and nothing else, so the
// same tests verify either build instead of being ignored on the release.
export const RELEASE_FLAGS = [
  "NEXT_PUBLIC_ARTIFACTS_LIBRARY",
  "NEXT_PUBLIC_PAGES_OPEN_ARTIFACTS",
  "NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT",
  "NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS",
  "NEXT_PUBLIC_CHAT_CRAFT_LEASE",
  "NEXT_PUBLIC_ARTIFACT_DASHBOARDS",
  "NEXT_PUBLIC_ARTIFACT_SLIDES",
  "NEXT_PUBLIC_ARTIFACTS_DESIGN",
] as const

export type BuildProfile = { name: "main" | "release"; env: Record<string, string | undefined> }

export function buildProfile(file = ".env.production"): BuildProfile {
  const env = parseEnv(readFileSync(file, "utf8"))
  const name = env.CREWSHIP_BUILD_PROFILE
  if (name !== "main" && name !== "release") throw new Error(`.env.production must set CREWSHIP_BUILD_PROFILE to main or release, got ${name}`)
  return { name, env }
}

// Code bound for main (push or merge queue) must carry the main profile.
export function refTargetsMain(ref = process.env.GITHUB_REF ?? ""): boolean {
  return ref === "refs/heads/main" || ref.startsWith("refs/heads/gh-readonly-queue/main/")
}
