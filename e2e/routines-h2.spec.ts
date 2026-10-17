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

// ---------------------------------------------------------------------------
// The routine page and the run page (Harbor 2 follow-up). These open the
// first routine in the panel, so they need a workspace with at least one
// routine (the demo seed has several).

async function openFirstRoutine(page: import("@playwright/test").Page): Promise<string> {
  await page.goto("/routines")
  const panel = page.locator('[data-slot="secondary-panel"][data-panel-id="routines"]')
  await expect(panel).toBeVisible({ timeout: TIMEOUT })
  const groups = panel.getByRole("group")
  await expect(groups.nth(1)).toBeVisible({ timeout: TIMEOUT })
  await groups.nth(1).getByRole("button").first().click()
  await expect(page).toHaveURL(/slug=/, { timeout: TIMEOUT })
  await expect(page.getByTestId("routine-page-header")).toBeVisible({ timeout: TIMEOUT })
  return new URL(page.url()).searchParams.get("slug")!
}

test("R05 tabs Overview/Steps/Triggers/Settings, Versions only in Full detail, depth persists after reload", async ({ page }) => {
  await openFirstRoutine(page)
  const tabs = page.getByRole("tablist", { name: "Routine" }).getByRole("tab")
  await page.getByRole("radio", { name: "Simple" }).click()
  await expect(tabs).toHaveText([/^Overview/, /^Steps/, /^Triggers/, /^Settings/])
  await page.getByRole("radio", { name: "Full detail" }).click()
  await expect(tabs).toHaveText([/^Overview/, /^Steps/, /^Triggers/, /^Versions/, /^Settings/])
  await page.reload()
  await expect(page.getByRole("radio", { name: "Full detail" })).toHaveAttribute("aria-checked", "true", { timeout: TIMEOUT })
  await page.getByRole("tab", { name: /^Settings/ }).click()
  await expect(page).toHaveURL(/tab=settings/)
  // The page actions live in the top bar, with the ⋯ menu.
  await expect(page.getByRole("button", { name: "Routine options" })).toBeVisible()
  await page.getByRole("radio", { name: "Simple" }).click()
})

test("R05 the ⋯ menu offers Duplicate as draft, never a live copy", async ({ page }) => {
  await openFirstRoutine(page)
  await page.getByRole("button", { name: "Routine options" }).click()
  await expect(page.getByRole("menuitem", { name: "Duplicate as draft" })).toBeVisible()
  await expect(page.getByRole("menuitem", { name: "Copy CLI command" })).toBeVisible()
  await expect(page.getByRole("menuitem", { name: /^Copy$/ })).toHaveCount(0)
})

test("Z27 legacy ?view= links land on the right tab and keep the slug", async ({ page }) => {
  const slug = await openFirstRoutine(page)
  for (const [view, tab] of [["plan", "triggers"], ["edit", "settings"], ["settings", "settings"], ["versions", "versions"]] as const) {
    await page.goto(`/routines?slug=${encodeURIComponent(slug)}&view=${view}`)
    await expect(page).toHaveURL(new RegExp(`tab=${tab}`), { timeout: TIMEOUT })
    await expect(page).toHaveURL(new RegExp(`slug=${encodeURIComponent(slug)}`))
    await expect(page).not.toHaveURL(/view=/)
  }
})

test("R06 ?view=history lands on the routine's runs in Activity", async ({ page }) => {
  const slug = await openFirstRoutine(page)
  await page.goto(`/routines?slug=${encodeURIComponent(slug)}&view=history`)
  await expect(page).toHaveURL(new RegExp(`/activity\\?pipeline=${encodeURIComponent(slug)}`), { timeout: TIMEOUT })
})

test("R08 Triggers lists everything that starts the routine in one list", async ({ page }) => {
  await openFirstRoutine(page)
  await page.getByRole("tab", { name: /^Triggers/ }).click()
  const list = page.getByTestId("routine-trigger-list")
  await expect(list).toBeVisible({ timeout: TIMEOUT })
  // The Run button is always there.
  await expect(list.locator('[data-kind="manual"]')).toHaveCount(1)
})

test("R09 leaving Settings with unsaved changes asks in the app's dialog", async ({ page }) => {
  await openFirstRoutine(page)
  await page.getByRole("tab", { name: /^Settings/ }).click()
  const name = page.locator("#routine-edit-name")
  await expect(name).toBeEnabled({ timeout: TIMEOUT })
  await name.fill(`${await name.inputValue()} (edited)`)
  await expect(page.getByRole("region", { name: "Unsaved changes" })).toBeVisible()
  await page.getByRole("link", { name: /Runs and history/ }).click()
  await expect(page.getByRole("alertdialog")).toContainText("unsaved change")
  await page.getByRole("button", { name: "Stay" }).click()
  await page.getByRole("region", { name: "Unsaved changes" }).getByRole("button", { name: "Discard" }).click()
})

test("R12 an unknown slug says the routine no longer exists", async ({ page }) => {
  await page.goto("/routines?slug=no-such-routine-e2e")
  await expect(page.getByText("This routine doesn’t exist")).toBeVisible({ timeout: TIMEOUT })
  await page.getByRole("button", { name: "Back to routines" }).click()
  await expect(page).not.toHaveURL(/slug=/)
})

test("R13 a run opens the run page with Copy for AI and its previous runs", async ({ page }) => {
  await openFirstRoutine(page)
  const bars = page.getByTestId("routine-run-bars")
  test.skip((await bars.count()) === 0, "the first routine has no runs")
  await bars.getByRole("listitem").last().click()
  await expect(page).toHaveURL(/run=/, { timeout: TIMEOUT })
  await expect(page.getByRole("button", { name: "Copy for AI" })).toBeVisible({ timeout: TIMEOUT })
  await expect(page.getByTestId("run-kpis")).toBeVisible()
  const previous = page.getByTestId("run-previous")
  if (await previous.count()) {
    await previous.getByRole("listitem").first().click()
    await expect(page.getByTestId("run-peek")).toBeVisible()
    await page.keyboard.press("Escape")
  }
  await page.getByRole("button", { name: "Back to routine" }).click()
  await expect(page).not.toHaveURL(/run=/)
})

test("R16 Import makes a draft in a chosen crew", async ({ page }) => {
  await page.goto("/routines")
  await page.getByRole("button", { name: "Import" }).click()
  const dialog = page.getByRole("dialog", { name: /Import a routine/ })
  await expect(dialog).toBeVisible({ timeout: TIMEOUT })
  await expect(dialog.getByRole("button", { name: "Import as draft" })).toBeDisabled()
  await dialog.getByLabel("Or paste it").fill('{"name":"e2e-import-probe","steps":[{"id":"a","type":"transform","transform":{"input":"1","expression":"."}}]}')
  await expect(dialog.getByTestId("import-checks")).toContainText("Valid routine")
  await dialog.getByRole("button", { name: "Cancel" }).click()
})

test.describe("R05 phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  test("header and tabs fit the width, with 44 px tab targets", async ({ page }) => {
    await page.goto("/routines")
    await page.getByRole("button", { name: "All routines" }).click()
    const sheet = page.getByRole("dialog", { name: "Routine list" })
    await sheet.getByRole("group").nth(1).getByRole("button").first().click()
    await expect(page.getByTestId("routine-page-header")).toBeVisible({ timeout: TIMEOUT })
    const box = await page.getByRole("tab", { name: /^Overview/ }).boundingBox()
    expect(box!.height).toBeGreaterThanOrEqual(44)
    const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth)
    expect(scrollWidth).toBeLessThanOrEqual(390)
  })
})
