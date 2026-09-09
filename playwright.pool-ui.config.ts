import { defineConfig } from "@playwright/test"

// Isolated static-export acceptance. Every API response is a fixture; no live
// authentication, account creation or provider request is used.
export default defineConfig({
  testDir: "./e2e", testMatch: "provider-pools.spec.ts", workers: 1,
  reporter: "list", timeout: 60000,
  use: { baseURL: "http://127.0.0.1:3913", headless: true },
  webServer: { command: "node e2e/serve-pool-ui.mjs", url: "http://127.0.0.1:3913/credentials", reuseExistingServer: false },
})
