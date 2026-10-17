import { test, expect } from "@playwright/test"

// Harbor 2 chrome and global states (#3421): deep link through login, the
// 404 inside and outside a session, and the reconnecting banner at phone
// width. Signed-in tests use the suite's stored session (global-setup.ts).

const EMAIL = process.env.E2E_EMAIL ?? "admin-baseline@crewship.local"
const PASSWORD = process.env.E2E_PASSWORD ?? "rbac-demo-1234"

test.describe("signed out", () => {
  test.use({ storageState: { cookies: [], origins: [] } })

  test("deep link while signed out → login → back to the link", async ({ page }) => {
    await page.goto("/routines?tab=library")
    await expect(page).toHaveURL(/\/login\?redirect=%2Froutines%3Ftab%3Dlibrary/, { timeout: 15_000 })
    await page.getByLabel(/email/i).fill(EMAIL)
    await page.getByLabel(/password/i).fill(PASSWORD)
    await page.getByRole("button", { name: /sign in|log in/i }).click()
    await expect(page).toHaveURL(/\/routines\?tab=library$/, { timeout: 15_000 })
  })

  test("an unknown page is the bare not-found state", async ({ page }) => {
    await page.goto("/does-not-exist")
    await expect(page.getByText("This page doesn’t exist")).toBeVisible()
    await expect(page.locator("main.bare-state")).toBeVisible()
    await expect(page.locator("[data-slot=sidebar-container]")).toHaveCount(0)
    await expect(page.getByRole("link", { name: "Go to Dashboard" })).toHaveAttribute("href", "/")
  })
})

test.describe("signed in", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("an unknown page renders inside the shell with Search", async ({ page }) => {
    await page.goto("/does-not-exist")
    await expect(page.getByText("This page doesn’t exist")).toBeVisible()
    await expect(page.locator("[data-slot=sidebar-container]")).toBeVisible()
    await page.getByRole("button", { name: "Search" }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
  })

  test("the first paint is the frame, never a full-page spinner", async ({ page }) => {
    await page.goto("/issues")
    await expect(page.locator(".animate-spin").filter({ visible: true })).toHaveCount(0)
    await expect(page.locator("header").first()).toBeVisible()
  })
})

test.describe("phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("offline 390: one banner, text wraps, Retry ≥ 44 px", async ({ page }) => {
    // Every realtime socket closes at once, so the shell loses its connection.
    await page.routeWebSocket(/\/ws/, (ws) => { ws.close() })
    await page.goto("/issues")
    const banner = page.locator('[data-state="offline"]')
    await expect(banner).toBeVisible({ timeout: 15_000 })
    await expect(banner).toHaveCount(1)
    await expect(page.getByText(/Reconnecting…|Connection lost\./)).toHaveCount(1)
    const retry = banner.getByRole("button", { name: "Retry now" })
    const box = await retry.boundingBox()
    expect(box!.height).toBeGreaterThanOrEqual(44)
    const b = await banner.boundingBox()
    expect(b!.width).toBeLessThanOrEqual(390)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
  })
})
