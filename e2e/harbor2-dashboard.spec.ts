import { test, expect, type Page, type Route } from "@playwright/test"

// Harbor 2 acceptance for the Dashboard (#3384, part of #3366/#3350):
// the window lives in the top bar, the page head greets, what needs a person
// is four tiles, cards collapse when empty, a failed read replaces only its
// own card. Measured in a real browser at 390 / 1024 / 1440.
//
// Uses the bare `page` rather than the auth fixture: every test here routes
// API calls before the first navigation. storageState from global-setup
// still authenticates it.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, PINNED)
}

/** Four kinds waiting: decisions, a failed run, issues for review, missed schedules. */
async function routeAttention(page: Page) {
  await page.route(/\/api\/v1\/inbox\?/, (route: Route) =>
    route.fulfill({
      json: { rows: [], count: 0, unread_count: 0, active_by_kind: { failed_run: 1, schedule_missed: 2 }, decision_count: 2 },
    }),
  )
  await page.route(/\/api\/v1\/issues\?.*status=REVIEW/, (route: Route) =>
    route.fulfill({
      json: [
        { id: "r1", identifier: "E2E-1", title: "Review one", status: "REVIEW", updated_at: new Date().toISOString() },
        { id: "r2", identifier: "E2E-2", title: "Review two", status: "REVIEW", updated_at: new Date().toISOString() },
      ],
    }),
  )
}

const tiles = (page: Page) => page.locator("[data-slot=attention-tile]")

for (const [name, viewport, cols] of [
  ["desktop", { width: 1440, height: 900 }, 4],
  ["tablet", { width: 1024, height: 900 }, 2],
  ["phone", { width: 390, height: 844 }, 2],
] as const) {
  test.describe(`${name} ${viewport.width}`, () => {
    test.use({ viewport })

    test(`layout: attention ${cols === 4 ? "4-up" : "2×2"}`, async ({ page }) => {
      await seed(page)
      await routeAttention(page)
      await page.goto("/")
      await expect(tiles(page)).toHaveCount(4)
      const tops = await tiles(page).evaluateAll((els) => els.map((el) => Math.round(el.getBoundingClientRect().top)))
      expect(new Set(tops).size).toBe(4 / cols)
    })

    test("page head: greeting size and no sticky period bar", async ({ page }) => {
      await seed(page)
      await page.goto("/")
      const greeting = page.getByTestId("dashboard-greeting")
      await expect(greeting).toHaveText(/^Good (morning|afternoon|evening)/)
      const size = await greeting.evaluate((el) => parseFloat(getComputedStyle(el).fontSize))
      // 21 px title on desktop and tablet (22 at the Standard text size), 18 px on a phone.
      if (viewport.width < 768) expect(Math.round(size)).toBe(18)
      else expect(Math.round(size)).toBeGreaterThanOrEqual(21)
      expect(await page.locator("[data-slot=sidebar-inset] .sticky").count()).toBe(0)
    })
  })
}

test.describe("window", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("window switch 7d updates KPI and chart titles", async ({ page }) => {
    await seed(page)
    await page.goto("/")
    const header = page.locator("header").first()
    const seg = header.getByRole("group", { name: "Dashboard time window" })
    await expect(seg).toBeVisible()
    const insights = page.waitForRequest(/\/api\/v1\/runs\/insights\?.*window=7d/)
    await seg.getByRole("button", { name: "7d" }).click()
    await insights
    await expect(seg.getByRole("button", { name: "7d" })).toHaveAttribute("aria-pressed", "true")
    await expect(page.getByRole("region", { name: "Run volume · 7d · by crew" })).toBeVisible()
  })

  test("the phone does not offer the window", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await seed(page)
    await page.goto("/")
    await expect(page.getByTestId("dashboard-greeting")).toBeVisible()
    await expect(page.getByRole("group", { name: "Dashboard time window" })).toBeHidden()
  })
})

test.describe("states", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("attention tile opens filtered Inbox", async ({ page }) => {
    await seed(page)
    await routeAttention(page)
    await page.goto("/")
    await tiles(page).filter({ hasText: "Decisions waiting" }).click()
    await expect(page).toHaveURL(/\/inbox\?attention=approvals/)
  })

  test("part failure replaces only that card", async ({ page }) => {
    await seed(page)
    await page.route(/\/api\/v1\/runs\/insights\?/, (route: Route) => route.fulfill({ status: 503, body: "" }))
    await page.goto("/")
    await expect(page.getByText("Could not load the run summary")).toBeVisible()
    await expect(page.getByRole("region", { name: "Results & review" })).toBeVisible()
    await expect(page.getByRole("region", { name: /Run volume/ })).toBeVisible()
  })

  test("loading keeps the window in the top bar and says Still loading after a second", async ({ page }) => {
    await seed(page)
    await page.route(/\/api\/v1\/crews\?/, async (route: Route) => {
      await new Promise((r) => setTimeout(r, 2500))
      await route.continue()
    })
    await page.goto("/")
    await expect(page.locator("[data-state=loading]")).toBeVisible()
    await expect(page.locator("header").first().getByRole("group", { name: "Dashboard time window" })).toBeVisible()
    await expect(page.getByText("Still loading…")).toBeVisible({ timeout: 2000 })
    await expect(page.getByTestId("dashboard-greeting")).toBeVisible({ timeout: 10_000 })
  })

  test("empty workspace shows Your workspace is ready with New crew", async ({ page }) => {
    await seed(page)
    await page.route(/\/api\/v1\/crews\?/, (route: Route) => route.fulfill({ json: [] }))
    await page.goto("/")
    await expect(page.getByRole("heading", { name: "Your workspace is ready" })).toBeVisible()
    await expect(page.getByRole("link", { name: "New crew" })).toHaveAttribute("href", "/crews?new=crew")
    await expect(page.getByRole("region", { name: "Results & review" })).toHaveCount(0)
  })

  test("unfinished onboarding lands on /onboarding without flashing the dashboard", async ({ page }) => {
    await seed(page)
    await page.route(/\/api\/v1\/onboarding\/status/, (route: Route) => route.fulfill({ json: { completed: false } }))
    let flashed = false
    page.on("framenavigated", async () => {
      if (await page.locator("[data-testid=attention-tiles]").count().catch(() => 0)) flashed = true
    })
    await page.goto("/")
    await expect(page).toHaveURL(/\/onboarding/)
    expect(flashed).toBe(false)
  })
})

test.describe("touch", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("touch targets ≥ 44 px with coarse pointer", async ({ page }) => {
    await seed(page)
    await routeAttention(page)
    await page.goto("/")
    await expect(tiles(page).first()).toBeVisible()
    const small = await page
      .locator("main a[href], main button")
      .evaluateAll((els) =>
        els
          .filter((el) => (el as HTMLElement).offsetParent !== null && el.getAttribute("aria-hidden") !== "true")
          .map((el) => ({ h: el.getBoundingClientRect().height, t: (el.textContent ?? "").trim().slice(0, 40) }))
          .filter((b) => b.h < 44),
      )
    expect(small).toEqual([])
  })
})
