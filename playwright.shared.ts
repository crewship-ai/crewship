// Shared bits between playwright.config.ts (main suite) and
// playwright.fresh.config.ts (onboarding wizard against fresh DB).
// Keep this file minimal — extract here only what's literally
// identical between the two configs; everything else stays in the
// config that owns it.

// NEXT_PORT is interpolated into webServer.command — Playwright spawns
// that through a shell, so an env value like `3001; rm -rf /` would
// otherwise execute. Reject anything that isn't a plain 1-65535 port
// and fall back to the default. Defensive against accidental shell
// metacharacters in dev env files more than a real attacker (env vars
// are already a trusted input surface), but the check is one line.
const rawPort = process.env.NEXT_PORT
const isValidPort = (s: string) => /^[1-9]\d{0,4}$/.test(s) && Number(s) <= 65535
const nextPort = rawPort && isValidPort(rawPort) ? rawPort : "3001"

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
