import { defineConfig, devices } from '@playwright/test'

// Deliberately has no webServer/globalSetup or default localhost target.
// These active acceptance tests require an explicitly configured owned document.
export default defineConfig({
  testDir: './e2e/wordcraft-ce',
  testMatch: '**/*.acceptance.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: true,
  timeout: 120_000,
  outputDir: process.env.WORDCRAFT_CE_OUTPUT_DIR ?? 'test-results/wordcraft-ce',
  reporter: [['list'], ['./e2e/wordcraft-ce/coverage-reporter.ts']],
  use: { ...devices['Desktop Chrome'], screenshot: 'only-on-failure', trace: 'off' },
})
