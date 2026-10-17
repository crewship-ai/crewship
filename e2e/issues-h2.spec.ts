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

const VIEW_MODE = "orchestration.issues.viewMode"

/**
 * List or Board is a per-user preference kept on the server, and every spec
 * here signs in as the same user, in parallel. The tests that read rows pin
 * List for their own page (the device copy and the server's answer), so the
 * S01 persistence test, or an earlier run that stopped on Board, cannot leave
 * them looking at the board.
 */
async function listView(page: Page) {
  await page.addInitScript((k) => localStorage.setItem(`crewship.pref.${k}`, JSON.stringify("list")), VIEW_MODE)
  await page.route(/\/api\/v1\/me\/preferences$/, async (route) => {
    if (route.request().method() !== "GET") return route.continue()
    const res = await route.fetch()
    const json = res.ok() ? await res.json() : {}
    await route.fulfill({ response: res, json: { ...json, [VIEW_MODE]: "list" } })
  })
}

const panel = (page: Page) => page.locator("[data-slot=secondary-panel][data-panel-id=issues]")
const firstRow = (page: Page) => page.locator("[data-slot=issue-row]").first()

test.describe("S01 desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("S01 list is the default for a new user and board persists per user", async ({ page }) => {
    await seed(page, PINNED)
    // A new user has no stored choice: drop whatever an earlier run left.
    await page.request.delete(`/api/v1/me/preferences/${VIEW_MODE}`)
    try {
      await page.goto("/issues")
      await expect(firstRow(page)).toBeVisible()
      const saved = page.waitForResponse((r) => r.url().includes(`/me/preferences/${VIEW_MODE}`) && r.request().method() === "PUT")
      await page.getByRole("radio", { name: "Board" }).click()
      await expect(page.locator("[data-slot=issues-board-grid]")).toBeVisible()
      await saved
      // Nothing left on the device: only the server remembers the choice.
      await page.evaluate((k) => localStorage.removeItem(`crewship.pref.${k}`), VIEW_MODE)
      await page.reload()
      await expect(page.locator("[data-slot=issues-board-grid]")).toBeVisible()
    } finally {
      await page.request.delete(`/api/v1/me/preferences/${VIEW_MODE}`)
    }
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
    await listView(page)
    await page.goto("/issues")
    const row = await firstRow(page).locator("button").last().boundingBox()
    expect(Math.round(row!.height)).toBe(38)
    const toolbar = await panel(page).locator(".secondary-panel-toolbar").boundingBox()
    expect(Math.round(toolbar!.height)).toBe(48)
  })

  test("S01 no bottom drawer on /issues", async ({ page }) => {
    await seed(page, PINNED)
    await listView(page)
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
    // The text matches the chip itself; remove with that chip's own button.
    await panel(page).getByText("Status: Todo").getByRole("button", { name: "Remove filter" }).click()
    await expect(page).not.toHaveURL(/status=TODO/)
    await expect(page).toHaveURL(new RegExp(`crew_id=${crews[0].id}`))
  })

  test("S05 click row opens 480 peek; Enter opens full page", async ({ page }) => {
    await seed(page, PINNED)
    await listView(page)
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
    await listView(page)
    await page.goto("/issues")
    await expect(firstRow(page)).toBeVisible()
    await page.locator("body").press("c")
    await expect(page.getByRole("dialog")).toBeVisible()
  })

  test("S08 /issues/<id>?from=/inbox&fromLabel=Inbox shows Inbox crumb and Back returns", async ({ page }) => {
    await seed(page, PINNED)
    await listView(page)
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
    await listView(page)
    await page.goto("/issues")
    await page.getByRole("button", { name: /^Filters/ }).click()
    await expect(page.locator("[data-slot=secondary-panel-sheet]")).toBeVisible()
    await page.keyboard.press("Escape")
    await firstRow(page).locator("button").last().click()
    await expect(page).toHaveURL(/\/issues\/[^/?]+/)
  })
})

// Follow-up (#3425): the full issue page, the project page, board keyboard
// moves and the restricted/legacy links. To run after the branch is deployed.
test.describe("S05/S06 follow-up desktop", () => {
  test.use({ viewport: { width: 1440, height: 1000 } })

  test("S05 full page is two columns: session block in the main column, Work & control first in the aside", async ({ page }) => {
    await seed(page, PINNED)
    await listView(page)
    await page.goto("/issues")
    const id = await firstRow(page).getAttribute("data-issue")
    await page.goto(`/issues/${id}`)
    const session = page.getByRole("region", { name: "Session" })
    const work = page.getByRole("region", { name: "Work & control" })
    await expect(session).toBeVisible()
    await expect(work).toBeVisible()
    const s = (await session.boundingBox())!
    const w = (await work.boundingBox())!
    expect(w.x).toBeGreaterThan(s.x + s.width - 1)
    expect(Math.round((await page.getByRole("complementary", { name: "Issue details" }).boundingBox())!.width)).toBe(320)
    await expect(page.getByRole("switch", { name: "Require client acceptance" })).toBeVisible()
  })

  test("S05 Hand off opens a dialog that requires a note", async ({ page }) => {
    await seed(page, PINNED)
    await listView(page)
    await page.goto("/issues")
    const id = await firstRow(page).getAttribute("data-issue")
    await page.goto(`/issues/${id}`)
    const handOff = page.getByRole("region", { name: "Work & control" }).getByRole("button", { name: "Hand off…" })
    test.skip((await handOff.count()) === 0, "the first issue offers no handoff")
    await handOff.click()
    const dialog = page.getByRole("dialog")
    await expect(dialog).toContainText(/Revision \d+ · operation id is generated/)
    await expect(dialog.getByRole("button", { name: "Hand off" })).toBeDisabled()
    await page.keyboard.press("Escape")
  })

  test("S06 ?project= opens Overview; tabs keep ?tab= in URL; unknown id shows not found", async ({ page }) => {
    await seed(page, PINNED)
    const projects = await (await page.request.get("/api/v1/projects")).json()
    test.skip(!Array.isArray(projects) || projects.length === 0, "no projects in the seed")
    await page.goto(`/issues?project=${projects[0].id}`)
    await expect(page.getByRole("heading", { level: 1, name: projects[0].name })).toBeVisible()
    const tabs = page.getByRole("tablist", { name: "Project" })
    await expect(tabs.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true")
    await expect(tabs.getByRole("tab", { name: /Updates/ })).toHaveCount(0)
    await tabs.getByRole("tab", { name: "Files" }).click()
    await expect(page).toHaveURL(/[?&]tab=files/)
    await page.reload()
    await expect(tabs.getByRole("tab", { name: "Files" })).toHaveAttribute("aria-selected", "true")

    await page.goto("/issues?project=does-not-exist")
    await expect(page.getByText("This project doesn’t exist")).toBeVisible()
    await page.getByRole("button", { name: "Back to issues" }).click()
    await expect(page).not.toHaveURL(/project=/)
  })

  test("Z10 legacy project link opens the project", async ({ page }) => {
    await seed(page, PINNED)
    const projects = await (await page.request.get("/api/v1/projects")).json()
    test.skip(!Array.isArray(projects) || projects.length === 0, "no projects in the seed")
    await page.goto(`/orchestration/projects/${projects[0].id}`)
    await expect(page).toHaveURL(new RegExp(`/issues\\?project=${projects[0].id}`))
  })

  test("S01 board card moves from the keyboard with Move to", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    await page.getByRole("radio", { name: "Board" }).click()
    const trigger = page.getByRole("button", { name: /^Move .+ to…$/ }).first()
    await trigger.focus()
    await page.keyboard.press("Enter")
    await expect(page.getByRole("menu")).toContainText("Move to")
    await page.keyboard.press("Escape")
    await page.getByRole("radio", { name: "List" }).click()
  })

  test("S01 a status group's + opens New issue in that status", async ({ page }) => {
    await seed(page, PINNED)
    await listView(page)
    await page.goto("/issues")
    const plus = page.getByRole("button", { name: "New issue in Todo" })
    test.skip((await plus.count()) === 0, "no Todo group on screen")
    await plus.click()
    await expect(page.getByRole("dialog")).toContainText("Todo")
  })
})
