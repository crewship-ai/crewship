// Library ↔ chat ↔ editor navigation without sending anything to an agent.
import { test } from "@playwright/test"
import { audit, focused, note, pause, shot, type Viewport } from "./audit"
import { browserContext, ownerApi, ownerStorage, walkWorkspace } from "./session"
import { step } from "./steps"

for (const vp of ["desktop", "mobile"] as Viewport[]) {
  test(`chat navigation ${vp}`, async ({ browser }) => {
    test.skip(process.env.UX_TARGET !== "chatnav")
    const api = await ownerApi()
    const ws = await walkWorkspace(api)
    const ctx = await browserContext(browser, vp, await ownerStorage())
    const page = await ctx.newPage()
    await step(page, `A10-chat-empty-${vp}`, async () => {
      await page.goto(`/chat?workspace_id=${ws.id}`)
      await pause(4000)
      const a = await audit(page, `A10-chat-${vp}`, vp)
      return { text: (await page.locator("main").innerText()).slice(0, 400), small: a.small.map((c) => `${c.name || c.html.slice(0, 60)} ${c.w}x${c.h}`) }
    })
    await step(page, `A11-chat-to-library-${vp}`, async () => {
      const direct = page.getByRole("link", { name: "Artifacts" }).first()
      let path = "sidebar"
      if (!(await direct.isVisible().catch(() => false))) {
        path = "More"
        await page.getByRole("button", { name: /^More/ }).or(page.getByRole("link", { name: /^More/ })).first().click()
        await pause(1000)
        await shot(page, `A11-phone-more-${vp}`)
      }
      await page.getByRole("link", { name: "Artifacts" }).first().click()
      await page.waitForURL(/\/artifacts/)
      await pause(1500)
      await shot(page, `A11-library-from-chat-${vp}`)
      return { path, focus: await focused(page) }
    })
    await step(page, `A12-library-panel-toggle-${vp}`, async () => {
      // Phone: where are search and the Empty view? The panel sits behind the toggle.
      const toggle = page.getByRole("button", { name: /Show Artifacts|Hide Artifacts|Artifacts panel/ }).first()
      const label = await toggle.getAttribute("aria-label").catch(() => null)
      if (await toggle.isVisible().catch(() => false)) {
        await toggle.click()
        await pause(1000)
      }
      const a = await audit(page, `A12-library-panel-${vp}`, vp)
      return { toggleLabel: label, small: a.small.map((c) => `${c.name || c.html.slice(0, 60)} ${c.w}x${c.h}`) }
    })
    note(`done:chatnav-${vp}`, true)
    await ctx.close()
    await api.dispose()
  })
}
