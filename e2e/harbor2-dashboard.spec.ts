import { test, expect, type Page, type Route } from "@playwright/test"

// Harbor 2 acceptance for the Dashboard (#3384, part of #3366/#3350):
// the window lives in the top bar, the page head greets, what needs a person
// is four tiles, cards collapse when empty, a failed read replaces only its
// own card. Measured in a real browser at 390 / 1024 / 1440.
//
// Uses the bare `page` rather than the auth fixture: every test here routes
// API calls before the first navigation. storageState from global-setup
// still authenticates it.

// Small is the text size the v67 sizes are drawn at; sync off so the account's
// stored size does not override it.
const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0", "crewship-text-size": "small" }

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
    const seg = header.getByRole("radiogroup", { name: "Dashboard time window" })
    await expect(seg).toBeVisible()
    const insights = page.waitForRequest(/\/api\/v1\/runs\/insights\?.*window=7d/)
    await seg.getByRole("radio", { name: "7d" }).click()
    await insights
    await expect(seg.getByRole("radio", { name: "7d" })).toHaveAttribute("aria-checked", "true")
    await expect(page.getByRole("region", { name: "Run volume · 7d · by crew" })).toBeVisible()
  })

  test("the phone does not offer the window", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await seed(page)
    await page.goto("/")
    await expect(page.getByTestId("dashboard-greeting")).toBeVisible()
    await expect(page.getByRole("radiogroup", { name: "Dashboard time window" })).toBeHidden()
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

  test("slow load keeps the window in the top bar and shows Still loading with Retry after 10 s", async ({ page }) => {
    await seed(page)
    await page.clock.install()
    // Crews answer only once the test lets them: the page stays loading.
    let release!: () => void
    const held = new Promise<void>((r) => { release = r })
    await page.route(/\/api\/v1\/crews\?/, async (route: Route) => {
      await held
      await route.continue().catch(() => {})
    })
    await page.goto("/")
    await expect(page.locator("[data-state=loading]")).toBeVisible()
    await expect(page.locator("header").first().getByRole("radiogroup", { name: "Dashboard time window" })).toBeVisible()
    // The skeleton carries the first seconds without a note.
    await page.clock.runFor(2_000)
    await expect(page.getByText("Still loading…")).toBeHidden()
    await page.clock.runFor(8_000)
    await expect(page.getByText("Still loading…")).toBeVisible()
    await expect(page.locator("[data-state=loading]").getByRole("button", { name: "Retry" })).toBeVisible()
    release()
    await expect(page.getByTestId("dashboard-greeting")).toBeVisible({ timeout: 10_000 })
  })

  test("the greeting is the page's one h1", async ({ page }) => {
    await seed(page)
    await page.goto("/")
    await expect(page.getByTestId("dashboard-greeting")).toBeVisible()
    await expect(page.getByRole("heading", { level: 1 })).toHaveCount(1)
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(/^Good (morning|afternoon|evening)/)
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

// DSH-01 (#3483): v67 Offline state — one "Reconnecting…" banner with the
// time the data is from, and the data under it dimmed and not clickable.
// The realtime socket is cut in the browser (routeWebSocket), not the server.
test.describe("offline", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("lost live updates dim the dashboard under one Reconnecting banner; reconnect lifts it", async ({ page }) => {
    await seed(page)
    await routeAttention(page)
    let cut = false
    const open: { close: () => Promise<void> }[] = []
    await page.routeWebSocket(
      (url) => !url.pathname.includes("_next") && !url.pathname.includes("webpack"),
      (ws) => {
        if (cut) {
          void ws.close()
          return
        }
        ws.connectToServer()
        open.push(ws)
      },
    )
    await page.goto("/")
    await expect(tiles(page)).toHaveCount(4)
    await expect(page.locator('[data-state="offline"]')).toHaveCount(0)

    cut = true
    for (const ws of open.splice(0)) await ws.close()

    const banner = page.locator('[data-state="offline"]')
    await expect(banner).toHaveCount(1, { timeout: 10_000 })
    await expect(banner).toContainText("Reconnecting…")
    await expect(banner).toContainText(/You see data from \d{1,2}:\d{2}/)
    const stale = page.locator(".page-stale")
    await expect(stale).toHaveCount(1)
    await expect(stale).toHaveAttribute("inert", "")
    await expect(stale.getByTestId("dashboard-greeting")).toBeVisible()
    await expect(stale.locator("[data-slot=attention-tile]")).toHaveCount(4)
    const opacity = await stale.evaluate((el) => parseFloat(getComputedStyle(el).opacity))
    expect(opacity).toBeLessThan(1)

    cut = false
    await expect(banner).toHaveCount(0, { timeout: 30_000 })
    await expect(page.locator(".page-stale")).toHaveCount(0)
  })
})

// PAR-011/012/013/014/018 (#3577): the dashboard matches v67 card by card.
// Run volume and the Pages (v67: Artifacts) card ARE in v67, below the KPI
// strip; the audit screenshot was cut before them.
test.describe("v67 parity", () => {
  test.describe("desktop 1440", () => {
    test.use({ viewport: { width: 1440, height: 900 } })

    test("cards: Up next has the Calendar glyph, Your crews lists at most six, Run volume and Artifacts follow the KPIs", async ({ page }) => {
      await seed(page)
      await routeAttention(page)
      await page.goto("/")
      const upNext = page.getByRole("region", { name: "Up next" })
      await expect(upNext.locator("[data-slot=concept-tile] svg.lucide-calendar-days")).toBeVisible()
      await expect(upNext.getByRole("link", { name: /Calendar/ })).toHaveAttribute("href", "/calendar")
      const crews = page.getByTestId("dashboard-fleet-board")
      expect(await crews.getByTestId("dashboard-fleet-card").count()).toBeLessThanOrEqual(6)
      await expect(crews.getByText(/more in Crews/)).toHaveCount(0)
      const kpisBottom = (await page.getByTestId("dashboard-kpis").boundingBox())!.y
      const volume = page.getByRole("region", { name: /^Run volume/ })
      await expect(volume).toBeVisible()
      expect((await volume.boundingBox())!.y).toBeGreaterThan(kpisBottom)
      await expect(page.getByRole("region", { name: "Artifacts" })).toBeVisible()
    })

    test("card headers are 50 px with a 28 px tile and a 13 px semibold title", async ({ page }) => {
      await seed(page)
      await routeAttention(page)
      await page.goto("/")
      const header = page.getByRole("region", { name: "Up next" }).locator("header")
      expect(Math.round((await header.boundingBox())!.height)).toBe(50)
      const tile = header.locator("[data-slot=concept-tile]")
      expect(Math.round((await tile.boundingBox())!.width)).toBe(28)
      const title = await header.locator("h2").evaluate((el) => getComputedStyle(el).fontSize + "/" + getComputedStyle(el).fontWeight)
      expect(title).toBe("13px/600")
    })
  })

  for (const [name, viewport] of [["tablet", { width: 1024, height: 900 }], ["phone", { width: 390, height: 844 }]] as const) {
    test.describe(name, () => {
      test.use({ viewport, hasTouch: viewport.width < 768, isMobile: viewport.width < 768 })

      test(`${name}: a card header keeps its 50 px under a coarse pointer and the link stays a 44 px target`, async ({ page }) => {
        await seed(page)
        await routeAttention(page)
        await page.goto("/")
        const card = page.getByRole("region", { name: "Up next" })
        await expect(card).toBeVisible()
        expect(Math.round((await card.locator("header").boundingBox())!.height)).toBeLessThanOrEqual(52)
        if (viewport.width < 768) {
          expect((await card.getByRole("link", { name: /Calendar/ }).boundingBox())!.height).toBeGreaterThanOrEqual(44)
        }
      })
    })
  }
})
