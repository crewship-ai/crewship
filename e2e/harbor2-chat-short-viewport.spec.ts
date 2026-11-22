import { test, expect } from "@playwright/test"

test.use({ hasTouch: true, isMobile: true })

for (const viewport of [{ width: 844, height: 390 }, { width: 390, height: 844 }]) {
  for (const available of [true, false]) {
    test(`the chat composer fits at ${viewport.width}×${viewport.height} with Docker ${available ? "available" : "unavailable"}`, async ({ page }) => {
      await page.setViewportSize(viewport)
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

      await editor.fill(Array.from({ length: 12 }, (_, i) => `Draft line ${i + 1}`).join("\n"))
      const send = page.locator("form").filter({ has: editor }).locator('button[type="submit"]')
      await expect(send).toBeVisible()
      await expect.poll(() => send.evaluate((element) => {
        const rect = element.getBoundingClientRect()
        const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
        const paneBottom = element.closest(".surface-pane")!.getBoundingClientRect().bottom
        return rect.bottom <= Math.min(paneBottom, window.innerHeight) + 1 &&
          !!hit && element.contains(hit)
      })).toBe(true)
    })
  }
}
