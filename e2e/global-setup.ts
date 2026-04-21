import { chromium, FullConfig } from "@playwright/test"
import * as fs from "fs"
import * as os from "os"
import * as path from "path"

/**
 * Global setup — logs in ONCE at the start of the whole test run and
 * writes the resulting NextAuth session cookies to a shared
 * storageState file. Every test in every worker then loads that state
 * instead of re-calling /api/auth/callback/credentials.
 *
 * This avoids the NextAuth credentials rate limit (kicks in around 5
 * hits within a minute and persists ~60s). Per-worker fixtures still
 * hit the limit when there are more than a handful of specs because
 * Playwright's outputDir clean + context teardown between tests can
 * invalidate cached state.
 */
export default async function globalSetup(config: FullConfig) {
  const email = process.env.E2E_EMAIL
  const password = process.env.E2E_PASSWORD
  if (!email || !password) {
    throw new Error("E2E_EMAIL and E2E_PASSWORD environment variables must be set for e2e")
  }

  // Fallback baseURL follows the multi-instance convention
  // (CLAUDE.md: "crewship_N dirs → Go :8080+N, Next.js :3010+N").
  // NEXT_PORT takes precedence when set by the caller; otherwise
  // default to 3010 — NOT 3001 — so a second instance started via
  // `crewship_2/dev.sh start` doesn't silently authenticate against
  // instance 1's session cookies on shared CI runners.
  const baseURL =
    (config.projects[0]?.use?.baseURL as string) ||
    `http://localhost:${process.env.NEXT_PORT || "3010"}`
  const browser = await chromium.launch()
  try {
    const ctx = await browser.newContext({ baseURL })
    const page = await ctx.newPage()

    await page.goto("/login")
    await page.waitForLoadState("networkidle")

    const csrfToken = await page.evaluate(async () => {
      const res = await fetch("/api/auth/csrf")
      return (await res.json()).csrfToken as string
    })

    const result = await page.evaluate(
      async ({ email, password, csrf }) => {
        const res = await fetch("/api/auth/callback/credentials", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email, password, csrfToken: csrf, redirect: "false" }),
        })
        return res.json()
      },
      { email, password, csrf: csrfToken }
    )
    if (result?.error) {
      throw new Error(`global-setup login failed: ${result.error}`)
    }

    const file = storageFilePath()
    await ctx.storageState({ path: file })
    // Lock the cookie jar to owner-read/write only. storageState is
    // an authenticated NextAuth session token — on a shared CI
    // runner or multi-user dev VM the default 0644 world-readable
    // permission would let any local account hijack the session
    // without needing the user's password. chmod is a no-op on
    // Windows but Playwright runners in practice are Linux/macOS.
    try {
      fs.chmodSync(file, 0o600)
    } catch {
      // Non-fatal — the file still exists with defaults. Best-effort
      // hardening; on the rare FS where chmod fails (e.g. some
      // network mounts) the test run should still complete.
    }
  } finally {
    // finally so a thrown fetch/CSRF failure doesn't leak the
    // chromium process — the next CI retry would otherwise pile
    // up orphaned headless shells until the runner OOMs.
    await browser.close()
  }
}

// storageFilePath namespaces the cookie jar per instance so concurrent
// `crewship_N` dirs or different ports don't overwrite each other's
// auth. CREWSHIP_INSTANCE_ID comes from the multi-instance convention
// in CLAUDE.md; falls back to the port so local single-instance runs
// keep a stable filename.
//
// The parent dir (~/.crewship/e2e-auth) is created eagerly with mode
// 0o700 so an attacker on the same host can't pre-plant a symlink at
// the deterministic target path and trick Playwright into writing
// the session cookie to an attacker-controlled location. This closes
// the symlink-race window that existed when we wrote directly into
// os.tmpdir() — that directory is world-readable/writable by design
// on most Linux distros.
export function storageFilePath(): string {
  const instance = process.env.CREWSHIP_INSTANCE_ID || process.env.NEXT_PORT || "default"
  const authDir = path.join(os.homedir(), ".crewship", "e2e-auth")
  fs.mkdirSync(authDir, { recursive: true, mode: 0o700 })
  return path.join(authDir, `crewship-e2e-auth-${instance}.json`)
}
