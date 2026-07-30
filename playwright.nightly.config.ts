import { defineConfig, devices } from "@playwright/test"
import { storageFilePath } from "./e2e/global-setup"
import { baseURL, skipWebServer, webServerConfig } from "./playwright.shared"

/**
 * Nightly Playwright config — used by .github/workflows/nightly-e2e.yml.
 *
 * Why a third config instead of reusing playwright.config.ts:
 *
 *   playwright.config.ts carries a `testIgnore` list (smoke,
 *   manual-crews-walkthrough, crews-unification, full-integration,
 *   edge-cases, visual, mobile-crews) added when the /crews redesign
 *   deleted the route trees those specs walk. Positional filters on the
 *   CLI do NOT override testIgnore, so there is no way to ask the main
 *   config to run them — and "run them so we can see how stale they
 *   are" is exactly what the nightly drift job needs to do.
 *
 * This config therefore carries NO testIgnore. The workflow decides what
 * runs by passing explicit spec paths, in two buckets:
 *
 *   - the GATE bucket, verified green, hard-fails the nightly;
 *   - the DRIFT bucket, known-stale, reported into one tracking issue
 *     and never allowed to fail the workflow.
 *
 * The workflow also asserts that GATE + DRIFT + a small explicitly
 * excluded list covers every e2e/*.spec.ts file, so a newly added spec
 * cannot end up running nowhere.
 *
 * Deliberately NOT changed from the main config: testDir, globalSetup,
 * the storageState cookie jar, and the screenshot/trace policy. A
 * nightly that authenticates differently from the PR suite would be
 * debugging a different application.
 *
 * onboarding-wizard.spec.ts is never selected by the workflow: it needs
 * a NEVER-bootstrapped DB and bypasses globalSetup. It has its own
 * config (playwright.fresh.config.ts) and its own job in
 * e2e-devcontainer.yml — leave both alone.
 */
export default defineConfig({
  testDir: "./e2e",
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // One retry, not the main config's two. This suite is run to learn
  // which specs are green, and a genuinely-broken spec that burns three
  // 15s timeouts per assertion turns a 20-minute nightly into an hour.
  // One retry still absorbs the ordinary flake.
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI
    ? [["github"], ["json", { outputFile: "playwright-nightly-report.json" }], ["html", { open: "never" }]]
    : "list",

  use: {
    baseURL,
    storageState: storageFilePath(),
    screenshot: "only-on-failure",
    // retain-on-failure, not on-first-retry: the point of this run is
    // post-mortem on a machine nobody was watching, so every failure
    // must leave a trace even when the retry also fails.
    trace: "retain-on-failure",
    video: "retain-on-failure",
  },

  expect: {
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.01,
      animations: "disabled",
    },
  },

  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],

  ...(skipWebServer ? {} : { webServer: webServerConfig }),
})
