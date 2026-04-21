import { chromium, FullConfig } from "@playwright/test"
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

  const baseURL = (config.projects[0]?.use?.baseURL as string) || "http://localhost:3001"
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

    await ctx.storageState({ path: storageFilePath() })
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
export function storageFilePath(): string {
  const instance = process.env.CREWSHIP_INSTANCE_ID || process.env.NEXT_PORT || "default"
  return path.join(os.tmpdir(), `crewship-e2e-auth-${instance}.json`)
}
