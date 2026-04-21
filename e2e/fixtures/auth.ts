import { test as base, expect } from "@playwright/test"

// Auth is handled once in e2e/global-setup.ts, which writes a shared
// storageState file pointed at by playwright.config.ts → use.storageState.
// This fixture only lands the page on "/" so every spec starts on an
// authenticated dashboard without repeating the nav step. Kept as a
// fixture (instead of a test.beforeEach) so callers still get the
// typed `page` injection pattern and we can extend later without
// touching every spec.
export const test = base.extend({
  page: async ({ page }, use) => {
    await page.goto("/")
    await page.waitForLoadState("domcontentloaded")
    await use(page)
  },
})

export { expect }
