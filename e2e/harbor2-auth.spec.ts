import { test, expect, type Page } from "@playwright/test"

// Harbor 2 acceptance for the sign-in and public pages (#3410): one auth card
// on every unauthenticated route, measured in a real browser at the three
// layout widths and in both themes, plus the keyboard path and the deep-link
// round trip. Runs signed out.
//
// E2E_EMAIL / E2E_PASSWORD name an existing account for the round trip.

test.use({ storageState: { cookies: [], origins: [] } })

const WIDTHS = [
  { name: "desk", width: 1440, height: 900 },
  { name: "tablet", width: 1024, height: 768 },
  { name: "phone", width: 390, height: 844 },
] as const

async function theme(page: Page, scheme: "light" | "dark") {
  await page.emulateMedia({ colorScheme: scheme })
  await page.addInitScript((s) => {
    try {
      localStorage.setItem("theme", s)
    } catch {
      /* storage blocked */
    }
  }, scheme)
}

async function card(page: Page) {
  const c = page.locator("[data-slot=auth-card]").first()
  await expect(c).toBeVisible()
  return c
}

for (const w of WIDTHS) {
  for (const scheme of ["light", "dark"] as const) {
    test(`/login ${w.width} ${scheme} — snapshot`, async ({ page }, info) => {
      await page.setViewportSize({ width: w.width, height: w.height })
      await theme(page, scheme)
      await page.goto("/login")
      const c = await card(page)
      await expect(page.getByRole("heading", { name: "Sign in to Crewship" })).toBeVisible()
      // Exactly one primary in the card.
      expect(await c.locator("button.bg-primary-strong, a.bg-primary-strong").count()).toBe(1)
      const box = (await c.boundingBox())!
      if (w.width >= 768) {
        // A 480 px card with a frame on a desk and a tablet.
        expect(Math.round(box.width)).toBe(480)
        await expect(c).toHaveCSS("border-top-left-radius", "14px")
      } else {
        // The whole screen on a phone, with the primary across the width.
        expect(Math.round(box.width)).toBe(w.width)
        const primary = (await c.getByRole("button", { name: "Sign in" }).boundingBox())!
        expect(primary.width).toBeGreaterThan(w.width - 40)
      }
      await page.screenshot({ path: info.outputPath(`login-${w.width}-${scheme}.png`), fullPage: true })
    })
  }
}

test("tab order Email → Password → Forgot → Sign in", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto("/login")
  await card(page)
  await page.getByLabel("Email").focus()
  const order: string[] = []
  for (let i = 0; i < 3; i++) {
    await page.keyboard.press("Tab")
    order.push(
      await page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null
        return el?.id || el?.textContent?.trim() || ""
      }),
    )
  }
  // The primary comes before the footer links in the DOM, so this holds
  // whether or not sign-up is on.
  expect(order).toEqual(["password", "Forgot password?", "Sign in"])
})

test("touch targets are 44 px with a coarse pointer", async ({ browser }) => {
  const context = await browser.newContext({
    viewport: { width: 390, height: 844 },
    hasTouch: true,
    isMobile: true,
    storageState: { cookies: [], origins: [] },
  })
  const page = await context.newPage()
  await page.goto("/signup")
  const c = await card(page)
  for (const label of ["Full name", "Email", "Password", "Confirm password"]) {
    const field = page.getByLabel(label, { exact: true })
    await field.scrollIntoViewIfNeeded()
    await expect(field).toBeInViewport()
    expect(((await field.boundingBox())!).height).toBeGreaterThanOrEqual(44)
  }
  expect(((await c.getByRole("button", { name: "Create account" }).boundingBox())!).height).toBeGreaterThanOrEqual(44)
  await context.close()
})

test("a wrong confirmation is shown under its field, not above the form", async ({ page }) => {
  await page.goto("/signup")
  await card(page)
  await page.getByLabel("Full name").fill("Playwright Reader")
  await page.getByLabel("Email", { exact: true }).fill("pw-reader@example.test")
  await page.getByLabel("Password", { exact: true }).fill("long-enough-pw")
  await page.getByLabel("Confirm password").fill("long-enough-px")
  await page.getByRole("button", { name: "Create account" }).click()
  const error = page.locator("#confirm_password-error")
  await expect(error).toHaveText("Passwords do not match")
  await expect(page.getByLabel("Confirm password")).toHaveAttribute("aria-invalid", "true")
})

test("deep link → login → back to the same URL incl. query", async ({ page }) => {
  const email = process.env.E2E_EMAIL
  const password = process.env.E2E_PASSWORD
  test.skip(!email || !password, "E2E_EMAIL / E2E_PASSWORD not set")
  const target = "/issues?view=mine"
  await page.goto(`/login?redirect=${encodeURIComponent(target)}`)
  await card(page)
  await page.getByLabel("Email").fill(email!)
  await page.getByLabel("Password", { exact: true }).fill(password!)
  await page.getByRole("button", { name: "Sign in" }).click()
  await page.waitForURL((u) => u.pathname === "/issues")
  expect(new URL(page.url()).search).toBe("?view=mine")
})

for (const path of ["/forgot-password", "/reset-password?token=synthetic", "/join"]) {
  test(`${path} renders in the auth card`, async ({ page }) => {
    await page.goto(path)
    await card(page)
    await expect(page.locator("[data-slot=auth-lockup]")).toBeVisible()
  })
}

test("/shared-chat: public bar only, no account controls", async ({ page }) => {
  await page.goto("/shared-chat")
  const bar = page.locator("[data-slot=public-bar]")
  await expect(bar).toBeVisible()
  await expect(bar).toContainText("Shared chat")
  expect(await bar.locator("a, button").count()).toBe(0)
  expect(Math.round(((await bar.boundingBox())!).height)).toBe(48)
})

test("/p/<token> keeps noindex + no-referrer and shows the public bar", async ({ page }) => {
  const requests: string[] = []
  page.on("request", (r) => requests.push(new URL(r.url()).pathname))
  await page.goto("/p/synthetic-unknown-token")
  await expect(page.locator("[data-slot=public-bar]")).toContainText("Shared artifact")
  await expect(page.locator('meta[name="robots"]')).toHaveAttribute("content", /noindex/)
  await expect(page.locator('meta[name="referrer"]')).toHaveAttribute("content", "no-referrer")
  // No session or workspace request from the public page.
  expect(requests.filter((p) => p === "/api/v1/me" || p.startsWith("/api/v1/workspaces"))).toEqual([])
})
