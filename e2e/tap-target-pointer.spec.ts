import { test, expect, type Browser, type Page } from "@playwright/test"

// ODCH-06: under `pointer: coarse` the active area is at least 44 px; under
// `pointer: fine` sizes follow v67. A control that carries `coarse:min-h-[44px]`
// keeps its scale size at a fine pointer (1440, no touch) and is 44 px at a
// coarse one (390, hasTouch). lib/__tests__/coarse-tap-target-px.test.ts holds
// the static half; this is the rendered half, on the pages that hold the
// changed controls (Skills library rows, credential rows, the shell).

const PASSWORD = process.env.E2E_PASSWORD_ROLES ?? "rbac-demo-1234"
const PINNED = { crewship_sidebar_mode: "pinned", "crewship-appearance-sync": "0" }
const PAGES = ["/skills", "/credentials", "/crews", "/issues"]

async function signedIn(browser: Browser) {
  const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
  const page = await ctx.newPage()
  await page.goto("/login")
  await page.getByLabel(/email/i).fill("admin-baseline@crewship.local")
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD)
  await page.getByRole("button", { name: /sign in|log in/i }).click()
  await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 20_000 })
  const state = await ctx.storageState()
  await ctx.close()
  return state
}

/** Every rendered element that opts into the coarse floor, with its computed floor and box. */
async function floors(page: Page) {
  return page.evaluate(() =>
    [...document.querySelectorAll<HTMLElement>("[class*='coarse:min-h-[44px]']")]
      .filter((e) => e.getBoundingClientRect().height > 0)
      .map((e) => ({
        what: (e.getAttribute("data-slot") ?? e.tagName.toLowerCase()) + ": " + (e.textContent ?? "").trim().slice(0, 30),
        minHeight: getComputedStyle(e).minHeight,
        height: e.getBoundingClientRect().height,
      })),
  )
}

test.describe("fine vs coarse tap targets", () => {
  let state: Awaited<ReturnType<typeof signedIn>>
  test.beforeAll(async ({ browser }) => {
    state = await signedIn(browser)
  })

  test("fine pointer at 1440 keeps the scale size, no 44 px floor", async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: state, viewport: { width: 1440, height: 900 }, hasTouch: false })
    const page = await ctx.newPage()
    await page.addInitScript((e) => { for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v) }, PINNED)
    expect(await page.evaluate(() => matchMedia("(pointer: fine)").matches)).toBe(true)
    let seen = 0
    for (const url of PAGES) {
      await page.goto(url)
      await page.waitForLoadState("networkidle")
      for (const f of await floors(page)) {
        seen++
        expect(f.minHeight, `${url} ${f.what}`).not.toBe("44px")
      }
    }
    expect(seen, "some changed control rendered").toBeGreaterThan(0)
    await ctx.close()
  })

  test("coarse pointer at 390 gives every one of them at least 44 px", async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: state, viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })
    const page = await ctx.newPage()
    await page.addInitScript((e) => { for (const [k, v] of Object.entries(e)) localStorage.setItem(k, v) }, PINNED)
    let seen = 0
    for (const url of PAGES) {
      await page.goto(url)
      await page.waitForLoadState("networkidle")
      for (const f of await floors(page)) {
        seen++
        expect(f.minHeight, `${url} ${f.what}`).toBe("44px")
        expect(f.height, `${url} ${f.what}`).toBeGreaterThanOrEqual(44)
      }
    }
    expect(seen, "some changed control rendered").toBeGreaterThan(0)
    await ctx.close()
  })
})
