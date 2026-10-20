import { defineConfig, devices } from "@playwright/test"

/**
 * Document flows that need a real, freshly bootstrapped backend with the
 * Documents engine (scripts/throwaway-server.sh --documents). Run:
 *
 *   scripts/throwaway-server.sh run NAME --documents --binary PATH -- \
 *     pnpm exec playwright test --config=playwright.documents.config.ts
 *
 * (--env CREWSHIP_SKIP_SIDECAR=1 and --env CREWSHIP_ALLOWED_ORIGINS=http://localhost:3391,http://localhost:3392 are
 * needed: no sidecar binary beside a plain `go build`, and the browser origin is the dev frontend.)
 *
 * The run command exports CREWSHIP_SERVER; this config starts two development
 * frontends that proxy /api to that port, one with the review flag off and one on.
 */
const server = process.env.CREWSHIP_SERVER ?? ""
const goPort = /:(\d{2,5})\/?$/.exec(server)?.[1] ?? "8080"
// Two frontends on one backend: the default one (flag off, where compare must
// already work) and one with the review flag on (revert and proposals).
const webPort = process.env.DOCUMENTS_E2E_WEB_PORT && /^[1-9]\d{3,4}$/.test(process.env.DOCUMENTS_E2E_WEB_PORT) ? process.env.DOCUMENTS_E2E_WEB_PORT : "3391"
const flaggedPort = String(Number(webPort) + 1)

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
  webServer: [
    { port: webPort, flag: "false" },
    { port: flaggedPort, flag: "true" },
  ].map(({ port, flag }) => ({
    command: `pnpm dev --port ${port}`,
    url: `http://localhost:${port}`,
    reuseExistingServer: false,
    timeout: 240_000,
    stdout: "ignore" as const,
    stderr: "pipe" as const,
    env: { NEXT_PUBLIC_GO_PORT: goPort, NEXT_PUBLIC_DOCUMENT_REVIEW_ACTIONS: flag, NEXT_DIST_DIR: `.next-${port}` },
  })),
})
