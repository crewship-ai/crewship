import { defineConfig, devices } from "@playwright/test"

/**
 * Document flows that need a real, freshly bootstrapped backend with the
 * Documents engine (scripts/throwaway-server.sh --documents). Run:
 *
 *   scripts/throwaway-server.sh run NAME --documents --binary PATH -- \
 *     pnpm exec playwright test --config=playwright.documents.config.ts
 *
 * (--env CREWSHIP_SKIP_SIDECAR=1 and --env CREWSHIP_ALLOWED_ORIGINS=http://localhost:3391 are
 * needed: no sidecar binary beside a plain `go build`, and the browser origin is the dev frontend.)
 *
 * The run command exports CREWSHIP_SERVER; this config starts a development
 * frontend that proxies /api to that port, with the review flag on.
 */
const server = process.env.CREWSHIP_SERVER ?? ""
const goPort = /:(\d{2,5})\/?$/.exec(server)?.[1] ?? "8080"
const webPort = process.env.DOCUMENTS_E2E_WEB_PORT && /^[1-9]\d{3,4}$/.test(process.env.DOCUMENTS_E2E_WEB_PORT) ? process.env.DOCUMENTS_E2E_WEB_PORT : "3391"

export default defineConfig({
  testDir: "./e2e",
  testMatch: ["**/document-history-compare.spec.ts"],
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  timeout: 120_000,
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: `http://localhost:${webPort}`,
    storageState: { cookies: [], origins: [] },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: `pnpm dev --port ${webPort}`,
    url: `http://localhost:${webPort}`,
    reuseExistingServer: false,
    timeout: 240_000,
    stdout: "ignore",
    stderr: "pipe",
    env: { NEXT_PUBLIC_GO_PORT: goPort, NEXT_PUBLIC_DOCUMENT_REVIEW_ACTIONS: "true" },
  },
})
