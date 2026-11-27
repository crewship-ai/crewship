import { defineConfig } from "@playwright/test"

// The client's path through a Sheet (e2e/sheet-client), against a throwaway
// server that scripts/ci/sheet-client-e2e.sh starts. Headed
// Chromium under xvfb with software GL (the Sheet editor draws through WebGL),
// one worker, no retries: a failure is a finding, not noise to retry away.
export default defineConfig({
  testDir: "./e2e/sheet-client",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  timeout: 10 * 60_000,
  outputDir: process.env.CRAFT_GATE_OUTPUT ? `${process.env.CRAFT_GATE_OUTPUT}/playwright` : "test-results/sheet-client",
  reporter: [
    ["list"],
    ["json", { outputFile: `${process.env.CRAFT_GATE_OUTPUT ?? "test-results/sheet-client"}/report.json` }],
    ["html", { outputFolder: `${process.env.CRAFT_GATE_OUTPUT ?? "test-results/sheet-client"}/html`, open: "never" }],
  ],
  use: {
    headless: false,
    screenshot: "on",
    trace: "retain-on-failure",
    launchOptions: { args: ["--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--ignore-gpu-blocklist"] },
  },
  projects: [{ name: "chromium" }],
})
