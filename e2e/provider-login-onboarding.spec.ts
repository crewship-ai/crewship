import { test, expect } from "@playwright/test"

// Isolated UI acceptance: no real token, account creation or provider traffic.
// The real page and wizard render against an empty workspace API fixture.
for (const viewport of [{ width: 1280, height: 900 }, { width: 390, height: 844 }]) {
  test(`provider-first onboarding and zero-account filters at ${viewport.width}px`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname
      let body: unknown = []
      if (path === "/api/auth/session") body = { user: { id: "ui-test", email: "ui@example.test" }, expires: "2099-01-01T00:00:00Z" }
      else if (path === "/api/v1/workspaces") body = [{ id: "ui-workspace", name: "Browser fixture", slug: "browser-fixture", currentUserRole: "OWNER" }]
      else if (path.includes("/settings") || path.includes("/config") || path.includes("/health")) body = {}
      await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) })
    })
    await page.goto(viewport.width > 640 ? "/credentials" : "/credentials?tab=providers")
    if (viewport.width > 640) {
      // Pin global navigation so its hover overlay cannot cover the vault rail.
      await page.getByRole("button", { name: "Sidebar: hover", exact: true }).click()
      await expect(page.getByText("All providers", { exact: true })).toBeVisible()
      await expect(page.getByLabel("0 connected accounts")).toHaveCount(12)
      await page.getByText("Grok / xAI", { exact: true }).click()
    }
    await page.getByRole("button", { name: "Add provider login", exact: true }).first().click()
    await page.getByRole("button", { name: /^ChatGPT \/ OpenAI/ }).click()
    await expect(page.getByLabel(/Codex login \(auth.json\)/)).toBeVisible()
    await page.getByLabel("Provider", { exact: true }).selectOption("GOOGLE")
    await expect(page.getByLabel(/Gemini login/)).toBeVisible()
    await page.getByLabel(/Gemini login/).fill("fixture-only-not-a-token")
    await page.getByLabel("Provider", { exact: true }).selectOption("XAI")
    await expect(page.getByLabel("API key", { exact: true })).toHaveValue("")
    await expect(page.getByRole("button", { name: /^Subscription/ })).toHaveCount(0)
    await expect(page.getByRole("button", { name: /Sign in with a code/ })).toHaveCount(0)
    await expect(page.getByRole("button", { name: "Continue", exact: true })).toBeVisible()
    await expect(async () => {
      const box = await page.getByRole("dialog").boundingBox()
      expect(box).not.toBeNull()
      expect(box!.x).toBeGreaterThanOrEqual(-1)
      expect(box!.x + box!.width).toBeLessThanOrEqual(viewport.width + 1)
    }).toPass()
  })
}
