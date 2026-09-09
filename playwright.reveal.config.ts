import { defineConfig } from "@playwright/test"
export default defineConfig({
  testDir: "./e2e", testMatch: "credential-reveal.spec.ts", workers: 1,
  reporter: "list", timeout: 60000,
  use: { baseURL: "http://127.0.0.1:3914", headless: true },
  webServer: { command: "node e2e/serve-reveal-ui.mjs", url: "http://127.0.0.1:3914/credentials", reuseExistingServer: false },
})
