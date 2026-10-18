import { test, expect, type Page } from "@playwright/test"

// Harbor 2 appearance in a real browser (COM-17, #3481; PRD APR-11).
// With the theme on System the app follows the device's colour scheme live,
// without a reload; an explicit Light or Dark ignores the device.
//
// APR-03/11, STY-01: the style gallery in Settings › Appearance — picking a
// style paints and keeps it, a dark-only style locks the scheme, and the
// gallery lays out on a phone and a tablet.

async function seed(page: Page, theme: "system" | "light" | "dark") {
  await page.addInitScript((t) => {
    localStorage.setItem("theme", t)
    localStorage.setItem("crewship-appearance-sync", "0")
    localStorage.setItem("crewship_sidebar_mode", "pinned")
  }, theme)
}

const isDark = (page: Page) => page.evaluate(() => document.documentElement.classList.contains("dark"))

test.describe("System theme follows the device live", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("switches light to dark and back without a reload", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "light" })
    await seed(page, "system")
    await page.goto("/issues")
    await expect(page.locator("header").first()).toBeVisible()
    expect(await isDark(page)).toBe(false)
    const navigations: string[] = []
    page.on("framenavigated", (f) => { if (f === page.mainFrame()) navigations.push(f.url()) })

    await page.emulateMedia({ colorScheme: "dark" })
    await expect.poll(() => isDark(page)).toBe(true)
    // The body ground follows too, not only the class.
    const darkGround = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)

    await page.emulateMedia({ colorScheme: "light" })
    await expect.poll(() => isDark(page)).toBe(false)
    const lightGround = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
    expect(lightGround).not.toBe(darkGround)
    expect(navigations).toEqual([])
  })

  for (const theme of ["light", "dark"] as const) {
    test(`an explicit ${theme} theme ignores the device`, async ({ page }) => {
      await page.emulateMedia({ colorScheme: theme === "light" ? "dark" : "light" })
      await seed(page, theme)
      await page.goto("/issues")
      await expect(page.locator("header").first()).toBeVisible()
      expect(await isDark(page)).toBe(theme === "dark")
      await page.emulateMedia({ colorScheme: theme })
      await page.emulateMedia({ colorScheme: theme === "light" ? "dark" : "light" })
      // Give a listener the chance to (wrongly) react.
      await page.waitForTimeout(300)
      expect(await isDark(page)).toBe(theme === "dark")
    })
  }
})

const STYLE_KEY = "crewship-appearance-style"
const attr = (page: Page, name: string) => page.evaluate((n) => document.documentElement.getAttribute(n), name)

test.describe("Style gallery (STY-01, APR-03, APR-11)", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("picking a style paints it and it is still there after a reload", async ({ page }) => {
    await seed(page, "light")
    await page.goto("/settings?tab=appearance")
    const gallery = page.getByRole("radiogroup", { name: "Style" })
    await expect(gallery).toBeVisible()
    await expect(gallery.getByRole("radio", { name: "Harbor", exact: true })).toHaveAttribute("aria-checked", "true")
    // Plum and Harbor share a white content ground in light; their app ground
    // (the sidebar, --sidebar: #F3F4F6 vs #F7F5FB) is what differs.
    const ground = () => page.evaluate(() => getComputedStyle(document.querySelector("[data-slot=sidebar-inner]")!).backgroundColor)
    const harborGround = await ground()

    await gallery.getByRole("radio", { name: "Plum", exact: true }).click()
    await expect.poll(() => attr(page, "data-style")).toBe("plum")
    expect(await page.evaluate((k) => localStorage.getItem(k), STYLE_KEY)).toContain('"style":"plum"')
    await expect.poll(() => attr(page, "data-accent")).toBe("violet")

    await page.reload()
    await expect.poll(() => attr(page, "data-style")).toBe("plum")
    await expect(page.getByRole("radiogroup", { name: "Style" }).getByRole("radio", { name: "Plum", exact: true })).toHaveAttribute("aria-checked", "true")
    const plumGround = await ground()
    expect(plumGround).not.toBe(harborGround)

    await page.getByRole("button", { name: "Reset style" }).click()
    await expect.poll(() => attr(page, "data-style")).toBe("harbor")
  })

  test("a corner knob changes the radius tokens and nothing else", async ({ page }) => {
    await seed(page, "light")
    await page.goto("/settings?tab=appearance")
    await expect(page.getByRole("group", { name: "Corners" })).toBeVisible()
    const radius = () => page.evaluate(() => parseFloat(getComputedStyle(document.querySelector("[data-slot='card'], .rounded-card")!).borderTopLeftRadius))
    const before = await radius()
    await page.getByRole("group", { name: "Corners" }).getByRole("button", { name: "Sharp" }).click()
    await expect.poll(() => attr(page, "data-knob-radius")).toBe("sharp")
    expect(await radius()).toBeLessThan(before)
    expect(await attr(page, "data-style")).toBe("harbor")
    expect(await attr(page, "data-knob-cards")).toBeNull()
  })

  test("a dark-only style sets Dark, disables Light and System, and Switch to Harbor undoes it", async ({ page }) => {
    await seed(page, "light")
    await page.goto("/settings?tab=appearance")
    await page.getByRole("radiogroup", { name: "Style" }).getByRole("radio", { name: "Harbor Night", exact: true }).click()
    await expect.poll(() => isDark(page)).toBe(true)
    await expect.poll(() => attr(page, "data-style")).toBe("harbor-night")
    const themes = page.getByRole("radiogroup", { name: "Theme" })
    await expect(themes.getByRole("radio", { name: "Light" })).toBeDisabled()
    await expect(themes.getByRole("radio", { name: "System" })).toBeDisabled()
    await expect(page.getByTestId("scheme-hint")).toContainText("Harbor Night is a dark style")
    // The device switching to light does not matter: the theme is Dark, not System.
    await page.emulateMedia({ colorScheme: "light" })
    expect(await isDark(page)).toBe(true)

    await page.getByRole("button", { name: "Switch to Harbor" }).click()
    await expect.poll(() => attr(page, "data-style")).toBe("harbor")
    await expect(themes.getByRole("radio", { name: "Light" })).toBeEnabled()
  })
})

for (const [label, viewport, minColumns, maxColumns] of [
  ["phone 390", { width: 390, height: 844 }, 1, 1],
  ["tablet 1024", { width: 1024, height: 768 }, 2, 3],
] as const) {
  test.describe(`Style gallery at ${label}`, () => {
    test.use({ viewport })

    test("the grid collapses, nothing overflows and every preview shows", async ({ page }) => {
      await seed(page, "dark")
      await page.goto("/settings?tab=appearance")
      const gallery = page.getByRole("radiogroup", { name: "Style" })
      await expect(gallery).toBeVisible()
      const tiles = gallery.getByRole("radio")
      expect(await tiles.count()).toBeGreaterThanOrEqual(13)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
      const firstGrid = gallery.getByTestId("style-grid").first()
      const lefts = await firstGrid.getByRole("radio").evaluateAll((els) => new Set(els.map((e) => Math.round(e.getBoundingClientRect().left))).size)
      expect(lefts).toBeGreaterThanOrEqual(minColumns)
      expect(lefts).toBeLessThanOrEqual(maxColumns)
      for (const preview of await gallery.getByTestId("style-preview").all()) {
        await preview.scrollIntoViewIfNeeded()
        const box = await preview.boundingBox()
        expect(box!.width).toBeGreaterThan(40)
        expect(box!.height).toBeGreaterThan(24)
        expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width + 1)
      }
    })

    test("picking works here too", async ({ page }) => {
      await seed(page, "dark")
      await page.goto("/settings?tab=appearance")
      const tile = page.getByRole("radiogroup", { name: "Style" }).getByRole("radio", { name: "Slate", exact: true })
      await tile.scrollIntoViewIfNeeded()
      await tile.click()
      await expect.poll(() => attr(page, "data-style")).toBe("slate")
    })
  })
}

test.describe("Style gallery on a touch device", () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test("tiles and Reset style are at least 44 px tall", async ({ page }) => {
    await seed(page, "dark")
    await page.goto("/settings?tab=appearance")
    const gallery = page.getByRole("radiogroup", { name: "Style" })
    await expect(gallery).toBeVisible()
    for (const tile of (await gallery.getByRole("radio").all()).slice(0, 4)) {
      await tile.scrollIntoViewIfNeeded()
      expect((await tile.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    }
    const reset = page.getByRole("button", { name: "Reset style" })
    await reset.scrollIntoViewIfNeeded()
    expect((await reset.boundingBox())!.height).toBeGreaterThanOrEqual(43.5)
  })
})
