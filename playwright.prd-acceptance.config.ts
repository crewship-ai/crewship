import { defineConfig, devices } from "@playwright/test"
import { outputPaths, readPrdEnv, storageStatePath } from "./e2e/acceptance/prd/env"

// PRD acceptance suite (docs/development/prd-acceptance.md). Run it through
// `pnpm acceptance:prd`, which sets PRD_MODE/PRD_ORIGIN/PRD_OUTPUT_DIR and
// prints the requirement table. No webServer: quick mode targets an already
// running test server (CI's ephemeral one), full mode a deployed origin.
const prd = readPrdEnv()

export default defineConfig({
  testDir: "./e2e/acceptance/prd",
  testMatch: "**/*.acceptance.ts",
  globalSetup: "./e2e/acceptance/prd/global-setup.ts",
  // Scenarios mutate shared server state (locks, versions, quotas); run serially.
  fullyParallel: false,
  workers: 1,
  // A retry would hide the flake this suite exists to expose.
  retries: 0,
  forbidOnly: true,
  timeout: prd.mode === "full" ? 20 * 60_000 : 3 * 60_000,
  outputDir: outputPaths(prd.outputDir).playwright,
  reporter: [["list"], ["./e2e/acceptance/prd/reporter.ts"]],
  use: {
    ...devices["Desktop Chrome"],
    baseURL: prd.origin,
    storageState: storageStatePath(prd, "owner"),
    screenshot: "only-on-failure",
    // Traces record request bodies, including logins of provisioned people.
    trace: prd.trace ? "retain-on-failure" : "off",
  },
})
