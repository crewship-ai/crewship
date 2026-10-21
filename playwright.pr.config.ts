import { defineConfig, devices } from "@playwright/test"
import { storageFilePath } from "./e2e/global-setup"

// The PR browser net covers provider-free UI contracts, including the real HTTP
// Incoming endpoint lifecycle. Provider-backed
// chat send/receive and the chat-shell runtime suite remain in nightly-e2e.yml; the
// exclusion is explicit so a green PR cannot be mistaken for full browser
// coverage.
//
// Harbor 2 specs selected here hold on ci.yml's ephemeral `crewship seed
// --offline-demo` instance signed in as its only user (demo@crewship.ai, the
// workspace OWNER): they stub the API they assert on or read only what every
// seeded install has, none of them writes, and each was run repeatedly green
// before being listed (retries are 0 here). Steps that need another account
// skip and name it. Every Harbor 2 spec, these included, also runs in the
// HARBOR2_SPECS job of nightly-e2e.yml, which seeds the RBAC baseline users,
// signs in as a non-owner ADMIN and allows writes. Promote a spec from there
// to this list only after it has been measured stable on that footing.
export const HARBOR2_PR_SPECS = [
  "harbor2-shell.spec.ts",
  "harbor2-onboarding.spec.ts",
  "harbor2-auth.spec.ts",
  "harbor2-legacy-links.spec.ts",
  "vault-h2.spec.ts",
  "calendar-h2.spec.ts",
]

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")

export default defineConfig({
  testDir: "./e2e",
  testMatch: ["pr-contract.spec.ts", "shared-issue-search-retry.spec.ts", "known-project-manage.spec.ts", "resource-presets.spec.ts", ...HARBOR2_PR_SPECS],
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // A retry would rerun the global auth snapshot after a mutation and turn a
  // useful failure into a misleading NextAuth rate-limit failure.
  retries: 0,
  workers: 1,
  reporter: process.env.CI ? [["github"], ["json", { outputFile: "playwright-pr-report.json" }]] : "list",
  use: {
    baseURL: process.env.PLAYWRIGHT_BASE_URL ?? "http://localhost:8080",
    storageState: storageFilePath(),
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    video: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  // pr-contract.spec.ts by title, as before; each Harbor 2 spec as a whole
  // (grep is tested against "project file describe title", so the file name
  // selects every test in it).
  grep: [
    /PR browser contract subset|explicit preset consent and stale recovery/,
    new RegExp(`(^|\\s)(${HARBOR2_PR_SPECS.map(escape).join("|")})(\\s|$)`),
  ],
})
