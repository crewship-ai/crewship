import { defineConfig } from "@playwright/test"

// Craft package gate: scripts/ci/craft-package-gate.sh starts the server and runs
// this config under xvfb. Full (headed) Chromium with software GL, because the
// Word and Sheet editors draw through WebGL; one worker, no retries.
export default defineConfig({
  testDir: "./e2e/craft-packages",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  timeout: 6 * 60_000,
  outputDir: process.env.CRAFT_GATE_OUTPUT ? `${process.env.CRAFT_GATE_OUTPUT}/playwright` : "test-results/craft-packages",
  reporter: [["list"], ["json", { outputFile: `${process.env.CRAFT_GATE_OUTPUT ?? "test-results/craft-packages"}/report.json` }]],
  use: {
    headless: false,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    launchOptions: { args: ["--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist"] },
  },
  projects: [{ name: "chromium" }],
})
