import { expect, type Page } from "@playwright/test"

/** Inject only failures; recovery and not-found use the real webhook API. */
export async function triggerDetailLoadErrorFlow(page: Page, triggerID: string, name: string) {
  const detail = `/integrations?space=triggers&id=${encodeURIComponent(triggerID)}`
  for (const failure of ["http", "network"] as const) {
    let failing = true
    let failures = 0
    const pattern = "**/pipeline-webhooks*"
    const intercept = async (route: import("@playwright/test").Route) => {
      if (route.request().method() !== "GET" || !new URL(route.request().url()).pathname.endsWith("/pipeline-webhooks") || !failing) {
        await route.continue()
      } else {
        failures++
        if (failure === "network") await route.abort("failed")
        else await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: "Synthetic trigger list outage" }) })
      }
    }
    await page.route(pattern, intercept)
    try {
      await page.goto(detail)
      const alert = page.getByRole("alert").filter({ hasText: "Triggers could not load" })
      await expect(alert).toBeVisible({ timeout: 15_000 })
      expect(failures).toBeGreaterThan(0)
      await expect(page.getByText("This trigger doesn’t exist", { exact: true })).toHaveCount(0)
      failing = false
      const [recovered] = await Promise.all([
        page.waitForResponse(response => response.request().method() === "GET" && new URL(response.url()).pathname.endsWith("/pipeline-webhooks")),
        alert.getByRole("button", { name: "Retry", exact: true }).click(),
      ])
      expect(recovered.status()).toBe(200)
      await expect(page.getByRole("heading", { name, exact: true })).toBeVisible()
      await expect(alert).toHaveCount(0)
    } finally {
      await page.unroute(pattern, intercept)
    }
  }
  // A successful real list without the selected ID must still show not-found.
  await page.goto("/integrations?space=triggers&id=missing-trigger-load-error-fixture")
  await expect(page.getByRole("heading", { name: "This trigger doesn’t exist", exact: true })).toBeVisible()
  await expect(page.getByRole("alert").filter({ hasText: "Triggers could not load" })).toHaveCount(0)
}
