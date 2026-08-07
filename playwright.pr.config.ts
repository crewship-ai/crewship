import { defineConfig, devices } from "@playwright/test"
import { storageFilePath } from "./e2e/global-setup"

// The PR browser net is intentionally five no-provider checks. Provider-backed
// chat send/receive and the chat-shell runtime suite remain in nightly-e2e.yml; the
// exclusion is explicit so a green PR cannot be mistaken for full browser
// coverage.
export default defineConfig({
  testDir: "./e2e",
  testMatch: ["pr-contract.spec.ts"],
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // A retry would rerun the global auth snapshot after a mutation and turn a
  // useful failure into a misleading NextAuth rate-limit failure.
  retries: 0,
  workers: 1,
  reporter: process.env.CI ? [["github"], ["json", { outputFile: "playwright-pr-report.json" }]] : "list",
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:8080",
    storageState: storageFilePath(),
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    video: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  grep: /PR browser contract subset/,
})
