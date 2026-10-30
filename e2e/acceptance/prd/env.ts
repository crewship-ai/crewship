// Environment contract of the PRD acceptance suite. Every origin, account and
// output location comes from the environment; nothing here names a real host,
// account or server path. See docs/development/prd-acceptance.md.
import { createHash } from "node:crypto"
import * as fs from "node:fs"
import * as os from "node:os"
import * as path from "node:path"

export type PrdMode = "quick" | "full"

/** Role names a scenario can ask for. Each maps to PRD_<ROLE>_EMAIL/_PASSWORD. */
export type PrdRole = "owner" | "member" | "reader"
export const PRD_ROLES: readonly PrdRole[] = ["owner", "member", "reader"]

export interface PrdAccount {
  email: string
  password: string
}

export interface PrdEnv {
  /** Origin under test, without a trailing slash. */
  origin: string
  mode: PrdMode
  /** Directory for rows, evidence and Playwright output of this run. */
  outputDir: string
  /** Accounts that are configured; absent roles are simply missing. */
  accounts: Partial<Record<PrdRole, PrdAccount>>
  /** Optional existing QA workspace. Scenarios otherwise create their own. */
  workspaceId: string | null
  /** Full mode only: seeded agent slug and the workspace it lives in. */
  agentSlug: string | null
  agentWorkspaceId: string | null
  /** Path of the crewship CLI binary for CLI-driven scenarios (PRD_CLI). */
  cli: string | null
  /** Keep Playwright traces of failed tests (off by default: traces record request bodies). */
  trace: boolean
}

/** Default origin of quick mode: the ephemeral server CI's browser jobs start. */
export const QUICK_DEFAULT_ORIGIN = "http://localhost:8080"
export const DEFAULT_OUTPUT_DIR = path.join("test-results", "prd-acceptance")

type Env = Record<string, string | undefined>

function value(env: Env, name: string): string | null {
  const v = env[name]?.trim()
  return v ? v : null
}

export function normalizeOrigin(raw: string): string {
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    throw new Error(`PRD_ORIGIN is not a URL: ${JSON.stringify(raw)}`)
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`PRD_ORIGIN must be http(s), got ${url.protocol}`)
  }
  if (url.pathname !== "/" || url.search || url.hash || url.username || url.password) {
    throw new Error("PRD_ORIGIN must be a bare origin such as https://host (no path, query or credentials)")
  }
  return url.origin
}

function account(env: Env, role: PrdRole, mode: PrdMode): PrdAccount | undefined {
  const prefix = `PRD_${role.toUpperCase()}`
  let email = value(env, `${prefix}_EMAIL`)
  let password = value(env, `${prefix}_PASSWORD`)
  // Quick mode reuses the existing e2e login (E2E_EMAIL/E2E_PASSWORD) for the
  // owner so CI needs no second set of fixture variables.
  if (role === "owner" && mode === "quick" && !email && !password) {
    email = value(env, "E2E_EMAIL")
    password = value(env, "E2E_PASSWORD")
  }
  if (!email && !password) return undefined
  if (!email || !password) {
    throw new Error(`${prefix}_EMAIL and ${prefix}_PASSWORD must be set together`)
  }
  return { email, password }
}

export function readPrdEnv(env: Env = process.env): PrdEnv {
  const rawMode = value(env, "PRD_MODE") ?? "quick"
  if (rawMode !== "quick" && rawMode !== "full") {
    throw new Error(`PRD_MODE must be quick or full, got ${JSON.stringify(rawMode)}`)
  }
  const mode: PrdMode = rawMode
  const rawOrigin = value(env, "PRD_ORIGIN") ?? (mode === "quick" ? value(env, "PLAYWRIGHT_BASE_URL") ?? QUICK_DEFAULT_ORIGIN : null)
  if (!rawOrigin) throw new Error("PRD_ORIGIN is required in full mode (pass --origin to the runner)")
  const accounts: Partial<Record<PrdRole, PrdAccount>> = {}
  for (const role of PRD_ROLES) {
    const a = account(env, role, mode)
    if (a) accounts[role] = a
  }
  return {
    origin: normalizeOrigin(rawOrigin),
    mode,
    outputDir: path.resolve(value(env, "PRD_OUTPUT_DIR") ?? DEFAULT_OUTPUT_DIR),
    accounts,
    workspaceId: value(env, "PRD_WORKSPACE_ID"),
    agentSlug: value(env, "PRD_AGENT_SLUG"),
    agentWorkspaceId: value(env, "PRD_AGENT_WORKSPACE_ID"),
    cli: value(env, "PRD_CLI"),
    trace: value(env, "PRD_TRACE") === "1",
  }
}

/**
 * Storage-state file of one role for one origin. Lives with the other e2e
 * cookie jars (owner-only directory, file mode 0600), never in the output
 * directory, so evidence can be shared without session cookies.
 */
export function storageStatePath(origin: string, role: PrdRole): string {
  const dir = path.join(os.homedir(), ".crewship", "e2e-auth")
  fs.mkdirSync(dir, { recursive: true, mode: 0o700 })
  const tag = createHash("sha256").update(origin).digest("hex").slice(0, 12)
  return path.join(dir, `prd-acceptance-${role}-${tag}.json`)
}

/** Paths inside the output directory shared by global setup, fixtures and reporter. */
export function outputPaths(outputDir: string) {
  return {
    run: path.join(outputDir, "run.json"),
    rows: path.join(outputDir, "rows"),
    evidence: path.join(outputDir, "evidence"),
    playwright: path.join(outputDir, "playwright"),
  }
}
