import type { Locator, Page } from "@playwright/test"

// The same native editor host renders on /pages/craft/editor and inside the
// Artifacts frame (/artifacts/<id>, the release profile). In the frame (#5132)
// the lock controls live in the status chip's popover and the agent task is the
// Agent drawer's Edit mode; on the route they are rows above the editor. These
// helpers reach the same controls in either place, so a check means the same.

const LOCK = { role: "toolbar", name: "Document editing" } as const

/** The lock toolbar ("Document editing"), opening the status popover first when the frame keeps it there. */
export async function lockControls(page: Page, timeout = 60_000): Promise<Locator> {
  const bar = page.getByRole(LOCK.role, { name: LOCK.name })
  const chip = page.locator("button[data-status]").first()
  for (const end = Date.now() + timeout; Date.now() < end; await page.waitForTimeout(250)) {
    if (await bar.count()) return bar
    if (await chip.count()) {
      await chip.click()
      await bar.first().waitFor({ timeout: 5_000 }).catch(() => {})
      if (await bar.count()) return bar
    }
  }
  return bar
}

/** A lock action button ("Take over editing", "Hand back", …), wherever the page keeps the lock controls. */
export async function lockButton(page: Page, name: string, timeout = 60_000): Promise<Locator> {
  await lockControls(page, timeout)
  // The route's acquire-after-reconnect button sits next to the bar; the frame places both in the popover.
  return page.getByRole("button", { name, exact: true })
}

/** The Craft agent task form ("Edit with an agent"): opened from its toggle on the route, or the Agent drawer's Edit mode in the frame. */
export async function agentControl(page: Page, timeout = 60_000): Promise<Locator> {
  const control = page.getByRole("group", { name: "Edit with an agent" })
  const toggle = control.getByRole("button", { name: "Edit with an agent", exact: true })
  const picker = control.getByRole("combobox", { name: "Agent" })
  const drawer = page.getByRole("button", { name: "Agent", exact: true })
  const edit = page.getByRole("radiogroup", { name: "Agent mode" }).getByRole("radio", { name: "Edit" })
  for (const end = Date.now() + timeout; Date.now() < end; await page.waitForTimeout(250)) {
    if (await picker.isVisible().catch(() => false)) return control
    if (await toggle.isVisible().catch(() => false)) { await toggle.click(); continue }
    if (await drawer.count()) {
      if ((await drawer.first().getAttribute("aria-pressed")) !== "true") await drawer.first().click()
      if (await edit.isEnabled().catch(() => false)) await edit.click()
    }
  }
  return control
}
