import { test, expect, type Page } from "@playwright/test"

// Harbor 2 Inbox acceptance (#3385): the panel beside the detail on desktop
// and tablet, list → detail → back on a phone, the Overview when nothing is
// open, the keys, and the Dashboard's ?attention= links. Runs against seeded
// demo data (crew reset); a check that needs a kind the seed lacks skips with
// a reason instead of passing vacuously.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

const panel = (page: Page) => page.locator("[data-slot=secondary-panel][data-panel-id=inbox]")
const rows = (page: Page) => panel(page).getByTestId("inbox-row")

test.describe("desktop 1440", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("desktop 1440 shows panel 272 + detail", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    await expect(panel(page)).toBeVisible()
    expect(Math.round((await panel(page).boundingBox())!.width)).toBe(272)
    await expect(page.getByRole("main", { name: "Inbox detail" })).toBeVisible()
  })

  test("/inbox on 1440 shows Your inbox, at a glance", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    await expect(page.getByRole("heading", { level: 1, name: "Your inbox, at a glance" }).or(page.getByRole("heading", { name: "Nothing waits for you" }))).toBeVisible()
    // Nothing was opened for the reader: no decision block in the pane.
    await expect(page.getByTestId("decision-card")).toHaveCount(0)
  })

  test("key 1 opens To handle", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?view=updates")
    await page.locator("body").press("1")
    await expect(page).toHaveURL(/view=action/)
    await expect(panel(page).getByRole("tab", { name: /To handle/ })).toHaveAttribute("aria-selected", "true")
  })

  test("J/K moves selection and updates 4 / 16", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    const count = await rows(page).count()
    test.skip(count < 2, "needs at least two rows in To handle")
    await page.locator("body").press("j")
    await expect(page.getByText(new RegExp(`^1 / ${count}$`))).toBeVisible()
    await page.locator("body").press("j")
    await expect(page.getByText(new RegExp(`^2 / ${count}$`))).toBeVisible()
    await page.locator("body").press("k")
    await expect(page.getByText(new RegExp(`^1 / ${count}$`))).toBeVisible()
  })

  test("E archives a read update", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?view=updates")
    const before = await rows(page).count()
    test.skip(before === 0, "needs an update to archive")
    await rows(page).first().click()
    const archive = page.getByRole("button", { name: "Archive (E)" })
    test.skip(await archive.isDisabled(), "first update is source-managed")
    await page.locator("body").press("e")
    await expect(page.getByTestId("inbox-receipt")).toContainText("Archived")
  })

  test("⌘↵ sends answer for run_needs_human", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?kind=run_needs_human")
    test.skip((await rows(page).count()) === 0, "seed has no run_needs_human card")
    await rows(page).first().click()
    await page.getByRole("button", { name: "Answer" }).click()
    await page.getByRole("textbox", { name: "Your answer" }).fill("Keep the 3-seat limit vague until legal approves.")
    await page.getByRole("textbox", { name: "Your answer" }).press("ControlOrMeta+Enter")
    await expect(page.getByTestId("act-receipt")).toBeVisible()
  })

  test("dashboard tile Decisions waiting → Inbox shows Overview filtered to decisions", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?attention=approvals")
    await expect(panel(page).getByText("Decisions waiting").first()).toBeVisible()
    await expect(page.getByTestId("inbox-focus-overview").or(page.getByRole("heading", { name: "Nothing waits for you" }))).toBeVisible()
  })

  test("run-alerts deep link", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?attention=run-alerts")
    await expect(panel(page).getByText("Run alerts").first()).toBeVisible()
  })

  test("unknown ?item shows No longer in your inbox, list stays", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?item=does-not-exist")
    await expect(page.getByRole("heading", { name: "No longer in your inbox" })).toBeVisible()
    await expect(panel(page)).toBeVisible()
  })

  test("legacy links keep their parameters", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox?filter=casey")
    await expect(panel(page).getByRole("textbox", { name: "Search inbox…" })).toHaveValue("casey")
    await page.goto("/inbox-v2?item=x#y")
    await expect(page).toHaveURL(/\/inbox\?item=x#y$/)
  })
})

test.describe("wide 1600", () => {
  test.use({ viewport: { width: 1600, height: 900 } })

  test("wide 1600 shows ib-rail with Earlier from", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    test.skip((await rows(page).count()) === 0, "needs a row to open")
    await rows(page).first().click()
    await expect(page.getByTestId("inbox-rail")).toBeVisible()
  })
})

test.describe("tablet 1024", () => {
  test.use({ viewport: { width: 1024, height: 800 } })

  test("tablet 1024 shows panel + detail", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    await expect(panel(page)).toBeVisible()
    await expect(page.getByRole("main", { name: "Inbox detail" })).toBeVisible()
    await expect(page.getByTestId("inbox-rail")).toBeHidden()
  })
})

test.describe("phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("phone 390 list → detail → back", async ({ page }) => {
    await seed(page)
    await page.goto("/inbox")
    const list = page.locator("[data-slot=secondary-panel][data-phone=inline]")
    await expect(list).toBeVisible()
    await expect(page.getByRole("main", { name: "Inbox detail" })).toHaveCount(0)
    const first = list.getByTestId("inbox-row").first()
    test.skip((await first.count()) === 0, "needs a row to open")
    await first.click()
    await expect(page.getByRole("main", { name: "Inbox detail" })).toBeVisible()
    await page.getByRole("button", { name: "Back to inbox" }).click()
    await expect(list).toBeVisible()
  })
})

test.describe("empty", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("empty inbox shows Nothing waits for you", async ({ page }) => {
    await seed(page)
    // Every source answers with nothing.
    await page.route("**/api/v1/inbox?**", (route) => route.fulfill({ json: { rows: [], count: 0, unread_count: 0, has_more: false } }))
    await page.route("**/api/v1/approvals?**", (route) => route.fulfill({ json: { rows: [], status: "all", count: 0, has_more: false } }))
    await page.route("**/api/v1/missions?**", (route) => route.fulfill({ json: [] }))
    await page.goto("/inbox")
    await expect(page.getByRole("heading", { name: "Nothing waits for you" })).toBeVisible()
    await expect(page.getByText("crewship inbox")).toBeVisible()
  })
})
