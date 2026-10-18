import { test, expect, type Browser, type BrowserContext } from "@playwright/test"

// CHR-01 (#3482): the shell per role - sidebar at 1440, tab bar and More sheet
// at 390, and the user menu on both. Each role signs in once (NextAuth rate
// limits credential logins) and every test reuses that session.
//
// Roles are the demo-RBAC accounts: admin-baseline (instance administrator)
// and member-baseline (workspace member, not an administrator). The pinned
// role x row matrix is lib/__tests__/nav-role-matrix.test.ts; this spec checks
// the same rows survive the real shell.

const PASSWORD = process.env.E2E_PASSWORD_ROLES ?? "rbac-demo-1234"
const ROLES = {
  admin: { email: "admin-baseline@crewship.local", sees: { admin: true } },
  member: { email: "member-baseline@crewship.local", sees: { admin: false } },
} as const

// What a full session sees, in order. Calendar is checked separately: it is a
// row only where the Calendar page exists, and its place in Work is not what
// this spec is about.
const CORE_HREFS = [
  "/", "/inbox", "/chat", "/activity",
  "/issues", "/routines", "/pages",
  "/crews", "/skills", "/credentials", "/integrations",
]

// A sheet slides in; its box is only the docked one once the animation is over.
// Wait until nothing animates on it, its transform is gone and the box has been
// identical across two frames - measuring earlier reads a mid-slide rect.
async function settled(el: import("@playwright/test").Locator) {
  await el.evaluate(
    (node) =>
      new Promise<void>((resolve) => {
        let last = ""
        let same = 0
        const tick = () => {
          const r = node.getBoundingClientRect()
          const now = [r.x, r.y, r.width, r.height].join(",")
          const idle = node.getAnimations({ subtree: true }).length === 0
          const t = getComputedStyle(node).transform
          same = idle && (t === "none" || t === "matrix(1, 0, 0, 1, 0, 0)") && now === last ? same + 1 : 0
          last = now
          if (same >= 3) resolve()
          else requestAnimationFrame(tick)
        }
        requestAnimationFrame(tick)
      }),
  )
}

type Who = keyof typeof ROLES
const states = new Map<Who, ReturnType<BrowserContext["storageState"]>>()

async function sessionFor(browser: Browser, who: Who) {
  if (!states.has(who)) {
    states.set(who, (async () => {
      const ctx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
      const page = await ctx.newPage()
      await page.goto("/login")
      await page.getByLabel(/email/i).fill(ROLES[who].email)
      await page.getByLabel("Password", { exact: true }).fill(PASSWORD)
      await page.getByRole("button", { name: /sign in|log in/i }).click()
      await page.waitForURL((u) => !u.pathname.startsWith("/login"), { timeout: 20_000 })
      const state = await ctx.storageState()
      await ctx.close()
      return state
    })())
  }
  return states.get(who)!
}

for (const who of Object.keys(ROLES) as Who[]) {
  test.describe(`${who}-baseline`, () => {
    test.describe("desktop 1440", () => {
      test("the sidebar offers the full set of destinations, no Journal", async ({ browser }) => {
        const ctx = await browser.newContext({ storageState: await sessionFor(browser, who), viewport: { width: 1440, height: 900 } })
        const page = await ctx.newPage()
        await page.goto("/issues")
        const rail = page.locator("[data-slot=sidebar-container]")
        await expect(rail).toBeVisible()
        const hrefs = await rail.locator("a[href]").evaluateAll((as) => as.map((a) => a.getAttribute("href")))
        expect(hrefs.filter((h) => h !== "/calendar" && h !== "/admin" && h !== "/settings")).toEqual(CORE_HREFS)
        expect(hrefs).toContain("/settings")
        expect(hrefs).not.toContain("/journal")
        if (!ROLES[who].sees.admin) expect(hrefs).not.toContain("/admin")
        await ctx.close()
      })

      test("the user menu opens from the sidebar foot with the v67 rows", async ({ browser }) => {
        const ctx = await browser.newContext({ storageState: await sessionFor(browser, who), viewport: { width: 1440, height: 900 } })
        const page = await ctx.newPage()
        await page.goto("/issues")
        await page.getByRole("button", { name: "User menu" }).click()
        const menu = page.getByRole("menu")
        for (const name of [/^profile & account$/i, /^appearance$/i, /sessions & cli tokens/i, /help & resources/i, /^log out$/i]) {
          await expect(menu.getByRole("menuitem", { name })).toBeVisible()
        }
        await expect(menu.getByRole("radiogroup", { name: "Theme" })).toBeVisible()
        await ctx.close()
      })
    })

    test.describe("phone 390", () => {
      const phone = { viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true } as const

      test("tab bar is Home, Inbox, Chat, Issues, More", async ({ browser }) => {
        const ctx = await browser.newContext({ storageState: await sessionFor(browser, who), ...phone })
        const page = await ctx.newPage()
        await page.goto("/issues")
        const bar = page.getByRole("navigation", { name: "Primary" })
        await expect(bar.getByRole("link")).toHaveText(["Home", "Inbox", "Chat", "Issues"])
        await expect(bar.getByRole("button", { name: "More" })).toBeVisible()
        await ctx.close()
      })

      test("More is a grid of tiles with the user row, and no horizontal scroll", async ({ browser }) => {
        const ctx = await browser.newContext({ storageState: await sessionFor(browser, who), ...phone })
        const page = await ctx.newPage()
        await page.goto("/issues")
        await page.getByRole("navigation", { name: "Primary" }).getByRole("button", { name: "More" }).click()
        const grid = page.getByRole("list", { name: "Navigation" })
        await expect(grid).toBeVisible()
        const hrefs = await grid.locator("a[href]").evaluateAll((as) => as.map((a) => a.getAttribute("href")))
        expect(hrefs.filter((h) => h !== "/calendar" && h !== "/admin" && h !== "/settings")).toEqual(CORE_HREFS)
        expect(hrefs).not.toContain("/journal")
        if (!ROLES[who].sees.admin) expect(hrefs).not.toContain("/admin")
        await expect(grid.locator('[data-slot="concept-tile"]')).not.toHaveCount(0)
        // Four columns: the first tile row shares one y.
        const ys = await grid.locator("li").evaluateAll((li) => li.slice(0, 4).map((e) => Math.round(e.getBoundingClientRect().top)))
        expect(new Set(ys).size).toBe(1)
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
        expect(overflow).toBeLessThanOrEqual(0)
        // Every tile is a comfortable target.
        for (const box of await grid.locator("a").evaluateAll((as) => as.map((a) => a.getBoundingClientRect().height))) {
          expect(box).toBeGreaterThanOrEqual(44)
        }
        await ctx.close()
      })

      test("the user row in More opens the user menu as a bottom sheet", async ({ browser }) => {
        const ctx = await browser.newContext({ storageState: await sessionFor(browser, who), ...phone })
        const page = await ctx.newPage()
        await page.goto("/issues")
        await page.getByRole("navigation", { name: "Primary" }).getByRole("button", { name: "More" }).click()
        await page.getByRole("dialog", { name: "Navigation" }).getByRole("button", { name: "User menu" }).click()
        // A sheet (dialog) docked to the bottom edge, not a popover menu.
        const sheet = page.getByRole("dialog", { name: "User menu" })
        await expect(sheet).toBeVisible()
        await expect(page.getByRole("menu")).toHaveCount(0)
        await settled(sheet)
        const box = (await sheet.boundingBox())!
        expect(Math.round(box.x)).toBe(0)
        expect(Math.round(box.width)).toBe(390)
        expect(Math.round(box.y + box.height)).toBe(844)
        await expect(sheet.getByRole("link", { name: /^appearance$/i })).toBeVisible()
        await expect(sheet.getByRole("button", { name: /^log out$/i })).toBeVisible()
        // Every row is a comfortable target.
        // (Notification sounds is a switch row: it was 37.6 px from min-h-11.)
        const rows = await sheet.locator("[data-menu-row]").evaluateAll((els) =>
          els.map((e) => ({ name: (e.textContent ?? "").trim(), h: e.getBoundingClientRect().height })),
        )
        expect(rows.map((r) => r.name)).toContain("Notification sounds")
        for (const r of rows) expect(r.h, r.name).toBeGreaterThanOrEqual(44)
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
        expect(overflow).toBeLessThanOrEqual(0)
        // Picking a row closes it.
        await sheet.getByRole("link", { name: /^appearance$/i }).click()
        await expect(page.getByRole("dialog", { name: "User menu" })).toHaveCount(0)
        await ctx.close()
      })
    })

    // Tablet (768 to 1279 px): the rail is narrow, and the user menu is still
    // the popover - the sheet is for phones only. Role x rows matrix.
    test.describe("tablet 1024", () => {
      test("the user menu is a popover with the role's rows", async ({ browser }) => {
        const ctx = await browser.newContext({ storageState: await sessionFor(browser, who), viewport: { width: 1024, height: 768 } })
        const page = await ctx.newPage()
        await page.goto("/issues")
        await page.getByRole("button", { name: "User menu" }).click()
        const menu = page.getByRole("menu")
        await expect(menu).toBeVisible()
        await expect(page.getByRole("dialog", { name: "User menu" })).toHaveCount(0)
        // A popover: narrower than the viewport, not docked to the bottom edge.
        const box = (await menu.boundingBox())!
        expect(box.width).toBeLessThan(400)
        expect(box.y + box.height).toBeLessThan(768)
        // "Notification sounds" is a switch row: menuitemcheckbox, not menuitem.
        const rows = (await menu.locator('[role="menuitem"], [role="menuitemcheckbox"]').allTextContents()).map((t) => t.replace(/\d+ active$/, "").trim())
        expect(rows).toEqual(expect.arrayContaining(["Profile & account", "Appearance", "Sessions & CLI tokens", "Help & resources", "Log out"]))
        // Both demo roles are trusted sessions: the full set, in the v67 order.
        const order = ["Profile & account", "Appearance", "Notification sounds", "Notification preferences", "Sessions & CLI tokens", "Connect CLI…", "Help & resources", "Log out"]
        const seen = order.map((n) => rows.findIndex((r) => r.includes(n)))
        expect(seen.every((i) => i >= 0)).toBe(true)
        expect([...seen].sort((a, b) => a - b)).toEqual(seen)
        await expect(menu.getByRole("radiogroup", { name: "Theme" })).toBeVisible()
        await expect(menu.getByText(/^(Owner|Admin|Manager|Member|Viewer)$/)).toBeVisible()
        await ctx.close()
      })
    })
  })
}
