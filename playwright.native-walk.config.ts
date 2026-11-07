import { defineConfig } from "@playwright/test"
import prdConfig from "./playwright.prd-acceptance.config"

// Native walk against a deployed origin, kept out of the PRD suite. Run it with
// `pnpm acceptance:native-walk`: "Native walk" in docs/development/prd-acceptance.md.
// The walk launches its own full Chromium (NATIVE_WALK_CHROME); finished.json marks
// a run Playwright ended itself.
export default defineConfig({
  ...prdConfig,
  testDir: "./e2e/acceptance/native-walk",
  reporter: [["list"], ["./e2e/acceptance/prd/reporter.ts"], ["./e2e/acceptance/native-walk/finish-reporter.ts"]],
})
