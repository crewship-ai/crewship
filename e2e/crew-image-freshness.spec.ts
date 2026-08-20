import { test, expect } from "./fixtures/auth"

// #1845 — the image-freshness card, against the RENDERED UI.
//
// What the unit tests cannot prove and this can: that the card is actually
// reachable from a crew someone has selected, that it is wired to the real
// endpoint rather than to a mock, and that the endpoint answers at all under
// the app's own auth and workspace scoping. A card that renders perfectly in
// jsdom and 404s in the product is the failure this covers.
//
// It deliberately does NOT assert a particular verdict. On any given instance
// the crews may be current, behind, or running locally built cache images with
// no registry digest — all three are legitimate, and pinning one would make
// this a test of the fixture rather than of the feature. What must hold is
// that the card commits to one of the four answers, never to nothing.
//
// Skipped on bare local runs; point PLAYWRIGHT_BASE_URL at a deployment with a
// real Docker daemon to exercise it.

const QUICK = 8_000

test.describe("Crew image freshness card", () => {
  test.skip(!process.env.PLAYWRIGHT_BASE_URL, "requires a live backend with a container provider")
  test.setTimeout(60_000)

  test("a crew's settings report an image-freshness verdict, never silence", async ({ page }) => {
    await page.goto("/crews")

    const crewRow = page.locator("aside button").filter({ hasText: /\w/ }).first()
    await expect(crewRow).toBeVisible({ timeout: QUICK })
    await crewRow.click()
    await expect(page).toHaveURL(/[?&]crew=/, { timeout: QUICK })

    // CanvasTabs is a real tablist now, so select by role — narrower than the
    // old button-by-name match, which would have taken any button reading
    // "Settings" anywhere on the screen. `.first()` stays: other surfaces on
    // this page own tab strips too, and a strict-mode failure here would read
    // as a freshness-card regression.
    await page.getByRole("tab", { name: /^Settings$/i }).first().click()

    // The card lives inside the "Container image & features" collapsible.
    await page.getByText(/Container image & features/i).first().click()

    const verdict = page.getByTestId("crew-image-freshness-verdict")
    await expect(verdict).toBeVisible({ timeout: QUICK })

    // One of the four answers. "Checking…" is excluded on purpose: a card stuck
    // on its loading state is exactly the silence this feature exists to end.
    await expect(verdict).toHaveText(/^(Behind|Current|Unknown|Unavailable)$/, { timeout: QUICK })

    // Whenever the answer is not a clean comparison, the card owes the reader a
    // reason. "Not behind" with no explanation is indistinguishable from
    // "current", which is the false assurance the whole feature avoids.
    const text = (await verdict.textContent())?.trim()
    if (text === "Unknown" || text === "Unavailable") {
      await expect(page.getByTestId("crew-image-freshness-reason")).toBeVisible({ timeout: QUICK })
    }

    // The button is offered only when there is something to do about it.
    const refresh = page.getByTestId("crew-image-refresh")
    if (text === "Behind") {
      await expect(refresh).toBeVisible({ timeout: QUICK })
    } else {
      await expect(refresh).toHaveCount(0)
    }
  })
})
