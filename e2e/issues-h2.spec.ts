import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for the Issues page (#3386): the panel, the row, the
// peek and the deep links, measured in a real browser against a seeded
// workspace with issues in it.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page, entries: Record<string, string>) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, entries)
}

const panel = (page: Page) => page.locator("[data-slot=secondary-panel][data-panel-id=issues]")
const firstRow = (page: Page) => page.locator("[data-slot=issue-row]").first()

test.describe("S01 desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("S01 list is the default for a new user and board persists per user", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    await expect(firstRow(page)).toBeVisible()
    await page.getByRole("radio", { name: "Board" }).click()
    await expect(page.locator("[data-slot=issues-board-grid]")).toBeVisible()
    await page.reload()
    await expect(page.locator("[data-slot=issues-board-grid]")).toBeVisible()
    await page.getByRole("radio", { name: "List" }).click()
  })

  test("S01 panel order Views→Projects→Saved views and keys 1-6 switch views", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    const text = (await panel(page).innerText()).replace(/\s+/g, " ")
    expect(text.indexOf("Views")).toBeLessThan(text.indexOf("Projects"))
    expect(text.indexOf("Projects")).toBeLessThan(text.indexOf("Saved views"))
    await page.locator("body").press("1")
    await expect(page.locator("header h1")).toContainText("My work")
    await page.locator("body").press("2")
    await expect(page.locator("header h1")).toContainText("All issues")
  })

  test("S01 row height 38 and toolbar 48 at 1440", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    const row = await firstRow(page).locator("button").last().boundingBox()
    expect(Math.round(row!.height)).toBe(38)
    const toolbar = await panel(page).locator(".secondary-panel-toolbar").boundingBox()
    expect(Math.round(toolbar!.height)).toBe(48)
  })

  test("S01 no bottom drawer on /issues", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    await expect(firstRow(page)).toBeVisible()
    for (const tab of ["Runs", "Changes", "Spec", "Docker"]) {
      await expect(page.getByRole("button", { name: tab, exact: true })).toHaveCount(0)
    }
  })

  test("S04 crew_id and assignee_id deep links show removable chips", async ({ page }) => {
    await seed(page, PINNED)
    const ws = await (await page.request.get("/api/v1/workspaces")).json()
    const wsId = Array.isArray(ws) ? ws[0].id : ws.id
    const crews = await (await page.request.get(`/api/v1/crews?workspace_id=${wsId}`)).json()
    await page.goto(`/issues?crew_id=${crews[0].id}&status=TODO`)
    await expect(panel(page).getByText(`Crew: ${crews[0].name}`)).toBeVisible()
    await expect(panel(page).getByText("Status: Todo")).toBeVisible()
    await panel(page).getByText("Status: Todo").locator("..").getByRole("button", { name: "Remove filter" }).click()
    await expect(page).not.toHaveURL(/status=TODO/)
    await expect(page).toHaveURL(new RegExp(`crew_id=${crews[0].id}`))
  })

  test("S05 click row opens 480 peek; Enter opens full page", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    await firstRow(page).locator("button").last().click()
    const peek = page.locator("[data-slot=peek]")
    await expect(peek).toBeVisible()
    expect(Math.round((await peek.boundingBox())!.width)).toBe(480)
    await expect(page).toHaveURL(/[?&]issue=/)
    await peek.focus()
    await page.keyboard.press("Enter")
    await expect(page).toHaveURL(/\/issues\/[^/?]+\?from=/)
  })

  test("S07 C opens New issue", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    await expect(firstRow(page)).toBeVisible()
    await page.locator("body").press("c")
    await expect(page.getByRole("dialog")).toBeVisible()
  })

  test("S08 /issues/<id>?from=/inbox&fromLabel=Inbox shows Inbox crumb and Back returns", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    const id = await firstRow(page).getAttribute("data-issue")
    await page.goto(`/issues/${id}?from=/inbox&fromLabel=Inbox`)
    await expect(page.locator("header h1")).toContainText("Inbox")
    await page.getByRole("button", { name: "Back to Inbox" }).click()
    await expect(page).toHaveURL(/\/inbox/)
  })
})

test.describe("S01 phone", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("S01 phone 390: Filters chip opens panel sheet; a row opens the full page", async ({ page }) => {
    await page.goto("/issues")
    await page.getByRole("button", { name: /^Filters/ }).click()
    await expect(page.locator("[data-slot=secondary-panel-sheet]")).toBeVisible()
    await page.keyboard.press("Escape")
    await firstRow(page).locator("button").last().click()
    await expect(page).toHaveURL(/\/issues\/[^/?]+/)
  })
})
