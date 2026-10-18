import { test, expect, type Page } from "@playwright/test"

// Harbor 2 appearance in a real browser (COM-17, #3481; PRD APR-11).
// With the theme on System the app follows the device's colour scheme live,
// without a reload; an explicit Light or Dark ignores the device.
//
// APR-03 (choosing a style) has no UI to drive yet: the style gallery is M6,
// gated on the owner's visual approval (Q15). Its spec is added with M6.

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
