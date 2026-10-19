import { test, expect, type Page } from "@playwright/test"

// Theme packs (#3790): Afterglow in Settings › Appearance › Style › Gaming.
//
//   1. Choosing Afterglow re-themes the app (ground, primary action, title
//      face), moves the theme to Dark, keeps the Crewship sail blue, and a
//      reload keeps all of it.
//   2. Reduce motion stops the glow pulse.
//   3. A workspace can make it the default look for members who chose nothing
//      (needs the workspace default from #3771 and E2E_ALLOW_WRITES=1; the
//      default is put back as it was).

const STYLE_KEY = "crewship-appearance-style"
const attr = (page: Page, name: string) => page.evaluate((n) => document.documentElement.getAttribute(n), name)
const css = (page: Page, selector: string, prop: string) =>
  page.evaluate(([s, p]) => getComputedStyle(document.querySelector(s)!).getPropertyValue(p), [selector, prop])

async function seed(page: Page, theme: "light" | "dark" | "system" = "light") {
  await page.addInitScript((t) => {
    // Only the first load: a reload must see what the app itself stored.
    if (localStorage.getItem("theme") === null) localStorage.setItem("theme", t)
    localStorage.setItem("crewship-appearance-sync", "0")
    localStorage.setItem("crewship_sidebar_mode", "pinned")
  }, theme)
}

test.describe("Afterglow", () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test("choosing it re-themes the app and a reload keeps it", async ({ page }) => {
    await seed(page, "light")
    await page.goto("/settings?tab=appearance")
    const gallery = page.getByRole("radiogroup", { name: "Style" })
    await expect(gallery.getByRole("group", { name: "Gaming" })).toBeVisible()
    const harborGround = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)

    await gallery.getByRole("radio", { name: "Afterglow", exact: true }).click()
    await expect.poll(() => attr(page, "data-style")).toBe("afterglow")
    // A light-mode user is moved to Dark; Light and System are off, and the page says why.
    await expect.poll(() => page.evaluate(() => document.documentElement.classList.contains("dark"))).toBe(true)
    await expect(page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Light" })).toBeDisabled()
    await expect(page.getByTestId("scheme-hint")).toContainText("Afterglow is a dark style")
    expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe("rgb(12, 13, 27)")
    expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).not.toBe(harborGround)
    // The accent stays the Crewship blue: no pack touches the brand tokens.
    expect(await attr(page, "data-accent")).toBe("blue")
    const brand = (await css(page, ":root", "--brand")).trim()
    await page.evaluate(() => document.documentElement.setAttribute("data-style", "harbor"))
    expect((await css(page, ":root", "--brand")).trim(), "the pack leaves --brand as Harbor has it").toBe(brand)
    await page.evaluate(() => document.documentElement.setAttribute("data-style", "afterglow"))
    expect(await page.evaluate((k) => localStorage.getItem(k), STYLE_KEY)).toContain('"style":"afterglow"')

    await page.reload()
    await expect.poll(() => attr(page, "data-style")).toBe("afterglow")
    await expect(page.getByRole("radiogroup", { name: "Style" }).getByRole("radio", { name: "Afterglow", exact: true })).toHaveAttribute("aria-checked", "true")
    expect(await page.evaluate(() => getComputedStyle(document.body).backgroundColor)).toBe("rgb(12, 13, 27)")
  })

  test("page titles use the display face and the rest keeps the text face", async ({ page }) => {
    await seed(page, "dark")
    await page.addInitScript((k) => localStorage.setItem(k, JSON.stringify({ v: 1, style: "afterglow", knobs: {} })), STYLE_KEY)
    await page.goto("/settings?tab=appearance")
    await expect(page.getByRole("radiogroup", { name: "Style" })).toBeVisible()
    const title = page.locator('[data-slot="settings-page-head"] h2, [data-slot="settings-page-head"] h1').first()
    await expect(title).toBeVisible()
    expect(await title.evaluate((e) => getComputedStyle(e).fontFamily)).toContain("Big Shoulders Display")
    expect(await title.evaluate((e) => getComputedStyle(e).textTransform)).toBe("uppercase")
    expect(await page.evaluate(() => getComputedStyle(document.body).fontFamily)).toContain("Manrope")
  })

  test("the glow pulse stops under reduce motion", async ({ page }) => {
    await seed(page, "dark")
    await page.addInitScript((k) => localStorage.setItem(k, JSON.stringify({ v: 1, style: "afterglow", knobs: {} })), STYLE_KEY)
    await page.goto("/settings?tab=appearance")
    await page.evaluate(() => {
      const el = document.createElement("div")
      el.id = "probe"
      el.className = "pack-pulse"
      document.body.appendChild(el)
    })
    expect(await css(page, "#probe", "animation-name")).toContain("pack-glow-pulse")
    await page.emulateMedia({ reducedMotion: "reduce" })
    expect(await css(page, "#probe", "animation-name")).toBe("none")
    await page.emulateMedia({ reducedMotion: "no-preference" })
    await page.evaluate(() => (document.documentElement.dataset.motion = "reduce"))
    expect(await css(page, "#probe", "animation-name")).toBe("none")
  })
})

test.describe("Afterglow as the workspace default", () => {
  test.skip(!process.env.E2E_ALLOW_WRITES, "changes the workspace default; set E2E_ALLOW_WRITES=1 on a disposable environment")
  test.use({ viewport: { width: 1440, height: 900 } })

  test("a member who chose nothing sees it", async ({ page, request }) => {
    const list = await (await request.get("/api/v1/workspaces")).json()
    const id = (Array.isArray(list) ? list[0] : list)?.id as string
    const url = `/api/v1/workspaces/${encodeURIComponent(id)}?workspace_id=${encodeURIComponent(id)}`
    const ws = await (await request.get(url)).json()
    test.skip(!("appearance_default" in ws), "this build has no workspace default look (#3771)")
    const original = ws.appearance_default ?? {}
    try {
      const set = await request.patch(url, { data: { appearance_default: { style: "afterglow", theme: "dark" } } })
      expect(set.status(), await set.text()).toBeLessThan(300)
      // Nothing chosen on this browser, and sync on so the default can be inherited.
      await page.addInitScript(() => { localStorage.removeItem("crewship-appearance-style") })
      await page.goto("/settings?tab=appearance")
      await expect.poll(() => attr(page, "data-style")).toBe("afterglow")
    } finally {
      await request.patch(url, { data: { appearance_default: original } })
    }
  })
})
