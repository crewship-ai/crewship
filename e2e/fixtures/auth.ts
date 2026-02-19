import { test as base, expect } from "@playwright/test"

export const test = base.extend({
  page: async ({ browser }, use) => {
    const context = await browser.newContext()
    const page = await context.newPage()

    // Login via API (avoids React hydration timing issues with form fill)
    await page.goto("/login")
    await page.waitForLoadState("networkidle")

    const csrfToken = await page.evaluate(async () => {
      const res = await fetch("/api/auth/csrf")
      const data = await res.json()
      return data.csrfToken
    })

    const email = process.env.E2E_EMAIL
    const password = process.env.E2E_PASSWORD
    if (!email || !password) {
      throw new Error("E2E_EMAIL and E2E_PASSWORD environment variables must be set")
    }

    const loginResult = await page.evaluate(
      async ({ email, password, csrf }) => {
        const res = await fetch("/api/auth/callback/credentials", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ email, password, csrfToken: csrf, redirect: "false" }),
        })
        return res.json()
      },
      { email, password, csrf: csrfToken }
    )

    if (loginResult.error) {
      throw new Error(`Login failed: ${loginResult.error}`)
    }

    await page.goto("/")
    await page.waitForLoadState("domcontentloaded")

    await use(page)
    await context.close()
  },
})

export { expect }
