import { defineConfig } from "@playwright/test"

// Isolated static-export acceptance for #2861 part 1: a restricted member's
// shell must not open a realtime socket, retry /ws-token or send any request
// the server's restricted allowlist (internal/api/restricted_access.go)
// refuses. Every API response is a page.route fixture; no live
// authentication, seeded server or provider request is used, so this runs on
// every PR alongside playwright.reveal/pool-ui — the nightly GATE_SPECS
// classification alone left PR #2874 executing the spec zero times.
export default defineConfig({
  testDir: "./e2e", testMatch: "restricted-member-shell.spec.ts", workers: 1,
  reporter: "list", timeout: 60000, retries: 0,
  use: {
    baseURL: "http://127.0.0.1:3912", headless: true,
    trace: "retain-on-failure", screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
  webServer: { command: "node e2e/serve-restricted-ui.mjs", url: "http://127.0.0.1:3912/routines", reuseExistingServer: false },
})
