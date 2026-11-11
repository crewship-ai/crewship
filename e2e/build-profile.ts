import { readFileSync } from "node:fs"
import { parseEnv } from "node:util"

// The checked-in .env.production is the single build profile. main ships the
// "main" profile (every experimental surface off); a shared dev release commit
// switches it to "release" and nothing else, so the same tests verify either
// build instead of being ignored on the release.

// On in every profile. The Artifacts library is the only home of saved work:
// users never see Pages, on main as on a release (owner, 8 Nov 2026). The
// /api/v1/pages API and its data model stay as they are.
export const ALWAYS_ON_FLAGS = [
  "NEXT_PUBLIC_ARTIFACTS_LIBRARY",
] as const

export const RELEASE_FLAGS = [
  "NEXT_PUBLIC_CHAT_ARTIFACT_CONTEXT",
  "NEXT_PUBLIC_CHAT_ARTIFACT_PROJECTS",
  "NEXT_PUBLIC_CHAT_CRAFT_LEASE",
  "NEXT_PUBLIC_ARTIFACTS_DESIGN",
] as const

// Later milestones stay off in every profile until they pass their own
// acceptance: Dashboard is milestone C, Slides and Design System milestone D of the PRD.
export const UNACCEPTED_FLAGS = [
  "NEXT_PUBLIC_ARTIFACTS_DESIGN_HIDE_LOCK",
  "NEXT_PUBLIC_ARTIFACT_DASHBOARDS",
  "NEXT_PUBLIC_ARTIFACT_SLIDES",
  "NEXT_PUBLIC_ARTIFACT_DESIGN_SYSTEM",
] as const

export type BuildProfile = { name: "main" | "release" | "preview"; preview?: string; env: Record<string, string | undefined> }

// A shared dev slot may be built with a declared preview profile (#5261):
// CREWSHIP_BUILD_PROFILE=preview:<name>, and .env.production equal, key for key
// and value for value, to the tracked e2e/preview-profiles/<name>.env. A preview
// never targets main; see checkProfile.
export const PREVIEW_PROFILE_DIR = "e2e/preview-profiles"
const PREVIEW_NAME = /^preview:([a-z0-9][a-z0-9-]{0,31})$/

export function previewProfile(name: string): Record<string, string | undefined> {
  if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(name)) throw new Error(`invalid preview profile name ${JSON.stringify(name)}`)
  return parseEnv(readFileSync(`${PREVIEW_PROFILE_DIR}/${name}.env`, "utf8"))
}

export function buildProfile(file = ".env.production"): BuildProfile {
  const env = parseEnv(readFileSync(file, "utf8"))
  const name = env.CREWSHIP_BUILD_PROFILE
  if (name === "main" || name === "release") return { name, env }
  const preview = PREVIEW_NAME.exec(name ?? "")
  if (preview) return { name: "preview", preview: preview[1], env }
  throw new Error(`.env.production must set CREWSHIP_BUILD_PROFILE to main, release or preview:<name>, got ${name}`)
}

// Whether the code being built is bound for main. Fails closed (#5261): a pull
// request decides by its base branch; otherwise only a push to a branch other
// than main and its merge queue is not bound for main. An unknown or missing ref,
// a tag, or a pull request ref without a base counts as main.
export function refTargetsMain(ref = process.env.GITHUB_REF, base = process.env.GITHUB_BASE_REF): boolean {
  if (base) return base === "main"
  if (!ref) return true
  if (ref === "refs/heads/main" || ref.startsWith("refs/heads/gh-readonly-queue/main/")) return true
  return !ref.startsWith("refs/heads/")
}

// Every rule the checked-in profile must satisfy, as a list of violations.
// main: release and unaccepted flags off. release: release flags on, unaccepted
// off, never bound for main. preview: exactly its declaration, never bound for main.
// Every profile: always-on flags on, the sharing mock off.
export function checkProfile(profile: BuildProfile, { targetsMain }: { targetsMain: boolean }): string[] {
  const errors: string[] = []
  const env = profile.env
  if (targetsMain && profile.name !== "main") errors.push(`code bound for main must carry the main profile, not ${profile.name}`)
  for (const flag of ALWAYS_ON_FLAGS) if (env[flag] !== "true") errors.push(`${flag} must be on in every profile`)
  if (env.NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK === "true") errors.push("NEXT_PUBLIC_ARTIFACTS_SHARING_MOCK must never be on")
  if (profile.name === "main" || profile.name === "release") {
    for (const flag of RELEASE_FLAGS) if ((env[flag] === "true") !== (profile.name === "release")) errors.push(`${flag} must be ${profile.name === "release" ? "on" : "off"} in ${profile.name}`)
    for (const flag of UNACCEPTED_FLAGS) if (env[flag] === "true") errors.push(`${flag} must be off in ${profile.name}`)
    return errors
  }
  let declared: Record<string, string | undefined>
  try { declared = previewProfile(profile.preview ?? "") } catch (error) { return [...errors, `preview ${profile.preview} is not declared in ${PREVIEW_PROFILE_DIR}: ${String(error)}`] }
  for (const key of new Set([...Object.keys(declared), ...Object.keys(env)])) {
    if (declared[key] !== env[key]) errors.push(`${key} is ${JSON.stringify(env[key])}, declared ${JSON.stringify(declared[key])} in preview ${profile.preview}`)
  }
  return errors
}
