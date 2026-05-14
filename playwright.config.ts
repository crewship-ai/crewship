import { defineConfig, devices } from "@playwright/test"
import { storageFilePath } from "./e2e/global-setup"
import { baseURL, skipWebServer, webServerConfig } from "./playwright.shared"

export default defineConfig({
  testDir: "./e2e",
  // Specs that walk the deleted /crews/agents/[id]/* and /crews/new
  // route trees are temporarily ignored until rewritten against the
  // new selection-driven canvas. Coverage for the new surfaces lives
  // in crews-redesign.spec.ts.
  // TODO(crews-redesign-canvas-followup): rewrite these specs against
  //   /crews?agent=<slug> + /chat/[slug] flows, then drop testIgnore.
  testIgnore: [
    "**/smoke.spec.ts",
    "**/manual-crews-walkthrough.spec.ts",
    "**/crews-unification.spec.ts",
    "**/full-integration.spec.ts",
    "**/edge-cases.spec.ts",
    "**/visual.spec.ts",
    "**/mobile-crews.spec.ts",
    "**/a11y.spec.ts",
    // onboarding-wizard.spec.ts needs a fresh, NEVER-bootstrapped DB
    // and explicitly bypasses globalSetup's demo-user login. Running
    // it under the main config would either skip silently (already
    // bootstrapped) or false-fail globalSetup (no demo user on a
    // fresh DB). Run via
    //   pnpm exec playwright test --config=playwright.fresh.config.ts
    // instead — wired into the e2e-devcontainer nightly workflow.
    "**/onboarding-wizard.spec.ts",
  ],
  globalSetup: "./e2e/global-setup.ts",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: process.env.CI ? "github" : "html",

  use: {
    baseURL,
    // Every test inherits the cookies written by global-setup, so
    // specs never re-login and never hit the NextAuth rate limit.
    // storageFilePath() namespaces per CREWSHIP_INSTANCE_ID so
    // concurrent instances on the same host don't clobber each
    // other's auth file.
    storageState: storageFilePath(),
    screenshot: "only-on-failure",
    trace: "on-first-retry",
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
