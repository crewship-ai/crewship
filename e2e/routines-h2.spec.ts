import { test, expect } from "@playwright/test"

// Routines on the Harbor 2 shell: the shared top bar carries the breadcrumb
// and the page actions, the shared SecondaryPanel carries Views first and the
// routines grouped by crew.

const TIMEOUT = 20_000

test("R01 panel order Views→crews, keys 1-5 switch views and keep ?view=", async ({ page }) => {
  await page.goto("/routines")
  const panel = page.locator('[data-slot="secondary-panel"][data-panel-id="routines"]')
  await expect(panel).toBeVisible({ timeout: TIMEOUT })
  const views = panel.getByRole("group", { name: "Views" })
  await expect(views.getByRole("button")).toHaveText([/^Overview/, /^Needs you/, /^Running now/, /^All runs/, /^Drafts/])
  // Views come before any crew group.
  const first = await panel.getByRole("group").first().getAttribute("aria-label")
  expect(first).toBe("Views")

  await page.locator("body").press("2")
  await expect(page).toHaveURL(/view=needs/)
  await page.locator("body").press("3")
  await expect(page).toHaveURL(/view=live/)
  await page.locator("body").press("4")
  await expect(page).toHaveURL(/view=runs/)
  await page.locator("body").press("5")
  await expect(page).toHaveURL(/view=drafts/)
  await page.reload()
  await expect(page.getByRole("heading", { name: "Drafts", level: 2, exact: true })).toBeVisible({ timeout: TIMEOUT })
  await page.locator("body").press("1")
  await expect(page).not.toHaveURL(/view=/)
})

test("R01 C opens New routine for a Manager or above", async ({ page }) => {
  await page.goto("/routines")
  await expect(page.getByRole("button", { name: "New routine" }).first()).toBeEnabled({ timeout: TIMEOUT })
  await page.locator("body").press("c")
  await expect(page.getByRole("dialog", { name: "New routine" })).toBeVisible()
})

test("R01 a Member gets New routine disabled with the reason", async ({ browser, baseURL }) => {
  const email = process.env.E2E_MEMBER_EMAIL
  const password = process.env.E2E_MEMBER_PASSWORD
  test.skip(!email || !password, "set E2E_MEMBER_EMAIL / E2E_MEMBER_PASSWORD to a MEMBER account")
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await context.newPage()
  // Sign in the way global-setup does, and only then open the page: a
  // goto racing the form submit lands back on /login.
  await page.goto(`${baseURL}/login`)
  const result = await page.evaluate(
    async ({ email, password }) => {
      const csrf = (await (await fetch("/api/auth/csrf")).json()).csrfToken as string
      const res = await fetch("/api/auth/callback/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password, csrfToken: csrf, redirect: "false" }),
      })
      return res.json()
    },
    { email: email!, password: password! },
  )
  expect(result?.error, "member sign-in").toBeFalsy()
  await page.goto(`${baseURL}/routines`)
  await expect(page.locator('[data-slot="secondary-panel"][data-panel-id="routines"]')).toBeVisible({ timeout: TIMEOUT })
  const create = page.getByRole("button", { name: "New routine" }).first()
  await expect(create).toBeDisabled({ timeout: TIMEOUT })
  await expect(create).toHaveAccessibleDescription(/Manager or above/)
  await page.locator("body").press("c")
  await expect(page.getByRole("dialog", { name: "New routine" })).toHaveCount(0)
  await context.close()
})

test("R01 no custom aside width (the shared panel owns it)", async ({ page }) => {
  await page.goto("/routines")
  const panel = page.locator('[data-slot="secondary-panel"][data-panel-id="routines"]')
  await expect(panel).toBeVisible({ timeout: TIMEOUT })
  const box = await panel.boundingBox()
  expect(Math.round(box!.width)).toBe(272)
  await expect(page.getByRole("button", { name: "Back to routines" })).toHaveCount(0)
})

test.describe("R01 phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  test("the All routines chip opens the panel sheet", async ({ page }) => {
    await page.goto("/routines")
    await page.getByRole("button", { name: "All routines" }).click()
    const sheet = page.getByRole("dialog", { name: "Routine list" })
    await expect(sheet).toBeVisible()
    await expect(sheet.getByRole("group", { name: "Views" })).toBeVisible()
  })
})
