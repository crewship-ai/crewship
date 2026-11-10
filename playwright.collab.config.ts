import { defineConfig, devices } from "@playwright/test"

/**
 * Two-identity collaboration acceptance (#5144). Runs only against a disposable
 * server started by scripts/collab-throwaway.sh, which sets PLAYWRIGHT_BASE_URL,
 * CREWSHIP_BIN and the owner's CREWSHIP_CONFIG. No globalSetup and no shared
 * storageState: every scenario signs in its own accounts in separate contexts.
 * Serial on purpose: scenarios restart the server and revoke access.
 */
export default defineConfig({
  testDir: "./e2e",
  testMatch: ["collab-*.manual.ts"],
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: process.env.CI ? [["list"], ["json", { outputFile: "playwright-collab-report.json" }]] : "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL: process.env.PLAYWRIGHT_BASE_URL,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
})
