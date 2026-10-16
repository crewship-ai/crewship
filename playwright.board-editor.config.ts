import { defineConfig } from '@playwright/test'

// Real editor, isolated fixture and deterministic test store. This proves the
// public editor API contract, not production authorization or persistence.
export default defineConfig({
  testDir: './e2e/board-editor', testMatch: '*.spec.ts',
  workers: 1, fullyParallel: false, retries: 0, forbidOnly: !!process.env.CI,
  timeout: 45000, expect: { timeout: 10000 },
  outputDir: 'test-results/board-editor',
  reporter: [['list'], ['json', { outputFile: 'test-results/board-editor.json' }]],
  use: { actionTimeout: 10000, navigationTimeout: 20000, trace: 'retain-on-failure' },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium', channel: 'chromium' } },
    { name: 'firefox', use: { browserName: 'firefox' } },
    { name: 'webkit', use: { browserName: 'webkit' } },
  ],
})
