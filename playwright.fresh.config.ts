import { defineConfig, devices } from "@playwright/test"
import { baseURL, skipWebServer, webServerConfig } from "./playwright.shared"

/**
 * Fresh-DB Playwright config — runs the onboarding-wizard spec
 * against an instance that has NEVER been bootstrapped. Deliberately
 * separate from playwright.config.ts because:
 *
 *   - The main config's globalSetup logs in as a seeded demo user
 *     and hard-fails when that user doesn't exist.
 *   - The main config's storageState would carry that demo-user
 *     session, letting the wizard skip the bootstrap step we exist
 *     to test.
 *
 * Used by the e2e-devcontainer nightly workflow. Local devs run the
 * same way against any freshly-started server:
 *
 *   PLAYWRIGHT_BASE_URL=http://localhost:8080 \
 *     pnpm exec playwright test --config=playwright.fresh.config.ts
 */
export default defineConfig({
  testDir: "./e2e",
  testMatch: ["**/onboarding-wizard.spec.ts"],
  // No globalSetup — see header. Each spec brings its own state.
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // Bootstrap is a one-shot — POST /api/v1/bootstrap returns 403 after
  // the first user exists. Retries can't help and they turn real
  // failures into 3× the noise, so disable them regardless of env.
  retries: 0,
  workers: 1,
  reporter: process.env.CI ? "github" : "list",

  use: {
    baseURL,
    storageState: { cookies: [], origins: [] },
    screenshot: "only-on-failure",
    // retries: 0 above means on-first-retry never fires; retain-on-
    // failure keeps the trace whenever a test fails so debugging
    // doesn't depend on a retry that won't happen.
    trace: "retain-on-failure",
  },

  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],

  ...(skipWebServer ? {} : { webServer: webServerConfig }),
})
