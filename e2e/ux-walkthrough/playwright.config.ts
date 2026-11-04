import { defineConfig } from "@playwright/test"

// Human-style UX walkthrough of Artifacts and the native Word/Sheet editors on a
// deployed origin. Not part of CI. Run one browser at a time:
//   UX_OUT=<dir> UX_ORIGIN=https://… crewship-heavy pnpm exec playwright test -c e2e/ux-walkthrough/playwright.config.ts
export default defineConfig({
  testDir: ".",
  testMatch: "**/*.walk.ts",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 15 * 60_000,
  outputDir: process.env.UX_OUT ? `${process.env.UX_OUT}/playwright` : "test-results/ux-walkthrough",
  reporter: [["list"]],
  use: { actionTimeout: 15_000, navigationTimeout: 60_000, screenshot: "off", trace: "off", locale: "cs-CZ", timezoneId: "Europe/Prague" },
})
