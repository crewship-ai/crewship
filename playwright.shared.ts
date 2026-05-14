// Shared bits between playwright.config.ts (main suite) and
// playwright.fresh.config.ts (onboarding wizard against fresh DB).
// Keep this file minimal — extract here only what's literally
// identical between the two configs; everything else stays in the
// config that owns it.

const nextPort = process.env.NEXT_PORT || "3001"
const externalBaseURL = (process.env.PLAYWRIGHT_BASE_URL ?? "").trim()

export const baseURL = externalBaseURL || `http://localhost:${nextPort}`

// Only skip the local web server when an *actual* external URL is
// provided. An empty string used to set skipWebServer=true while
// baseURL fell back to localhost, leaving tests pointed at a port
// with nothing listening.
export const skipWebServer = externalBaseURL.length > 0

export const webServerConfig = {
  command: `pnpm dev --port ${nextPort}`,
  url: `http://localhost:${nextPort}`,
  reuseExistingServer: true,
  timeout: 60_000,
  stdout: "ignore" as const,
  stderr: "pipe" as const,
}
