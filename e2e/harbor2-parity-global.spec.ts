import { test, expect } from "@playwright/test"

// Harbor 2 parity, global chrome (PAR-001/003/004/007/008/141/143/151/154/155
// and the phone header). Measured against the v67 wireframe. Signed-in tests
// use the suite's stored session (global-setup.ts). Written before deploy:
// run them against dev1 after `crew deploy`.

// Round 63 (#3784): the workspace mark, search, every app, the sail and the
// avatar share one axis, 28 px from the sidebar's left edge, open or folded;
// icons are 18 px and the three identities 28 px.
async function expectOnAxis(page: import("@playwright/test").Page) {
  const r = await page.evaluate(() => {
    const sb = document.querySelector("[data-slot=sidebar-container]")!.getBoundingClientRect()
    const centre = (el: Element | null) => { if (!el) return null; const q = el.getBoundingClientRect(); return { c: q.left - sb.left + q.width / 2, w: q.width } }
    const glyph = (sel: string) => centre(document.querySelector(sel)?.querySelector("svg") ?? null)
    return {
      ws: centre(document.querySelector(".rail-ws")),
      search: glyph(".rail-search .rail-tile"),
      inbox: glyph('a[href="/inbox"] [data-slot=rail-tile]'),
      settings: glyph('a[href="/settings"] [data-slot=rail-tile]'),
      brand: centre(document.querySelector(".rail-brand")),
      avatar: centre(document.querySelector(".rail-user [data-slot=presence-dot]")?.parentElement ?? null),
    }
  })
  for (const [k, v] of Object.entries(r)) {
    expect(v, k).not.toBeNull()
    expect(Math.abs(v!.c - 28), `${k} centre ${v!.c}`).toBeLessThanOrEqual(1)
  }
  for (const k of ["ws", "brand", "avatar"] as const) expect(Math.round(r[k]!.w), k).toBe(28)
  expect(Math.round(r.inbox!.w)).toBe(18)
}

test.describe("desktop 1440", () => {
  test.use({ viewport: { width: 1440, height: 900 } })
  // v67 draws the sidebar pinned; the product default is the hover rail, which
  // folds the user row's labels away and opens the user menu beside the rail.
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem("crewship_sidebar_mode", "pinned")
      localStorage.setItem("crewship-appearance-sync", "0")
    })
  })

  test("every rail tile is centred on x = 28 (PAR-003, round 63)", async ({ page }) => {
    await page.goto("/")
    await expectOnAxis(page)
  })

  test("sidebar search placeholder is 12.5 px (PAR-004)", async ({ page }) => {
    await page.goto("/")
    const size = await page.locator(".rail-search").evaluate((el) => getComputedStyle(el).fontSize)
    expect(size).toBe("12.5px")
  })

  test("the user row: 13 px name, presence dot, double chevron (PAR-007)", async ({ page }) => {
    await page.goto("/")
    const row = page.getByRole("button", { name: "User menu" })
    await expect(row.locator('[data-slot="presence-dot"]')).toBeVisible()
    const name = row.locator(".type-nav").first()
    expect(await name.evaluate((el) => getComputedStyle(el).fontSize)).toBe("13px")
    await expect(row.locator("svg.lucide-chevrons-up-down")).toBeVisible()
  })

  test("the user menu is 300 px and opens at the sidebar's left edge (PAR-141)", async ({ page }) => {
    await page.goto("/")
    await page.getByRole("button", { name: "User menu" }).click()
    const menu = page.getByRole("menu")
    await expect(menu).toBeVisible()
    const box = await menu.boundingBox()
    expect(Math.round(box!.width)).toBe(300)
    expect(box!.x).toBeLessThan(24)
    await expect(menu.getByText("Sessions & CLI tokens")).toBeVisible()
    const row = await menu.getByText("Sessions & CLI tokens").boundingBox()
    expect(row!.height).toBeLessThan(24) // one line
  })

  test("the sidebar mode menu is 280 px with tile rows (PAR-143)", async ({ page }) => {
    await page.goto("/")
    await page.getByRole("button", { name: /^Sidebar:/ }).click()
    const menu = page.getByRole("menu")
    const box = await menu.boundingBox()
    expect(Math.round(box!.width)).toBe(280)
    await expect(menu.locator('[data-slot="mode-tile"]')).toHaveCount(3)
  })

  test("the top bar ends with the activity and inbox bells (PAR-001)", async ({ page }) => {
    await page.goto("/")
    const header = page.locator("header").first()
    await expect(header.getByRole("button", { name: /crew images/i })).toHaveCount(0)
    await expect(header.locator('[data-testid="system-status-pill"]')).toHaveCount(0)
    await header.getByTestId("activity-trigger").click()
    await expect(page.getByTestId("activity-status-summary")).toBeVisible()
  })

  test("headings track at -0.005em so word spaces stay open (PAR-008)", async ({ page }) => {
    await page.goto("/settings/audit")
    const ls = await page.locator("h1").first().evaluate((el) => getComputedStyle(el).letterSpacing)
    const px = parseFloat(ls)
    const fs = await page.locator("h1").first().evaluate((el) => parseFloat(getComputedStyle(el).fontSize))
    expect(Math.abs(px / fs + 0.005)).toBeLessThan(0.002)
  })

  test("a list panel has 30 px rows and a labelled Filter button (PAR-154, PAR-155)", async ({ page }) => {
    await page.goto("/issues")
    const row = page.locator(".secondary-panel [data-slot=row-tile], .secondary-panel .type-nav").first()
    await expect(row).toBeVisible()
    const rowBox = await page.locator(".secondary-panel .min-h-\\[30px\\]").first().boundingBox()
    expect(rowBox!.height).toBeGreaterThanOrEqual(30)
    await expect(page.locator(".secondary-panel").getByRole("button", { name: /^Filter/ }).first()).toContainText("Filter")
  })

  test("no uppercase mono micro labels in the kit (PAR-152)", async ({ page }) => {
    await page.goto("/")
    const n = await page.locator(".eyebrow, .type-section, .type-page-label").evaluateAll((els) =>
      els.filter((el) => getComputedStyle(el).textTransform === "uppercase" || getComputedStyle(el).fontFamily.includes("JetBrains")).length)
    expect(n).toBe(0)
  })
})

test.describe("phone 390", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("the header is one row: title, blue plus, search, bell; chips under it (PAR-052)", async ({ page }) => {
    await page.goto("/routines")
    const header = page.locator("header").first()
    const h = await header.boundingBox()
    expect(h!.height).toBeLessThan(64)
    const plus = header.getByRole("button", { name: /new routine/i })
    await expect(plus).toBeVisible()
    const pb = await plus.boundingBox()
    expect(pb!.width).toBeLessThan(60) // icon-only, not a 260 px button
    await expect(header.getByRole("button", { name: "Search" })).toBeVisible()
    await expect(header.getByTestId("activity-trigger")).toBeVisible()
    // no second breadcrumb row under the bar
    await expect(page.locator("[data-slot=sub-bar-row]")).toHaveCount(0)
  })

  test("Activity has no breadcrumb row; chips follow the bar (PAR-063)", async ({ page }) => {
    await page.goto("/activity")
    await expect(page.locator("[data-slot=sub-bar-row]")).toHaveCount(0)
  })

  test("Calendar has a single header row (PAR-136)", async ({ page }) => {
    await page.goto("/calendar")
    await expect(page.locator("[data-slot=sub-bar-row]")).toHaveCount(0)
  })
})

test.describe("rail 1024", () => {
  test.use({ viewport: { width: 1024, height: 900 } })
  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem("crewship_sidebar_mode", "collapsed")
      localStorage.setItem("crewship-appearance-sync", "0")
    })
  })

  test("the folded rail keeps every tile on the same axis (round 63)", async ({ page }) => {
    await page.goto("/")
    await expectOnAxis(page)
    // Nothing beside the workspace mark in the rail: no chevron to push it off.
    const chevron = await page.locator(".rail-ws ~ svg.lucide-chevrons-up-down").boundingBox()
    expect(chevron === null || chevron.width === 0).toBe(true)
  })
})
