import { defineConfig, devices } from "@playwright/test"
import { storageFilePath } from "./e2e/global-setup"

const nextPort = process.env.NEXT_PORT || "3001"

export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? "github" : "html",

  use: {
    baseURL: `http://localhost:${nextPort}`,
    // Every test inherits the cookies written by global-setup, so
    // specs never re-login and never hit the NextAuth rate limit.
    // storageFilePath() namespaces per CREWSHIP_INSTANCE_ID so
    // concurrent instances on the same host don't clobber each
    // other's auth file.
    storageState: storageFilePath(),
    screenshot: "only-on-failure",
    trace: "on-first-retry",
  },

  expect: {
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.01,
      animations: "disabled",
    },
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],

  webServer: {
    command: `pnpm dev --port ${nextPort}`,
    url: `http://localhost:${nextPort}`,
    reuseExistingServer: true,
    timeout: 60_000,
    stdout: "ignore",
    stderr: "pipe",
  },
})
