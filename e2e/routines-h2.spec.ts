import { test, expect } from "@playwright/test"
import { expectInlineFacets } from "./helpers/inline-facets"

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

// ---------------------------------------------------------------------------
// Stabilisation (#3492): RT-01 … RT-09.

const routinesPanel = (page: import("@playwright/test").Page) => page.locator('[data-slot="secondary-panel"][data-panel-id="routines"]')

test("RT-01 the routine facets open inside the panel (kit COM-01)", async ({ page }) => {
  await page.goto("/routines")
  await expect(routinesPanel(page)).toBeVisible({ timeout: TIMEOUT })
  await expectInlineFacets(page, "Filter routines")
})

test("RT-02 Trigger facet; View groups by Trigger and sorts by Next run or Spend", async ({ page }) => {
  await page.goto("/routines")
  const panel = routinesPanel(page)
  await expect(panel).toBeVisible({ timeout: TIMEOUT })
  await panel.getByRole("button", { name: /^Filter/ }).click()
  const block = panel.getByRole("group", { name: "Filter routines" })
  await expect(block.getByRole("group", { name: "Trigger" }).getByRole("button")).toHaveText([/^Schedule/, /^Webhook/, /^Issue start/, /^Chat command/])
  await page.keyboard.press("Escape")
  await panel.getByRole("button", { name: "View: group and sort" }).click()
  for (const name of ["Crew", "Trigger", "None", "Next run", "Last run", "Name", "Spend"]) await expect(page.getByRole("menuitemradio", { name })).toBeVisible()
  await page.getByRole("menuitemradio", { name: "Trigger" }).click()
  await expect(panel.getByRole("group", { name: /^(Schedule|Webhook|Issue start|Chat command|Only when someone presses Run)$/ }).first()).toBeVisible()
  await panel.getByRole("button", { name: "View: group and sort" }).click()
  await page.getByRole("menuitemradio", { name: "Crew" }).click()
})

test("RT-03 select several routines: a bulk bar with Turn off and Run now", async ({ page }) => {
  await page.goto("/routines")
  const panel = routinesPanel(page)
  await expect(panel).toBeVisible({ timeout: TIMEOUT })
  await panel.getByRole("button", { name: "Select several" }).click()
  const bar = panel.getByRole("toolbar", { name: "Selected routines" })
  await expect(bar).toContainText("Pick routines")
  await expect(bar.getByRole("button", { name: "Run now" })).toBeDisabled()
  await panel.getByRole("checkbox").first().click()
  await expect(bar).toContainText("1 selected")
  await expect(bar.getByRole("button", { name: "Run now" })).toBeEnabled()
  // Turn off asks first; this only checks the question, it turns nothing off.
  if (await bar.getByRole("button", { name: "Turn off" }).count()) {
    await bar.getByRole("button", { name: "Turn off" }).click()
    await expect(page.getByRole("alertdialog")).toContainText("Turn off 1 routine?")
    await page.getByRole("button", { name: "Cancel" }).click()
  }
  await bar.getByRole("button", { name: "Done selecting" }).click()
  await expect(bar).toHaveCount(0)
})

test("RT-04 the overview opens with four figures, Needs you, Running now and Next 24 hours", async ({ page }) => {
  await page.goto("/routines")
  const figures = page.getByRole("list", { name: "Routine figures" })
  await expect(figures).toBeVisible({ timeout: TIMEOUT })
  await expect(figures.getByRole("listitem")).toHaveText([/^Needs you/, /^Running now/, /^Runs · 7 days/, /^Spend · /])
  for (const name of ["Needs you", "Running now", "Next 24 hours"]) await expect(page.getByRole("region", { name })).toBeVisible()
  await expect(page.getByRole("region", { name: "Next 24 hours" }).getByRole("link", { name: "Calendar", exact: true })).toHaveAttribute("href", "/calendar")
})

test("RT-05 Run opens one dialog: Run now · Test run · Later, limits in the footer", async ({ page }) => {
  await openFirstRoutine(page)
  const run = page.getByRole("button", { name: /^Run$/ })
  test.skip((await run.count()) === 0 || (await run.isDisabled()), "the first routine cannot be run by this account")
  await run.click()
  const dialog = page.getByRole("dialog")
  const how = dialog.getByRole("radiogroup", { name: "How to run" })
  await expect(how).toBeVisible({ timeout: TIMEOUT })
  await how.getByRole("radio", { name: "Later" }).click()
  await expect(dialog.getByLabel("Start at")).toBeVisible()
  await expect(dialog.getByText("Creates a one-time run you can cancel in Triggers")).toBeVisible()
  await expect(dialog.locator('select:not([aria-hidden="true"])')).toHaveCount(0)
  if (await how.getByRole("radio", { name: "Test run" }).count()) {
    await how.getByRole("radio", { name: "Test run" }).click()
    await expect(dialog.getByText("No side effects")).toBeVisible()
  }
  await dialog.getByRole("button", { name: "Cancel" }).click()
  await expect(dialog).toHaveCount(0)
})

async function openFirstRun(page: import("@playwright/test").Page) {
  await openFirstRoutine(page)
  const bars = page.getByTestId("routine-run-bars")
  test.skip((await bars.count()) === 0, "the first routine has no runs")
  await bars.getByRole("listitem").last().click()
  await expect(page).toHaveURL(/run=/, { timeout: TIMEOUT })
}

test("RT-06 the run page lists the steps with an inspector: Output · Input · Log · Details", async ({ page }) => {
  await openFirstRun(page)
  const steps = page.getByRole("list", { name: "Steps of this run" })
  test.skip((await steps.count()) === 0, "the run's recipe is not available")
  await expect(steps).toBeVisible({ timeout: TIMEOUT })
  const inspector = page.getByRole("region", { name: "Step inspector" })
  await expect(inspector.getByRole("radiogroup", { name: "Inspect" }).getByRole("radio")).toHaveText(["Output", "Input", "Log", "Details"])
  await inspector.getByRole("radio", { name: "Log" }).click()
  await expect(inspector).not.toContainText("Loading the log…", { timeout: TIMEOUT })
  await inspector.getByRole("radio", { name: "Details" }).click()
  await expect(inspector).toContainText("Attempts")
  await expect(page.getByTestId("run-kpis")).toContainText("Produced")
})

test("RT-07 with a run open the panel lists its runs, and a row opens the peek", async ({ page }) => {
  await openFirstRun(page)
  const panel = routinesPanel(page)
  await expect(panel.getByRole("group", { name: "Run status" })).toBeVisible({ timeout: TIMEOUT })
  await panel.getByRole("button", { name: /^Peek at run / }).first().click()
  await expect(page.getByTestId("run-peek")).toBeVisible()
  await page.keyboard.press("Escape")
  await panel.getByRole("button", { name: "All routines" }).click()
  await expect(panel.getByRole("group", { name: "Views" })).toBeVisible()
})

test("RT-07 All runs: a row opens the run peek, Open run the run page", async ({ page }) => {
  await page.goto("/routines?view=runs")
  const list = page.getByRole("list", { name: "All runs" })
  test.skip(!(await list.isVisible({ timeout: TIMEOUT }).catch(() => false)), "no runs in this workspace")
  await list.getByRole("button").first().click()
  await expect(page.getByTestId("run-peek")).toBeVisible()
  await page.getByRole("button", { name: "Open run" }).click()
  await expect(page).toHaveURL(/run=/, { timeout: TIMEOUT })
})

test("RT-08 Triggers: nothing below the list repeats it", async ({ page }) => {
  await openFirstRoutine(page)
  await page.getByRole("tab", { name: /^Triggers/ }).click()
  await expect(page.getByTestId("routine-trigger-list")).toBeVisible({ timeout: TIMEOUT })
  await expect(page.getByRole("region", { name: "Schedules" })).toHaveCount(0)
  await expect(page.getByRole("region", { name: "One-time starts" })).toHaveCount(0)
  await expect(page.getByTestId("routine-automations")).toHaveCount(0)
})

test("RT-09 How routines work opens the intro, without templates or figures", async ({ page }) => {
  await page.goto("/routines")
  await page.getByRole("button", { name: "How routines work" }).click()
  await expect(page).toHaveURL(/view=intro/)
  await expect(page.getByRole("heading", { name: "Work that repeats, done by your crew." })).toBeVisible({ timeout: TIMEOUT })
  await expect(page.getByRole("heading", { name: "From a sentence to a routine in three steps" })).toBeVisible()
  await expect(page.getByText(/template/i)).toHaveCount(0)
})

test.describe("RT-06 phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  test("a step opens the inspector as its own screen with Back", async ({ page }) => {
    await page.goto("/routines")
    await page.getByRole("button", { name: "All routines" }).click()
    const sheet = page.getByRole("dialog", { name: "Routine list" })
    await sheet.getByRole("group").nth(1).getByRole("button").first().click()
    const bars = page.getByTestId("routine-run-bars")
    await expect(page.getByTestId("routine-page-header")).toBeVisible({ timeout: TIMEOUT })
    test.skip((await bars.count()) === 0, "the first routine has no runs")
    await bars.getByRole("listitem").last().click()
    const steps = page.getByRole("list", { name: "Steps of this run" })
    test.skip(!(await steps.isVisible({ timeout: TIMEOUT }).catch(() => false)), "the run's recipe is not available")
    await steps.getByRole("button").first().click()
    await expect(page.getByRole("region", { name: "Step inspector" })).toBeVisible()
    await expect(steps).toHaveCount(0)
    await page.getByRole("button", { name: "Back to steps" }).click()
    await expect(page.getByRole("list", { name: "Steps of this run" })).toBeVisible()
  })
})

// ---------------------------------------------------------------------------
// Finish pass (#3525): COM-12, COM-14, COM-15, RT-07, RT-10.

test("COM-12 the routine's actions sit in the top bar's own slot, with no hand-off node", async ({ page }) => {
  await openFirstRoutine(page)
  const slot = page.locator('[data-slot="sub-bar-actions"]').first()
  await expect(slot.getByRole("button").first()).toBeVisible({ timeout: TIMEOUT })
  await expect(page.locator('[data-slot="routine-page-actions"]')).toHaveCount(0)
})

test("COM-14 the save bar on a routine editor says Save as draft", async ({ page }) => {
  await openFirstRoutine(page)
  await page.getByRole("tab", { name: /^Settings/ }).click()
  const name = page.locator("#routine-edit-name")
  await expect(name).toBeEnabled({ timeout: TIMEOUT })
  await name.fill(`${await name.inputValue()} (edited)`)
  const bar = page.getByRole("region", { name: "Unsaved changes" })
  await expect(bar.getByRole("button", { name: "Save as draft" })).toBeVisible()
  await bar.getByRole("button", { name: "Discard" }).click()
})

test("COM-15 the open view in the panel is marked current", async ({ page }) => {
  await page.goto("/routines?view=runs")
  const views = routinesPanel(page).getByRole("group", { name: "Views" })
  await expect(views.locator('[aria-current="page"]')).toHaveText(/^All runs/, { timeout: TIMEOUT })
})

test("RT-07 Activity: a routine run row opens the routine run peek with version and cost", async ({ page }) => {
  await page.goto("/activity?lens=routines")
  const row = page.getByRole("region", { name: "Latest runs" }).getByRole("button").first()
  test.skip(!(await row.isVisible({ timeout: TIMEOUT }).catch(() => false)), "no runs in this workspace")
  await row.click()
  const peek = page.getByTestId("run-peek")
  await expect(peek).toBeVisible()
  await expect(peek.getByText("Version")).toBeVisible()
  await expect(peek.getByText("Cost")).toBeVisible()
  await page.getByRole("button", { name: "Open run" }).click()
  await expect(peek).toHaveCount(0)
})

test.describe("RT-07 / COM-12 tablet 1024", () => {
  test.use({ viewport: { width: 1024, height: 900 } })
  test("the run peek from Activity fits the viewport", async ({ page }) => {
    await page.goto("/activity")
    const row = page.getByRole("region", { name: "Latest runs" }).getByRole("button").first()
    test.skip(!(await row.isVisible({ timeout: TIMEOUT }).catch(() => false)), "no runs in this workspace")
    await row.click()
    const box = await page.getByRole("dialog").first().boundingBox()
    expect(box!.x + box!.width).toBeLessThanOrEqual(1024)
  })
})

test.describe("RT-07 phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
  test("the run peek from Activity is a bottom sheet within the width", async ({ page }) => {
    await page.goto("/activity")
    const row = page.getByRole("region", { name: "Latest runs" }).getByRole("button").first()
    test.skip(!(await row.isVisible({ timeout: TIMEOUT }).catch(() => false)), "no runs in this workspace")
    await row.click()
    const box = await page.getByRole("dialog").first().boundingBox()
    expect(box!.width).toBeLessThanOrEqual(390)
    await expect(page.getByTestId("run-peek")).toBeVisible()
  })
})


test("PAR-046/047/048/051 overview has no tab strip, sentence-case cards, sans KPI numerals and a C chip", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto("/routines")
  const figures = page.getByRole("list", { name: "Routine figures" })
  await expect(figures).toBeVisible({ timeout: TIMEOUT })
  await expect(page.getByRole("navigation", { name: "Routines views" })).toHaveCount(0)
  // Card titles are sentence case, 13 px class, not mono caps.
  const title = page.getByRole("region", { name: "Needs you" }).getByRole("heading", { name: /^Needs you/ })
  expect(await title.evaluate((el) => getComputedStyle(el).textTransform)).toBe("none")
  expect(await title.evaluate((el) => getComputedStyle(el).fontFamily)).not.toMatch(/mono/i)
  // KPI numerals: sans, tabular, bold.
  const value = figures.getByRole("listitem").first().locator("span").nth(1)
  expect(await value.evaluate((el) => getComputedStyle(el).fontFamily)).not.toMatch(/mono/i)
  expect(await value.evaluate((el) => getComputedStyle(el).fontVariantNumeric)).toContain("tabular-nums")
  // New routine carries the C chip, still named "New routine".
  const create = page.getByRole("button", { name: "New routine", exact: true }).first()
  await expect(create.locator("kbd")).toHaveText("C")
})

test("PAR-046 phone: the overview still has no tab strip and no horizontal scroll", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto("/routines")
  await expect(page.getByRole("list", { name: "Routine figures" })).toBeVisible({ timeout: TIMEOUT })
  await expect(page.getByRole("navigation", { name: "Routines views" })).toHaveCount(0)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

// Gap UI (RT-06, RT-07, RT-24): the Health card on the overview and the
// Checkup on a routine page fit 390, 1024 and 1440 without sideways scroll;
// the wizard offers the artifact panel step.
for (const width of [390, 1024, 1440]) {
  test.describe(`RT-06/07 Health and Checkup at ${width}`, () => {
    test.use({ viewport: { width, height: 900 }, hasTouch: width < 768, isMobile: width < 768 })
    test("the Health card lists routines and nothing scrolls sideways", async ({ page }) => {
      await page.goto("/routines")
      const card = page.getByTestId("routines-health")
      await expect(card).toBeVisible({ timeout: TIMEOUT })
      await expect(card).not.toContainText(/30 ?d/)
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow).toBeLessThanOrEqual(0)
    })
    test("a routine page shows the Checkup with n/m", async ({ page }) => {
      await page.goto("/routines")
      await expect(page.getByTestId("routines-health")).toBeVisible({ timeout: TIMEOUT })
      await page.getByTestId("routines-health").getByRole("button").first().click()
      await expect(page.getByTestId("checkup")).toBeVisible({ timeout: TIMEOUT })
      await expect(page.getByTestId("checkup-circle")).toHaveText(/^\d+\/\d+$/)
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
      expect(overflow).toBeLessThanOrEqual(0)
    })
  })
}
