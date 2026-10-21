import { test, expect, type Page } from "@playwright/test"

// #3787 (wireframe round 64, Spojení §4): the full-screen states between
// sessions. Every server answer that ends a session is MOCKED here — the suite
// shares one stored session (global-setup.ts), and a real Log out or a real
// revocation would sign every other spec out with it.

const screenOf = (page: Page) => page.locator("[data-slot=signed-out-screen]")

/** Every app API call answers 401 with `reason`, and so does the refresh. */
async function endSession(page: Page, reason: "session_expired" | "session_revoked") {
  const body = JSON.stringify({ error: reason })
  await page.route(/\/api\/v1\//, (route) => route.fulfill({ status: 401, contentType: "application/json", body }))
  await page.route(/\/api\/auth\/token\/refresh/, (route) => route.fulfill({ status: 401, contentType: "application/json", body }))
}

test("an expired session opens on Your session expired and comes back to the same page", async ({ page }) => {
  await endSession(page, "session_expired")
  await page.goto("/issues?view=all")
  const state = screenOf(page)
  await expect(state).toHaveAttribute("data-state", "expired", { timeout: 15_000 })
  await expect(page).toHaveURL(/\/login\?reason=expired&redirect=%2Fissues%3Fview%3Dall/)
  await expect(state.getByRole("heading", { level: 1, name: "Your session expired" })).toBeVisible()
  await expect(state).toContainText("Sign in again to pick up where you left off.")
  await expect(state.getByRole("button", { name: "Use another account" })).toBeVisible()
  await state.getByRole("button", { name: "Sign in" }).click()
  await expect(page.getByRole("heading", { name: "Sign in to Crewship" })).toBeVisible()
  // The way back survives the form: the card says where it returns to.
  await expect(page.locator("[data-slot=auth-return-to]")).toHaveText("You'll return to Issues.")
})

test("a revoked session opens on Signed out on this device", async ({ page }) => {
  await endSession(page, "session_revoked")
  await page.goto("/issues")
  const state = screenOf(page)
  await expect(state).toHaveAttribute("data-state", "revoked", { timeout: 15_000 })
  await expect(state).toContainText("Someone signed you out from another device or an admin ended your session.")
  await state.getByRole("button", { name: "Sign in" }).click()
  await expect(page.getByRole("heading", { name: "Sign in to Crewship" })).toBeVisible()
})

test.describe("signed out", () => {
  test.use({ storageState: { cookies: [], origins: [] } })

  test("Log out lands on You're signed out, without error colours", async ({ page }) => {
    // Where Log out sends the browser (lib/signed-out.ts SIGNED_OUT_URL).
    await page.goto("/login?reason=signed-out")
    const state = screenOf(page)
    await expect(state.getByRole("heading", { level: 1, name: "You're signed out" })).toBeVisible()
    await expect(state).toContainText("See you next time.")
    await expect(state.getByRole("link", { name: "Back to crewship.ai" })).toHaveAttribute("href", "https://crewship.ai")
    const bg = await state.locator("section").evaluate((el) => getComputedStyle(el).backgroundColor)
    const card = await page.evaluate(() => {
      const probe = document.createElement("div")
      probe.className = "bg-card"
      document.body.append(probe)
      const value = getComputedStyle(probe).backgroundColor
      probe.remove()
      return value
    })
    expect(bg).toBe(card)
    await state.getByRole("button", { name: "Sign in again" }).click()
    await expect(page.getByRole("heading", { name: "Sign in to Crewship" })).toBeVisible()
  })
})

test("a backend that is down while the app opens shows Crewship is restarting, then recovers", async ({ page }) => {
  let down = true
  await page.route(/\/api\//, (route) => (down ? route.abort("connectionrefused") : route.fallback()))
  await page.goto("/")
  const state = screenOf(page)
  // The session check retries for ~8 s before it says so.
  await expect(state).toHaveAttribute("data-state", "restarting", { timeout: 30_000 })
  await expect(state.getByRole("heading", { level: 1, name: "Crewship is restarting" })).toBeVisible()
  await expect(state).toContainText("This usually takes under a minute. The page reconnects on its own.")
  await expect(state).toContainText(/Next try in \d+ s · Try now/)
  // Nobody was signed out on the way.
  expect(page.url()).not.toContain("/login")
  down = false
  await state.getByRole("button", { name: "Try now" }).click()
  await expect(state).toHaveCount(0, { timeout: 15_000 })
  await expect(page.locator("[data-slot=sidebar-container], [data-slot=sidebar]").first()).toBeVisible({ timeout: 15_000 })
})

test("the sail does not move under reduced motion", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" })
  await page.route(/\/api\//, (route) => route.abort("connectionrefused"))
  await page.goto("/")
  const mark = screenOf(page).locator("[data-slot=signed-out-mark]")
  await expect(mark).toBeVisible({ timeout: 30_000 })
  await expect(mark).toHaveCSS("animation-name", "none")
})
