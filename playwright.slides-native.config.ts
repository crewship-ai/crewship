import { defineConfig } from "@playwright/test"

// Real native components and DOM interactions. No server/API/storage/model run.
export default defineConfig({
  testDir: "./e2e/slides-native", testMatch: "*.spec.ts", workers: 1, retries: 0,
  reporter: "list", timeout: 45000,
  use: { baseURL: "http://127.0.0.1:3928", screenshot: "only-on-failure", trace: "retain-on-failure" },
  projects: [
    { name: "desktop", use: { browserName: "chromium", viewport: { width: 1280, height: 900 } } },
    { name: "touch", use: { browserName: "chromium", viewport: { width: 390, height: 844 }, hasTouch: true } },
  ],
  webServer: { command: "pnpm exec vite --config e2e/slides-native/vite.config.ts", url: "http://127.0.0.1:3928", reuseExistingServer: false },
})
