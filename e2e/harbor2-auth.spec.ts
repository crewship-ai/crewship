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

// AUTH-01 (A06): the card names the page a valid redirect returns to, by its
// navigation name, and says nothing for a target the sign-in would refuse.
test("A06 a valid redirect names the page under the title; a refused one says nothing", async ({ page }) => {
  await page.goto(`/login?redirect=${encodeURIComponent("/issues/ISS-1?tab=activity")}`)
  const c = await card(page)
  await expect(c.locator("[data-slot=auth-return-to]")).toHaveText("You'll return to Issues.")
  await expect(c).not.toContainText("ISS-1")

  await page.goto(`/login?redirect=${encodeURIComponent("https://example.com/issues")}`)
  await card(page)
  await expect(page.getByRole("heading", { name: "Sign in to Crewship" })).toBeVisible()
  await expect(page.locator("[data-slot=auth-return-to]")).toHaveCount(0)
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

// ── AUTH-02 / AUTH-03: the public views in Harbor 2 (P01–P06) ───────────────
// Tablet 1024 and phone 390: nothing scrolls sideways, the public bar is the
// only chrome, and the viewport height is owned by the page, not by the view.

const PUBLIC_WIDTHS = [
  { name: "tablet", width: 1024, height: 768 },
  { name: "phone", width: 390, height: 844 },
] as const

async function noSidewaysScroll(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
}

const SHARE_ID = `cshr_${"a".repeat(32)}`
const SHARE_TOKEN = `cshr_${"B".repeat(43)}`

for (const w of PUBLIC_WIDTHS) {
  test(`P03 /p/<token> unavailable at ${w.width}: one sentence, no actions, no overflow`, async ({ page }) => {
    await page.setViewportSize({ width: w.width, height: w.height })
    await page.route("**/api/v1/public/pages/**", (route) =>
      route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { message: "gone" } }) }),
    )
    await page.goto("/p/synthetic-unknown-token")
    const state = page.locator("[data-state=not-found]")
    await expect(state).toBeVisible()
    await expect(state).toContainText("This link is not available")
    await expect(state).toContainText("It may have expired or been withdrawn.")
    expect(await state.locator("a, button").count()).toBe(0)
    await expect(page.locator("[data-slot=public-bar]")).toBeVisible()
    await noSidewaysScroll(page)
  })

  test(`P02 /p/<token> password at ${w.width}: auth card, server text verbatim, token not in URL`, async ({ page }) => {
    await page.setViewportSize({ width: w.width, height: w.height })
    // The refusal exactly as internal/api/pages_public.go replyPublicPasswordRefused
    // writes it: 401, the sentence as a plain string under "error".
    await page.route("**/api/v1/public/pages/*/unlock", (route) =>
      route.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ error: "that link and password do not match", password_required: true }) }),
    )
    await page.route("**/api/v1/public/pages/*", (route) =>
      route.fulfill({ status: 401, contentType: "application/json", body: "{}" }),
    )
    await page.goto("/p/synthetic-protected-token")
    const c = await card(page)
    await expect(page.getByRole("heading", { name: "This artifact is protected" })).toBeVisible()
    await expect(page.locator("[data-slot=auth-lockup]")).toHaveCount(0)
    await page.getByLabel("Password").fill("not-the-password")
    await page.getByRole("button", { name: "Open" }).click()
    // Next.js mounts an empty <div role="alert" id="__next-route-announcer__">
    // for client-side route announcements, so a bare getByRole("alert")
    // resolves to two elements. Exclude only the announcer: the page must
    // still carry exactly one alert, and it must read the server's sentence.
    await expect(page.locator('[role="alert"]:not(#__next-route-announcer__)')).toHaveText("that link and password do not match")
    if (w.width < 768) {
      expect(Math.round(((await c.boundingBox())!).width)).toBe(w.width)
    } else {
      expect(Math.round(((await c.boundingBox())!).width)).toBe(480)
    }
    expect(page.url()).not.toContain("not-the-password")
    await noSidewaysScroll(page)
  })

  test(`P01/P04 /p/<token> ready at ${w.width}: head, read-only meta, expiry, no controls`, async ({ page }) => {
    await page.setViewportSize({ width: w.width, height: w.height })
    await page.route("**/api/v1/public/pages/*", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          slug: "close",
          name: "Monthly close",
          description: "",
          generated_at: "2026-10-18T09:00:00Z",
          expires_at: "2026-10-31T09:00:00Z",
          show_provenance: false,
          panels: [],
        }),
      }),
    )
    await page.goto("/p/synthetic-ready-token")
    await expect(page.getByRole("heading", { level: 1, name: "Monthly close" })).toBeVisible()
    await expect(page.locator("[data-slot=public-page-meta]")).toContainText("Shared read-only · rendered")
    await expect(page.locator("[data-slot=public-page-footer]")).toContainText("This link stops working")
    expect(await page.locator("[data-slot=public-page-frame] button, [data-slot=public-page-frame] a").count()).toBe(0)
    const frame = (await page.locator("[data-slot=public-page-frame]").boundingBox())!
    expect(Math.round(frame.x)).toBe(w.width >= 768 ? 24 : 16)
    await noSidewaysScroll(page)
  })

  test(`P05/P06 /shared-chat at ${w.width}: form folds into one line, rows have avatar and time`, async ({ page }) => {
    await page.setViewportSize({ width: w.width, height: w.height })
    await page.route("**/api/v1/shared-chats/*/messages", (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          messages: [
            { id: "1", role: "user", content: "Where is the report?", created_at: "2026-10-18T08:05:00Z" },
            { id: "2", role: "assistant", content: "It is attached.", created_at: "2026-10-18T08:06:00Z" },
          ],
        }),
      }),
    )
    await page.goto("/shared-chat")
    await page.getByLabel("Share ID").fill(SHARE_ID)
    await page.getByLabel("Share token").fill(SHARE_TOKEN)
    await page.getByRole("button", { name: "Open transcript" }).click()
    const summary = page.locator("[data-slot=shared-chat-summary]")
    await expect(summary).toContainText("cshr_aaaaaaaa…")
    await expect(page.getByLabel("Share token")).toHaveCount(0)
    const rows = page.locator("[data-slot=shared-message]")
    await expect(rows).toHaveCount(2)
    expect(Math.round(((await rows.first().locator("[data-slot=shared-message-avatar]").boundingBox())!).width)).toBe(30)
    await expect(rows.first().locator("time")).toBeVisible()
    const column = (await page.locator("[data-slot=shared-transcript-column]").boundingBox())!
    expect(Math.round(column.width)).toBe(w.width >= 1024 ? 760 : w.width)
    // The token is never in the address or the page text.
    expect(page.url()).not.toContain(SHARE_TOKEN)
    expect(await page.locator("body").innerText()).not.toContain(SHARE_TOKEN)
    await noSidewaysScroll(page)
  })

  test(`P06 /shared-chat at ${w.width}: not-available offers Try another share`, async ({ page }) => {
    await page.setViewportSize({ width: w.width, height: w.height })
    await page.route("**/api/v1/shared-chats/*/messages", (route) => route.fulfill({ status: 404, body: "{}" }))
    await page.goto("/shared-chat")
    await page.getByLabel("Share ID").fill(SHARE_ID)
    await page.getByLabel("Share token").fill(SHARE_TOKEN)
    await page.getByRole("button", { name: "Open transcript" }).click()
    await expect(page.getByText("This share is not available")).toBeVisible()
    await page.getByRole("button", { name: "Try another share" }).click()
    await expect(page.getByLabel("Share ID")).toBeFocused()
    await noSidewaysScroll(page)
  })
}
