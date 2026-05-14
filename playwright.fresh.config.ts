import { defineConfig, devices } from "@playwright/test"

/**
 * Fresh-DB Playwright config — runs the onboarding-wizard spec
 * against an instance that has NEVER been bootstrapped. Deliberately
 * separate from playwright.config.ts because:
 *
 *   - The main config's globalSetup logs in as a seeded demo user
 *     and hard-fails when that user doesn't exist. Fresh DB by
 *     definition has no users.
 *   - The main config's storageState carries that demo-user session;
 *     loading it would let the wizard see an "authenticated visitor"
 *     and skip the bootstrap step the spec is designed to exercise.
 *
 * Used by the e2e-devcontainer nightly workflow after `crewship start`
 * brings up a clean instance. Local devs can run it the same way
 * against any freshly-started server:
 *
 *   PLAYWRIGHT_BASE_URL=http://localhost:8080 \
 *     pnpm exec playwright test --config=playwright.fresh.config.ts
 */

const externalBaseURL = (process.env.PLAYWRIGHT_BASE_URL ?? "").trim()
const nextPort = process.env.NEXT_PORT || "3001"
const baseURL = externalBaseURL || `http://localhost:${nextPort}`
// Mirror the main config: only skip the webServer when an actual
// external URL is provided. An empty string used to set
// skipWebServer=true while baseURL fell back to localhost, leaving
// tests pointed at a port with nothing listening.
const skipWebServer = externalBaseURL.length > 0

export default defineConfig({
  testDir: "./e2e",
  testMatch: ["**/onboarding-wizard.spec.ts"],
  // No globalSetup — see header. Each spec brings its own state.
  fullyParallel: false, // serial inside the spec; this is belt + braces
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1, // bootstrap is a one-shot; never parallelise
  reporter: process.env.CI ? "github" : "list",

  use: {
    baseURL,
    // Explicitly empty storage — no inherited cookies from the main
    // suite's demo-user session.
    storageState: { cookies: [], origins: [] },
    screenshot: "only-on-failure",
    trace: "on-first-retry",
  },

  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],

  ...(skipWebServer
    ? {}
    : {
        webServer: {
          command: `pnpm dev --port ${nextPort}`,
          url: `http://localhost:${nextPort}`,
          reuseExistingServer: true,
          timeout: 60_000,
          stdout: "ignore" as const,
          stderr: "pipe" as const,
        },
      }),
})
