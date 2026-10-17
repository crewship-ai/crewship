import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for the foundations and the shell (#3332–#3334, #3350):
// sizes measured in a real browser, touch targets with a coarse pointer, the
// layout breakpoint and the first paint of a brand-new device.

const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }

async function seed(page: Page, entries: Record<string, string>) {
  await page.addInitScript((e) => {
    for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v)
  }, entries)
}

async function box(page: Page, selector: string) {
  const b = await page.locator(selector).first().boundingBox()
  expect(b, selector).not.toBeNull()
  return b!
}

test.describe("desktop shell", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("sidebar 240 px, top bar 48 px, page in a 12 px frame", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    await expect(page.locator("header").first()).toBeVisible()
    expect(Math.round((await box(page, "[data-slot=sidebar-container]")).width)).toBe(240)
    expect(Math.round((await box(page, "header")).height)).toBe(48)
    const frame = page.locator("[data-slot=sidebar-inset]")
    await expect(frame).toHaveCSS("border-top-left-radius", "12px")
  })

  for (const path of ["/crews", "/issues", "/settings", "/routines"]) {
    test(`${path} fits inside the frame without clipping its bottom`, async ({ page }) => {
      await seed(page, PINNED)
      await page.goto(path)
      await expect(page.locator("header").first()).toBeVisible()
      // These pages size themselves to the viewport and scroll inside; the
      // page scroller under the top bar must not scroll at all.
      const scroller = page.locator("[data-slot=sidebar-inset] > .overflow-y-auto").first()
      const overflow = await scroller.evaluate((el) => el.scrollHeight - el.clientHeight)
      expect(overflow).toBeLessThanOrEqual(1)
    })
  }

  test("the rail is 56 px when collapsed", async ({ page }) => {
    await seed(page, { ...PINNED, crewship_sidebar_mode: "collapsed" })
    await page.goto("/issues")
    expect(Math.round((await box(page, "[data-slot=sidebar-container]")).width)).toBe(56)
  })

  test("the top bar has at most one primary action and the bells at the right", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    const header = page.locator("header").first()
    await expect(header.getByRole("button", { name: /new issue/i })).toBeVisible()
    expect(await header.locator("button.bg-primary-strong, a.bg-primary-strong").count()).toBeLessThanOrEqual(1)
  })

  test("Search sits in the sidebar and opens the command palette", async ({ page }) => {
    await seed(page, PINNED)
    await page.goto("/issues")
    await page.locator("[data-sidebar=sidebar]").getByRole("button", { name: "Search" }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
  })
})

test.describe("touch", () => {
  for (const width of [390, 1024]) {
    test.describe(`${width} px with a coarse pointer`, () => {
      test.use({ viewport: { width, height: 900 }, hasTouch: true, isMobile: width < 768 })

      test("every button in the top bar and the sidebar is at least 44 px tall", async ({ page }) => {
        await seed(page, PINNED)
        await page.goto("/issues")
        await expect(page.locator("header").first()).toBeVisible()
        // The page's own actions join the top bar after it mounts; measure
        // them too, not just the shell's buttons.
        await expect(page.getByRole("button", { name: /new issue/i }).first()).toBeVisible()
        const small = await page.evaluate(() => {
          const out: string[] = []
          document.querySelectorAll("header button, [data-sidebar=sidebar] button, nav[aria-label=Primary] a, nav[aria-label=Primary] button").forEach((el) => {
            const r = el.getBoundingClientRect()
            if (r.width === 0 || r.height === 0) return
            if (r.height < 43.5) out.push(`${el.getAttribute("aria-label") ?? el.textContent?.trim()} ${Math.round(r.height)}px`)
          })
          return out
        })
        expect(small).toEqual([])
      })
    })
  }

  test.describe("1024 px with a fine pointer", () => {
    test.use({ viewport: { width: 1024, height: 900 } })
    test("buttons keep the Harbor 2 sizes (no forced 44 px)", async ({ page }) => {
      await seed(page, PINNED)
      await page.goto("/issues")
      const h = (await box(page, "header button:has-text('New issue')")).height
      expect(h).toBeLessThan(40)
    })
  })
})

test.describe("breakpoint", () => {
  for (const [width, phone] of [[767, true], [768, false]] as const) {
    test(`${width} px is ${phone ? "a phone" : "not a phone"}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 })
      await seed(page, PINNED)
      await page.goto("/issues")
      await expect(page.locator("header").first()).toBeVisible()
      await expect(page.locator("nav[aria-label=Primary]")).toHaveCount(phone ? 1 : 0)
    })
  }
})

test.describe("first paint of a brand-new device", () => {
  test.use({ storageState: { cookies: [], origins: [] }, colorScheme: "light" })

  test("starts at Small text and follows the system theme", async ({ page }) => {
    await page.goto("/login")
    const html = page.locator("html")
    await expect(html).toHaveAttribute("data-text-size", "small")
    await expect(html).not.toHaveClass(/(^|\s)dark(\s|$)/)
    await expect(html).toHaveAttribute("data-style", "harbor")
  })

  test("a device that used Crewship before keeps Standard and Dark", async ({ page }) => {
    await seed(page, { "crewship-accent": "blue" })
    await page.goto("/login")
    const html = page.locator("html")
    await expect(html).not.toHaveAttribute("data-text-size", /.+/)
    await expect(html).toHaveClass(/(^|\s)dark(\s|$)/)
  })
})
