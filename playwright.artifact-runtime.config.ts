import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e/artifact-runtime', testMatch: '*.spec.ts',
  workers: 1, fullyParallel: false, retries: 0, forbidOnly: !!process.env.CI,
  timeout: 20000, expect: { timeout: 5000 },
  reporter: [['list'], ['json', { outputFile: 'test-results/artifact-runtime.json' }]],
  use: { ignoreHTTPSErrors: true, actionTimeout: 5000, navigationTimeout: 7000 },
  projects: [
    // Full Chromium's site isolation differs from the default headless shell.
    { name: 'chromium', use: { browserName: 'chromium', channel: 'chromium' } },
    // The production preparation component deliberately supports Chromium only.
    { name: 'firefox', testIgnore: 'component.spec.ts', use: { browserName: 'firefox' } },
    { name: 'webkit', testIgnore: 'component.spec.ts', use: { browserName: 'webkit' } },
  ],
})
