import { defineConfig } from "@playwright/test"
import prdConfig from "./playwright.prd-acceptance.config"

// Targeted pre-RC proof with a throwaway server, kept out of the PRD suite. Run it
// with `pnpm acceptance:draft-revocation`: "Draft revocation proof" in
// docs/development/prd-acceptance.md.
export default defineConfig({ ...prdConfig, testDir: "./e2e/acceptance/draft-revocation" })
