import { test, expect } from "@playwright/test"

test.use({ viewport: { width: 844, height: 390 }, hasTouch: true, isMobile: true })

for (const available of [true, false]) {
  test(`the chat composer fits in landscape with Docker ${available ? "available" : "unavailable"}`, async ({ page }) => {
    await page.route("**/api/v1/system/runtime", (route) => route.fulfill({ json: { available } }))
    const runtime = page.waitForResponse("**/api/v1/system/runtime")
    await page.goto("/chat/jordan")
    await runtime
    await expect(page.getByRole("button", { name: "New chat" }).first()).toBeVisible()
    const banner = page.getByText("Docker is not running.", { exact: true })
    if (available) {
      await expect(banner).toBeHidden()
    } else {
      await expect(banner).toBeVisible()
    }

    const editor = page.locator("textarea[data-slot=input-group-control]")
    await expect(editor).toBeVisible()
    await expect.poll(() => editor.evaluate((element) =>
      element.closest("[data-slot=input-group]")!.getBoundingClientRect().bottom -
      element.closest(".surface-pane")!.getBoundingClientRect().bottom,
    )).toBeLessThanOrEqual(1)
  })
}
