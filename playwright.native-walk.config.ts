import { defineConfig, type ReporterDescription } from "@playwright/test"
import prdConfig from "./playwright.prd-acceptance.config"

// Native walk against a deployed origin, kept out of the PRD suite. Run it with
// `pnpm acceptance:native-walk`: "Native walk" in docs/development/prd-acceptance.md.
// The walk launches its own full Chromium (NATIVE_WALK_CHROME). finished.json marks a
// run Playwright ended itself; only a run with PRD_RUN_ID writes it, so the runner's
// `--list` (started without one) cannot leave a marker behind.
// rows-reporter.ts wraps the PRD reporter and proves it completed (reporter.json).
const reporter: ReporterDescription[] = [["list"], ["./e2e/acceptance/native-walk/rows-reporter.ts"]]
if (process.env.PRD_RUN_ID?.trim()) reporter.push(["./e2e/acceptance/native-walk/finish-reporter.ts"])

export default defineConfig({ ...prdConfig, testDir: "./e2e/acceptance/native-walk", reporter })
